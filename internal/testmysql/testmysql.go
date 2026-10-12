// Package testmysql gives every MySQL-backed test its own throwaway
// database. BNBLOC_TEST_MYSQL_DSN names a server (and a base database name);
// tests never use that database itself. `go test ./...` runs packages in
// parallel, so packages sharing one database raced each other's migrations,
// truncations and cross-tenant claims and flaked at random. Each database is
// created on the DSN's server, named after the DSN's database
// (`<base>_t<unix>_<hex>`), and dropped when its test or test binary ends; a
// test binary also sweeps databases of runs that were killed before they
// could drop theirs.
//
// The DSN's user needs CREATE and DROP on `<base>_t*` databases. When the
// variable is set and a database cannot be created, the test fails: a set
// DSN means the MySQL tests are expected to run.
package testmysql

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// EnvDSN names the MySQL server the integration tests run against. Unset,
// they run on the memory backends only (or skip).
const EnvDSN = "BNBLOC_TEST_MYSQL_DSN"

// staleAfter is how old a leftover test database must be before a later run
// drops it: far longer than any test binary runs.
const staleAfter = 6 * time.Hour

// maxDBName is MySQL's identifier limit.
const maxDBName = 64

// Enabled reports whether MySQL integration tests run.
func Enabled() bool { return strings.TrimSpace(os.Getenv(EnvDSN)) != "" }

// Fresh returns a DSN for a new, empty database that is dropped when t ends.
// It skips t when EnvDSN is unset. Use it for a test that alters the schema
// or needs a database no other test touches.
func Fresh(t testing.TB) string {
	t.Helper()
	if !Enabled() {
		t.Skipf("set %s to run the MySQL integration test", EnvDSN)
	}
	dsn, drop, err := create()
	if err != nil {
		t.Fatalf("testmysql: %v", err)
	}
	t.Cleanup(func() {
		if err := drop(); err != nil {
			t.Errorf("testmysql: %v", err)
		}
	})
	return dsn
}

var shared struct {
	sync.Mutex
	inMain bool
	once   sync.Once
	dsn    string
	drop   func() error
	err    error
}

// Main runs a package's tests with one shared database per test binary (see
// Shared) and drops it afterwards. Call it from the package's TestMain:
//
//	func TestMain(m *testing.M) { os.Exit(testmysql.Main(m)) }
func Main(m *testing.M) int {
	shared.Lock()
	shared.inMain = true
	shared.Unlock()
	if Enabled() {
		if err := sweepStale(); err != nil {
			fmt.Fprintf(os.Stderr, "testmysql: sweep stale test databases: %v\n", err)
		}
	}
	code := m.Run()
	if shared.drop != nil {
		if err := shared.drop(); err != nil {
			fmt.Fprintf(os.Stderr, "testmysql: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	}
	return code
}

// Shared returns the DSN of the test binary's shared database, created on
// first use, or "" when EnvDSN is unset. Tests sharing it keep their ids
// unique. The package must run its tests through Main, which drops it.
func Shared() (string, error) {
	shared.Lock()
	inMain := shared.inMain
	shared.Unlock()
	if !Enabled() {
		return "", nil
	}
	if !inMain {
		return "", errors.New("testmysql.Shared needs the package's TestMain to call testmysql.Main (it drops the database)")
	}
	shared.once.Do(func() { shared.dsn, shared.drop, shared.err = create() })
	return shared.dsn, shared.err
}

// server opens an admin connection to the DSN's server (no database) and
// returns the DSN's config and base database name.
func server() (*sql.DB, *mysql.Config, string, error) {
	cfg, err := mysql.ParseDSN(strings.TrimSpace(os.Getenv(EnvDSN)))
	if err != nil {
		return nil, nil, "", fmt.Errorf("parse %s: %w", EnvDSN, err)
	}
	base := cfg.DBName
	if base == "" {
		base = "bnbloc"
	}
	admin := cfg.Clone()
	admin.DBName = ""
	db, err := sql.Open("mysql", admin.FormatDSN())
	if err != nil {
		return nil, nil, "", fmt.Errorf("open %s server: %w", EnvDSN, err)
	}
	return db, cfg, base, nil
}

// prefix is the name every test database of base starts with.
func prefix(base string) string {
	// "_t" + unix seconds (10) + "_" + 8 hex = 21 characters after the base.
	if len(base) > maxDBName-21 {
		base = base[:maxDBName-21]
	}
	return base + "_t"
}

func create() (dsn string, drop func() error, err error) {
	admin, cfg, base, err := server()
	if err != nil {
		return "", nil, err
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		admin.Close()
		return "", nil, err
	}
	name := fmt.Sprintf("%s%d_%s", prefix(base), time.Now().Unix(), hex.EncodeToString(suffix))
	if _, err := admin.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4"); err != nil {
		admin.Close()
		return "", nil, fmt.Errorf("create test database %s (the %s user needs CREATE and DROP on %s* databases): %w", name, EnvDSN, prefix(base), err)
	}
	cfg.DBName = name
	return cfg.FormatDSN(), func() error {
		defer admin.Close()
		if _, err := admin.Exec("DROP DATABASE IF EXISTS `" + name + "`"); err != nil {
			return fmt.Errorf("drop test database %s: %w", name, err)
		}
		return nil
	}, nil
}

// sweepStale drops test databases of base older than staleAfter: what a
// killed test binary left behind.
func sweepStale() error {
	admin, _, base, err := server()
	if err != nil {
		return err
	}
	defer admin.Close()
	p := prefix(base)
	rows, err := admin.Query("SHOW DATABASES")
	if err != nil {
		return err
	}
	var stale []string
	cutoff := time.Now().Add(-staleAfter).Unix()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		if created, ok := createdAt(name, p); ok && created < cutoff {
			stale = append(stale, name)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, name := range stale {
		if _, err := admin.Exec("DROP DATABASE IF EXISTS `" + name + "`"); err != nil {
			return err
		}
	}
	return nil
}

// createdAt parses the creation time out of a test database name
// (`<prefix><unix>_<8 hex>`); ok is false for any other name.
func createdAt(name, prefix string) (int64, bool) {
	rest, ok := strings.CutPrefix(name, prefix)
	if !ok {
		return 0, false
	}
	stamp, tag, ok := strings.Cut(rest, "_")
	if !ok || len(tag) != 8 {
		return 0, false
	}
	if _, err := hex.DecodeString(tag); err != nil {
		return 0, false
	}
	unix, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil {
		return 0, false
	}
	return unix, true
}

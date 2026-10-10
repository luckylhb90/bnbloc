package store

import (
	"strings"
	"testing"
)

// The store choice is logged at startup; the log line must identify the
// database without ever carrying credentials.
func TestDSNSummaryIdentifiesDatabaseWithoutCredentials(t *testing.T) {
	cases := []struct {
		dsn  string
		want string
	}{
		{"user:secret-pw@tcp(10.1.2.3:3306)/bnbloc-prod?parseTime=true", "mysql://10.1.2.3:3306/bnbloc-prod"},
		{"user:secret-pw@tcp(mysql:3306)/bnbloc", "mysql://mysql:3306/bnbloc"},
		{"user@unix(/tmp/mysql.sock)/bnbloc", "mysql:///tmp/mysql.sock/bnbloc"},
		{"not a dsn", "mysql (unparsed DSN)"},
	}
	for _, c := range cases {
		got := dsnSummary(c.dsn)
		if got != c.want {
			t.Errorf("dsnSummary(%q) = %q, want %q", c.dsn, got, c.want)
		}
		if strings.Contains(got, "secret-pw") {
			t.Fatalf("dsnSummary(%q) leaked credentials: %q", c.dsn, got)
		}
	}
}

// OPS-5: production never silently boots on the in-memory store.
func TestNewBackendsRefusesInMemoryInProduction(t *testing.T) {
	t.Setenv("BNBLOC_ENV", "production")
	if _, err := NewBackends(""); err == nil || !strings.Contains(err.Error(), "BNBLOC_MYSQL_DSN") {
		t.Fatalf("NewBackends(\"\") in production = %v, want a DSN-required error", err)
	}
	t.Setenv("BNBLOC_ENV", "")
	if b, err := NewBackends(""); err != nil || b == nil {
		t.Fatalf("NewBackends(\"\") in dev = %v, want in-memory backends", err)
	}
}

// mysqlConfig turns on parameter interpolation (one round trip per query)
// only for a utf8mb4 connection: client-side escaping is unsafe in the
// multibyte charsets whose trailing bytes can be a backslash, so a DSN asking
// for any other charset or collation is refused instead of interpolated.
func TestMySQLConfigInterpolatesOnlyUTF8MB4(t *testing.T) {
	for _, dsn := range []string{
		"u:p@tcp(db:3306)/bnbloc?parseTime=true",
		"u:p@tcp(db:3306)/bnbloc?parseTime=true&charset=utf8mb4",
		"u:p@tcp(db:3306)/bnbloc?parseTime=true&collation=utf8mb4_unicode_ci",
		"u:p@tcp(db:3306)/bnbloc?parseTime=true&multiStatements=false&interpolateParams=false",
	} {
		cfg, err := mysqlConfig(dsn)
		if err != nil {
			t.Fatalf("mysqlConfig(%q) = %v, want accepted", dsn, err)
		}
		if !cfg.InterpolateParams || !cfg.MultiStatements || !cfg.ParseTime {
			t.Fatalf("mysqlConfig(%q): interpolate=%v multiStatements=%v parseTime=%v, want all true",
				dsn, cfg.InterpolateParams, cfg.MultiStatements, cfg.ParseTime)
		}
	}
	for _, dsn := range []string{
		"u:p@tcp(db:3306)/bnbloc?parseTime=true&charset=gbk",
		"u:p@tcp(db:3306)/bnbloc?parseTime=true&charset=utf8mb4,utf8",
		"u:p@tcp(db:3306)/bnbloc?parseTime=true&collation=big5_chinese_ci",
		"u:p@tcp(db:3306)/bnbloc?parseTime=true&collation=latin1_swedish_ci",
		"u:p@tcp(db:3306)/bnbloc?parseTime=true&character_set_client=sjis",
		"not a dsn",
	} {
		if _, err := mysqlConfig(dsn); err == nil {
			t.Fatalf("mysqlConfig(%q) accepted, want refused", dsn)
		}
	}
}

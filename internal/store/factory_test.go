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

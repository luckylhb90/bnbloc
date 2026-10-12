package store

import (
	"os"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/testmysql"
)

// The package's MySQL tests run in a database of this test binary's own
// (testmysql.Shared), dropped when the binary ends.
func TestMain(m *testing.M) { os.Exit(testmysql.Main(m)) }

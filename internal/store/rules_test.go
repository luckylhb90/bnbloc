package store

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// A failover's errors are the database unavailable; a request's own are not.
func TestUnavailable(t *testing.T) {
	for _, err := range []error{
		&mysql.MySQLError{Number: 1290, Message: "running with --read-only"},
		&mysql.MySQLError{Number: 1040},
		fmt.Errorf("commit: %w", driver.ErrBadConn),
		mysql.ErrInvalidConn,
		&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED},
	} {
		if !Unavailable(err) {
			t.Errorf("%v: not unavailable", err)
		}
	}
	for _, err := range []error{nil, errors.New("boom"), &mysql.MySQLError{Number: 1062}, ErrDuplicateSeq} {
		if Unavailable(err) {
			t.Errorf("%v: unavailable", err)
		}
	}
}

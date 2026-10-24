//go:build !windows

package main

import (
	"os"
	"syscall"
)

// ownedByCurrentUser reports whether the file belongs to the user running vmexec
// (a file another user could have planted is not the member's own).
func ownedByCurrentUser(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return !ok || int(st.Uid) == os.Getuid()
}

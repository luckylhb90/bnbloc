//go:build windows

package main

import "os"

func ownedByCurrentUser(os.FileInfo) bool { return true }

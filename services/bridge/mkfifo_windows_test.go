//go:build windows

package bridge

import "errors"

func syscallMkfifo(string) error { return errors.New("no fifo on Windows") }

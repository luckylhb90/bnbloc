package main

import "testing"

func TestListenAddrIsLoopbackOnly(t *testing.T) {
	for _, ok := range []string{"127.0.0.1:7192", "[::1]:7192", "localhost:7192"} {
		if got, err := listenAddr(ok); err != nil || got != ok {
			t.Errorf("%q: %q %v", ok, got, err)
		}
	}
	for _, bad := range []string{"", "http://127.0.0.1:7192", "127.0.0.1", "127.0.0.1:", "0.0.0.0:7192", "example.com:80"} {
		if got, err := listenAddr(bad); err == nil {
			t.Errorf("%q must be refused, got %q", bad, got)
		}
	}
}

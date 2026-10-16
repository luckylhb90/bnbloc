package main

import "testing"

func TestListenAddrIsLoopbackOnly(t *testing.T) {
	if got, err := listenAddr("http://127.0.0.1:7192"); err != nil || got != "127.0.0.1:7192" {
		t.Fatalf("%q %v", got, err)
	}
	if got, err := listenAddr("http://[::1]:7192"); err != nil || got != "[::1]:7192" {
		t.Fatalf("%q %v", got, err)
	}
	if got, err := listenAddr("http://localhost:7192"); err != nil || got != "localhost:7192" {
		t.Fatalf("%q %v", got, err)
	}
	for _, bad := range []string{"", "127.0.0.1:7192", "https://127.0.0.1:7192", "http://127.0.0.1", "http://0.0.0.0:7192", "http://example.com:80"} {
		if got, err := listenAddr(bad); err == nil {
			t.Errorf("%q must be refused, got %q", bad, got)
		}
	}
}

package main

import "testing"

// As the web client: a 5xx is sent again three times, a 503 (the database
// failing over) four times more; a 4xx never.
func TestSendRetryWaits(t *testing.T) {
	count := func(err error) int {
		n := 0
		for ; ; n++ {
			if _, ok := sendRetryWait(n, err); !ok {
				return n
			}
		}
	}
	if n := count(&statusError{status: 500}); n != 3 {
		t.Fatalf("500 resent %d times, want 3", n)
	}
	if n := count(&statusError{status: 503}); n != 7 {
		t.Fatalf("503 resent %d times, want 7", n)
	}
	if retryableSend(&statusError{status: 429}) {
		t.Fatal("a 4xx is resent")
	}
}

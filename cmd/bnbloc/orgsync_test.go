package main

import (
	"reflect"
	"testing"
)

func TestOrgSyncRulesBodyAcceptsListOrObject(t *testing.T) {
	for _, in := range []string{`[{"tenant":"acme"}]`, `{"rules":[{"tenant":"acme"}]}`} {
		body, err := rulesBody([]byte(in))
		if err != nil || string(body["rules"]) != `[{"tenant":"acme"}]` {
			t.Fatalf("rulesBody(%s) = %s, %v", in, body["rules"], err)
		}
	}
	if _, err := rulesBody([]byte(`{"tenant":"acme"}`)); err == nil {
		t.Fatal("a body without rules was accepted")
	}
}

func TestOrgSyncDeprovisionLinesSkipBlanksAndComments(t *testing.T) {
	got := lines([]byte("a@x\n\n# departed in May\n  b@x  \n"))
	if !reflect.DeepEqual(got, []string{"a@x", "b@x"}) {
		t.Fatalf("lines = %v", got)
	}
}

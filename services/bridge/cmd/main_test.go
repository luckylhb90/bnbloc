package main

import (
	"reflect"
	"testing"
)

func TestParseDisabledProviders(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want map[string]bool
	}{
		{name: "empty", raw: "", want: nil},
		{name: "whitespace only", raw: "   ", want: nil},
		{name: "single", raw: "core-a/cursor", want: map[string]bool{"core-a/cursor": true}},
		{
			name: "spaces and trailing empty",
			raw:  "core-a/cursor, core-b/codex , ",
			want: map[string]bool{"core-a/cursor": true, "core-b/codex": true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDisabledProviders(tt.raw)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseDisabledProviders(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

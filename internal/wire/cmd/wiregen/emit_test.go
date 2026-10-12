package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/sample.ts")

// TestEmitGolden pins how each Go form renders (testdata/sample covers them
// all); regenerate with `go test ./internal/wire/cmd/wiregen -update`.
func TestEmitGolden(t *testing.T) {
	got, err := emitPackage(filepath.Join("testdata", "sample"), "example.com/sample", nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "sample.ts")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("rendering drifted; review and run with -update.\n got:\n%s", got)
	}
}

// A tstype tag naming nothing the output defines is refused, so a typo
// never reaches the web as an undefined type.
func TestUnknownTSTypeRefused(t *testing.T) {
	dir := t.TempDir()
	src := "package x\ntype T struct {\n\tA string `json:\"a\" tstype:\"Nope\"`\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := emitPackage(dir, "example.com/x", nil); err == nil {
		t.Fatal("want an error for an unknown tstype")
	}
}

// A union directive may not shadow a type of the same name.
func TestUnionCollisionRefused(t *testing.T) {
	dir := t.TempDir()
	src := "package x\ntype K struct{}\n\n//wire:union K\nconst A = \"a\"\n"
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := emitPackage(dir, "example.com/x", nil); err == nil {
		t.Fatal("want an error for a union named like a type")
	}
}

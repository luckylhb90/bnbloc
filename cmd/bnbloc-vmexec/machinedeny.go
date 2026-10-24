package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Machine-wide read denials. A component that keeps a login of its own (a
// credential the confined agent's open network could send away) names its
// directory in `~/.bnbloc/confine.d/<name>.json`:
//
//	{"denyRead": ["/abs/path"]}
//
// and every `confine` on this machine, whichever core runs it, denies reading it
// the way it does the descriptor's own denyRead (after every allow). The list
// can only restrict: nothing here allows a path, and a file that does not parse,
// is not owned by the member, is writable by others, or names a relative path is
// skipped with a note on stderr. `~/.bnbloc` is itself denied to a confined
// agent (credentialPaths), so an agent cannot add, change or remove an entry.
//
// A CLI that is itself the tool whose state is named (it must read its own
// login) is run with BNBLOC_CONFINE_MACHINE_DENY=off by the core that confines
// it; the descriptor's own denyRead is unaffected.
const (
	machineDenyDir     = "confine.d"
	machineDenyOffEnv  = "BNBLOC_CONFINE_MACHINE_DENY"
	machineDenyMaxFile = 64 << 10
	machineDenyMaxLen  = 64 // files read; the rest are ignored
)

type machineDenyDoc struct {
	DenyRead []string `json:"denyRead"`
}

// machineDenyRead returns the paths named in <home>/.bnbloc/confine.d/*.json, in
// file name order.
func machineDenyRead(home string, note io.Writer) []string {
	dir := filepath.Join(home, ".bnbloc", machineDenyDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) > machineDenyMaxLen {
		fmt.Fprintf(note, "bnbloc-vmexec confine: %s has %d files; reading the first %d\n", dir, len(names), machineDenyMaxLen)
		names = names[:machineDenyMaxLen]
	}
	var out []string
	for _, name := range names {
		path := filepath.Join(dir, name)
		paths, err := readMachineDeny(path)
		if err != nil {
			fmt.Fprintf(note, "bnbloc-vmexec confine: ignoring %s: %v\n", path, err)
			continue
		}
		out = append(out, paths...)
	}
	return out
}

func readMachineDeny(path string) ([]string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("writable by others (mode %v)", info.Mode().Perm())
	}
	if !ownedByCurrentUser(info) {
		return nil, fmt.Errorf("not owned by this user")
	}
	if info.Size() > machineDenyMaxFile {
		return nil, fmt.Errorf("larger than %d bytes", machineDenyMaxFile)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc machineDenyDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	for _, p := range doc.DenyRead {
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("path %q is not absolute", p)
		}
	}
	return doc.DenyRead, nil
}

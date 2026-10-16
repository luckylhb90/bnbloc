package bridge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	corepkg "github.com/luckylhb90/bnbloc/core"
	sandbox "github.com/luckylhb90/bnbloc/sandbox/protocol"
)

// mountFixture is a workspaces root, a mounts dir, a guest root and one
// component's folder under the root's .mounts.
type mountFixture struct {
	mountsDir, root, workdir, guestRoot string
}

func newMountFixture(t *testing.T) mountFixture {
	t.Helper()
	root := t.TempDir()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	f := mountFixture{mountsDir: t.TempDir(), root: root, workdir: filepath.Join(root, "space-1"), guestRoot: "/guest/home"}
	if err := os.MkdirAll(f.workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f mountFixture) folder(t *testing.T, component, name string) string {
	t.Helper()
	p := filepath.Join(MountsHostRoot(f.root, component), name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func (f mountFixture) descriptor(t *testing.T, component string, body any) {
	t.Helper()
	dir := filepath.Join(f.mountsDir, component)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(body)
	if err := os.WriteFile(filepath.Join(dir, mountsFileName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f mountFixture) read(borrowed bool) ([]sandbox.SpaceMount, []mountDrop) {
	return readContributedMounts(f.mountsDir, f.root, f.workdir, []string{f.guestRoot}, borrowed)
}

type desc = map[string]any

func descriptor(mounts ...desc) desc {
	return desc{"contract": SandboxMountsContract, "mounts": mounts}
}

func TestContributedMounts_acceptedMountIsReadOnlyAndShared(t *testing.T) {
	f := newMountFixture(t)
	host := f.folder(t, "local-skills", "demo")
	f.descriptor(t, "local-skills", descriptor(desc{"host": host, "guest": f.guestRoot + "/.agents/skills/demo"}))
	mounts, dropped := f.read(false)
	want := []sandbox.SpaceMount{{Kind: sandbox.MountShared, HostPath: host, GuestPath: f.guestRoot + "/.agents/skills/demo", ReadOnly: true}}
	if len(dropped) != 0 || len(mounts) != 1 || mounts[0] != want[0] {
		t.Fatalf("mounts %+v dropped %v, want %+v", mounts, dropped, want)
	}
}

func TestContributedMounts_forSelectsOwnAndBorrowed(t *testing.T) {
	f := newMountFixture(t)
	a, b, c, d := f.folder(t, "c1", "a"), f.folder(t, "c1", "b"), f.folder(t, "c1", "c"), f.folder(t, "c1", "d")
	f.descriptor(t, "c1", descriptor(
		desc{"host": a, "guest": f.guestRoot + "/a"},                    // default: own
		desc{"host": b, "guest": f.guestRoot + "/b", "for": "borrowed"}, // borrowed only
		desc{"host": c, "guest": f.guestRoot + "/c", "for": "all"},
		desc{"host": d, "guest": f.guestRoot + "/d", "for": "nobody"},
	))
	guests := func(m []sandbox.SpaceMount) (g []string) {
		for _, x := range m {
			g = append(g, strings.TrimPrefix(x.GuestPath, f.guestRoot+"/"))
		}
		return
	}
	own, drops := f.read(false)
	if !slices.Equal(guests(own), []string{"a", "c"}) || len(drops) != 1 || !strings.Contains(drops[0].Reason, "nobody") {
		t.Errorf("own run: %v, dropped %v", guests(own), drops)
	}
	borrowed, _ := f.read(true)
	if !slices.Equal(guests(borrowed), []string{"b", "c"}) {
		t.Errorf("borrowed run: %v", guests(borrowed))
	}
}

func TestContributedMounts_refusals(t *testing.T) {
	f := newMountFixture(t)
	ok := f.folder(t, "c1", "ok")
	outside := t.TempDir()
	// A symlink inside the component's folder that leaves it.
	if err := os.Symlink(outside, filepath.Join(MountsHostRoot(f.root, "c1"), "escape")); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(MountsHostRoot(f.root, "c1"), "afile")
	os.WriteFile(file, []byte("x"), 0o644)
	other := f.folder(t, "c2", "theirs") // another component's folder
	cases := []struct {
		name string
		m    desc
		why  string
	}{
		{"host outside the share", desc{"host": outside, "guest": f.guestRoot + "/x"}, "not inside the component's folder"},
		{"another component's folder", desc{"host": other, "guest": f.guestRoot + "/x"}, "not inside the component's folder"},
		{"the component's folder itself", desc{"host": MountsHostRoot(f.root, "c1"), "guest": f.guestRoot + "/x"}, "not inside the component's folder"},
		{"a dotted host path", desc{"host": ok + "/../ok", "guest": f.guestRoot + "/x"}, "not inside the component's folder"},
		{"a relative host path", desc{"host": "ok", "guest": f.guestRoot + "/x"}, "not inside the component's folder"},
		{"a link out of the folder", desc{"host": filepath.Join(MountsHostRoot(f.root, "c1"), "escape"), "guest": f.guestRoot + "/x"}, "leaves the component's folder"},
		{"a missing folder", desc{"host": filepath.Join(MountsHostRoot(f.root, "c1"), "nope"), "guest": f.guestRoot + "/x"}, "missing"},
		{"a file, not a folder", desc{"host": file, "guest": f.guestRoot + "/x"}, "not a folder"},
		{"a relative guest path", desc{"host": ok, "guest": "skills/x"}, "absolute, clean"},
		{"a dotted guest path", desc{"host": ok, "guest": f.guestRoot + "/a/../x"}, "absolute, clean"},
		{"the guest root itself", desc{"host": ok, "guest": f.guestRoot}, "outside the allowed folders"},
		{"the guest filesystem root", desc{"host": ok, "guest": "/"}, "overlaps the workspace"},
		{"a system path", desc{"host": ok, "guest": "/usr/bin"}, "outside the allowed folders"},
		{"over the workspace", desc{"host": ok, "guest": f.workdir}, "overlaps the workspace"},
		{"inside the workspace", desc{"host": ok, "guest": f.workdir + "/skills"}, "overlaps the workspace"},
	}
	for _, c := range cases {
		f.descriptor(t, "c1", descriptor(c.m))
		mounts, dropped := f.read(false)
		if len(mounts) != 0 || len(dropped) != 1 || !strings.Contains(dropped[0].Reason, c.why) {
			t.Errorf("%s: mounts %+v dropped %v, want one drop mentioning %q", c.name, mounts, dropped, c.why)
		}
	}
	// A guest folder outside the default root is fine once the member allows it.
	f.descriptor(t, "c1", descriptor(desc{"host": ok, "guest": "/opt/skills/x"}))
	mounts, _ := readContributedMounts(f.mountsDir, f.root, f.workdir, []string{f.guestRoot, "/opt/skills"}, false)
	if len(mounts) != 1 {
		t.Errorf("an allowed extra root: %+v", mounts)
	}
}

func TestContributedMounts_aGuestPathHasOneOwner(t *testing.T) {
	f := newMountFixture(t)
	a, b := f.folder(t, "c1", "a"), f.folder(t, "c2", "b")
	f.descriptor(t, "c1", descriptor(desc{"host": a, "guest": f.guestRoot + "/same"}))
	f.descriptor(t, "c2", descriptor(desc{"host": b, "guest": f.guestRoot + "/same"}))
	mounts, dropped := f.read(false)
	if len(mounts) != 1 || mounts[0].HostPath != a || len(dropped) != 1 || !strings.Contains(dropped[0].Reason, "already uses") {
		t.Errorf("the first component keeps the path (components in name order): %+v %v", mounts, dropped)
	}
	// Two mounts of one component at one path likewise.
	f.descriptor(t, "c1", descriptor(desc{"host": a, "guest": f.guestRoot + "/p"}, desc{"host": a, "guest": f.guestRoot + "/p"}))
	os.RemoveAll(filepath.Join(f.mountsDir, "c2"))
	if mounts, dropped := f.read(false); len(mounts) != 1 || len(dropped) != 1 {
		t.Errorf("duplicate guest path in one file: %+v %v", mounts, dropped)
	}
}

func TestContributedMounts_descriptorProblems(t *testing.T) {
	f := newMountFixture(t)
	f.folder(t, "c1", "a")
	dir := filepath.Join(f.mountsDir, "c1")
	os.MkdirAll(dir, 0o755)
	write := func(body string) { os.WriteFile(filepath.Join(dir, mountsFileName), []byte(body), 0o644) }
	for name, body := range map[string]string{
		"not json":       "{",
		"wrong contract": `{"contract":"other/v1","mounts":[]}`,
		"no contract":    `{"mounts":[]}`,
	} {
		write(body)
		if m, d := f.read(false); len(m) != 0 || len(d) != 1 || !strings.Contains(d[0].Reason, SandboxMountsContract) {
			t.Errorf("%s: %+v %v", name, m, d)
		}
	}
	write(strings.Repeat(" ", maxMountsFileBytes+1))
	if m, d := f.read(false); len(m) != 0 || len(d) != 1 || !strings.Contains(d[0].Reason, "256 KiB") {
		t.Errorf("a huge descriptor: %+v %v", m, d)
	}
	os.Remove(filepath.Join(dir, mountsFileName))
	if m, d := f.read(false); len(m) != 0 || len(d) != 0 {
		t.Errorf("a component with no descriptor contributes nothing, quietly: %+v %v", m, d)
	}
	// A descriptor that is a link is not followed.
	target := filepath.Join(t.TempDir(), "x.json")
	os.WriteFile(target, []byte(`{"contract":"`+SandboxMountsContract+`","mounts":[]}`), 0o644)
	os.Symlink(target, filepath.Join(dir, mountsFileName))
	if _, d := f.read(false); len(d) != 1 {
		t.Errorf("a linked descriptor: %v", d)
	}
	// A component whose folder under the workspaces root is missing.
	os.Remove(filepath.Join(dir, mountsFileName))
	other := filepath.Join(f.mountsDir, "orphan")
	os.MkdirAll(other, 0o755)
	os.WriteFile(filepath.Join(other, mountsFileName), []byte(`{"contract":"`+SandboxMountsContract+`","mounts":[]}`), 0o644)
	if _, d := f.read(false); len(d) != 1 || d[0].Component != "orphan" || !strings.Contains(d[0].Reason, "folder under the workspaces root is missing") {
		t.Errorf("no folder: %v", d)
	}
	// A directory that is not a component name is dropped, not read.
	os.MkdirAll(filepath.Join(f.mountsDir, "Bad Name"), 0o755)
	if _, d := f.read(false); !slices.ContainsFunc(d, func(x mountDrop) bool { return x.Component == "Bad Name" }) {
		t.Errorf("a bad component name: %v", d)
	}
	if m, d := readContributedMounts("", f.root, f.workdir, nil, false); m != nil || d != nil {
		t.Error("no mounts dir configured: nothing")
	}
	if m, d := readContributedMounts(filepath.Join(f.mountsDir, "nowhere"), f.root, f.workdir, nil, false); m != nil || d != nil {
		t.Error("a mounts dir that is not there: nothing")
	}
}

func TestContributedMounts_countsAreBounded(t *testing.T) {
	f := newMountFixture(t)
	host := f.folder(t, "c1", "a")
	var many []desc
	for i := 0; i < maxMountsPerComponent+5; i++ {
		many = append(many, desc{"host": host, "guest": f.guestRoot + "/m" + string(rune('a'+i%26)) + strings.Repeat("x", i/26)})
	}
	f.descriptor(t, "c1", descriptor(many...))
	mounts, dropped := f.read(false)
	if len(mounts) != maxMountsPerComponent || len(dropped) == 0 || !strings.Contains(dropped[len(dropped)-1].Reason, "more than 256 mounts") {
		t.Errorf("per component: %d mounts, dropped %d", len(mounts), len(dropped))
	}
	for i := 0; i < maxMountComponents+3; i++ {
		name := "comp-" + string(rune('a'+i))
		f.folder(t, name, "x")
		f.descriptor(t, name, descriptor())
	}
	_, dropped = f.read(false)
	extra := 0
	for _, d := range dropped {
		if strings.Contains(d.Reason, "more than 16 components") {
			extra++
		}
	}
	if extra == 0 {
		t.Errorf("components are bounded: %v", dropped)
	}
}

func TestParseMountRoots(t *testing.T) {
	roots, refused := ParseMountRoots("", "/Users/me")
	if !slices.Equal(roots, []string{"/Users/me"}) || len(refused) != 0 {
		t.Errorf("default is the home: %v %v", roots, refused)
	}
	roots, refused = ParseMountRoots("/opt/a, /opt/b\n/opt/c", "/Users/me")
	if !slices.Equal(roots, []string{"/opt/a", "/opt/b", "/opt/c"}) || len(refused) != 0 {
		t.Errorf("a list replaces the home: %v %v", roots, refused)
	}
	roots, refused = ParseMountRoots("/opt/a rel/path / /x/../y", "/Users/me")
	if !slices.Equal(roots, []string{"/opt/a"}) || len(refused) != 3 {
		t.Errorf("relative, root and unclean entries are refused: %v %v", roots, refused)
	}
	roots, _ = ParseMountRoots("relative", "/Users/me")
	if len(roots) != 0 {
		t.Errorf("a list with only refused entries does not fall back to the home: %v", roots)
	}
}

// --- through a drive -----------------------------------------------------------

func mountBridge(t *testing.T, sbx *fakeSandboxd, mode SandboxMode, prov corepkg.Provider) (*Bridge, mountFixture) {
	t.Helper()
	gw := newSandboxGateway(t, "room-1", false)
	f := newMountFixture(t)
	cfg := SandboxConfig{Mode: mode, URL: sbx.srv.URL, Token: sandboxAdminToken, VMExec: "/apps/sandbox/bnbloc-vmexec", Machine: "bnbloc", WorkspacesRoot: f.root, MountsDir: f.mountsDir, MountGuestRoots: []string{f.guestRoot}}
	return New(Config{GatewayBaseURL: gw.srv.URL, GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov), Sandbox: cfg}), f
}

func TestSandbox_componentMountsReachTheSpaceReadOnly_neverTheCoresPathMapping(t *testing.T) {
	sbx := newFakeSandboxd(t)
	sbx.caps = append(sbx.advertised(), sandbox.CapabilityReadOnlyMounts)
	prov := &sandboxProv{supports: true}
	prov.targets = []corepkg.Target{{ID: "c", Provider: "codex"}}
	b, f := mountBridge(t, sbx, SandboxAll, prov)
	own, shared := f.folder(t, "local-skills", "own"), f.folder(t, "local-skills", "shared")
	f.descriptor(t, "local-skills", descriptor(
		desc{"host": own, "guest": f.guestRoot + "/skills/own"},
		desc{"host": shared, "guest": f.guestRoot + "/skills/shared", "for": "all"},
	))
	ctx := context.Background()

	b.runBridgeJob(ctx, job("job-1", "alice")) // own drive, sandboxed by mode all
	b.runBridgeJob(ctx, job("job-2", "bob"))   // borrowed

	if len(sbx.ensures) != 2 {
		t.Fatalf("ensures = %d", len(sbx.ensures))
	}
	guestsOf := func(r sandbox.SpaceRequest) (g []string) {
		for _, m := range r.Mounts[1:] {
			if !m.ReadOnly || m.Kind != sandbox.MountShared {
				t.Errorf("a contributed mount must be a read-only shared mount: %+v", m)
			}
			g = append(g, strings.TrimPrefix(m.GuestPath, f.guestRoot+"/skills/"))
		}
		return
	}
	if got := guestsOf(sbx.ensures[0]); !slices.Equal(got, []string{"own", "shared"}) {
		t.Errorf("the owner's own run: %v", got)
	}
	if got := guestsOf(sbx.ensures[1]); !slices.Equal(got, []string{"shared"}) {
		t.Errorf("a borrowed run gets only what is offered to it: %v", got)
	}
	if sbx.ensures[0].Mounts[0].ReadOnly {
		t.Error("the workspace stays writable")
	}
	if got := prov.opts.Execution.Sandbox.Mounts; len(got) != 1 || got[0].Host != got[0].Guest {
		t.Errorf("the core's path mapping is the workspace only: %+v", got)
	}
}

func TestSandbox_componentMountsNeedTheBackendToEnforceReadOnly(t *testing.T) {
	sbx := newFakeSandboxd(t) // advertises no readOnlyMounts
	prov := &sandboxProv{supports: true}
	prov.targets = []corepkg.Target{{ID: "c", Provider: "codex"}}
	b, f := mountBridge(t, sbx, SandboxAll, prov)
	f.descriptor(t, "local-skills", descriptor(desc{"host": f.folder(t, "local-skills", "x"), "guest": f.guestRoot + "/skills/x"}))
	b.runBridgeJob(context.Background(), job("job-1", "alice"))
	if len(sbx.ensures) != 1 || len(sbx.ensures[0].Mounts) != 1 {
		t.Fatalf("a mount only read-only by the guest's goodwill is not offered: %+v", sbx.ensures)
	}
}

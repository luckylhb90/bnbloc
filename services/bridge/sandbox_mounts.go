package bridge

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/luckylhb90/bnbloc/component"
	corepkg "github.com/luckylhb90/bnbloc/core"
	sandbox "github.com/luckylhb90/bnbloc/sandbox/protocol"
)

// Mounts a component contributes to the sandbox space of a turn. The bridge
// knows no component and no skill: a component writes a descriptor file
//
//	<mounts dir>/<component>/mounts.json
//	{"contract":"bnbloc.sandbox.mounts/v1",
//	 "mounts":[{"host":"…","guest":"…","for":"own|borrowed|all"}]}
//
// and before each sandboxed turn the bridge adds the mounts that pass the
// checks below, read-only, to the turn's space. The checks assume the
// component cannot be trusted with the sandbox: it can offer read-only copies
// under its own folder of the one share, at paths under the member's chosen
// guest roots, and nothing else. A mount that fails is dropped and logged; the
// turn never fails because of a component.

// The descriptor (component.MountsFile) is the component contract's.
const (
	SandboxMountsContract = component.SandboxMountsContract
	mountsFileName        = component.MountsFileName
	maxMountComponents    = 16
	maxMountsPerComponent = 256
	maxMountsFileBytes    = 256 << 10
	// mountsHostDir is the folder under the workspaces root that holds every
	// component's own folder (the machine has exactly one share).
	mountsHostDir = ".mounts"
)

var componentName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// mountEnvName is the variable a per-space mount may set: its own namespace, so
// a component cannot rewrite anything the core or the sandbox sets.
var mountEnvName = regexp.MustCompile(`^BNBLOC_MOUNT_[A-Z0-9_]{1,48}$`)

// mountDrop is a mount that did not pass, and why.
type mountDrop struct {
	Component string
	Host      string
	Guest     string
	Reason    string
}

func (d mountDrop) String() string {
	return fmt.Sprintf("%s: %s -> %s: %s", d.Component, d.Host, d.Guest, d.Reason)
}

// MountsHostRoot is where a component's mounted folders must live.
func MountsHostRoot(workspacesRoot, component string) string {
	return filepath.Join(workspacesRoot, mountsHostDir, component)
}

// ParseMountRoots reads BNBLOC_SANDBOX_MOUNT_ROOTS (comma, space or newline
// separated absolute folders): the guest paths a component may mount under.
// Empty is the member's home folder, where skills and agent configuration
// live. A relative entry is refused (dropped; the caller logs it).
func ParseMountRoots(raw, home string) (roots, refused []string) {
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
		if !filepath.IsAbs(f) || filepath.Clean(f) != f || f == "/" {
			refused = append(refused, f)
			continue
		}
		roots = append(roots, f)
	}
	if len(roots) == 0 && home != "" && len(refused) == 0 {
		roots = []string{home}
	}
	return roots, refused
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	_, ok := corepkg.CleanRelPath(filepath.ToSlash(rel))
	return ok
}

func overlaps(a, b string) bool { return within(a, b) || within(b, a) }

func selects(forWhom string, borrowed bool) (bool, bool) {
	switch forWhom {
	case "", component.MountForOwn:
		return !borrowed, true
	case component.MountForBorrowed:
		return borrowed, true
	case component.MountForAll:
		return true, true
	}
	return false, false
}

// readContributedMounts reads every component's descriptor under mountsDir and
// returns the read-only mounts for a turn (own or borrowed) in a workdir, and
// what was dropped.
func readContributedMounts(mountsDir, workspacesRoot, workdir, space string, guestRoots []string, borrowed bool) (mounts []sandbox.SpaceMount, env map[string]string, dropped []mountDrop) {
	if mountsDir == "" {
		return nil, nil, nil
	}
	list, err := os.ReadDir(mountsDir)
	if err != nil {
		return nil, nil, nil
	}
	names := make([]string, 0, len(list))
	for _, e := range list {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	guests := map[string]bool{}
	taken := 0
	for _, name := range names {
		if !componentName.MatchString(name) {
			dropped = append(dropped, mountDrop{Component: name, Reason: "not a component name"})
			continue
		}
		if taken >= maxMountComponents {
			dropped = append(dropped, mountDrop{Component: name, Reason: fmt.Sprintf("more than %d components", maxMountComponents)})
			continue
		}
		taken++
		got, vars, drops := readComponentMounts(filepath.Join(mountsDir, name), name, workspacesRoot, workdir, space, guestRoots, borrowed, guests)
		mounts = append(mounts, got...)
		for k, v := range vars {
			if _, dup := env[k]; dup {
				dropped = append(dropped, mountDrop{Component: name, Reason: "the variable " + k + " is already set by another mount"})
				continue
			}
			if env == nil {
				env = map[string]string{}
			}
			env[k] = v
		}
		dropped = append(dropped, drops...)
	}
	return mounts, env, dropped
}

func readComponentMounts(dir, name, workspacesRoot, workdir, space string, guestRoots []string, borrowed bool, guests map[string]bool) (mounts []sandbox.SpaceMount, env map[string]string, dropped []mountDrop) {
	drop := func(host, guest, why string) { dropped = append(dropped, mountDrop{name, host, guest, why}) }
	file := filepath.Join(dir, mountsFileName)
	st, err := os.Lstat(file)
	if err != nil {
		return nil, nil, nil // no descriptor: nothing contributed
	}
	if !st.Mode().IsRegular() || st.Size() > maxMountsFileBytes {
		drop("", "", "the descriptor is not a plain file of at most 256 KiB")
		return
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		drop("", "", "the descriptor cannot be read")
		return
	}
	var f component.MountsFile
	if err := json.Unmarshal(raw, &f); err != nil || f.Contract != SandboxMountsContract {
		drop("", "", "the descriptor is not "+SandboxMountsContract)
		return
	}
	hostRoot := MountsHostRoot(workspacesRoot, name)
	realRoot, err := filepath.EvalSymlinks(hostRoot)
	if err != nil {
		drop("", "", "its folder under the workspaces root is missing")
		return
	}
	for i, m := range f.Mounts {
		if i >= maxMountsPerComponent {
			drop(m.Host, m.Guest, fmt.Sprintf("more than %d mounts", maxMountsPerComponent))
			break
		}
		want, known := selects(m.For, borrowed)
		if !known {
			drop(m.Host, m.Guest, fmt.Sprintf("for %q is not own, borrowed or all", m.For))
			continue
		}
		if !want {
			continue
		}
		if m.Env != "" && (!m.PerSpace || !mountEnvName.MatchString(m.Env)) {
			drop(m.Host, m.Guest, "env needs perSpace and a name of the form BNBLOC_MOUNT_<NAME>")
			continue
		}
		if !filepath.IsAbs(m.Host) || filepath.Clean(m.Host) != m.Host || !within(hostRoot, m.Host) || m.Host == hostRoot {
			drop(m.Host, m.Guest, "the host path is not inside the component's folder")
			continue
		}
		real, err := filepath.EvalSymlinks(m.Host)
		if err != nil || !within(realRoot, real) {
			drop(m.Host, m.Guest, "the host folder is missing or leaves the component's folder")
			continue
		}
		if st, err := os.Stat(real); err != nil || !st.IsDir() {
			drop(m.Host, m.Guest, "the host path is not a folder")
			continue
		}
		if !filepath.IsAbs(m.Guest) || filepath.Clean(m.Guest) != m.Guest {
			drop(m.Host, m.Guest, "the guest path is not an absolute, clean path")
			continue
		}
		hostPath, guestPath := m.Host, m.Guest
		if m.PerSpace {
			guestPath = m.Guest + "-" + space
		}
		if overlaps(workdir, guestPath) {
			drop(m.Host, m.Guest, "the guest path overlaps the workspace")
			continue
		}
		underRoot := false
		for _, r := range guestRoots {
			if guestPath != r && within(r, guestPath) {
				underRoot = true
				break
			}
		}
		if !underRoot {
			drop(m.Host, m.Guest, "the guest path is outside the allowed folders (BNBLOC_SANDBOX_MOUNT_ROOTS)")
			continue
		}
		if guests[guestPath] {
			drop(m.Host, m.Guest, "another mount already uses the guest path")
			continue
		}
		if m.PerSpace {
			tree, err := perSpaceTree(realRoot, space, m.Guest, real)
			if err != nil {
				drop(m.Host, m.Guest, "its tree for this space cannot be made: "+err.Error())
				continue
			}
			hostPath = tree
			if m.Env != "" {
				if _, dup := env[m.Env]; dup {
					drop(m.Host, m.Guest, "the variable "+m.Env+" is already set by another mount")
					continue
				}
				if env == nil {
					env = map[string]string{}
				}
				env[m.Env] = guestPath
			}
		}
		guests[guestPath] = true
		mounts = append(mounts, sandbox.SpaceMount{Kind: sandbox.MountShared, HostPath: hostPath, GuestPath: guestPath, ReadOnly: true})
	}
	return mounts, env, dropped
}

// contributedMounts is what the component descriptors add to a turn: nothing
// unless the sandbox is known to enforce read-only mounts (a mount that is
// only read-only by the guest's goodwill is not offered to a guest).
func (s *sandboxRuntime) contributedMounts(workdir, space string, borrowed bool) ([]sandbox.SpaceMount, map[string]string) {
	if s.cfg.MountsDir == "" {
		return nil, nil
	}
	mounts, env, dropped := readContributedMounts(s.cfg.MountsDir, s.cfg.WorkspacesRoot, workdir, space, s.cfg.MountGuestRoots, borrowed)
	for _, d := range dropped {
		slog.Warn("bnbloc bridge sandbox mount dropped", "event", "bnbloc.bridge.sandbox_mount_dropped", "mount", d.String())
	}
	if len(mounts) == 0 {
		return nil, nil
	}
	s.mu.Lock()
	enforced := s.readOnlyMounts
	s.mu.Unlock()
	if !enforced {
		slog.Warn("bnbloc bridge sandbox mounts not applied: the sandbox does not advertise readOnlyMounts", "event", "bnbloc.bridge.sandbox_mounts_unenforced", "count", len(mounts))
		return nil, nil
	}
	return mounts, env
}

package contentpkg

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// What a room package carries beyond its setup
// (docs/prd/2026-10-20-complete-room-capture.md §2.2), each a capability
// the package kind has, never a use case:
//
//   - INSTRUCTIONS.md, the room's instructions. Install makes them the new
//     room's expert (a private expert of the installer's, set as the room
//     expert: the room expert is the one place a room's instructions
//     live), so a package carries instructions or names requires.expert,
//     never both.
//   - appSnapshots: apps made in the room, each its site's files under
//     its folder "apps/<name>" (a site's entry, index.html or
//     dist/index.html, in it). Install copies each in as a new site, as
//     "Use this app" copies a work's: a fresh, empty data space.
//   - files: room files the saver ticked, each under "files/<name>".
//     Install restores them as the new room's files.
//
// The apps' sites and the files together stay within
// wire.MaxRoomCarryBytes, the files within wire.MaxRoomFiles; nothing else
// may lie in the bundle. A room package holds no agent configuration
// (core.ProtectedWorkspacePath) in an app or as a file.

// Folders of a room package's carried parts.
const (
	roomAppsDir  = "apps"
	roomFilesDir = "files"
)

// RoomCapture is what a room package's save took from its room (the
// gateway reads it as the saver, from the save's include): the apps made in
// it, each a title and its site, and room files, each a name and its
// bytes. It is never decoded from a request.
type RoomCapture struct {
	Apps  []CapturedApp
	Files []CapturedFile
}

// CapturedApp is an app made in the room: its title and its site's
// current version.
type CapturedApp struct {
	Title string
	Site  *tarbundle.Bundle
}

// CapturedFile is a room file: its name and its current version's bytes.
type CapturedFile struct {
	Name string
	Data []byte
}

// Size is what the capture adds to a package: its sites' and files'
// bytes.
func (c *RoomCapture) Size() int64 {
	if c == nil {
		return 0
	}
	var n int64
	for _, a := range c.Apps {
		n += int64(a.Site.Size())
	}
	for _, f := range c.Files {
		n += int64(len(f.Data))
	}
	return n
}

// errRoomCarryTooLarge: the carried parts are past the package's bound.
var errRoomCarryTooLarge = fmt.Errorf("the apps and files a room package carries are larger than %d bytes together", wire.MaxRoomCarryBytes)

// layoutRoomCarry lays out the instructions, the apps and files the form
// keeps from base (by folder and path, as the edited version holds them)
// and those the save captured, onto m and files.
func layoutRoomCarry(f Form, base *Package, m *Manifest, files map[string][]byte) error {
	if text := strings.TrimSpace(f.Instructions); text != "" {
		files[InstructionsFile] = []byte(text + "\n")
	}
	if (len(f.AppSnapshots) > 0 || len(f.Files) > 0) && base == nil {
		return errors.New("kept apps and files are a version's: name the version the form edits (baseVersion)")
	}
	taken := map[string]bool{}
	for _, a := range f.AppSnapshots {
		kept := false
		for _, p := range base.Bundle.Paths() {
			if strings.HasPrefix(p, a.Dir+"/") {
				files[p], _ = base.Bundle.File(p)
				kept = true
			}
		}
		if !kept || taken[a.Dir] {
			return fmt.Errorf("app %q: %s is not an app of the edited version", a.Title, a.Dir)
		}
		taken[a.Dir] = true
		m.AppSnapshots = append(m.AppSnapshots, wire.RoomAppSnapshot{Title: strings.TrimSpace(a.Title), Dir: a.Dir})
	}
	for _, fl := range f.Files {
		data, ok := base.Bundle.File(fl.Path)
		if !ok || taken[fl.Path] {
			return fmt.Errorf("file %q: %s is not a file of the edited version", fl.Name, fl.Path)
		}
		taken[fl.Path] = true
		files[fl.Path] = data
		m.Files = append(m.Files, wire.RoomPackageFile{Name: strings.TrimSpace(fl.Name), Path: fl.Path, SizeBytes: int64(len(data))})
	}
	c := f.Captured
	if c == nil {
		return nil
	}
	if c.Size() > wire.MaxRoomCarryBytes {
		return errRoomCarryTooLarge
	}
	for _, a := range c.Apps {
		dir := freePath(roomAppsDir, firstNonEmpty(NameFrom(a.Title), "app"), taken)
		for _, p := range a.Site.Paths() {
			files[dir+"/"+p], _ = a.Site.File(p)
		}
		m.AppSnapshots = append(m.AppSnapshots, wire.RoomAppSnapshot{Title: firstNonEmpty(strings.TrimSpace(a.Title), path.Base(dir)), Dir: dir})
	}
	for _, fl := range c.Files {
		name := strings.TrimSpace(path.Base(strings.ReplaceAll(fl.Name, "\\", "/")))
		p := freePath(roomFilesDir, safeReferenceName(name), taken)
		files[p] = fl.Data
		m.Files = append(m.Files, wire.RoomPackageFile{Name: firstNonEmpty(name, path.Base(p)), Path: p, SizeBytes: int64(len(fl.Data))})
	}
	return nil
}

// freePath is dir/base, or dir/<n>-base when that is taken, marked taken.
func freePath(dir, base string, taken map[string]bool) string {
	p := dir + "/" + base
	for n := 2; taken[p]; n++ {
		p = fmt.Sprintf("%s/%d-%s", dir, n, base)
	}
	taken[p] = true
	return p
}

// checkRoomCarry: the instructions (text within an expert's bound, never
// beside requires.expert), each app a folder of its own under apps/ with
// a site's entry, each file a path of its own under files/ of its stated
// size, within their counts and wire.MaxRoomCarryBytes together, no agent
// configuration in either, and nothing else in the bundle but the
// manifest and README.md. It sets p.Instructions.
func checkRoomCarry(p *Package) error {
	m := p.Manifest
	known := map[string]bool{ManifestFile: true, ReadmeFile: true}
	if _, ok := p.Bundle.File(InstructionsFile); ok {
		if m.Requires.Expert != "" {
			return errors.New("a room package carries its room's instructions or names its room expert (requires.expert), not both: the instructions become the room's expert")
		}
		text, err := readText(p.Bundle, InstructionsFile, maxInstructions)
		if err != nil {
			return err
		}
		if strings.TrimSpace(text) == "" {
			return fmt.Errorf("%s is empty", InstructionsFile)
		}
		p.Instructions, known[InstructionsFile] = text, true
	}
	if len(m.Apps)+len(m.AppSnapshots) > MaxRoomApps {
		return fmt.Errorf("at most %d apps (works and apps made in the room together)", MaxRoomApps)
	}
	if len(m.Files) > wire.MaxRoomFiles {
		return fmt.Errorf("at most %d files", wire.MaxRoomFiles)
	}
	var carried int64
	dirs := map[string]bool{}
	for _, a := range m.AppSnapshots {
		name, ok := strings.CutPrefix(a.Dir, roomAppsDir+"/")
		switch {
		case !ok || !ValidName(name):
			return fmt.Errorf("app %q: its folder is %s/<name> (lower-case letters, digits and hyphens)", a.Title, roomAppsDir)
		case dirs[a.Dir]:
			return fmt.Errorf("app folder %s is named twice", a.Dir)
		case strings.TrimSpace(a.Title) == "":
			return fmt.Errorf("app %s: a title", a.Dir)
		}
		if err := checkText("app title", a.Title, maxDisplayName, true); err != nil {
			return err
		}
		dirs[a.Dir] = true
		entry := false
		for _, f := range p.Bundle.Paths() {
			rel, in := strings.CutPrefix(f, a.Dir+"/")
			if !in {
				continue
			}
			if core.ProtectedWorkspacePath(rel) {
				return fmt.Errorf("%s: an app holds no agent configuration or room files", f)
			}
			data, _ := p.Bundle.File(f)
			carried += int64(len(data))
			known[f] = true
			entry = entry || rel == SiteEntry || rel == siteDistDir+"/"+SiteEntry
		}
		if !entry {
			return fmt.Errorf("app %s needs %s at its folder's root (or %s/%s)", a.Dir, SiteEntry, siteDistDir, SiteEntry)
		}
	}
	for _, fl := range m.Files {
		dir, base := path.Split(fl.Path)
		data, ok := p.Bundle.File(fl.Path)
		switch {
		case dir != roomFilesDir+"/" || base == "" || known[fl.Path]:
			return fmt.Errorf("file %q: a path of its own under %s/", fl.Path, roomFilesDir)
		case !ok:
			return fmt.Errorf("file %s is not in the bundle", fl.Path)
		case int64(len(data)) != fl.SizeBytes:
			return fmt.Errorf("file %s: %d bytes, not %d", fl.Path, len(data), fl.SizeBytes)
		case strings.TrimSpace(fl.Name) == "" || strings.ContainsAny(fl.Name, `/\`) || core.ProtectedWorkspacePath(fl.Name):
			return fmt.Errorf("file %s: a name of one file (no folder, no agent configuration)", fl.Path)
		}
		if err := checkText("file name", fl.Name, maxReferenceName, true); err != nil {
			return err
		}
		carried += fl.SizeBytes
		known[fl.Path] = true
	}
	if carried > wire.MaxRoomCarryBytes {
		return errRoomCarryTooLarge
	}
	for _, f := range p.Bundle.Paths() {
		if !known[f] {
			return fmt.Errorf("%s: a room package holds %s, %s, %s, its apps under %s/ and its files under %s/", f, ManifestFile, ReadmeFile, InstructionsFile, roomAppsDir, roomFilesDir)
		}
	}
	return nil
}

// RoomSite is a carried app's site as a room copies it: its folder read as
// a site snapshot, through WorkSite (the one rule of a site's entry and of
// a dist/ build's files).
func RoomSite(p *Package, a wire.RoomAppSnapshot) (map[string][]byte, error) {
	files := map[string][]byte{}
	for _, f := range p.Bundle.Paths() {
		if rel, in := strings.CutPrefix(f, a.Dir+"/"); in {
			files[rel], _ = p.Bundle.File(f)
		}
	}
	raw, err := tarbundle.PackFiles(files, tarbundle.SiteLimits)
	if err != nil {
		return nil, err
	}
	site, err := tarbundle.Parse(raw, "", tarbundle.SiteLimits)
	if err != nil {
		return nil, err
	}
	return WorkSite(&Package{Manifest: Manifest{Kind: KindWork, Type: wire.WorkTypeApp}, Bundle: site}), nil
}

// RoomFile is a carried file's bytes.
func RoomFile(p *Package, f wire.RoomPackageFile) []byte {
	data, _ := p.Bundle.File(f.Path)
	return data
}

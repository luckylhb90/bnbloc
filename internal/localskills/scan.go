package localskills

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
)

// maxSkillMD bounds the SKILL.md read for its frontmatter.
const maxSkillMD = 64 << 10

// File is one file of a skill, relative to its folder.
type File struct {
	Rel     string
	Abs     string
	Size    int64
	Mode    fs.FileMode
	ModTime time.Time
}

// Skill is one skill folder as found: what it is, whether it can be used and,
// if not, why (in words the member reads).
type Skill struct {
	Name        string
	Description string
	// Source is the folder the member named, Dir the skill's own folder in it.
	Source string
	Dir    string
	Files  int
	Bytes  int64
	// HasScripts is true when it holds anything the policy counts as a script.
	HasScripts bool
	Eligible   bool
	Reason     string
	// PublishProblem is why the skill cannot be added to the library ("" when
	// it can): the library takes file names of letters, digits, '.', '_' and
	// '-' only. It does not stop the skill reaching the sandbox.
	PublishProblem string
	// Hash is the digest of the skill's files (names, sizes, contents), set when
	// the skill could be read whole: the same hash is the same skill.
	Hash string
}

// Scanner reads folders by a Policy.
type Scanner struct {
	p     Policy
	rules []compiledRule
}

// NewScanner prepares the policy's secret rules.
func NewScanner(p Policy) (*Scanner, error) {
	rules, err := compileRules(p.SecretRules)
	if err != nil {
		return nil, err
	}
	return &Scanner{p: p, rules: rules}, nil
}

func matchAny(patterns []string, name string) bool {
	for _, pat := range patterns {
		if ok, _ := path.Match(pat, name); ok {
			return true
		}
	}
	return false
}

func within(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	_, ok := core.CleanRelPath(filepath.ToSlash(rel))
	return ok
}

// walk lists the files of a skill folder that the policy lets through, and the
// first thing that makes the folder unusable as a whole ("" when none): a link
// that leaves the folder or points at a folder, more files or bytes than the
// limits.
func (s *Scanner) walk(dir string) (files []File, problem string) {
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, fmt.Sprintf("cannot read the folder: %v", err)
	}
	var total int64
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			problem = fmt.Sprintf("cannot read %s: %v", relOf(root, p), err)
			return fs.SkipAll
		}
		if p == root {
			return nil
		}
		if matchAny(s.p.Exclude, d.Name()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		abs := p
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			target, terr := filepath.EvalSymlinks(p)
			if terr != nil || !within(root, target) {
				problem = fmt.Sprintf("%s is a link that leaves the skill's folder", relOf(root, p))
				return fs.SkipAll
			}
			ti, serr := os.Stat(target)
			if serr != nil || ti.IsDir() {
				problem = fmt.Sprintf("%s is a link to a folder", relOf(root, p))
				return fs.SkipAll
			}
			abs, info = target, ti
		}
		if d.IsDir() || !info.Mode().IsRegular() {
			return nil
		}
		if len(files) >= s.p.Limits.Files {
			problem = fmt.Sprintf("more than %d files", s.p.Limits.Files)
			return fs.SkipAll
		}
		total += info.Size()
		if total > s.p.Limits.Bytes {
			problem = fmt.Sprintf("more than %d MiB", s.p.Limits.Bytes>>20)
			return fs.SkipAll
		}
		files = append(files, File{Rel: filepath.ToSlash(relOf(root, p)), Abs: abs, Size: info.Size(), Mode: info.Mode(), ModTime: info.ModTime()})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Rel < files[j].Rel })
	return files, problem
}

func relOf(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return r
}

// isScript says whether a file counts as a script by the policy.
func (s *Scanner) isScript(f File) bool {
	if f.Mode&0o111 != 0 {
		return true
	}
	if slices.Contains(s.p.ScriptExts, strings.ToLower(path.Ext(f.Rel))) {
		return true
	}
	for _, part := range strings.Split(path.Dir(f.Rel), "/") {
		if slices.Contains(s.p.ScriptDirs, part) {
			return true
		}
	}
	return false
}

// credential reports why a file looks like it holds a credential ("" when it
// does not): its name, or its first bytes (a binary file is not read).
func (s *Scanner) credential(f File) string {
	if matchAny(s.p.SecretNames, path.Base(f.Rel)) {
		return fmt.Sprintf("%s looks like a credential file", f.Rel)
	}
	fh, err := os.Open(f.Abs)
	if err != nil {
		return ""
	}
	defer fh.Close()
	head, _ := io.ReadAll(io.LimitReader(fh, s.p.Limits.SecretScanBytes))
	if bytes.IndexByte(head[:min(len(head), 8192)], 0) >= 0 {
		return ""
	}
	for _, r := range s.rules {
		if r.re.Match(head) {
			return fmt.Sprintf("%s looks like it holds %s", f.Rel, r.name)
		}
	}
	return ""
}

// digest is the hash of a skill's files: the same names and bytes, the same hash.
func digest(files []File) (string, error) {
	h := sha256.New()
	for _, f := range files {
		fh, err := os.Open(f.Abs)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", f.Rel, f.Size)
		_, err = io.Copy(h, fh)
		fh.Close()
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Inspect reads one skill folder.
func (s *Scanner) Inspect(source, dir string) Skill {
	sk := Skill{Name: filepath.Base(dir), Source: source, Dir: dir}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		sk.Reason = "cannot read the folder"
		return sk
	}
	raw, err := os.ReadFile(filepath.Join(root, "SKILL.md"))
	if err != nil {
		sk.Reason = "no SKILL.md"
		return sk
	}
	if len(raw) > maxSkillMD {
		raw = raw[:maxSkillMD]
	}
	fm, _ := contentpkg.ParseFrontmatter(string(raw))
	if n := strings.TrimSpace(fm["name"]); n != "" {
		sk.Name = n
	}
	sk.Description = strings.TrimSpace(fm["description"])
	files, problem := s.walk(dir)
	sk.Files = len(files)
	for _, f := range files {
		sk.Bytes += f.Size
		if s.isScript(f) {
			sk.HasScripts = true
		}
	}
	switch {
	case !contentpkg.ValidName(sk.Name):
		sk.Reason = fmt.Sprintf("the name %q is not lower-case letters, digits and hyphens", sk.Name)
	case sk.Description == "":
		sk.Reason = "SKILL.md has no description"
	case problem != "":
		sk.Reason = problem
	default:
		for _, f := range files {
			if why := s.credential(f); why != "" {
				sk.Reason = why
				break
			}
		}
	}
	if problem == "" {
		sk.Hash, _ = digest(files)
	}
	for _, f := range files {
		if !core.ValidBundlePath(sk.Name + "/" + f.Rel) {
			sk.PublishProblem = fmt.Sprintf("%s: the library takes file names of letters, digits, '.', '_' and '-' only", f.Rel)
			break
		}
	}
	sk.Eligible = sk.Reason == ""
	return sk
}

// ScanSource finds the skills under a source folder: every folder with a
// SKILL.md, looking up to ScanDepth levels down and at most ScanEntries entries
// (a bigger source is cut; the bool says so). A folder that holds a SKILL.md is
// a skill and is not looked into.
func (s *Scanner) ScanSource(source string) (skills []Skill, truncated bool, err error) {
	entries := 0
	var visit func(dir string, depth int)
	visit = func(dir string, depth int) {
		list, rerr := os.ReadDir(dir)
		if rerr != nil {
			if dir == source {
				err = rerr
			}
			return
		}
		for _, e := range list {
			if entries >= s.p.Limits.ScanEntries {
				truncated = true
				return
			}
			entries++
			p := filepath.Join(dir, e.Name())
			if st, serr := os.Stat(p); serr != nil || !st.IsDir() || matchAny(s.p.Exclude, e.Name()) {
				continue
			}
			if _, serr := os.Stat(filepath.Join(p, "SKILL.md")); serr == nil {
				skills = append(skills, s.Inspect(source, p))
			} else if depth < s.p.Limits.ScanDepth {
				visit(p, depth+1)
			}
		}
	}
	visit(source, 1)
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills, truncated, err
}

// Files lists the files of a skill that a copy or a pack carries.
func (s *Scanner) Files(dir string) ([]File, error) {
	files, problem := s.walk(dir)
	if problem != "" {
		return nil, fmt.Errorf("%s", problem)
	}
	return files, nil
}

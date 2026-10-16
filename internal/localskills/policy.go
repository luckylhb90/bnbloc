// Package localskills is a member's own skills made usable elsewhere: found in
// the folders they name, checked, copied read-only for a sandbox, packed to
// publish. It is the one owner of those rules and holds none of them in code:
// the agent folders to look for links in, what is never copied, what looks like
// a credential, what counts as a script and every limit are the Policy, a data
// file (policy.default.json) a member's own policy file adds to or replaces.
// The package knows nothing of any agent, of the sandbox or of the Library; the
// component that applies it (cmd/bnbloc-local-skills) does.
package localskills

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
)

//go:embed policy.default.json
var defaultPolicyJSON []byte

// Limits are every number the package works to. A zero in an overriding policy
// means "keep the base's".
type Limits struct {
	// Files and Bytes bound one skill (the Library's import limits).
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
	// Skills bounds the skills mirrored at once; MirrorBytes the mirror.
	Skills      int   `json:"skills"`
	MirrorBytes int64 `json:"mirrorBytes"`
	// ScanDepth and ScanEntries bound reading one source folder.
	ScanDepth   int `json:"scanDepth"`
	ScanEntries int `json:"scanEntries"`
	// SecretScanBytes is how much of each file is read for credentials.
	SecretScanBytes int64 `json:"secretScanBytes"`
}

// SecretRule is one pattern that marks a file as holding a credential.
type SecretRule struct {
	Name    string `json:"name"`
	Pattern string `json:"pattern"`
}

// Policy is the data the rules read.
type Policy struct {
	// AgentDirs are the folders agent CLIs read skills from; a skill is also
	// reachable at an entry of one that links to it.
	AgentDirs []string `json:"agentDirs"`
	// Exclude are name patterns never copied (matched against each path part).
	Exclude []string `json:"exclude"`
	// ScriptExts and ScriptDirs say what counts as a script.
	ScriptExts []string `json:"scriptExts"`
	ScriptDirs []string `json:"scriptDirs"`
	// SecretNames are file name patterns, SecretRules content patterns.
	SecretNames []string     `json:"secretNames"`
	SecretRules []SecretRule `json:"secretRules"`
	Limits      Limits       `json:"limits"`
}

// DefaultPolicy is the embedded policy.
func DefaultPolicy() Policy {
	var p Policy
	if err := json.Unmarshal(defaultPolicyJSON, &p); err != nil {
		panic(fmt.Sprintf("localskills: embedded policy: %v", err))
	}
	return p
}

func union(a, b []string) []string {
	out := slices.Clone(a)
	for _, s := range b {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// Merge is p with o's lists added after p's (each entry once) and o's non-zero
// limits in place of p's.
func (p Policy) Merge(o Policy) Policy {
	out := p
	out.AgentDirs = union(p.AgentDirs, o.AgentDirs)
	out.Exclude = union(p.Exclude, o.Exclude)
	out.ScriptExts = union(p.ScriptExts, o.ScriptExts)
	out.ScriptDirs = union(p.ScriptDirs, o.ScriptDirs)
	out.SecretNames = union(p.SecretNames, o.SecretNames)
	out.SecretRules = slices.Clone(p.SecretRules)
	for _, r := range o.SecretRules {
		if !slices.Contains(out.SecretRules, r) {
			out.SecretRules = append(out.SecretRules, r)
		}
	}
	l, ol := &out.Limits, o.Limits
	if ol.Files > 0 {
		l.Files = ol.Files
	}
	if ol.Bytes > 0 {
		l.Bytes = ol.Bytes
	}
	if ol.Skills > 0 {
		l.Skills = ol.Skills
	}
	if ol.MirrorBytes > 0 {
		l.MirrorBytes = ol.MirrorBytes
	}
	if ol.ScanDepth > 0 {
		l.ScanDepth = ol.ScanDepth
	}
	if ol.ScanEntries > 0 {
		l.ScanEntries = ol.ScanEntries
	}
	if ol.SecretScanBytes > 0 {
		l.SecretScanBytes = ol.SecretScanBytes
	}
	return out
}

// LoadPolicy is the default policy with the file's added (an empty path is
// the default alone). A file that cannot be read or parsed is an error: a
// member's policy is never silently ignored.
func LoadPolicy(path string) (Policy, error) {
	base := DefaultPolicy()
	if path == "" {
		return base, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Policy{}, fmt.Errorf("policy file: %w", err)
	}
	var o Policy
	if err := json.Unmarshal(raw, &o); err != nil {
		return Policy{}, fmt.Errorf("policy file %s: %w", path, err)
	}
	merged := base.Merge(o)
	if _, err := compileRules(merged.SecretRules); err != nil {
		return Policy{}, err
	}
	return merged, nil
}

type compiledRule struct {
	name string
	re   *regexp.Regexp
}

func compileRules(rules []SecretRule) ([]compiledRule, error) {
	out := make([]compiledRule, 0, len(rules))
	for _, r := range rules {
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return nil, fmt.Errorf("secret rule %q: %w", r.Name, err)
		}
		out = append(out, compiledRule{name: r.Name, re: re})
	}
	return out, nil
}

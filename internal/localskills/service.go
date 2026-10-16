package localskills

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/core"
)

// ProviderContract names the API a Settings view renders and a component
// serves (api.go).
const ProviderContract = "bnbloc.skills.provider/v1"

// MountsContract names the descriptor file the bridge reads
// (docs/architecture/sandbox-mounts.md).
const MountsContract = "bnbloc.sandbox.mounts/v1"

// Limits on what a settings update may carry.
const (
	maxSources   = 16
	maxAgentDirs = 32
	maxChoices   = 512
	maxAllowed   = 32
)

// Service is the component: it holds a member's settings, scans their sources,
// keeps the mirror and the mount descriptor up to date, and packs a skill. It
// runs on the member's machine as the member.
type Service struct {
	// Home is the member's home folder; SettingsFile where the settings live.
	Home         string
	SettingsFile string
	// MountsFile is the descriptor the bridge reads ("" = no mounts are
	// published); MirrorRoot the folder (under the workspaces root) holding the
	// copies.
	MountsFile string
	MirrorRoot string

	mu     sync.Mutex
	policy Policy
	loaded bool
	// notes are what the last publish left out, for State.
	notes []string
}

// SourceState is one source folder as the Settings view shows it.
type SourceState struct {
	Path      string `json:"path"`
	Error     string `json:"error,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// SkillState is one skill with the member's choice.
type SkillState struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	HasScripts  bool   `json:"hasScripts"`
	Files       int    `json:"files"`
	Bytes       int64  `json:"bytes"`
	Eligible    bool   `json:"eligible"`
	Reason      string `json:"reason,omitempty"`
	// PublishProblem is why it cannot be added to the library, when that is
	// all that is wrong with it.
	PublishProblem string `json:"publishProblem,omitempty"`
	// Finding is the file a content rule flagged, with its hash and whether the
	// member may allow it (a file flagged by its name never is); Allowed are the
	// flagged files the member let through for the sandbox.
	Finding *FindingState `json:"finding,omitempty"`
	Allowed []string      `json:"allowed,omitempty"`
	// Hidden is the source whose skill of the same name is the one used.
	HiddenBy string `json:"hiddenBy,omitempty"`
	Hash     string `json:"hash,omitempty"`
	Choice   Choice `json:"choice"`
	// Changed is true when the skill was published and its files differ now.
	Changed bool `json:"changed,omitempty"`
}

// FindingState is a flagged file as the Settings view shows it.
type FindingState struct {
	File      string `json:"file"`
	Hash      string `json:"hash"`
	Allowable bool   `json:"allowable"`
}

// State is the provider API's answer to "what do you have".
type State struct {
	Contract string `json:"contract"`
	// Settings are the member's own settings as saved: what an update edits
	// and sends back whole.
	Settings  Settings      `json:"settings"`
	Enabled   bool          `json:"enabled"`
	Sources   []SourceState `json:"sources"`
	AgentDirs []string      `json:"agentDirs"`
	// Suggested are folders worth adding that exist and are not added.
	Suggested []string     `json:"suggested"`
	Skills    []SkillState `json:"skills"`
	// Notes say what the last publish left out and why.
	Notes []string `json:"notes"`
}

func (s *Service) policyLocked(settings Settings) (Policy, error) {
	path := ""
	if settings.PolicyFile != "" {
		path = ExpandHome(settings.PolicyFile, s.Home)
	}
	return LoadPolicy(path)
}

func (s *Service) load() (Settings, Policy, error) {
	settings, err := LoadSettings(s.SettingsFile)
	if err != nil {
		return Settings{}, Policy{}, err
	}
	p, err := s.policyLocked(settings)
	return settings, p, err
}

// State reads the settings and every source.
func (s *Service) State() (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateLocked()
}

func (s *Service) stateLocked() (State, error) {
	settings, policy, err := s.load()
	if err != nil {
		return State{}, err
	}
	sc, err := ScannerFor(policy, settings)
	if err != nil {
		return State{}, err
	}
	if settings.Sources == nil {
		settings.Sources = []string{}
	}
	if settings.AgentDirs == nil {
		settings.AgentDirs = []string{}
	}
	if settings.Skills == nil {
		settings.Skills = map[string]Choice{}
	}
	st := State{Contract: ProviderContract, Settings: settings, Enabled: settings.Enabled, AgentDirs: union(policy.AgentDirs, settings.AgentDirs), Notes: slicesOrEmpty(s.notes)}
	owner := map[string]string{}
	for _, src := range settings.Sources {
		path := ExpandHome(src, s.Home)
		ss := SourceState{Path: src}
		skills, truncated, err := sc.ScanSource(path)
		if err != nil {
			ss.Error = err.Error()
		}
		ss.Truncated = truncated
		st.Sources = append(st.Sources, ss)
		for _, sk := range skills {
			row := SkillState{Name: sk.Name, Description: sk.Description, Source: src, HasScripts: sk.HasScripts, Files: sk.Files, Bytes: sk.Bytes,
				Eligible: sk.Eligible, Reason: sk.Reason, PublishProblem: sk.PublishProblem, Allowed: sk.Allowed, Hash: sk.Hash, Choice: settings.Skills[sk.Name]}
			if sk.Finding != nil {
				row.Finding = &FindingState{File: sk.Finding.File, Hash: sk.Finding.Hash, Allowable: sk.Finding.Allowable}
			}
			if first, taken := owner[sk.Name]; taken {
				row.HiddenBy = first
				row.Choice = Choice{}
			} else {
				owner[sk.Name] = src
			}
			if p := row.Choice.Published; p != nil && sk.Hash != "" && p.Hash != sk.Hash {
				row.Changed = true
			}
			st.Skills = append(st.Skills, row)
		}
	}
	for _, sug := range policy.SuggestedSources {
		if _, err := os.Stat(ExpandHome(sug, s.Home)); err == nil && !containsPath(settings.Sources, sug) {
			st.Suggested = append(st.Suggested, sug)
		}
	}
	if st.Sources == nil {
		st.Sources = []SourceState{}
	}
	if st.Skills == nil {
		st.Skills = []SkillState{}
	}
	if st.Suggested == nil {
		st.Suggested = []string{}
	}
	return st, nil
}

func slicesOrEmpty(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func containsPath(list []string, p string) bool {
	for _, x := range list {
		if x == p {
			return true
		}
	}
	return false
}

// Validate checks a settings update.
func (s *Service) Validate(in Settings) error {
	if len(in.Sources) > maxSources || len(in.AgentDirs) > maxAgentDirs || len(in.Skills) > maxChoices {
		return fmt.Errorf("too many entries (at most %d sources, %d agent folders, %d choices)", maxSources, maxAgentDirs, maxChoices)
	}
	for _, p := range append(append([]string{}, in.Sources...), in.AgentDirs...) {
		if p == "" || !(filepath.IsAbs(ExpandHome(p, s.Home))) {
			return fmt.Errorf("%q is not an absolute folder", p)
		}
	}
	if in.PolicyFile != "" && !filepath.IsAbs(ExpandHome(in.PolicyFile, s.Home)) {
		return fmt.Errorf("the policy file %q is not an absolute path", in.PolicyFile)
	}
	for name, c := range in.Skills {
		if len(c.Allowed) > maxAllowed {
			return fmt.Errorf("the skill %s allows more than %d files", name, maxAllowed)
		}
		for _, a := range c.Allowed {
			if clean, ok := core.CleanRelPath(a.File); !ok || clean != a.File || len(a.Hash) != 64 {
				return fmt.Errorf("%q is not an allowable file of %s", a.File, name)
			}
		}
	}
	for name := range in.Skills {
		if clean, ok := core.CleanRelPath(name); !ok || clean != name || strings.Contains(name, "/") {
			return fmt.Errorf("%q is not a skill name", name)
		}
	}
	return nil
}

// Apply saves the settings and brings the mirror and the mount descriptor up to
// date; the new State is returned.
func (s *Service) Apply(in Settings) (State, error) {
	if err := s.Validate(in); err != nil {
		return State{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.policyLocked(in); err != nil {
		return State{}, err
	}
	if err := in.Save(s.SettingsFile); err != nil {
		return State{}, err
	}
	if err := s.publishLocked(); err != nil {
		return State{}, err
	}
	return s.stateLocked()
}

// Publish brings the mirror and the descriptor up to date (the component runs
// it at start and every few seconds, so an edited skill reaches the next turn).
func (s *Service) Publish() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.publishLocked()
}

type descriptorMount struct {
	Host  string `json:"host"`
	Guest string `json:"guest"`
	For   string `json:"for"`
}

func (s *Service) publishLocked() error {
	settings, policy, err := s.load()
	if err != nil {
		return err
	}
	if s.MountsFile == "" || s.MirrorRoot == "" {
		s.notes = []string{"this component was not given a folder to mount from, so it publishes no mounts"}
		return nil
	}
	in := PlanInput{Settings: settings, Policy: policy, Home: s.Home, MirrorRoot: s.MirrorRoot}
	own, err := BuildPlan(in)
	if err != nil {
		return err
	}
	in.Borrowed = true
	borrowed, err := BuildPlan(in)
	if err != nil {
		return err
	}
	inBorrowed := map[Mount]bool{}
	for _, m := range borrowed.Mounts {
		inBorrowed[m] = true
	}
	mounts := []descriptorMount{}
	for _, m := range own.Mounts {
		kind := "own"
		if inBorrowed[m] {
			kind = "all"
		}
		mounts = append(mounts, descriptorMount{Host: m.Host, Guest: m.Guest, For: kind})
	}
	s.notes = own.Notes
	sort.Strings(s.notes)
	return writeJSONAtomic(s.MountsFile, map[string]any{"contract": MountsContract, "mounts": mounts}, 0o644)
}

func writeJSONAtomic(path string, v any, mode os.FileMode) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Pack finds a skill by name (the first source's wins) and packs it.
func (s *Service) Pack(name string) (archive []byte, hash string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	settings, policy, err := s.load()
	if err != nil {
		return nil, "", err
	}
	sc, err := ScannerFor(policy, settings)
	if err != nil {
		return nil, "", err
	}
	for _, src := range settings.Sources {
		skills, _, _ := sc.ScanSource(ExpandHome(src, s.Home))
		for _, sk := range skills {
			if sk.Name == name {
				b, err := sc.Pack(sk)
				return b, sk.Hash, err
			}
		}
	}
	return nil, "", fmt.Errorf("no skill named %q in the folders", name)
}

// Run publishes now and then every interval until stop is closed, so edits to a
// skill reach the mirror within the interval.
func (s *Service) Run(interval time.Duration, stop <-chan struct{}, onError func(error)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := s.Publish(); err != nil && onError != nil {
			onError(err)
		}
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}

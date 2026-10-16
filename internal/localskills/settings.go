package localskills

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Choice is what a member chose for one skill; both are false until ticked.
type Choice struct {
	// VM puts the skill in the sandbox the member's own runs use.
	VM bool `json:"vm"`
	// Borrowers also puts it in the sandbox of a run another member borrows.
	Borrowers bool `json:"borrowers"`
}

// Settings are a member's, kept on their machine. The zero value is the
// default: off, no source, nothing ticked.
type Settings struct {
	Enabled bool `json:"enabled"`
	// Sources are the folders of skill folders the member named.
	Sources []string `json:"sources"`
	// AgentDirs are agent skill folders the member adds to the policy's.
	AgentDirs []string `json:"agentDirs"`
	// Skills are the choices, by skill name.
	Skills map[string]Choice `json:"skills"`
	// PolicyFile is a member's policy added to the default (optional).
	PolicyFile string `json:"policyFile,omitempty"`
}

// LoadSettings reads the settings file; a file that does not exist is the
// default (off).
func LoadSettings(file string) (Settings, error) {
	raw, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, err
	}
	var s Settings
	if err := json.Unmarshal(raw, &s); err != nil {
		return Settings{}, fmt.Errorf("local skills settings %s: %w", file, err)
	}
	return s, nil
}

// Save writes the settings file: owner-only, replaced whole.
func (s Settings) Save(file string) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}

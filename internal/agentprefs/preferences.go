// Package agentprefs remembers the last submitted coding-agent selection.
package agentprefs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/vm"
)

// Selection is the complete answer, including an explicitly empty selection.
type Selection struct {
	Claude   bool `json:"claude"`
	Codex    bool `json:"codex"`
	OpenCode bool `json:"opencode"`
	Pi       bool `json:"pi"`
}

func FromConfig(c vm.CreateConfig) Selection {
	return Selection{Claude: c.WithClaude, Codex: c.WithCodex, OpenCode: c.WithOpenCode, Pi: c.WithPi}
}

func (s Selection) Apply(c *vm.CreateConfig) {
	c.WithClaude = s.Claude
	c.WithCodex = s.Codex
	c.WithOpenCode = s.OpenCode
	c.WithPi = s.Pi
}

func preferencesPath() (string, error) {
	root := os.Getenv("XDG_DATA_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(root, "sandbar", "agent-preferences.json"), nil
}

// Load distinguishes a saved all-off selection from absent preferences.
func Load() (Selection, bool, error) {
	p, err := preferencesPath()
	if err != nil {
		return Selection{}, false, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return Selection{}, false, nil
	}
	if err != nil {
		return Selection{}, false, err
	}
	var record struct {
		Version   int       `json:"version"`
		Selection Selection `json:"selection"`
	}
	if err = json.Unmarshal(b, &record); err != nil {
		return Selection{}, false, fmt.Errorf("read agent preferences: %w", err)
	}
	if record.Version != 1 {
		return Selection{}, false, fmt.Errorf("unsupported agent preferences version %d", record.Version)
	}
	return record.Selection, true, nil
}

// Save atomically replaces the submitted selection in host-local state.
func Save(s Selection) error {
	p, err := preferencesPath()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	b, err := json.Marshal(struct {
		Version   int       `json:"version"`
		Selection Selection `json:"selection"`
	}{1, s})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".agent-preferences-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), p)
}

// LoadOrMigrate seeds absent preferences only from a legacy base stamp. Modern
// base stamps contain no agent information and must not override defaults.
func LoadOrMigrate(hf lima.HostFiles, baseName string) (Selection, error) {
	s, ok, err := Load()
	if err != nil || ok {
		return s, err
	}
	s = FromConfig(vm.DefaultCreateConfig())
	if hf != nil {
		if set, legacy := provision.LegacyBaseToolset(hf, baseName); legacy {
			s = Selection{Claude: set["claude"], Codex: set["codex"]}
			// Re-read after the potentially slow remote stamp read: a submitted local
			// choice takes precedence over migration.
			if current, found, err := Load(); err != nil {
				return Selection{}, err
			} else if found {
				return current, nil
			}
			if err := Save(s); err != nil {
				return Selection{}, err
			}
		}
	}
	return s, nil
}

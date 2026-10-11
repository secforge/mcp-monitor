// Package store persists monitors per project in one file per machine,
// laid out like mcp-hub-client's connection store: state.json under the OS
// config directory (or MCP_MONITOR_STATE_DIR), mode 0600 in a 0700
// directory, every access a full read-modify-write under a cross-process
// advisory lock.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// EnvDir overrides the directory holding state.json, its lock and the
// monitors' output files.
const EnvDir = "MCP_MONITOR_STATE_DIR"

// Entry is one persisted monitor.
type Entry struct {
	ID          string    `json:"id"`
	Command     string    `json:"command"`
	Description string    `json:"description"`
	Created     time.Time `json:"created"`
	Output      string    `json:"output"`
	// Owner is the MCP server process currently running the monitor,
	// identified by pid and start time so a recycled pid is not mistaken
	// for it. An entry whose owner is gone is resumed by the next server
	// started for the same project.
	OwnerPID   int    `json:"ownerPid,omitempty"`
	OwnerStart string `json:"ownerStart,omitempty"`
	// PGID and GroupStart identify the monitor's process group, so a group
	// left behind by an owner that was killed can be cleaned up.
	PGID       int    `json:"pgid,omitempty"`
	GroupStart string `json:"groupStart,omitempty"`
}

// State is the whole file: project -> monitor id -> entry.
type State struct {
	Projects map[string]map[string]*Entry `json:"projects"`
}

// Monitors returns the project's map, creating it when create is set.
func (s *State) Monitors(project string, create bool) map[string]*Entry {
	if s.Projects == nil {
		if !create {
			return nil
		}
		s.Projects = map[string]map[string]*Entry{}
	}
	m := s.Projects[project]
	if m == nil && create {
		m = map[string]*Entry{}
		s.Projects[project] = m
	}
	return m
}

// Dir is where state.json lives. Never the working directory.
func Dir() string {
	if d := os.Getenv(EnvDir); d != "" {
		return d
	}
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "mcp-monitor")
	}
	return filepath.Join(os.TempDir(), "mcp-monitor")
}

// OutputPath is the output file for one monitor.
func OutputPath(id string) string {
	return filepath.Join(Dir(), "output", id+".log")
}

// Update runs fn on the state under an exclusive lock and saves the result
// unless fn returns an error.
func Update(fn func(*State) error) error {
	return withLock(true, func(d string) error {
		s, err := load(d)
		if err != nil {
			return err
		}
		if err := fn(&s); err != nil {
			return err
		}
		return save(d, s)
	})
}

// Read runs fn on the state under a shared lock.
func Read(fn func(*State) error) error {
	return withLock(false, func(d string) error {
		s, err := load(d)
		if err != nil {
			return err
		}
		return fn(&s)
	})
}

func withLock(exclusive bool, fn func(d string) error) error {
	d := Dir()
	if err := ensureDir(d); err != nil {
		return err
	}
	fl := flock.New(filepath.Join(d, "state.json.lock"))
	var err error
	if exclusive {
		err = fl.Lock()
	} else {
		err = fl.RLock()
	}
	if err != nil {
		return err
	}
	defer fl.Unlock()
	return fn(d)
}

func ensureDir(d string) error {
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	if fi, err := os.Stat(d); err == nil && fi.Mode().Perm() != 0o700 {
		return os.Chmod(d, 0o700)
	}
	return nil
}

func load(d string) (State, error) {
	p := filepath.Join(d, "state.json")
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("reading %s: %w", p, err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		// Left in place: overwriting it would silently drop every
		// project's monitors.
		return State{}, fmt.Errorf("parsing %s: %v (repair or remove it; nothing here will overwrite it)", p, err)
	}
	return s, nil
}

func save(d string, s State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	target := filepath.Join(d, "state.json")
	tmp := target + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

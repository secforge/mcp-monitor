// Package store persists everything mcp-monitor keeps, for every project on
// the machine, in one JSON file laid out like mcp-hub-client's store:
// project first, then that project's monitors and schedules.
//
//	{
//	  "/source/products/x": {
//	    "monitors":  { "mon-…": {…} },
//	    "schedules": { "sch-…": {…} }
//	  }
//	}
//
// The file is state.json under the OS config directory (or
// MCP_MONITOR_STATE_DIR), mode 0600 in a 0700 directory, and every access is
// a full read-modify-write under one cross-process advisory lock.
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

	"github.com/secforge/mcp-monitor/internal/schedule"
)

// EnvDir overrides the directory holding state.json, its lock and the
// monitors' output files.
const EnvDir = "MCP_MONITOR_STATE_DIR"

const (
	fileName = "state.json"
	lockName = fileName + ".lock"
)

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

// Project is everything kept for one project.
type Project struct {
	Monitors  map[string]*Entry             `json:"monitors,omitempty"`
	Schedules map[string]*schedule.Schedule `json:"schedules,omitempty"`
}

// State is the whole file: project -> its monitors and schedules.
type State map[string]*Project

func (s *State) project(p string, create bool) *Project {
	if *s == nil {
		if !create {
			return nil
		}
		*s = State{}
	}
	pr := (*s)[p]
	if pr == nil && create {
		pr = &Project{}
		(*s)[p] = pr
	}
	return pr
}

// Monitors returns the project's monitors, creating the map when create is
// set; nil when there are none and create is not set.
func (s *State) Monitors(p string, create bool) map[string]*Entry {
	pr := s.project(p, create)
	if pr == nil {
		return nil
	}
	if pr.Monitors == nil && create {
		pr.Monitors = map[string]*Entry{}
	}
	return pr.Monitors
}

// Schedules returns the project's schedules, as Monitors does.
func (s *State) Schedules(p string, create bool) map[string]*schedule.Schedule {
	pr := s.project(p, create)
	if pr == nil {
		return nil
	}
	if pr.Schedules == nil && create {
		pr.Schedules = map[string]*schedule.Schedule{}
	}
	return pr.Schedules
}

// prune drops projects with nothing left in them, so the file does not
// collect an empty object for every project ever opened.
func (s State) prune() {
	for p, pr := range s {
		if pr == nil || (len(pr.Monitors) == 0 && len(pr.Schedules) == 0) {
			delete(s, p)
		}
	}
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

// Update runs fn on the state under the exclusive lock and saves the result
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

// Read runs fn on the state under the shared lock.
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
	fl := flock.New(filepath.Join(d, lockName))
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
	s := State{}
	if err := loadFile(filepath.Join(d, fileName), &s); err != nil {
		return nil, err
	}
	return s, nil
}

func save(d string, s State) error {
	s.prune()
	return saveFile(filepath.Join(d, fileName), s)
}

// loadFile reads one JSON file into v; a missing file leaves v untouched.
func loadFile(p string, v any) error {
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading %s: %w", p, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		// Left in place: overwriting it would silently drop every
		// project's entries.
		return fmt.Errorf("parsing %s: %v (repair or remove it; nothing here will overwrite it)", p, err)
	}
	return nil
}

func saveFile(target string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
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

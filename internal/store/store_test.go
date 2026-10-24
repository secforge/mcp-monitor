package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/secforge/mcp-monitor/internal/schedule"
)

func TestProjectKeyedLayoutAndPermissions(t *testing.T) {
	d := filepath.Join(t.TempDir(), "st")
	t.Setenv(EnvDir, d)
	if err := Update(func(s *State) error {
		s.Monitors("/p1", true)["mon-a"] = &Entry{ID: "mon-a", Command: "x"}
		s.Schedules("/p1", true)["sch-b"] = &schedule.Schedule{ID: "sch-b", Kind: schedule.Once, NextRun: time.Unix(0, 0)}
		s.Monitors("/p2", true)["mon-c"] = &Entry{ID: "mon-c"}
		s.Monitors("/empty", true)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(d, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var outer map[string]json.RawMessage
	if err := json.Unmarshal(raw, &outer); err != nil {
		t.Fatal(err)
	}
	top := map[string]map[string]json.RawMessage{}
	for k, v := range outer {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(v, &m); err != nil {
			t.Fatalf("top-level %q is not a project object: %v", k, err)
		}
		top[k] = m
	}
	if _, ok := top["/p1"]["monitors"]; !ok {
		t.Fatalf("project is not the top-level key: %s", raw)
	}
	if _, ok := top["/p1"]["schedules"]; !ok {
		t.Fatalf("schedules not under the project: %s", raw)
	}
	if _, ok := top["/empty"]; ok {
		t.Fatal("empty project kept")
	}
	fi, _ := os.Stat(filepath.Join(d, "state.json"))
	di, _ := os.Stat(d)
	if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
		t.Fatalf("perms file=%v dir=%v", fi.Mode().Perm(), di.Mode().Perm())
	}
}

func TestUnreadableIsNotOverwritten(t *testing.T) {
	d := t.TempDir()
	t.Setenv(EnvDir, d)
	p := filepath.Join(d, "state.json")
	os.WriteFile(p, []byte("{broken"), 0o600)
	if err := Update(func(*State) error { return nil }); err == nil {
		t.Fatal("expected an error")
	}
	if b, _ := os.ReadFile(p); string(b) != "{broken" {
		t.Fatal("file was overwritten")
	}
}

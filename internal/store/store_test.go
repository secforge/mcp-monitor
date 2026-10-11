package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTripAndPermissions(t *testing.T) {
	d := filepath.Join(t.TempDir(), "st")
	t.Setenv(EnvDir, d)
	if err := Update(func(s *State) error {
		s.Monitors("/p1", true)["a"] = &Entry{ID: "a", Command: "x"}
		s.Monitors("/p2", true)["b"] = &Entry{ID: "b"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var got map[string]*Entry
	if err := Read(func(s *State) error { got = s.Monitors("/p1", false); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["a"].Command != "x" {
		t.Fatalf("got %+v", got)
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

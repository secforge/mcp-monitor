package runner

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type rec struct {
	mu      sync.Mutex
	batches [][]string
	reason  EndReason
	code    int
	flooded bool
	ended   chan struct{}
}

func newRec() *rec { return &rec{ended: make(chan struct{})} }

func (r *rec) cb() Callbacks {
	return Callbacks{
		Event: func(_ *Monitor, l []string) { r.mu.Lock(); r.batches = append(r.batches, l); r.mu.Unlock() },
		End: func(_ *Monitor, reason EndReason, code int, flooded bool) {
			r.reason, r.code, r.flooded = reason, code, flooded
			close(r.ended)
		},
	}
}

func spec(t *testing.T, cmd string) Spec {
	d := t.TempDir()
	return Spec{ID: "t", Command: cmd, Description: "test", Dir: d, Output: filepath.Join(d, "out", "t.log")}
}

func wait(t *testing.T, r *rec) {
	select {
	case <-r.ended:
	case <-time.After(10 * time.Second):
		t.Fatal("monitor did not end")
	}
}

func TestBatchingAndExitCode(t *testing.T) {
	r := newRec()
	s := spec(t, "echo a; echo b; sleep 0.6; echo c; echo err >&2; exit 3")
	if _, err := Start(s, r.cb()); err != nil {
		t.Fatal(err)
	}
	wait(t, r)
	if len(r.batches) != 2 || strings.Join(r.batches[0], ",") != "a,b" || strings.Join(r.batches[1], ",") != "c" {
		t.Fatalf("batches = %q", r.batches)
	}
	if r.reason != Exited || r.code != 3 {
		t.Fatalf("reason=%v code=%d", r.reason, r.code)
	}
	out, _ := os.ReadFile(s.Output)
	if !strings.Contains(string(out), "err\n") || !strings.Contains(string(out), "c\n") {
		t.Fatalf("output file missing stdout/stderr: %q", out)
	}
}

func TestStopKillsWholeGroup(t *testing.T) {
	r := newRec()
	s := spec(t, "sleep 1000 | cat & echo $!; wait")
	m, err := Start(s, r.cb())
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	r.mu.Lock()
	pid, _ := strconv.Atoi(r.batches[0][0])
	r.mu.Unlock()
	m.Stop(Stopped)
	wait(t, r)
	if r.reason != Stopped {
		t.Fatalf("reason=%v", r.reason)
	}
	time.Sleep(100 * time.Millisecond)
	if syscall.Kill(pid, 0) == nil {
		t.Fatalf("pipeline member %d survived Stop", pid)
	}
	_ = m
}

func TestFloodGuard(t *testing.T) {
	r := newRec()
	s := spec(t, "while true; do echo x; sleep 0.25; done")
	s.MaxPerMinute = 3
	if _, err := Start(s, r.cb()); err != nil {
		t.Fatal(err)
	}
	wait(t, r)
	if !r.flooded || r.reason != Stopped || len(r.batches) != 3 {
		t.Fatalf("flooded=%v reason=%v batches=%d", r.flooded, r.reason, len(r.batches))
	}
}

func TestKillStaleGroup(t *testing.T) {
	r := newRec()
	m, err := Start(spec(t, "sleep 1000"), r.cb())
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if n := KillStaleGroup(m.PGID, m.GroupStart); n < 1 {
		t.Fatalf("killed %d", n)
	}
	wait(t, r)
	if r.reason != Exited || r.code != 137 {
		t.Fatalf("reason=%v code=%d", r.reason, r.code)
	}
}

package schedule

import (
	"testing"
	"time"
)

func mustNext(t *testing.T, spec string, from time.Time) time.Time {
	t.Helper()
	n, err := Next(spec, from)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestParseAtNeedsOffset(t *testing.T) {
	if _, err := ParseAt("2026-10-25T21:00:00"); err == nil {
		t.Fatal("accepted a time without offset")
	}
	got, err := ParseAt("2026-10-25T21:00:00+02:00")
	if err != nil || !got.Equal(time.Date(2026, 10, 25, 19, 0, 0, 0, time.UTC)) {
		t.Fatalf("got %v %v", got, err)
	}
}

func TestParseCron(t *testing.T) {
	for _, ok := range []string{"*/5 * * * *", "0 9 * * mon-fri", "@daily", "@every 90m", "CRON_TZ=UTC 0 3 * * *"} {
		if _, err := ParseCron(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "* * * *", "61 * * * *", "0 0 0 * * *"} {
		if _, err := ParseCron(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestCronTZ(t *testing.T) {
	from := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	if n := mustNext(t, "CRON_TZ=UTC 0 3 * * *", from); !n.Equal(time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC)) {
		t.Fatalf("next = %v", n)
	}
}

func seq(ids ...string) func() string {
	i := 0
	return func() string { i++; return ids[i-1] }
}

func none(string) bool { return false }

func TestOnceRunsAndIsRemoved(t *testing.T) {
	at := time.Date(2026, 10, 24, 21, 0, 0, 0, time.UTC)
	m := map[string]*Schedule{"s": {ID: "s", Kind: Once, NextRun: at}}
	if out := Claim(m, at.Add(-time.Second), none, seq("x")); len(out) != 0 {
		t.Fatalf("ran early: %+v", out)
	}
	out := Claim(m, at.Add(10*time.Second), none, seq("mon-1"))
	if len(out) != 1 || out[0].Run == nil || out[0].Run.Late || out[0].Run.Schedule.LastMonitor != "mon-1" {
		t.Fatalf("out = %+v", out)
	}
	if len(m) != 0 {
		t.Fatal("once schedule kept after running")
	}
}

func TestMissedOnceRunsLate(t *testing.T) {
	at := time.Date(2026, 10, 24, 21, 0, 0, 0, time.UTC)
	m := map[string]*Schedule{"s": {ID: "s", Kind: Once, NextRun: at}}
	out := Claim(m, at.Add(3*time.Hour), none, seq("mon-1"))
	if len(out) != 1 || !out[0].Run.Late || !out[0].Run.Due.Equal(at) {
		t.Fatalf("out = %+v", out[0].Run)
	}
}

func TestMissedCronCatchesUpOnce(t *testing.T) {
	spec := "CRON_TZ=UTC 0 * * * *" // hourly
	first := time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC)
	m := map[string]*Schedule{"s": {ID: "s", Kind: Cron, Spec: spec, NextRun: first}}
	now := time.Date(2026, 10, 24, 5, 30, 0, 0, time.UTC) // four runs missed
	out := Claim(m, now, none, seq("mon-1", "mon-2"))
	if len(out) != 1 || !out[0].Run.Late {
		t.Fatalf("out = %+v", out)
	}
	if want := time.Date(2026, 10, 24, 6, 0, 0, 0, time.UTC); !m["s"].NextRun.Equal(want) {
		t.Fatalf("next = %v, want %v", m["s"].NextRun, want)
	}
	if m["s"].Runs != 1 {
		t.Fatalf("runs = %d", m["s"].Runs)
	}
}

func TestCronSkipsWhilePreviousRuns(t *testing.T) {
	spec := "CRON_TZ=UTC */5 * * * *"
	t0 := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	m := map[string]*Schedule{"s": {ID: "s", Kind: Cron, Spec: spec, NextRun: t0, LastMonitor: "mon-old"}}
	running := func(id string) bool { return id == "mon-old" }
	out := Claim(m, t0, running, seq("mon-new"))
	if len(out) != 1 || out[0].Run != nil || out[0].Skipped == "" {
		t.Fatalf("out = %+v", out)
	}
	if m["s"].Skipped != 1 || m["s"].LastMonitor != "mon-old" || !m["s"].NextRun.Equal(t0.Add(5*time.Minute)) {
		t.Fatalf("schedule = %+v", m["s"])
	}
	if !out[0].FirstSkip {
		t.Fatal("first skip not flagged")
	}
	out = Claim(m, t0.Add(5*time.Minute), running, seq("mon-new"))
	if len(out) != 1 || out[0].FirstSkip {
		t.Fatalf("second skip in a row announced: %+v", out)
	}
	out = Claim(m, t0.Add(10*time.Minute), none, seq("mon-new"))
	if len(out) != 1 || out[0].Run == nil || m["s"].SkipStreak != 0 {
		t.Fatalf("run after skips: %+v %+v", out, m["s"])
	}
}

func TestClaimIsExactlyOnce(t *testing.T) {
	// Two servers sharing one stored map: the second claim at the same
	// instant finds nothing due, because the first moved NextRun.
	spec := "CRON_TZ=UTC 0 * * * *"
	t0 := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	m := map[string]*Schedule{"s": {ID: "s", Kind: Cron, Spec: spec, NextRun: t0}}
	if n := len(Claim(m, t0, none, seq("a"))); n != 1 {
		t.Fatalf("first claim: %d", n)
	}
	if n := len(Claim(m, t0, none, seq("b"))); n != 0 {
		t.Fatalf("second claim ran again: %d", n)
	}
}

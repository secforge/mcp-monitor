// Package schedule decides when a scheduled monitor runs. It holds no
// timers and touches no files: given the stored schedules and the time, it
// says which are due and what each one's next run is, so the rules can be
// tested without waiting for a clock.
//
// Two kinds:
//
//   - once: runs at one point in time, then is gone.
//   - cron: a standard five-field expression (minute hour day-of-month month
//     day-of-week), a descriptor such as @daily or @every 90m, optionally
//     prefixed with CRON_TZ=<zone>; without a prefix it is read in the
//     server's local time zone.
//
// A run whose time passed while no server for the project was running is
// not lost: the next server to look runs it once, late, and says so. A cron
// schedule that missed several runs catches up with ONE run, not one per
// missed occurrence. A cron run that comes due while that schedule's
// previous run is still going is skipped and counted, never doubled up.
package schedule

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// Kinds of schedule.
const (
	Once = "once"
	Cron = "cron"
)

// LateAfter is how far past its time a run has to start before it is
// reported as a missed run being caught up rather than an ordinary one.
const LateAfter = time.Minute

// Schedule is one stored schedule.
type Schedule struct {
	ID          string    `json:"id"`
	Command     string    `json:"command"`
	Description string    `json:"description"`
	Created     time.Time `json:"created"`
	Kind        string    `json:"kind"`
	// Spec is the cron expression as given; empty for once.
	Spec string `json:"spec,omitempty"`
	// NextRun is when it is next due. For once it is the requested time.
	NextRun time.Time `json:"nextRun"`
	LastRun time.Time `json:"lastRun,omitzero"`
	// LastMonitor is the monitor the latest run started.
	LastMonitor string `json:"lastMonitor,omitempty"`
	Runs        int    `json:"runs,omitempty"`
	Skipped     int    `json:"skipped,omitempty"`
	// SkipStreak counts skips since the last run, so only the first of a
	// row is announced.
	SkipStreak int `json:"skipStreak,omitempty"`
}

var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// ParseCron validates a cron spec and returns its schedule.
func ParseCron(spec string) (cron.Schedule, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, fmt.Errorf("cron expression is empty")
	}
	sch, err := parser.Parse(spec)
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression %q: %v (expected five fields: minute hour day-of-month month day-of-week, or a descriptor such as @daily, optionally prefixed with CRON_TZ=Europe/Berlin)", spec, err)
	}
	return sch, nil
}

// ParseAt reads a once time. An explicit offset or Z is required: a bare
// local time is ambiguous across a DST change and depends on whichever
// zone the server happens to run in.
func ParseAt(at string) (time.Time, error) {
	at = strings.TrimSpace(at)
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid time %q: give an RFC 3339 time with an explicit offset, e.g. 2026-10-25T21:00:00+02:00 or 2026-10-25T19:00:00Z", at)
	}
	return t, nil
}

// Next is the first run of a cron spec strictly after t.
func Next(spec string, t time.Time) (time.Time, error) {
	sch, err := ParseCron(spec)
	if err != nil {
		return time.Time{}, err
	}
	n := sch.Next(t)
	if n.IsZero() {
		return time.Time{}, fmt.Errorf("cron expression %q never runs", spec)
	}
	return n, nil
}

// Run is one decision to start a schedule's command now.
type Run struct {
	Schedule Schedule // as it was when it came due
	Due      time.Time
	Late     bool // its time passed while nothing was there to run it
}

// Outcome is what Claim decided for one schedule that was due.
type Outcome struct {
	Run     *Run   // nil when the run was skipped
	Skipped string // why, when it was skipped
	// FirstSkip is set on the first skip since the schedule last ran;
	// later ones in the same row are only counted.
	FirstSkip   bool
	ID          string
	Description string
}

// Claim takes every schedule that is due at now, decides for each whether it
// runs or is skipped, and updates the map in place: once schedules that run
// are removed, cron schedules move to their next run. running reports
// whether a monitor id is still active; newID names the monitor a run will
// start, recorded as LastMonitor before it starts so a concurrent claim
// sees it.
func Claim(scheds map[string]*Schedule, now time.Time, running func(monitorID string) bool, newID func() string) []Outcome {
	var out []Outcome
	for id, s := range scheds {
		if s.NextRun.After(now) {
			continue
		}
		due := s.NextRun
		late := now.Sub(due) > LateAfter
		if s.Kind == Cron {
			next, err := Next(s.Spec, now)
			if err != nil {
				// Stored specs were validated when they were added, so
				// this means a parser change; drop it rather than spin.
				delete(scheds, id)
				out = append(out, Outcome{ID: id, Description: s.Description, FirstSkip: true, Skipped: "removed: " + err.Error()})
				continue
			}
			s.NextRun = next
			if s.LastMonitor != "" && running(s.LastMonitor) {
				s.Skipped++
				s.SkipStreak++
				out = append(out, Outcome{ID: id, Description: s.Description, FirstSkip: s.SkipStreak == 1,
					Skipped: fmt.Sprintf("its previous run %s is still going", s.LastMonitor)})
				continue
			}
		}
		mon := newID()
		s.LastRun, s.LastMonitor, s.SkipStreak = now, mon, 0
		s.Runs++
		snap := *s
		if s.Kind == Once {
			delete(scheds, id)
		}
		out = append(out, Outcome{ID: id, Description: s.Description, Run: &Run{Schedule: snap, Due: due, Late: late}})
	}
	return out
}

// Earliest is the soonest NextRun among scheds, or the zero time when there
// are none.
func Earliest(scheds map[string]*Schedule) time.Time {
	var t time.Time
	for _, s := range scheds {
		if t.IsZero() || s.NextRun.Before(t) {
			t = s.NextRun
		}
	}
	return t
}

// Stamp renders a time for a reader in the server's local zone, always with
// the zone named.
func Stamp(t time.Time) string {
	return t.Local().Format("2006-01-02 15:04:05 MST")
}

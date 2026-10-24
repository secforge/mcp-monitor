package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/secforge/mcp-monitor/internal/schedule"
	"github.com/secforge/mcp-monitor/internal/store"
)

const schedulePrefix = "sch-"

// maxSleep bounds how long the scheduler sleeps between looks, so a
// schedule added by another session of the project is noticed even though
// nothing here was told about it.
const maxSleep = 15 * time.Second

func scheduleDescription() string {
	zone, _ := time.Now().Zone()
	return fmt.Sprintf(`Schedule a command to be started as a monitor later: once at a given time, or repeatedly on a cron schedule. Each run is an ordinary monitor, exactly as if monitor_start had been called at that moment — same output fencing, notifications, monitor_list and monitor_stop, no timeout, resumed after a restart.

Use it instead of sleeping or polling to start something later (a nightly sync, a check every hour, a reminder-style job at a given time).

- at: run once, at an RFC 3339 time with an explicit offset (e.g. 2026-10-25T21:00:00+02:00). It must be in the future. The schedule is deleted once it has run.
- cron: run repeatedly. Five fields (minute hour day-of-month month day-of-week), names allowed (mon-fri, jan), or a descriptor (@hourly, @daily, @weekly, @every 90m). Read in this server's local time zone (%s, %s) unless prefixed with CRON_TZ=<IANA zone>, e.g. "CRON_TZ=UTC 0 3 * * *".

Missed runs are not lost. Schedules belong to the project and are run by whichever session of the project is open; if a run's time passes while none is, the next session to start runs it once, late, and says so. A cron schedule that missed several runs catches up with ONE run. A cron run due while the schedule's previous run is still going is skipped and counted, never started twice.

Each run starts with a notice "[schedule <sch-id> · <description>] started monitor <mon-id> …" from the server. Delete a schedule with monitor_stop(<sch-id>); monitor_list shows schedules and their next run.`, zone, time.Local.String())
}

func newScheduleID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return schedulePrefix + hex.EncodeToString(b)
}

func (s *Server) schedule(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	p, err := s.prepare(ctx, req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	command := req.GetString("command", "")
	desc := req.GetString("description", "")
	at := strings.TrimSpace(req.GetString("at", ""))
	spec := strings.TrimSpace(req.GetString("cron", ""))
	switch {
	case strings.TrimSpace(command) == "":
		return mcp.NewToolResultError("command is required"), nil
	case strings.TrimSpace(desc) == "":
		return mcp.NewToolResultError("description is required"), nil
	case (at == "") == (spec == ""):
		return mcp.NewToolResultError("give exactly one of at (run once) and cron (run repeatedly)"), nil
	}
	now := time.Now()
	sc := &schedule.Schedule{ID: newScheduleID(), Command: command, Description: desc, Created: now.UTC()}
	if at != "" {
		t, err := schedule.ParseAt(at)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if !t.After(now) {
			return mcp.NewToolResultError(fmt.Sprintf("%s is not in the future (now is %s); use monitor_start to run it now", schedule.Stamp(t), schedule.Stamp(now))), nil
		}
		sc.Kind, sc.NextRun = schedule.Once, t
	} else {
		next, err := schedule.Next(spec, now)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		sc.Kind, sc.Spec, sc.NextRun = schedule.Cron, spec, next
	}
	if err := store.Update(func(st *store.State) error {
		st.Schedules(p, true)[sc.ID] = sc
		return nil
	}); err != nil {
		return mcp.NewToolResultError("could not persist the schedule: " + err.Error()), nil
	}
	s.wakeScheduler()
	what := "once"
	if sc.Kind == schedule.Cron {
		what = "on cron schedule " + sc.Spec
	}
	msg := fmt.Sprintf("Schedule %s created for project %s: runs %s, next at %s.\nDelete it with monitor_stop(%q).", sc.ID, p, what, schedule.Stamp(sc.NextRun), sc.ID)
	if ok, why := s.available(); !ok {
		msg += "\n\nWARNING: this server cannot push to its session (" + why + "), so it does not run schedules. Another session of this project, or the next one, will."
	}
	return mcp.NewToolResultText(msg), nil
}

func (s *Server) unschedule(p, id string) (*mcp.CallToolResult, error) {
	var removed *schedule.Schedule
	if err := store.Update(func(st *store.State) error {
		m := st.Schedules(p, false)
		removed = m[id]
		delete(m, id)
		return nil
	}); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if removed == nil {
		return mcp.NewToolResultError(fmt.Sprintf("no schedule %s in project %s", id, p)), nil
	}
	msg := fmt.Sprintf("Schedule %s deleted; it will not run again.", id)
	if removed.LastMonitor != "" {
		msg += fmt.Sprintf(" Its last run started monitor %s, which is not affected: stop it with monitor_stop(%q) if it is still running.", removed.LastMonitor, removed.LastMonitor)
	}
	return mcp.NewToolResultText(msg), nil
}

func (s *Server) listSchedules(b *strings.Builder, p string) {
	var list []*schedule.Schedule
	if err := store.Read(func(st *store.State) error {
		for _, sc := range st.Schedules(p, false) {
			list = append(list, sc)
		}
		return nil
	}); err != nil {
		fmt.Fprintf(b, "\nSchedules: unreadable: %v\n", err)
		return
	}
	if len(list) == 0 {
		b.WriteString("\nSchedules: (none)\n")
		return
	}
	sort.Slice(list, func(i, j int) bool { return list[i].NextRun.Before(list[j].NextRun) })
	b.WriteString("\nSchedules:\n")
	for _, sc := range list {
		kind := "once"
		if sc.Kind == schedule.Cron {
			kind = "cron " + sc.Spec
		}
		next := schedule.Stamp(sc.NextRun)
		if !sc.NextRun.After(time.Now()) {
			next += " (due; starts as soon as a session of this project can push)"
		}
		fmt.Fprintf(b, "\n- %s · %s\n  runs: %s\n  next: %s\n  command: %s\n", sc.ID, sc.Description, kind, next, sc.Command)
		if sc.Runs > 0 || sc.Skipped > 0 {
			fmt.Fprintf(b, "  history: %d run(s), %d skipped; last at %s, monitor %s\n", sc.Runs, sc.Skipped, schedule.Stamp(sc.LastRun), sc.LastMonitor)
		}
	}
}

func (s *Server) wakeScheduler() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// schedulerLoop runs due schedules of project p until shutdown. It sleeps
// until the earliest next run, but never longer than maxSleep.
func (s *Server) schedulerLoop(p string) {
	for {
		s.fireDue(p)
		wait := maxSleep
		var earliest time.Time
		_ = store.Read(func(st *store.State) error {
			earliest = schedule.Earliest(st.Schedules(p, false))
			return nil
		})
		if !earliest.IsZero() {
			if d := time.Until(earliest); d < wait {
				wait = max(d, 0) + 50*time.Millisecond
			}
		}
		select {
		case <-s.quit:
			return
		case <-s.wake:
		case <-time.After(wait):
		}
	}
}

// fireDue claims every due schedule of p under the store lock — so two
// sessions of the project never both run one — and then starts the claimed
// runs outside it.
func (s *Server) fireDue(p string) {
	var outcomes []schedule.Outcome
	err := store.Update(func(st *store.State) error {
		m := st.Schedules(p, false)
		if len(m) == 0 {
			return nil
		}
		active := st.Monitors(p, false)
		outcomes = schedule.Claim(m, time.Now(), func(id string) bool { _, ok := active[id]; return ok }, newID)
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp-monitor: scheduler: %v\n", err)
		return
	}
	for _, o := range outcomes {
		if o.Run == nil {
			if o.FirstSkip {
				s.pushNotice(fmt.Sprintf("[schedule %s · %s] skipped a run: %s. Further skips in a row are only counted (monitor_list).", o.ID, oneLine(o.Description), o.Skipped), nil)
			}
			continue
		}
		sc := o.Run.Schedule
		head := fmt.Sprintf("[schedule %s · %s]", sc.ID, oneLine(sc.Description))
		when := "due " + schedule.Stamp(o.Run.Due)
		if o.Run.Late {
			when += ", missed while no session of this project could run it, so run now"
		}
		var next string
		if sc.Kind == schedule.Cron {
			next = " Next run: " + schedule.Stamp(sc.NextRun) + "."
		}
		e, err := s.startMonitor(p, sc.LastMonitor, sc.Command, sc.Description)
		if err != nil {
			s.pushNotice(fmt.Sprintf("%s FAILED to start its run (%s): %v.%s", head, when, err, next), nil)
			continue
		}
		s.pushNotice(fmt.Sprintf("%s started monitor %s (%s).%s Output file: %s", head, e.ID, when, next, e.Output), nil)
	}
}

// Package runner runs one monitor command the way Claude Code's built-in
// Monitor does: bash runs the command, each stdout line is an event, lines
// arriving within 200ms of each other become one notification, stderr goes
// only to the output file, and the command exiting ends the watch with its
// exit code reported. There is no timeout.
package runner

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// BatchWindow is how long lines are collected into one notification.
const BatchWindow = 200 * time.Millisecond

// EndReason says why a monitor ended.
type EndReason int

const (
	// Exited: the command ended on its own.
	Exited EndReason = iota
	// Stopped: Stop was called (monitor_stop, or the flood guard).
	Stopped
	// Shutdown: the MCP server is going away; the monitor stays persisted.
	Shutdown
)

// Spec describes what to run.
type Spec struct {
	ID          string
	Command     string
	Description string
	Dir         string
	Output      string
	// MaxPerMinute stops the monitor when it produces more notifications
	// than this within a minute, like the built-in flood guard. 0 disables.
	MaxPerMinute int
}

// Monitor is one running command.
type Monitor struct {
	Spec
	Started    time.Time
	PGID       int
	GroupStart string

	cmd  *exec.Cmd
	done chan struct{}

	mu       sync.Mutex
	events   int
	reason   EndReason
	ending   bool
	exitCode int
	flooded  bool
	recent   []time.Time
}

// Callbacks receive the monitor's output. Both are called from the
// monitor's own goroutine, never concurrently for one monitor.
type Callbacks struct {
	// Event receives one batch of stdout lines.
	Event func(m *Monitor, lines []string)
	// End is called once, after the process has exited and every event has
	// been delivered.
	End func(m *Monitor, reason EndReason, exitCode int, flooded bool)
}

// Start launches the command.
func Start(spec Spec, cb Callbacks) (*Monitor, error) {
	if err := os.MkdirAll(filepath.Dir(spec.Output), 0o700); err != nil {
		return nil, err
	}
	out, err := os.OpenFile(spec.Output, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(out, "=== %s started: %s\n", time.Now().Format(time.RFC3339), spec.Command)

	cmd := exec.Command("bash", "-c", spec.Command)
	cmd.Dir = spec.Dir
	cmd.Stdin = nil
	cmd.Stderr = out
	// Its own process group, so stopping it reaches every pipeline stage,
	// and SIGKILL when this server's thread dies, so a killed server does
	// not leave the shell running. Later pipeline stages can survive that;
	// they are cleaned up when the monitor is resumed (see KillStaleGroup).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		out.Close()
		return nil, err
	}
	// Pdeathsig fires when the creating OS thread exits. Locking keeps the
	// fork on one thread; the Go runtime does not retire idle threads.
	runtime.LockOSThread()
	err = cmd.Start()
	runtime.UnlockOSThread()
	if err != nil {
		out.Close()
		return nil, err
	}
	m := &Monitor{Spec: spec, Started: time.Now(), cmd: cmd, done: make(chan struct{}), PGID: cmd.Process.Pid}
	m.GroupStart, _ = procStart(m.PGID)
	go m.run(stdout, out, cb)
	return m, nil
}

func (m *Monitor) run(stdout io.Reader, out *os.File, cb Callbacks) {
	defer close(m.done)
	lines := make(chan string, 256)
	go func() {
		defer close(lines)
		r := bufio.NewReaderSize(stdout, 64<<10)
		for {
			s, err := r.ReadString('\n')
			if s != "" {
				s = strings.TrimRight(s, "\r\n")
				fmt.Fprintln(out, s)
				lines <- s
			}
			if err != nil {
				return
			}
		}
	}()

	var batch []string
	var timer <-chan time.Time
	flush := func() {
		if len(batch) == 0 {
			return
		}
		b := batch
		batch, timer = nil, nil
		if m.count() && cb.Event != nil {
			cb.Event(m, b)
		}
	}
loop:
	for {
		select {
		case s, ok := <-lines:
			if !ok {
				break loop
			}
			batch = append(batch, s)
			if timer == nil {
				timer = time.After(BatchWindow)
			}
		case <-timer:
			flush()
		}
	}
	flush()

	err := m.cmd.Wait()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			code = 128 + int(ws.Signal())
		}
	} else if err != nil {
		code = -1
	}
	fmt.Fprintf(out, "=== %s ended: exit code %d\n", time.Now().Format(time.RFC3339), code)
	out.Close()

	m.mu.Lock()
	reason, flooded := m.reason, m.flooded
	m.exitCode = code
	m.mu.Unlock()
	if cb.End != nil {
		cb.End(m, reason, code, flooded)
	}
}

// count records one notification and reports whether it may be delivered.
// Past the flood limit the monitor is stopped instead.
func (m *Monitor) count() bool {
	m.mu.Lock()
	if m.ending {
		m.mu.Unlock()
		return false
	}
	m.events++
	if m.MaxPerMinute > 0 {
		now := time.Now()
		cut := 0
		for cut < len(m.recent) && now.Sub(m.recent[cut]) > time.Minute {
			cut++
		}
		m.recent = append(m.recent[cut:], now)
		if len(m.recent) > m.MaxPerMinute {
			m.flooded = true
			m.mu.Unlock()
			// Not Stop: this runs on the goroutine Stop would wait for.
			m.kill(Stopped)
			return false
		}
	}
	m.mu.Unlock()
	return true
}

// Events is the number of notifications produced so far.
func (m *Monitor) Events() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.events
}

// Running reports whether the process is still running.
func (m *Monitor) Running() bool {
	select {
	case <-m.done:
		return false
	default:
		return true
	}
}

// Stop kills the whole process group and records why. It returns once the
// monitor has ended and its End callback has returned.
func (m *Monitor) Stop(reason EndReason) {
	m.kill(reason)
	<-m.done
}

// kill signals the process group without waiting: SIGTERM, then SIGKILL
// if the monitor has not ended two seconds later.
func (m *Monitor) kill(reason EndReason) {
	m.mu.Lock()
	if !m.ending {
		m.ending = true
		m.reason = reason
	}
	m.mu.Unlock()
	_ = syscall.Kill(-m.PGID, syscall.SIGTERM)
	go func() {
		select {
		case <-m.done:
		case <-time.After(2 * time.Second):
			_ = syscall.Kill(-m.PGID, syscall.SIGKILL)
		}
	}()
}

// Done is closed once the monitor has ended and End has returned.
func (m *Monitor) Done() <-chan struct{} { return m.done }

// Package harness holds the one piece of mcp-hub-client's harness handling
// this server needs: refusing a messaging socket it inherited without being
// the MCP server of that session.
package harness

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/secforge/harness-transport/deliver"
)

// ReleaseInheritedSession drops a messaging socket this process inherited
// without being the MCP server its session launched, and reports whether it
// did. Copied from mcp-hub's internal/harness.
//
// A Claude Code session exports its socket to everything it spawns, so a
// server started by `claude mcp list` from inside a session would otherwise
// resume this project's monitors and push their events into a conversation
// it does not serve. The session's own MCP server descends from the
// socket's pid with no other `claude` process in between.
func ReleaseInheritedSession() bool {
	sock := os.Getenv(deliver.EnvClaudeSocket)
	if sock == "" {
		return false
	}
	owner, err := strconv.Atoi(strings.TrimSuffix(filepath.Base(sock), ".sock"))
	if err != nil || owner <= 0 {
		return false
	}
	if launchedBy(owner, os.Getppid()) {
		return false
	}
	os.Unsetenv(deliver.EnvClaudeSocket)
	os.Unsetenv(deliver.EnvClaudeToken)
	return true
}

func launchedBy(owner, parent int) bool {
	pid := parent
	for depth := 0; depth < 64; depth++ {
		if pid == owner {
			return true
		}
		if pid <= 1 {
			return false
		}
		comm, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "comm"))
		if err != nil {
			return depth == 0
		}
		if strings.TrimSpace(string(comm)) == "claude" {
			return false
		}
		next, ok := parentOf(pid)
		if !ok {
			return false
		}
		pid = next
	}
	return false
}

func parentOf(pid int) (int, bool) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, false
	}
	s := string(raw)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, false
	}
	fields := strings.Fields(s[i+1:])
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}
	return ppid, true
}

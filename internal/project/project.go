// Package project resolves which project this MCP server serves, the same
// way mcp-hub-client does: an explicit environment override first
// (MCP_MONITOR_PROJECT_DIR, then MCP_PROJECT_DIR), then the
// first root the MCP client advertises, then the working directory. The
// answer is fixed once per process, because it is half the key every stored
// monitor is filed under.
package project

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// EnvOverride names the project explicitly and wins over everything else.
const EnvOverride = "MCP_MONITOR_PROJECT_DIR"

// EnvSharedOverride is the generic override, consulted when EnvOverride is
// unset, so one variable can scope several MCP servers at once.
const EnvSharedOverride = "MCP_PROJECT_DIR"

// rootsTimeout bounds the one roots request. Unlike mcp-hub this resolver
// cannot wait on a tool call's context: it also runs at session start, where
// no call exists, and persisted monitors are resumed from there.
const rootsTimeout = 20 * time.Second

var cache struct {
	sync.Mutex
	value    string
	resolved bool
}

// Resolve returns the project for this process, resolving it on first use.
func Resolve(ctx context.Context) string {
	cache.Lock()
	defer cache.Unlock()
	if cache.resolved {
		return cache.value
	}
	p := Normalize(os.Getenv(EnvOverride))
	if p == "" {
		p = Normalize(os.Getenv(EnvSharedOverride))
	}
	if p == "" {
		p = fromRoots(ctx)
	}
	if p == "" {
		if wd, err := os.Getwd(); err == nil {
			p = Normalize(wd)
		}
	}
	cache.value, cache.resolved = p, true
	return p
}

// Known returns the project if it has already been resolved.
func Known() (string, bool) {
	cache.Lock()
	defer cache.Unlock()
	return cache.value, cache.resolved
}

func fromRoots(ctx context.Context) string {
	s := server.ServerFromContext(ctx)
	if s == nil {
		return ""
	}
	if sess, ok := server.ClientSessionFromContext(ctx).(server.SessionWithClientInfo); ok &&
		sess.GetClientCapabilities().Roots == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, rootsTimeout)
	defer cancel()
	res, err := s.RequestRoots(ctx, mcp.ListRootsRequest{
		Request: mcp.Request{Method: string(mcp.MethodListRoots)},
	})
	if err != nil || res == nil || len(res.Roots) == 0 {
		return ""
	}
	return Normalize(rootPath(res.Roots[0].URI))
}

func rootPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return strings.TrimPrefix(uri, "file://")
	}
	return u.Path
}

// Normalize is mcp-hub's connstore.NormalizeProject: absolute, cleaned, and
// symlinks resolved along the longest existing prefix. "" stays "".
func Normalize(p string) string {
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	} else {
		p = filepath.Clean(p)
	}
	var rest []string
	for cur := p; ; {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(append([]string{resolved}, rest...)...)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = append([]string{filepath.Base(cur)}, rest...)
		cur = parent
	}
}

// ResetForTesting forgets the resolved project.
func ResetForTesting() {
	cache.Lock()
	defer cache.Unlock()
	cache.value, cache.resolved = "", false
}

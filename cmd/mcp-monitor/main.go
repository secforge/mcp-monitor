// Command mcp-monitor is a stdio MCP server offering a Monitor tool like
// Claude Code's built-in one, without a timeout, with list and stop, and
// with monitors persisted per project.
package main

import (
	"fmt"
	"os"

	"github.com/mark3labs/mcp-go/server"

	"github.com/secforge/mcp-monitor/internal/harness"
	"github.com/secforge/mcp-monitor/internal/tools"
	"github.com/secforge/mcp-monitor/internal/version"
)

func main() {
	if len(os.Args) >= 2 && (os.Args[1] == "--version" || os.Args[1] == "version" || os.Args[1] == "-version") {
		fmt.Println(version.String())
		return
	}
	// Before the delivery channel is opened: a socket inherited from a
	// session this process does not serve must not receive its events.
	harness.ReleaseInheritedSession()
	s := server.NewMCPServer("mcp-monitor", version.Short(), server.WithToolCapabilities(false), server.WithRoots(), server.WithInstructions(tools.Instructions))
	t := tools.New()
	t.Register(s)
	err := server.ServeStdio(s)
	t.Shutdown()
	if err != nil {
		os.Exit(1)
	}
}

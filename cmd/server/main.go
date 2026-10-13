// Command server is the MCP entry point, ported from index.ts main() and
// the graceful shutdown handlers.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"aosfather.drawio-mcp/internal/bridge"
	"aosfather.drawio-mcp/internal/mcplog"
	"aosfather.drawio-mcp/internal/store"
	"aosfather.drawio-mcp/internal/tools"

	"github.com/mark3labs/mcp-go/server"
)

// packageVersion mirrors package.json so the MCP handshake never drifts.
const packageVersion = "0.3.0"

func main() {
	mcplog.Info("Starting MCP server for Next AI Draw.io (embedded mode)...")

	autosaver := store.NewAutosaver(store.DefaultDataDir(), 0, 0)
	bridge.OnStateChange(func(sessionID, xml string) {
		autosaver.Schedule(sessionID, xml)
	})
	bridge.OnSessionRecreate(func(sessionID string) string {
		return autosaver.Load(sessionID)
	})

	s := server.NewMCPServer("next-ai-drawio", packageVersion,
		server.WithInstructions(tools.INSTRUCTIONS),
	)
	tools.Register(s, tools.Deps{Autosaver: autosaver, Version: packageVersion})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Graceful shutdown: flush the autosaver, stop the HTTP bridge, exit.
	shutdown := func(reason string) {
		mcplog.Info("Shutting down: %s", reason)
		autosaver.Flush()
		bridge.Shutdown()
		os.Exit(0)
	}
	// Signals (may not work reliably on Windows)
	go func() {
		<-ctx.Done()
		shutdown("SIGINT/SIGTERM")
	}()

	// Listen returns when stdin ends (primary method, works on all
	// platforms) or the context is cancelled.
	stdio := server.NewStdioServer(s)
	err := stdio.Listen(ctx, os.Stdin, os.Stdout)
	if err != nil {
		mcplog.Error("Fatal error: %v", err)
		os.Exit(1)
	}
	shutdown("stdin closed")
}

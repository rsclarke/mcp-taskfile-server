// Package main implements an MCP server that exposes Taskfile tasks as tools.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rsclarke/mcp-taskfile-server/internal/taskfileserver"
)

// Build-time variables set via -ldflags.
var (
	serverName    = "mcp-taskfile-server"
	serverVersion = "dev"
)

// logLevelEnv names the environment variable used to configure the log
// level. Recognised values: debug, info, warn, error (case-insensitive).
const logLevelEnv = "MCP_TASKFILE_LOG_LEVEL"

// parseLogLevel resolves the configured log level. Unrecognised or empty
// values fall back to info.
func parseLogLevel(raw string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// newLogger builds the structured logger used by the server. It writes
// JSON to stderr (the only safe channel under stdio MCP transport) and
// honours MCP_TASKFILE_LOG_LEVEL.
func newLogger() *slog.Logger {
	level := parseLogLevel(os.Getenv(logLevelEnv))
	handler := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	return slog.New(handler).With(
		slog.String("service", serverName),
		slog.String("version", serverVersion),
	)
}

func run() error {
	logger := newLogger()

	// Create taskfile server
	taskfileServer := taskfileserver.New()
	taskfileServer.SetLogger(logger)
	defer taskfileServer.Shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create MCP server with lifecycle handlers
	mcpServer := mcp.NewServer(
		&mcp.Implementation{
			Name:    serverName,
			Version: serverVersion,
		},
		&mcp.ServerOptions{
			InitializedHandler:      taskfileServer.HandleInitialized,
			RootsListChangedHandler: taskfileServer.HandleRootsChanged,
		},
	)
	taskfileServer.SetToolRegistry(mcpServer)

	// Start the stdio server.
	//
	// TODO(#98): mirror selected log lines through the MCP `logging`
	// capability so they reach the client. Tracked separately from #88
	// (slog migration) and the deferral in commit 84473b2 (#87).
	if err := mcpServer.Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("server error: %w", err)
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}

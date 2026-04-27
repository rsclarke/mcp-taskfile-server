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
//
// MCP-side mirroring is wired in by the Server itself once the client
// handshake completes, by extending this logger with the SDK's
// LoggingHandler bound to the active session. The client controls
// what reaches it via logging/setLevel, independently of stderr.
func newLogger() *slog.Logger {
	level := parseLogLevel(os.Getenv(logLevelEnv))
	handler := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	return slog.New(handler).With(
		slog.String("service", serverName),
		slog.String("version", serverVersion),
	)
}

func run() error {
	taskfileServer := taskfileserver.New()
	taskfileServer.SetLogger(newLogger())
	defer taskfileServer.Shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create MCP server with lifecycle handlers. The default server
	// capabilities advertised by the SDK already include logging.
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

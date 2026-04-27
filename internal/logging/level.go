package logging

import (
	"log/slog"
	"os"
	"strings"
)

// LevelEnv names the environment variable used to configure the log
// level. Recognised values: debug, info, warn, error (case-insensitive).
const LevelEnv = "MCP_TASKFILE_LOG_LEVEL"

// ParseLevel resolves the configured log level. Unrecognised or empty
// values fall back to info.
func ParseLevel(raw string) slog.Level {
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

// NewLogger builds the structured logger used by the server. It writes
// JSON to stderr (the only safe channel under stdio MCP transport) and
// honours MCP_TASKFILE_LOG_LEVEL.
//
// MCP-side mirroring is wired in by the Server itself once the client
// handshake completes, by extending this logger with the SDK's
// LoggingHandler bound to the active session via InstallMCP. The client
// controls what reaches it via logging/setLevel, independently of stderr.
func NewLogger(serviceName, serviceVersion string) *slog.Logger {
	level := ParseLevel(os.Getenv(LevelEnv))
	handler := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	return slog.New(handler).With(
		slog.String("service", serviceName),
		slog.String("version", serviceVersion),
	)
}

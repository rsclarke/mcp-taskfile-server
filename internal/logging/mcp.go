package logging

import (
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// LoggerName is the logger name reported on logging/message
// notifications sent to the connected MCP client.
const LoggerName = "mcp-taskfile-server"

// InstallMCP returns a logger derived from parent that fans every
// record out to both parent's existing handler and an MCP arm bound to
// ss. Subsequent records reach the connected client via logging/message
// in addition to whatever sink was previously installed (typically a
// JSON handler on stderr).
//
// The SDK's LoggingHandler enforces the client-set threshold internally
// via logging/setLevel; the parent arm keeps its own threshold from
// MCP_TASKFILE_LOG_LEVEL. Failures forwarding to the client are dropped
// inside the SDK to avoid recursion through this handler.
func InstallMCP(parent *slog.Logger, ss *mcp.ServerSession) *slog.Logger {
	mcpHandler := mcp.NewLoggingHandler(ss, &mcp.LoggingHandlerOptions{
		LoggerName: LoggerName,
	})
	return slog.New(NewFanout(parent.Handler(), mcpHandler))
}

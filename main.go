// Package main implements an MCP server that exposes Taskfile tasks as tools.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rsclarke/mcp-taskfile-server/internal/logging"
	"github.com/rsclarke/mcp-taskfile-server/internal/taskfileserver"
)

// Build-time variables set via -ldflags.
var (
	serverName    = "mcp-taskfile-server"
	serverVersion = "dev"
)

func run() error {
	taskfileServer := taskfileserver.New()
	taskfileServer.SetLogger(logging.NewLogger(serverName, serverVersion))
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

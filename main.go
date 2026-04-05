// Package main implements an MCP server that exposes Taskfile tasks as tools.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rsclarke/mcp-taskfile-server/internal/taskfileserver"
)

// Build-time variables set via -ldflags.
var (
	serverName    = "mcp-taskfile-server"
	serverVersion = "dev"
)

func run() error {
	// Create taskfile server
	taskfileServer := taskfileserver.New()
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
	taskfileServer.SetMCPServer(mcpServer)

	// Start the stdio server
	if err := mcpServer.Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("server error: %w", err)
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Printf("Server error: %v\n", err)
		os.Exit(1)
	}
}

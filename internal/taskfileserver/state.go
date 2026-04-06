package taskfileserver

import (
	"context"
	"maps"
	"sync"

	"github.com/go-task/task/v3/taskfile/ast"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Root holds the loaded per-root Taskfile data.
type Root struct {
	taskfile       *ast.Taskfile
	workdir        string
	watchDirs      []string
	watchTaskfiles map[string]struct{}
}

// toolRegistry is the subset of *mcp.Server used for tool registration.
type toolRegistry interface {
	AddTool(tool *mcp.Tool, handler mcp.ToolHandler)
	RemoveTools(names ...string)
}

// Server represents our MCP server for Taskfile.yml.
type Server struct {
	roots           map[string]*Root
	mcpServer       *mcp.Server
	toolRegistry    toolRegistry
	registeredTools map[string]mcp.Tool
	mu              sync.Mutex
	generation      uint64
	watchCancel     context.CancelFunc
	watchDone       chan struct{}
	shuttingDown    bool
}

// toolStateSnapshot captures the inputs needed by buildToolPlan,
// frozen at a specific generation.
type toolStateSnapshot struct {
	generation uint64
	roots      map[string]*Root
}

// snapshotToolStateLocked returns a snapshot of the current tool-relevant
// server state. The caller must hold s.mu.
func (s *Server) snapshotToolStateLocked() toolStateSnapshot {
	snap := toolStateSnapshot{
		generation: s.generation,
		roots:      make(map[string]*Root, len(s.roots)),
	}
	maps.Copy(snap.roots, s.roots)
	return snap
}

// rootSnapshot is a canonical root URI captured under lock for use by
// watchTaskfiles without holding the mutex.
type rootSnapshot struct {
	uri string
}

// cloneStringSet returns a shallow copy of a string set.
func cloneStringSet(values map[string]struct{}) map[string]struct{} {
	cloned := make(map[string]struct{}, len(values))
	for value := range values {
		cloned[value] = struct{}{}
	}
	return cloned
}

// cloneStrings returns a copy of a string slice.
func cloneStrings(values []string) []string {
	return append([]string(nil), values...)
}

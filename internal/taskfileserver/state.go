package taskfileserver

import (
	"context"
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
	toolRegistry    toolRegistry
	registeredTools map[string]mcp.Tool
	mu              sync.Mutex
	generation      uint64
	watchCancel     context.CancelFunc
	watchDone       chan struct{}
	shuttingDown    bool
}

// toolRootSnapshot is an immutable per-root view captured under lock so
// the planner can run without holding s.mu and without dereferencing a
// live *Root that another goroutine may mutate. The taskfile pointer is
// captured by value: even if reloadRoot later swaps root.taskfile, this
// snapshot keeps observing the AST that was current at snapshot time.
type toolRootSnapshot struct {
	workdir  string
	taskfile *ast.Taskfile
}

// toolStateSnapshot captures the inputs needed by buildToolPlan,
// frozen at a specific generation.
type toolStateSnapshot struct {
	generation uint64
	roots      map[string]toolRootSnapshot
}

// snapshotToolStateLocked returns a snapshot of the current tool-relevant
// server state. The caller must hold s.mu.
func (s *Server) snapshotToolStateLocked() toolStateSnapshot {
	snap := toolStateSnapshot{
		generation: s.generation,
		roots:      make(map[string]toolRootSnapshot, len(s.roots)),
	}
	for uri, root := range s.roots {
		snap.roots[uri] = toolRootSnapshot{
			workdir:  root.workdir,
			taskfile: root.taskfile,
		}
	}
	return snap
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

package taskfileserver

import (
	"context"
	"sync"

	"github.com/go-task/task/v3/taskfile/ast"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Root holds the loaded per-root Taskfile data. Once a *Root is published
// into Server.roots its fields are treated as read-only; mutations are
// performed by replacing the pointer in the map rather than writing
// through the existing value, so concurrent readers (snapshots, watchers)
// always observe a consistent state.
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
//
// The mu mutex protects in-memory mutations of roots, registeredTools,
// generation, and the watcher bookkeeping fields (watchCancel, watchDone,
// shuttingDown). Functions called while holding mu must not block,
// perform I/O, or interact with goroutines that themselves acquire mu.
// Heavier lifecycle work (loading Taskfiles, cancelling watchers,
// notifying the MCP registry) MUST be driven by callers after mu has
// been released.
type Server struct {
	roots           map[string]*Root
	toolRegistry    toolRegistry
	registeredTools map[string]registeredTool
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

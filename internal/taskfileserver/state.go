package taskfileserver

import (
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/go-task/task/v3/taskfile/ast"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rsclarke/mcp-taskfile-server/internal/roots"
)

// toolRegistry is the subset of *mcp.Server used for tool registration.
type toolRegistry interface {
	AddTool(tool *mcp.Tool, handler mcp.ToolHandler)
	RemoveTools(names ...string)
}

// Server represents our MCP server for Taskfile.yml.
//
// The mu mutex protects in-memory mutations of roots, registeredTools,
// and generation. Functions called while holding mu must not block,
// perform I/O, or interact with goroutines that themselves acquire mu.
// Heavier lifecycle work (loading Taskfiles, cancelling watchers,
// notifying the MCP registry) MUST be driven by callers after mu has
// been released.
//
// The watcher set is owned by watchers, which has its own internal lock
// and lifecycle that is independent of mu. Callers must not hold mu
// while invoking watcher methods.
type Server struct {
	roots           map[string]*roots.Root
	toolRegistry    toolRegistry
	registeredTools map[string]registeredTool
	watchers        *watcherManager
	logger          atomic.Pointer[slog.Logger]
	mu              sync.Mutex
	generation      uint64
}

// log returns the current logger. It is safe to call from any goroutine
// and never returns nil; New seeds the field with a discard logger and
// SetLogger preserves that invariant.
func (s *Server) log() *slog.Logger {
	return s.logger.Load()
}

// toolRootSnapshot is an immutable per-root view captured under lock so
// the planner can run without holding s.mu and without dereferencing a
// live *Root that another goroutine may mutate. The taskfile pointer is
// captured by value: even if reloadRoot later swaps root.Taskfile, this
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
			workdir:  root.Workdir,
			taskfile: root.Taskfile,
		}
	}
	return snap
}

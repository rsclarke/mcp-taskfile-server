package taskfileserver

import (
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rsclarke/mcp-taskfile-server/internal/roots"
	"github.com/rsclarke/mcp-taskfile-server/internal/tools"
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
	registeredTools map[string]tools.RegisteredTool
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

// snapshotToolStateLocked returns a snapshot of the current tool-relevant
// server state. The caller must hold s.mu.
func (s *Server) snapshotToolStateLocked() tools.StateSnapshot {
	snap := tools.StateSnapshot{
		Generation: s.generation,
		Roots:      make(map[string]tools.RootSnapshot, len(s.roots)),
	}
	for uri, root := range s.roots {
		snap.Roots[uri] = tools.RootSnapshot{
			Workdir:  root.Workdir,
			Taskfile: root.Taskfile,
		}
	}
	return snap
}

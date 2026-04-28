package server

import (
	"context"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rsclarke/mcp-taskfile-server/internal/logging"
	"github.com/rsclarke/mcp-taskfile-server/internal/roots"
	"github.com/rsclarke/mcp-taskfile-server/internal/tools"
	"github.com/rsclarke/mcp-taskfile-server/internal/watch"
)

// New creates a new Taskfile MCP server. The server starts with a
// silent logger; callers should override it via SetLogger before Run.
func New() *Server {
	s := &Server{
		roots:           make(map[string]*roots.Root),
		registeredTools: make(map[string]tools.RegisteredTool),
	}
	s.logger.Store(slog.New(slog.DiscardHandler))
	s.watchers = watch.New(s, s.log)
	return s
}

// SetToolRegistry attaches the registry used for tool registration updates.
func (s *Server) SetToolRegistry(registry toolRegistry) {
	s.toolRegistry = registry
}

// SetLogger replaces the structured logger used by the server. Passing
// a nil logger restores the default silent logger so Server methods can
// log unconditionally without nil-checking. The store is atomic so it
// is safe to call from any goroutine, including after watcher goroutines
// have started reading the logger.
func (s *Server) SetLogger(logger *slog.Logger) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	s.logger.Store(logger)
}

// HandleInitialized is called after the client handshake completes.
// Before doing any other work it extends the active logger with an MCP
// arm bound to req.Session so subsequent log records reach the client.
func (s *Server) HandleInitialized(ctx context.Context, req *mcp.InitializedRequest) {
	s.SetLogger(logging.InstallMCP(s.log(), req.Session))
	if err := s.initializeRootsFromSession(ctx, req.Session); err != nil {
		s.log().Error("failed to initialize roots",
			slog.String("event", "roots.init_failed"),
			slog.Any("error", err),
		)
	}
}

// HandleRootsChanged is called when the client sends roots/list_changed.
// It applies the per-root watcher diff regardless of whether syncTools
// succeeds, so the watcher set continues to track the live root
// membership.
func (s *Server) HandleRootsChanged(ctx context.Context, req *mcp.RootsListChangedRequest) {
	rootRes, err := req.Session.ListRoots(ctx, nil)
	if err != nil {
		s.log().Error("failed to list roots after change",
			slog.String("event", "roots.list_failed"),
			slog.Any("error", err),
		)
		return
	}

	res := s.replaceRoots(ctx, rootRes.Roots)

	if err := s.syncTools(); err != nil {
		s.log().Error("failed to sync tools after roots change",
			slog.String("event", "tools.sync_failed"),
			slog.Any("error", err),
		)
	}
	s.watchers.Apply(ctx, res.added, res.removed)
}

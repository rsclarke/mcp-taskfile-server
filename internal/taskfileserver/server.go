package taskfileserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
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

// isMethodNotFound reports whether err is a JSON-RPC "method not found" error,
// which indicates the client does not support the requested capability.
func isMethodNotFound(err error) bool {
	var wireErr *jsonrpc.Error
	return errors.As(err, &wireErr) && wireErr.Code == jsonrpc.CodeMethodNotFound
}

// reconcileResult describes the diff produced by initializeRoots and
// replaceRoots. It is returned to the caller so that side effects driven
// by root membership changes (tool sync, watcher lifecycle, future
// per-root managers) can run after s.mu has been released.
//
// added and removed contain canonical root URIs; entries are unique within
// each slice but ordering is unspecified.
type reconcileResult struct {
	added   []string
	removed []string
}

// changed reports whether any roots were added or removed.
func (r reconcileResult) changed() bool {
	return len(r.added) > 0 || len(r.removed) > 0
}

// loadDesired canonicalises raw root specs and loads any roots that are not
// already present in existing. It does no I/O for URIs already known to the
// caller. Returns the canonical URI set the caller should converge to and
// the freshly-loaded roots keyed by canonical URI. Invalid URIs and load
// failures that cannot fall back to an unloaded placeholder are logged and
// skipped.
//
// This helper performs disk I/O (roots.Load, roots.NewUnloaded) and MUST
// be called without s.mu held.
func (s *Server) loadDesired(ctx context.Context, mcpRoots []*mcp.Root, existing map[string]struct{}) (map[string]struct{}, map[string]*roots.Root) {
	desiredURIs := make(map[string]struct{}, len(mcpRoots))
	loadedRoots := make(map[string]*roots.Root, len(mcpRoots))

	for _, r := range mcpRoots {
		canonicalURI, dir, parseErr := roots.CanonicalRootURI(r.URI)
		if parseErr != nil {
			s.log().Warn("skipping root with invalid URI",
				slog.String("event", "root.invalid_uri"),
				slog.String("root_uri", r.URI),
				slog.Any("error", parseErr),
			)
			continue
		}
		desiredURIs[canonicalURI] = struct{}{}
		if _, exists := existing[canonicalURI]; exists {
			continue
		}
		if _, exists := loadedRoots[canonicalURI]; exists {
			continue
		}

		root, loadErr := roots.Load(ctx, dir)
		if loadErr != nil {
			s.log().Warn("failed to load root, falling back to unloaded placeholder",
				slog.String("event", "root.load_failed"),
				slog.String("root_uri", r.URI),
				slog.Any("error", loadErr),
			)
			root, loadErr = roots.NewUnloaded(dir)
			if loadErr != nil {
				s.log().Error("failed to watch unloaded root",
					slog.String("event", "root.unloaded_failed"),
					slog.String("root_uri", r.URI),
					slog.Any("error", loadErr),
				)
				continue
			}
		}
		loadedRoots[canonicalURI] = root
	}

	return desiredURIs, loadedRoots
}

// snapshotExistingRoots returns the set of canonical URIs currently
// tracked by the server. The returned map is owned by the caller.
func (s *Server) snapshotExistingRoots() map[string]struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing := make(map[string]struct{}, len(s.roots))
	for uri := range s.roots {
		existing[uri] = struct{}{}
	}
	return existing
}

// initializeRoots installs the initial set of roots from the client. It is
// additive: roots already present are left in place and roots not in the
// desired list are NOT removed. It is an error for the resulting root set
// to be empty.
//
// Side effects (syncTools, watcher start/stop) are NOT performed here;
// the caller drives them using the returned reconcileResult after s.mu
// has been released.
func (s *Server) initializeRoots(ctx context.Context, roots []*mcp.Root) (reconcileResult, error) {
	existing := s.snapshotExistingRoots()
	_, loadedRoots := s.loadDesired(ctx, roots, existing)

	var res reconcileResult
	s.mu.Lock()
	for uri, root := range loadedRoots {
		if _, present := s.roots[uri]; present {
			continue
		}
		s.roots[uri] = root
		res.added = append(res.added, uri)
	}
	if res.changed() {
		s.generation++
	}
	if len(s.roots) == 0 {
		s.mu.Unlock()
		return reconcileResult{}, errors.New("no valid roots found")
	}
	s.mu.Unlock()

	return res, nil
}

// replaceRoots reconciles s.roots so that its membership matches roots
// exactly: previously loaded roots not in the desired list are removed and
// new roots are loaded and added. An empty roots list is allowed and
// results in an empty server root set.
//
// Side effects (syncTools, watcher start/stop) are NOT performed here;
// the caller drives them using the returned reconcileResult after s.mu
// has been released.
func (s *Server) replaceRoots(ctx context.Context, roots []*mcp.Root) reconcileResult {
	existing := s.snapshotExistingRoots()
	desiredURIs, loadedRoots := s.loadDesired(ctx, roots, existing)

	var res reconcileResult
	s.mu.Lock()
	for uri := range s.roots {
		if _, ok := desiredURIs[uri]; ok {
			continue
		}
		s.unloadRoot(uri)
		res.removed = append(res.removed, uri)
	}
	for uri, root := range loadedRoots {
		if _, present := s.roots[uri]; present {
			continue
		}
		s.roots[uri] = root
		res.added = append(res.added, uri)
	}
	if res.changed() {
		s.generation++
	}
	s.mu.Unlock()

	return res
}

// initializeRootsFromSession queries the client for its root list and loads
// each one. If the client does not support roots, it falls back to os.Getwd().
// On success it drives the post-reconcile side effects (syncTools, watcher
// start) outside of s.mu.
func (s *Server) initializeRootsFromSession(ctx context.Context, session *mcp.ServerSession) error {
	roots, err := listClientRoots(ctx, session)
	if err != nil {
		return err
	}

	res, err := s.initializeRoots(ctx, roots)
	if err != nil {
		return err
	}

	syncErr := s.syncTools()
	if syncErr == nil {
		s.watchers.Apply(ctx, res.added, res.removed)
	}
	return syncErr
}

// listClientRoots returns the desired roots for initialization, falling
// back to the working directory if the client does not advertise the
// roots capability.
func listClientRoots(ctx context.Context, session *mcp.ServerSession) ([]*mcp.Root, error) {
	rootRes, err := session.ListRoots(ctx, nil)
	if err == nil {
		return rootRes.Roots, nil
	}
	if !isMethodNotFound(err) {
		return nil, fmt.Errorf("failed to list roots: %w", err)
	}

	workdir, wdErr := os.Getwd()
	if wdErr != nil {
		return nil, fmt.Errorf("failed to get working directory: %w", wdErr)
	}
	return []*mcp.Root{{URI: roots.DirToURI(workdir)}}, nil
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

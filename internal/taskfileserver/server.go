package taskfileserver

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// New creates a new Taskfile MCP server.
func New() *Server {
	s := &Server{
		roots:           make(map[string]*Root),
		registeredTools: make(map[string]registeredTool),
	}
	s.watchers = newWatcherManager(s.runRootWatcher)
	return s
}

// SetToolRegistry attaches the registry used for tool registration updates.
func (s *Server) SetToolRegistry(registry toolRegistry) {
	s.toolRegistry = registry
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
// This helper performs disk I/O (loadRoot, newUnloadedRoot) and MUST be
// called without s.mu held.
func loadDesired(ctx context.Context, roots []*mcp.Root, existing map[string]struct{}) (map[string]struct{}, map[string]*Root) {
	desiredURIs := make(map[string]struct{}, len(roots))
	loadedRoots := make(map[string]*Root, len(roots))

	for _, r := range roots {
		canonicalURI, dir, parseErr := canonicalRootURI(r.URI)
		if parseErr != nil {
			log.Printf("skipping root with invalid URI %q: %v", r.URI, parseErr)
			continue
		}
		desiredURIs[canonicalURI] = struct{}{}
		if _, exists := existing[canonicalURI]; exists {
			continue
		}
		if _, exists := loadedRoots[canonicalURI]; exists {
			continue
		}

		root, loadErr := loadRoot(ctx, dir)
		if loadErr != nil {
			log.Printf("failed to load root %q: %v", r.URI, loadErr)
			root, loadErr = newUnloadedRoot(dir)
			if loadErr != nil {
				log.Printf("failed to watch unloaded root %q: %v", r.URI, loadErr)
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
	_, loadedRoots := loadDesired(ctx, roots, existing)

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
	desiredURIs, loadedRoots := loadDesired(ctx, roots, existing)

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
		s.watchers.apply(ctx, res.added, res.removed)
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
	return []*mcp.Root{{URI: dirToURI(workdir)}}, nil
}

// HandleInitialized is called after the client handshake completes.
func (s *Server) HandleInitialized(ctx context.Context, req *mcp.InitializedRequest) {
	if err := s.initializeRootsFromSession(ctx, req.Session); err != nil {
		log.Printf("failed to initialize roots: %v", err)
	}
}

// HandleRootsChanged is called when the client sends roots/list_changed.
// It applies the per-root watcher diff regardless of whether syncTools
// succeeds, so the watcher set continues to track the live root
// membership.
func (s *Server) HandleRootsChanged(ctx context.Context, req *mcp.RootsListChangedRequest) {
	rootRes, err := req.Session.ListRoots(ctx, nil)
	if err != nil {
		log.Printf("failed to list roots after change: %v", err)
		return
	}

	res := s.replaceRoots(ctx, rootRes.Roots)

	if err := s.syncTools(); err != nil {
		log.Printf("failed to sync tools after roots change: %v", err)
	}
	s.watchers.apply(ctx, res.added, res.removed)
}

package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rsclarke/mcp-taskfile-server/internal/roots"
)

// unloadRoot removes the root with the given canonical URI from the
// server's in-memory map. The caller must hold s.mu.
//
// This function is intentionally trivial: per the s.mu invariant, anything
// called under the lock must be non-blocking and lock-free transitively.
// Heavier per-root cleanup (e.g. cancelling a per-root watcher and waiting
// for it to drain) MUST be performed by the caller after s.mu has been
// released, driven by the removed-URI list returned from replaceRoots.
func (s *Server) unloadRoot(uri string) {
	delete(s.roots, uri)
}

// disableRootToolsLocked replaces the root at uri with a fresh *Root that
// preserves the workdir and watcher set but has no loaded Taskfile, then
// bumps the generation. Replacing rather than mutating keeps Root values
// reachable from prior snapshots effectively immutable. The caller must
// hold s.mu and is responsible for running syncTools afterwards.
func (s *Server) disableRootToolsLocked(uri string, root *roots.Root) {
	s.roots[uri] = &roots.Root{
		Workdir:        root.Workdir,
		WatchDirs:      root.WatchDirs,
		WatchTaskfiles: root.WatchTaskfiles,
	}
	s.generation++
}

// ReloadRoot re-creates the task executor for a given canonical root URI and
// syncs the global MCP tool set.
func (s *Server) ReloadRoot(ctx context.Context, uri string) error {
	s.mu.Lock()
	root, ok := s.roots[uri]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown root %q", uri)
	}
	workdir := root.Workdir
	s.mu.Unlock()

	newRoot, err := roots.Build(ctx, workdir)
	if err != nil {
		s.mu.Lock()
		s.disableRootToolsLocked(uri, root)
		s.mu.Unlock()
		syncErr := s.syncTools()
		if syncErr != nil {
			return fmt.Errorf("failed to reload root %s: %w", uri, errors.Join(err, fmt.Errorf("failed to clear stale tools: %w", syncErr)))
		}
		return fmt.Errorf("failed to reload root %s: %w", uri, err)
	}

	s.mu.Lock()
	s.roots[uri] = newRoot
	s.generation++
	s.mu.Unlock()

	return s.syncTools()
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
func (s *Server) initializeRoots(ctx context.Context, mcpRoots []*mcp.Root) (reconcileResult, error) {
	existing := s.snapshotExistingRoots()
	_, loadedRoots := s.loadDesired(ctx, mcpRoots, existing)

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
func (s *Server) replaceRoots(ctx context.Context, mcpRoots []*mcp.Root) reconcileResult {
	existing := s.snapshotExistingRoots()
	desiredURIs, loadedRoots := s.loadDesired(ctx, mcpRoots, existing)

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
	mcpRoots, err := listClientRoots(ctx, session)
	if err != nil {
		return err
	}

	res, err := s.initializeRoots(ctx, mcpRoots)
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

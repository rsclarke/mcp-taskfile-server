package taskfileserver

import (
	"context"
	"errors"
	"fmt"

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

// reloadRoot re-creates the task executor for a given canonical root URI and
// syncs the global MCP tool set.
func (s *Server) reloadRoot(ctx context.Context, uri string) error {
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

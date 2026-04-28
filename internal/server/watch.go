package server

import (
	"context"
	"maps"
	"slices"
)

// RootWatchState returns copies of the current watch configuration for a
// root. It satisfies watch.StateProvider.
func (s *Server) RootWatchState(uri string) ([]string, map[string]struct{}, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	root, ok := s.roots[uri]
	if !ok {
		return nil, nil, false
	}

	return slices.Clone(root.WatchDirs), maps.Clone(root.WatchTaskfiles), true
}

// Shutdown stops every running watcher and waits for them to exit. It is
// idempotent and safe to call multiple times. After Shutdown returns,
// future calls into the watcher manager become no-ops.
func (s *Server) Shutdown() {
	s.watchers.Shutdown()
}

// restartWatchers reconciles the watcher set so that one watcher is
// running per current root. It is a thin wrapper around the per-root
// watcher manager: existing watchers are left running, watchers for
// removed roots are cancelled, and watchers for newly added roots are
// spawned.
//
// The provided ctx is detached via context.WithoutCancel inside the
// manager, because callers may pass request-scoped contexts that are
// cancelled after the handler returns; watchers must outlive the request.
func (s *Server) restartWatchers(ctx context.Context) {
	s.mu.Lock()
	rootURIs := make([]string, 0, len(s.roots))
	for uri := range s.roots {
		rootURIs = append(rootURIs, uri)
	}
	s.mu.Unlock()

	s.watchers.Reconcile(ctx, rootURIs)
}

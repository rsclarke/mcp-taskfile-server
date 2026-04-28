package server

import (
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

package taskfileserver

import (
	"context"
	"fmt"
	"log"
	"maps"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// rootWatcher tracks a single per-root watcher goroutine.
type rootWatcher struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// watcherManager owns the lifecycle of per-root watcher goroutines. Each
// root URI is watched by at most one goroutine; the manager spawns
// watchers for newly added URIs and cancels watchers for removed URIs
// without disturbing the rest. The manager has its own internal lock and
// must be invoked without Server.mu held.
type watcherManager struct {
	// run executes the per-root watch loop. It receives a context that
	// is cancelled when the watcher is stopped (via apply, reconcile, or
	// shutdown). The function MUST return promptly after ctx is
	// cancelled so shutdown can drain.
	run func(ctx context.Context, uri string)

	mu           sync.Mutex
	watchers     map[string]*rootWatcher
	shuttingDown bool
}

// newWatcherManager constructs an empty manager. run is the per-root
// watch loop the manager will spawn for each URI.
func newWatcherManager(run func(ctx context.Context, uri string)) *watcherManager {
	return &watcherManager{
		run:      run,
		watchers: make(map[string]*rootWatcher),
	}
}

// apply spawns watchers for URIs in added that are not already running
// and cancels watchers for URIs in removed. The cancelled watchers exit
// asynchronously; apply does not wait for them. baseCtx is detached via
// context.WithoutCancel so watchers outlive the caller's request scope.
//
// apply is a no-op once shutdown has been called.
func (m *watcherManager) apply(baseCtx context.Context, added, removed []string) {
	type spawn struct {
		uri string
		ctx context.Context
		w   *rootWatcher
	}

	var (
		stopping []*rootWatcher
		spawning []spawn
	)

	m.mu.Lock()
	if m.shuttingDown {
		m.mu.Unlock()
		return
	}
	for _, uri := range removed {
		if w, ok := m.watchers[uri]; ok {
			stopping = append(stopping, w)
			delete(m.watchers, uri)
		}
	}
	parent := context.WithoutCancel(baseCtx)
	for _, uri := range added {
		if _, ok := m.watchers[uri]; ok {
			continue
		}
		// cancel is stored in rootWatcher.cancel and invoked from
		// apply (on removal) or shutdown; gosec cannot trace this.
		ctx, cancel := context.WithCancel(parent) //nolint:gosec // cancel is held in rootWatcher.cancel
		w := &rootWatcher{cancel: cancel, done: make(chan struct{})}
		m.watchers[uri] = w
		spawning = append(spawning, spawn{uri: uri, ctx: ctx, w: w})
	}
	m.mu.Unlock()

	for _, w := range stopping {
		w.cancel()
	}
	for _, sp := range spawning {
		go func() {
			defer close(sp.w.done)
			m.run(sp.ctx, sp.uri)
		}()
	}
}

// reconcile diffs the running watchers against desired and applies the
// difference. Existing watchers for URIs in desired are not disturbed.
// reconcile is a no-op once shutdown has been called.
func (m *watcherManager) reconcile(baseCtx context.Context, desired []string) {
	desiredSet := make(map[string]struct{}, len(desired))
	for _, uri := range desired {
		desiredSet[uri] = struct{}{}
	}

	var added, removed []string

	m.mu.Lock()
	if m.shuttingDown {
		m.mu.Unlock()
		return
	}
	for uri := range m.watchers {
		if _, ok := desiredSet[uri]; !ok {
			removed = append(removed, uri)
		}
	}
	for uri := range desiredSet {
		if _, ok := m.watchers[uri]; !ok {
			added = append(added, uri)
		}
	}
	m.mu.Unlock()

	m.apply(baseCtx, added, removed)
}

// shutdown cancels every running watcher and waits for them to exit.
// After shutdown returns, all subsequent apply and reconcile calls are
// no-ops. shutdown is idempotent and safe to call from multiple
// goroutines concurrently.
func (m *watcherManager) shutdown() {
	m.mu.Lock()
	if m.shuttingDown {
		m.mu.Unlock()
		return
	}
	m.shuttingDown = true
	watchers := m.watchers
	m.watchers = make(map[string]*rootWatcher)
	m.mu.Unlock()

	for _, w := range watchers {
		w.cancel()
	}
	for _, w := range watchers {
		<-w.done
	}
}

// active reports the number of running watchers. Intended for tests.
func (m *watcherManager) active() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.watchers)
}

// isShuttingDown reports whether shutdown has been called. Intended for
// tests.
func (m *watcherManager) isShuttingDown() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.shuttingDown
}

// rootWatchState returns copies of the current watch configuration for a root.
func (s *Server) rootWatchState(uri string) ([]string, map[string]struct{}, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	root, ok := s.roots[uri]
	if !ok {
		return nil, nil, false
	}

	return slices.Clone(root.watchDirs), maps.Clone(root.watchTaskfiles), true
}

// syncWatcherDirs ensures the fsnotify watcher is subscribed to the provided
// directories and unsubscribed from any directories no longer needed.
func syncWatcherDirs(watcher *fsnotify.Watcher, current map[string]struct{}, desired []string) (map[string]struct{}, error) {
	desiredSet := make(map[string]struct{}, len(desired))
	for _, dir := range desired {
		desiredSet[dir] = struct{}{}
		if _, ok := current[dir]; ok {
			continue
		}
		if err := watcher.Add(dir); err != nil {
			return current, fmt.Errorf("watch %s: %w", dir, err)
		}
	}

	for dir := range current {
		if _, ok := desiredSet[dir]; ok {
			continue
		}
		_ = watcher.Remove(dir)
	}

	return desiredSet, nil
}

// watchRootTaskfiles watches a single root's resolved Taskfile graph for
// changes and reloads tools when one of those files is modified.
func (s *Server) watchRootTaskfiles(ctx context.Context, uri string) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create file watcher: %w", err)
	}
	defer func() {
		_ = watcher.Close()
	}()

	watchDirs, watchTaskfiles, ok := s.rootWatchState(uri)
	if !ok {
		return nil
	}
	currentWatchDirs := make(map[string]struct{}, len(watchDirs))
	currentWatchDirs, err = syncWatcherDirs(watcher, currentWatchDirs, watchDirs)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("failed to watch Taskfile directories: %w", err)
	}

	const debounce = 200 * time.Millisecond
	timer := time.NewTimer(0)
	if !timer.Stop() {
		<-timer.C
	}
	timerPending := false

	for {
		select {
		case <-ctx.Done():
			if !timer.Stop() && timerPending {
				<-timer.C
			}
			return nil
		case <-timer.C:
			timerPending = false
			if err := s.reloadRoot(ctx, uri); err != nil {
				log.Printf("failed to reload tools for root %s: %v", uri, err)
				continue
			}

			watchDirs, watchTaskfiles, ok = s.rootWatchState(uri)
			if !ok {
				return nil
			}
			currentWatchDirs, err = syncWatcherDirs(watcher, currentWatchDirs, watchDirs)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("failed to refresh Taskfile watches for root %s: %w", uri, err)
			}
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}

			eventPath := filepath.Clean(event.Name)
			if _, ok := watchTaskfiles[eventPath]; !ok {
				continue
			}

			// Debounce rapid events.
			if !timer.Stop() && timerPending {
				<-timer.C
			}
			timer.Reset(debounce)
			timerPending = true
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			log.Printf("file watcher error: %v", err)
		}
	}
}

// runRootWatcher is the watcherManager run callback. It logs and discards
// any error returned by watchRootTaskfiles so a failed watcher does not
// take down the manager's bookkeeping.
func (s *Server) runRootWatcher(ctx context.Context, uri string) {
	if err := s.watchRootTaskfiles(ctx, uri); err != nil {
		log.Printf("file watcher for %s failed: %v", uri, err)
	}
}

// Shutdown stops every running watcher and waits for them to exit. It is
// idempotent and safe to call multiple times. After Shutdown returns,
// future calls into the watcher manager become no-ops.
func (s *Server) Shutdown() {
	s.watchers.shutdown()
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

	s.watchers.reconcile(ctx, rootURIs)
}

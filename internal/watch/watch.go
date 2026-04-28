// Package watch owns the per-root fsnotify watcher lifecycle.
//
// A Manager spawns at most one goroutine per root URI. Each goroutine
// runs an fsnotify-based watch loop that observes the root's resolved
// Taskfile graph and triggers reloads on the orchestrator (Server) via
// the StateProvider interface. Manager has its own internal lock and
// must be invoked without the orchestrator's mutex held.
package watch

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// StateProvider is the contract the orchestrator must satisfy for the
// watch package to drive Taskfile reloads. It exposes the watch
// configuration for a root and triggers a reload of that root's tools.
//
// Implementations must be safe to call from goroutines spawned by the
// Manager; in particular, RootWatchState and ReloadRoot must not require
// the caller to hold any orchestrator-internal lock.
type StateProvider interface {
	// RootWatchState returns the directories the watcher should
	// subscribe to and the set of Taskfile paths within those
	// directories whose modification should trigger a reload. ok is
	// false when the root is no longer tracked, in which case the
	// watch loop exits cleanly.
	RootWatchState(uri string) ([]string, map[string]struct{}, bool)

	// ReloadRoot re-resolves the root's Taskfile graph and updates the
	// orchestrator's tool registry. It is invoked from the watcher
	// goroutine after a debounced filesystem event.
	ReloadRoot(ctx context.Context, uri string) error
}

// LoggerFunc returns the logger to use for a single log call. The watch
// package re-reads it on every emission so the orchestrator can swap its
// logger (e.g. after MCP initialization) without restarting watchers.
type LoggerFunc func() *slog.Logger

// rootWatcher tracks a single per-root watcher goroutine.
type rootWatcher struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// Manager owns the lifecycle of per-root watcher goroutines. Each
// root URI is watched by at most one goroutine; the manager spawns
// watchers for newly added URIs and cancels watchers for removed URIs
// without disturbing the rest. The manager has its own internal lock and
// must be invoked without the orchestrator's mutex held.
type Manager struct {
	// run executes the per-root watch loop. It receives a context that
	// is cancelled when the watcher is stopped (via Apply, Reconcile, or
	// Shutdown). The function MUST return promptly after ctx is
	// cancelled so shutdown can drain.
	run func(ctx context.Context, uri string)

	mu           sync.Mutex
	watchers     map[string]*rootWatcher
	shuttingDown bool
}

// New constructs a Manager that drives an fsnotify-based watch loop for
// each root, calling back into provider on every reload.
func New(provider StateProvider, logger LoggerFunc) *Manager {
	m := newWithRun(nil)
	m.run = func(ctx context.Context, uri string) {
		if err := Watch(ctx, provider, logger, uri); err != nil {
			logger().Error("file watcher failed",
				slog.String("event", "watcher.failed"),
				slog.String("root_uri", uri),
				slog.Any("error", err),
			)
		}
	}
	return m
}

// newWithRun constructs an empty Manager with a custom per-root run
// function. It is intended for tests that want to observe the manager's
// scheduling behaviour without spinning up real fsnotify watchers.
func newWithRun(run func(ctx context.Context, uri string)) *Manager {
	return &Manager{
		run:      run,
		watchers: make(map[string]*rootWatcher),
	}
}

// Apply spawns watchers for URIs in added that are not already running
// and cancels watchers for URIs in removed. The cancelled watchers exit
// asynchronously; Apply does not wait for them. baseCtx is detached via
// context.WithoutCancel so watchers outlive the caller's request scope.
//
// Apply is a no-op once Shutdown has been called.
func (m *Manager) Apply(baseCtx context.Context, added, removed []string) {
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
		// Apply (on removal) or Shutdown; gosec cannot trace this.
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

// Reconcile diffs the running watchers against desired and applies the
// difference. Existing watchers for URIs in desired are not disturbed.
// Reconcile is a no-op once Shutdown has been called.
func (m *Manager) Reconcile(baseCtx context.Context, desired []string) {
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

	m.Apply(baseCtx, added, removed)
}

// Shutdown cancels every running watcher and waits for them to exit.
// After Shutdown returns, all subsequent Apply and Reconcile calls are
// no-ops. Shutdown is idempotent and safe to call from multiple
// goroutines concurrently.
func (m *Manager) Shutdown() {
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

// Active reports the number of running watchers. Intended for tests.
func (m *Manager) Active() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.watchers)
}

// IsShuttingDown reports whether Shutdown has been called. Intended for
// tests.
func (m *Manager) IsShuttingDown() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.shuttingDown
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

// Watch runs the per-root fsnotify watch loop until ctx is cancelled or
// the watcher reports a fatal setup error. It is exported primarily so
// tests can drive the loop directly without going through Manager.
//
// On reload events, Watch invokes provider.ReloadRoot. After every
// reload it re-reads provider.RootWatchState so newly-included
// Taskfiles are picked up. logger is re-read on every emission so the
// caller can swap loggers without restarting the watcher.
func Watch(ctx context.Context, provider StateProvider, logger LoggerFunc, uri string) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create file watcher: %w", err)
	}
	defer func() {
		_ = watcher.Close()
	}()

	watchDirs, watchTaskfiles, ok := provider.RootWatchState(uri)
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
			if err := provider.ReloadRoot(ctx, uri); err != nil {
				logger().Error("failed to reload tools for root",
					slog.String("event", "watcher.reload_failed"),
					slog.String("root_uri", uri),
					slog.Any("error", err),
				)
				continue
			}

			watchDirs, watchTaskfiles, ok = provider.RootWatchState(uri)
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
			logger().Warn("file watcher error",
				slog.String("event", "watcher.fs_error"),
				slog.String("root_uri", uri),
				slog.Any("error", err),
			)
		}
	}
}

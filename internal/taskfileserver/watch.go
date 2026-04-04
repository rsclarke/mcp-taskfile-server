package taskfileserver

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// rootWatchState returns copies of the current watch configuration for a root.
func (s *Server) rootWatchState(uri string) ([]string, map[string]struct{}, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	root, ok := s.roots[uri]
	if !ok {
		return nil, nil, false
	}

	return cloneStrings(root.watchDirs), cloneStringSet(root.watchTaskfiles), true
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

// watchTaskfiles starts file watchers for the given roots and blocks until the
// context is cancelled. The caller must provide a snapshot captured under lock.
func (s *Server) watchTaskfiles(ctx context.Context, roots []rootSnapshot) error {
	var wg sync.WaitGroup
	var firstErr error
	var errOnce sync.Once

	for _, rs := range roots {
		wg.Go(func() {
			if err := s.watchRootTaskfiles(ctx, rs.uri); err != nil {
				errOnce.Do(func() { firstErr = err })
			}
		})
	}

	wg.Wait()
	return firstErr
}

// restartWatchers cancels any running file watchers, waits for them to exit,
// then starts new ones for all current roots. The provided ctx is
// intentionally detached via context.WithoutCancel because callers pass
// request-scoped contexts that are cancelled after the handler returns.
func (s *Server) restartWatchers(ctx context.Context) {
	s.mu.Lock()

	// Capture previous watcher generation's cancel and done channel.
	prevCancel := s.watchCancel
	prevDone := s.watchDone

	// Snapshot roots while we hold the lock.
	snap := make([]rootSnapshot, 0, len(s.roots))
	for uri := range s.roots {
		snap = append(snap, rootSnapshot{uri: uri})
	}

	// Prepare the new watcher generation before releasing the lock.
	// Detach from the caller's request-scoped context which is cancelled
	// after the handler returns; the watcher must outlive the request.
	watchCtx, cancel := context.WithCancel(context.WithoutCancel(ctx)) //nolint:gosec // cancel is stored in s.watchCancel and called on next restart or shutdown
	done := make(chan struct{})
	s.watchCancel = cancel
	s.watchDone = done
	s.mu.Unlock()

	if prevCancel != nil {
		prevCancel()
	}
	if prevDone != nil {
		<-prevDone
	}

	go func() {
		defer close(done)
		if err := s.watchTaskfiles(watchCtx, snap); err != nil {
			log.Printf("file watcher failed: %v", err)
		}
	}()
}

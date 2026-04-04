package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/go-task/task/v3"
)

// dirToURI converts an absolute directory path to a file:// URI.
func dirToURI(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}).String()
}

// uriToDir converts a file:// URI back to a local directory path.
func uriToDir(uri string) (string, error) {
	return fileURIToPath(uri)
}

// fileURIToPath parses a local file:// URI into a filesystem path.
func fileURIToPath(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid URI %q: %w", raw, err)
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("unsupported URI scheme %q (only file:// is supported)", u.Scheme)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("file URI %q must not include query or fragment", raw)
	}
	if u.Host != "" && !strings.EqualFold(u.Host, "localhost") {
		return "", fmt.Errorf("UNC file URI %q is not supported", raw)
	}

	path := u.Path
	if path == "" {
		return "", fmt.Errorf("file URI %q is missing a path", raw)
	}
	if isWindowsDriveURIPath(path) {
		if runtime.GOOS != "windows" {
			return "", fmt.Errorf("windows file URI %q is not supported on %s", raw, runtime.GOOS)
		}
		path = strings.TrimPrefix(path, "/")
	}

	return filepath.Clean(filepath.FromSlash(path)), nil
}

func isWindowsDriveURIPath(path string) bool {
	if len(path) < 3 || path[0] != '/' || path[2] != ':' {
		return false
	}

	drive := path[1]
	return (drive >= 'a' && drive <= 'z') || (drive >= 'A' && drive <= 'Z')
}

// loadRoot creates a new rootState by loading the Taskfile from the given directory.
func loadRoot(ctx context.Context, dir string) (*rootState, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve path: %w", err)
	}

	watchTaskfiles, watchDirs, err := loadTaskfileWatchSet(ctx, abs)
	if err != nil {
		return nil, err
	}

	executor := task.NewExecutor(
		task.WithDir(abs),
		task.WithSilent(true),
	)

	if err := executor.Setup(); err != nil {
		return nil, fmt.Errorf("failed to setup task executor for %s: %w", abs, err)
	}

	return &rootState{
		executor:       executor,
		taskfile:       executor.Taskfile,
		workdir:        abs,
		watchDirs:      watchDirs,
		watchTaskfiles: watchTaskfiles,
	}, nil
}

// unloadRoot removes and cleans up the root with the given URI.
func (s *TaskfileServer) unloadRoot(uri string) {
	root, ok := s.roots[uri]
	if !ok {
		return
	}
	if root.watcher != nil {
		_ = root.watcher.Close()
	}
	delete(s.roots, uri)
}

// disableRootTools clears the loaded Taskfile state for a root and syncs the
// server so any previously registered tools are withdrawn. The existing watch
// set is preserved so restoring the Taskfile can be detected and reloaded.
func (s *TaskfileServer) disableRootTools(root *rootState) error {
	root.executor = nil
	root.taskfile = nil
	return s.syncTools()
}

// reloadRoot re-creates the task executor for a given root URI and syncs
// the global MCP tool set.
func (s *TaskfileServer) reloadRoot(ctx context.Context, uri string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	root, ok := s.roots[uri]
	if !ok {
		return fmt.Errorf("unknown root %q", uri)
	}

	watchTaskfiles, watchDirs, err := loadTaskfileWatchSet(ctx, root.workdir)
	if err != nil {
		if syncErr := s.disableRootTools(root); syncErr != nil {
			return fmt.Errorf("failed to reload root %s: %w", uri, errors.Join(err, fmt.Errorf("failed to clear stale tools: %w", syncErr)))
		}
		return fmt.Errorf("failed to reload root %s: %w", uri, err)
	}

	executor := task.NewExecutor(
		task.WithDir(root.workdir),
		task.WithSilent(true),
	)
	if err := executor.Setup(); err != nil {
		if syncErr := s.disableRootTools(root); syncErr != nil {
			return fmt.Errorf("failed to setup task executor for %s: %w", root.workdir, errors.Join(err, fmt.Errorf("failed to clear stale tools: %w", syncErr)))
		}
		return fmt.Errorf("failed to setup task executor for %s: %w", root.workdir, err)
	}
	root.executor = executor
	root.taskfile = executor.Taskfile
	root.watchDirs = watchDirs
	root.watchTaskfiles = watchTaskfiles
	return s.syncTools()
}

// loadAndRegisterTools re-creates the task executor from the single root
// working directory and syncs the MCP tool set. This is a convenience
// wrapper for the single-root case.
func (s *TaskfileServer) loadAndRegisterTools() error {
	for uri := range s.roots {
		return s.reloadRoot(context.Background(), uri)
	}
	return errors.New("no roots configured")
}

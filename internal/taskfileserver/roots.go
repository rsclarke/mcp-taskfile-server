package taskfileserver

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/go-task/task/v3"
	"github.com/go-task/task/v3/taskfile"
)

// dirToURI converts an absolute directory path to a file:// URI.
func dirToURI(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}).String()
}

// canonicalRootURI resolves a client-provided local file URI to the canonical
// absolute file URI we use as the server's internal root identity. Equivalent
// aliases such as file:///repo and file://localhost/repo collapse to the same
// canonical URI and directory.
func canonicalRootURI(raw string) (string, string, error) {
	dir, err := fileURIToPath(raw)
	if err != nil {
		return "", "", err
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", "", fmt.Errorf("failed to resolve path for %q: %w", raw, err)
	}

	return dirToURI(abs), abs, nil
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

// loadRoot creates a new Root by loading the Taskfile from the given directory.
func loadRoot(ctx context.Context, dir string) (*Root, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve path: %w", err)
	}

	entrypoint, watchTaskfiles, watchDirs, err := loadTaskfileWatchSet(ctx, abs)
	if err != nil {
		return nil, err
	}

	executor := task.NewExecutor(
		task.WithDir(abs),
		task.WithEntrypoint(entrypoint),
		task.WithSilent(true),
	)

	if err := executor.Setup(); err != nil {
		return nil, fmt.Errorf("failed to setup task executor for %s: %w", abs, err)
	}

	return &Root{
		taskfile:       executor.Taskfile,
		workdir:        abs,
		watchDirs:      watchDirs,
		watchTaskfiles: watchTaskfiles,
	}, nil
}

// newUnloadedRoot creates a root placeholder for a workspace that does not yet
// have a loadable root Taskfile. Watching the root directory lets us pick up a
// Taskfile that is created or fixed after startup.
func newUnloadedRoot(dir string) (*Root, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve path: %w", err)
	}

	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("failed to stat root %s: %w", abs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("root %s is not a directory", abs)
	}

	watchTaskfiles := make(map[string]struct{}, len(taskfile.DefaultTaskfiles))
	for _, filename := range taskfile.DefaultTaskfiles {
		watchTaskfiles[filepath.Join(abs, filename)] = struct{}{}
	}

	return &Root{
		workdir:        abs,
		watchDirs:      []string{abs},
		watchTaskfiles: watchTaskfiles,
	}, nil
}

// unloadRoot removes and cleans up the root with the given canonical URI.
func (s *Server) unloadRoot(uri string) {
	delete(s.roots, uri)
}

// disableRootToolsLocked clears the loaded Taskfile state for a root and
// bumps the generation. The caller must hold s.mu. The actual MCP sync
// is performed by the caller after releasing the lock.
func (s *Server) disableRootToolsLocked(root *Root) {
	root.taskfile = nil
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
	workdir := root.workdir
	s.mu.Unlock()

	entrypoint, watchTaskfiles, watchDirs, err := loadTaskfileWatchSet(ctx, workdir)
	if err != nil {
		s.mu.Lock()
		s.disableRootToolsLocked(root)
		s.mu.Unlock()
		syncErr := s.syncTools()
		if syncErr != nil {
			return fmt.Errorf("failed to reload root %s: %w", uri, errors.Join(err, fmt.Errorf("failed to clear stale tools: %w", syncErr)))
		}
		return fmt.Errorf("failed to reload root %s: %w", uri, err)
	}

	executor := task.NewExecutor(
		task.WithDir(workdir),
		task.WithEntrypoint(entrypoint),
		task.WithSilent(true),
	)
	if err := executor.Setup(); err != nil {
		s.mu.Lock()
		s.disableRootToolsLocked(root)
		s.mu.Unlock()
		syncErr := s.syncTools()
		if syncErr != nil {
			return fmt.Errorf("failed to setup task executor for %s: %w", workdir, errors.Join(err, fmt.Errorf("failed to clear stale tools: %w", syncErr)))
		}
		return fmt.Errorf("failed to setup task executor for %s: %w", workdir, err)
	}

	s.mu.Lock()
	root.taskfile = executor.Taskfile
	root.watchDirs = watchDirs
	root.watchTaskfiles = watchTaskfiles
	s.generation++
	s.mu.Unlock()

	return s.syncTools()
}

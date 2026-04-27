// Package roots loads and represents the Taskfile graphs for one or more
// workspace roots. It is the single owner of the *Root value type and
// contains all URI canonicalisation and Taskfile graph parsing helpers.
//
// This package must not import the MCP SDK; it is consumed by the server
// package, which adapts roots.Root values into MCP state.
package roots

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-task/task/v3"
	"github.com/go-task/task/v3/taskfile"
)

// Build resolves the Taskfile graph for workdir, sets up a task
// executor, and returns a fully populated *Root. workdir must already be
// an absolute path.
func Build(ctx context.Context, workdir string) (*Root, error) {
	entrypoint, watchTaskfiles, watchDirs, err := loadTaskfileWatchSet(ctx, workdir)
	if err != nil {
		return nil, err
	}

	executor := task.NewExecutor(
		task.WithDir(workdir),
		task.WithEntrypoint(entrypoint),
		task.WithSilent(true),
	)
	if err := executor.Setup(); err != nil {
		return nil, fmt.Errorf("failed to setup task executor for %s: %w", workdir, err)
	}

	return &Root{
		Taskfile:       executor.Taskfile,
		Workdir:        workdir,
		WatchDirs:      watchDirs,
		WatchTaskfiles: watchTaskfiles,
	}, nil
}

// Load creates a new Root by loading the Taskfile from the given directory.
func Load(ctx context.Context, dir string) (*Root, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve path: %w", err)
	}

	return Build(ctx, abs)
}

// NewUnloaded creates a root placeholder for a workspace that does not
// yet have a loadable root Taskfile. Watching the root directory lets
// callers pick up a Taskfile that is created or fixed after startup.
func NewUnloaded(dir string) (*Root, error) {
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
		Workdir:        abs,
		WatchDirs:      []string{abs},
		WatchTaskfiles: watchTaskfiles,
	}, nil
}

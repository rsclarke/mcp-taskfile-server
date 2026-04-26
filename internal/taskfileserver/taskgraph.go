package taskfileserver

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"

	"github.com/go-task/task/v3/taskfile"
)

// loadTaskfileWatchSet reads the resolved Taskfile graph for a root and returns
// the local Taskfile files and parent directories that should be watched.
func loadTaskfileWatchSet(ctx context.Context, dir string) (string, map[string]struct{}, []string, error) {
	entrypoint, err := resolveRootTaskfile(dir)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to resolve root Taskfile for %s: %w", dir, err)
	}

	rootNode, err := taskfile.NewRootNode(entrypoint, dir, false, 0)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to resolve root Taskfile for %s: %w", dir, err)
	}

	graph, err := taskfile.NewReader().Read(ctx, rootNode)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to read Taskfile graph for %s: %w", dir, err)
	}

	adjacencyMap, err := graph.AdjacencyMap()
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to inspect Taskfile graph for %s: %w", dir, err)
	}

	watchTaskfiles := make(map[string]struct{}, len(adjacencyMap))
	watchDirs := make(map[string]struct{}, len(adjacencyMap))
	for location := range adjacencyMap {
		path, ok, err := taskfileLocationToPath(location)
		if err != nil {
			return "", nil, nil, err
		}
		if !ok {
			continue
		}
		watchTaskfiles[path] = struct{}{}
		watchDirs[filepath.Dir(path)] = struct{}{}
	}

	return entrypoint, watchTaskfiles, sortedKeys(watchDirs), nil
}

func resolveRootTaskfile(dir string) (string, error) {
	for _, filename := range taskfile.DefaultTaskfiles {
		path := filepath.Join(dir, filename)
		info, err := os.Stat(path)
		if err == nil {
			if info.IsDir() {
				continue
			}
			return filepath.Clean(path), nil
		}
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		return "", fmt.Errorf("failed to stat %s: %w", path, err)
	}

	return "", fmt.Errorf("no Taskfile found directly in root %s", dir)
}

// taskfileLocationToPath normalizes a Taskfile graph vertex location into a
// local filesystem path. Non-local taskfiles are ignored.
func taskfileLocationToPath(location string) (string, bool, error) {
	if filepath.IsAbs(location) {
		return filepath.Clean(location), true, nil
	}

	u, err := url.Parse(location)
	if err != nil {
		return "", false, fmt.Errorf("invalid Taskfile location %q: %w", location, err)
	}
	if u.Scheme == "" {
		path, absErr := filepath.Abs(location)
		if absErr != nil {
			return "", false, fmt.Errorf("failed to resolve Taskfile path %q: %w", location, absErr)
		}
		return filepath.Clean(path), true, nil
	}
	if u.Scheme != "file" {
		return "", false, nil
	}

	path, err := fileURIToPath(location)
	if err != nil {
		return "", false, err
	}

	return path, true, nil
}

// sortedKeys returns the sorted keys of a string set.
func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for value := range values {
		keys = append(keys, value)
	}
	slices.Sort(keys)
	return keys
}

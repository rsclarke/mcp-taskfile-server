package main

import (
	"context"
	"sync"

	"github.com/fsnotify/fsnotify"
	"github.com/go-task/task/v3"
	"github.com/go-task/task/v3/taskfile/ast"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// rootState holds the per-root task executor state.
type rootState struct {
	executor        *task.Executor
	taskfile        *ast.Taskfile
	workdir         string
	registeredTools []string
	watchDirs       []string
	watchTaskfiles  map[string]struct{}
	watcher         *fsnotify.Watcher
}

// TaskfileServer represents our MCP server for Taskfile.yml.
type TaskfileServer struct {
	roots           map[string]*rootState
	mcpServer       *mcp.Server
	registeredTools map[string]mcp.Tool
	mu              sync.Mutex
	watchCancel     context.CancelFunc
	watchDone       chan struct{}
}

// rootSnapshot is a URI→rootState pair captured under lock for use by
// watchTaskfiles without holding the mutex.
type rootSnapshot struct {
	uri  string
	root *rootState
}

// cloneStringSet returns a shallow copy of a string set.
func cloneStringSet(values map[string]struct{}) map[string]struct{} {
	cloned := make(map[string]struct{}, len(values))
	for value := range values {
		cloned[value] = struct{}{}
	}
	return cloned
}

// cloneStrings returns a copy of a string slice.
func cloneStrings(values []string) []string {
	return append([]string(nil), values...)
}

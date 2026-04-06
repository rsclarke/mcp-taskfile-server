package taskfileserver

import (
	"context"
	"sync"

	"github.com/go-task/task/v3/taskfile/ast"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Root holds the loaded per-root Taskfile data.
type Root struct {
	taskfile       *ast.Taskfile
	workdir        string
	watchDirs      []string
	watchTaskfiles map[string]struct{}
}

// Server represents our MCP server for Taskfile.yml.
type Server struct {
	roots           map[string]*Root
	mcpServer       *mcp.Server
	registeredTools map[string]mcp.Tool
	mu              sync.Mutex
	watchCancel     context.CancelFunc
	watchDone       chan struct{}
	shuttingDown    bool
}

// rootSnapshot is a canonical root URI captured under lock for use by
// watchTaskfiles without holding the mutex.
type rootSnapshot struct {
	uri string
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

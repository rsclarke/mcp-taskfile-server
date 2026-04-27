package roots

import "github.com/go-task/task/v3/taskfile/ast"

// Root holds the loaded per-root Taskfile data. Once a *Root is published
// to a consumer (e.g. a server's roots map) its fields are treated as
// read-only; mutations are performed by replacing the pointer rather than
// writing through the existing value, so concurrent readers (snapshots,
// watchers) always observe a consistent state. *Root values therefore
// must NOT be mutated by code outside of construction sites in this
// package.
//
// The fields are exported because reconcile, tool planning, and watcher
// code lives in other packages and needs to read them. They are still
// considered immutable after construction.
type Root struct {
	Taskfile       *ast.Taskfile
	Workdir        string
	WatchDirs      []string
	WatchTaskfiles map[string]struct{}
}

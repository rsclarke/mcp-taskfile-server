// Package main implements an MCP server that exposes Taskfile tasks as tools.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/go-task/task/v3"
	"github.com/go-task/task/v3/taskfile"
	"github.com/go-task/task/v3/taskfile/ast"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Build-time variables set via -ldflags.
var (
	serverName    = "mcp-taskfile-server"
	serverVersion = "dev"
)

// sanitizeToolName converts a Taskfile task name into a valid MCP tool name.
// It replaces colons with underscores and strips wildcard (*) segments.
// The returned name conforms to the MCP spec: [a-zA-Z0-9_.-]{1,128}.
func sanitizeToolName(taskName string) string {
	// Replace colons with underscores
	name := strings.ReplaceAll(taskName, ":", "_")

	// Remove wildcard segments ("_*" left over from ":*")
	for strings.Contains(name, "_*") {
		name = strings.ReplaceAll(name, "_*", "")
	}

	// Remove any remaining standalone asterisks
	name = strings.ReplaceAll(name, "*", "")

	// Trim trailing underscores left after stripping wildcards
	name = strings.TrimRight(name, "_")

	return name
}

// isWildcardTask returns true if the task name contains wildcard segments.
func isWildcardTask(taskName string) bool {
	return strings.Contains(taskName, "*")
}

// countWildcards returns the number of wildcard segments in a task name.
func countWildcards(taskName string) int {
	return strings.Count(taskName, "*")
}

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

// validRootPrefixChars matches characters NOT allowed in a root prefix.
var validRootPrefixChars = regexp.MustCompile(`[^a-zA-Z0-9_.\-]`)

// sanitizeRootPrefix converts a root name or directory basename into a valid
// MCP tool name prefix component.
func sanitizeRootPrefix(name string) string {
	s := validRootPrefixChars.ReplaceAllString(name, "_")
	s = strings.Trim(s, "_")
	if s == "" {
		return "root"
	}
	return s
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

// loadTaskfileWatchSet reads the resolved Taskfile graph for a root and returns
// the local Taskfile files and parent directories that should be watched.
func loadTaskfileWatchSet(ctx context.Context, dir string) (map[string]struct{}, []string, error) {
	rootNode, err := taskfile.NewRootNode("", dir, false, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to resolve root Taskfile for %s: %w", dir, err)
	}

	graph, err := taskfile.NewReader().Read(ctx, rootNode)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read Taskfile graph for %s: %w", dir, err)
	}

	adjacencyMap, err := graph.AdjacencyMap()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to inspect Taskfile graph for %s: %w", dir, err)
	}

	watchTaskfiles := make(map[string]struct{}, len(adjacencyMap))
	watchDirs := make(map[string]struct{}, len(adjacencyMap))
	for location := range adjacencyMap {
		path, ok, err := taskfileLocationToPath(location)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			continue
		}
		watchTaskfiles[path] = struct{}{}
		watchDirs[filepath.Dir(path)] = struct{}{}
	}

	return watchTaskfiles, sortedKeys(watchDirs), nil
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

// rootPrefix returns the tool name prefix for a root. When there is only one
// root the prefix is empty; with multiple roots it is derived from the root
// directory's basename.
func (s *TaskfileServer) rootPrefix(root *rootState) string {
	if len(s.roots) <= 1 {
		return ""
	}
	return sanitizeRootPrefix(filepath.Base(root.workdir))
}

// prefixedToolName returns the tool name with an optional root prefix.
func prefixedToolName(prefix, toolName string) string {
	if prefix == "" {
		return toolName
	}
	return prefix + "_" + toolName
}

// NewTaskfileServer creates a new Taskfile MCP server.
func NewTaskfileServer() *TaskfileServer {
	return &TaskfileServer{
		roots: make(map[string]*rootState),
	}
}

// createTaskHandler creates a handler function for a specific task.
// For wildcard tasks, it reconstructs the full task name from the MATCH argument.
func createTaskHandler(root *rootState, taskName string) mcp.ToolHandler {
	wildcard := isWildcardTask(taskName)
	wildcardCount := countWildcards(taskName)

	return func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Extract variables from request arguments
		var arguments map[string]any
		if request.Params.Arguments != nil {
			if err := json.Unmarshal(request.Params.Arguments, &arguments); err != nil {
				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Failed to parse arguments: %v", err)}},
					IsError: true,
				}, nil
			}
		}

		// Resolve the actual task name, substituting wildcards from MATCH
		resolvedName := taskName
		if wildcard {
			matchVal, ok := arguments["MATCH"].(string)
			if !ok || matchVal == "" {
				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: "MATCH argument is required for wildcard tasks"}},
					IsError: true,
				}, nil
			}
			parts := strings.SplitN(matchVal, ",", wildcardCount)
			if len(parts) != wildcardCount {
				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("MATCH requires %d comma-separated value(s), got %d", wildcardCount, len(parts))}},
					IsError: true,
				}, nil
			}
			resolvedName = taskName
			for _, p := range parts {
				resolvedName = strings.Replace(resolvedName, "*", strings.TrimSpace(p), 1)
			}
			delete(arguments, "MATCH")
		}

		vars := ast.NewVars()

		// Add all provided arguments as variables
		for key, value := range arguments {
			if strValue, ok := value.(string); ok {
				vars.Set(key, ast.Var{Value: strValue})
			}
		}

		// Create buffers to capture output
		var stdout, stderr bytes.Buffer

		// Create a new executor with output capture for this execution
		executor := task.NewExecutor(
			task.WithDir(root.workdir),
			task.WithStdout(&stdout),
			task.WithStderr(&stderr),
			task.WithSilent(true),
		)

		// Setup the executor
		if err := executor.Setup(); err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Task '%s' setup failed: %v", resolvedName, err)}},
				IsError: true,
			}, nil
		}

		// Create a call for this task
		call := &task.Call{
			Task: resolvedName,
			Vars: vars,
		}

		// Execute the task
		taskErr := executor.Run(ctx, call)

		// Collect output
		stdoutStr := stdout.String()
		stderrStr := stderr.String()

		// Build result message
		var result strings.Builder

		if taskErr != nil {
			fmt.Fprintf(&result, "Task '%s' failed with error: %v\n", resolvedName, taskErr)
		} else {
			fmt.Fprintf(&result, "Task '%s' completed successfully.\n", resolvedName)
		}

		if stdoutStr != "" {
			result.WriteString("\nOutput:\n" + stdoutStr)
		}

		if stderrStr != "" {
			result.WriteString("\nErrors:\n" + stderrStr)
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: result.String()}},
			IsError: taskErr != nil,
		}, nil
	}
}

// createToolForTask creates an MCP tool definition for a given task.
// The tool name is sanitized for MCP compatibility; the description
// references the original Taskfile task name for clarity. The prefix
// parameter is used in multi-root mode to namespace tool names.
func createToolForTask(root *rootState, prefix, taskName string, taskDef *ast.Task) *mcp.Tool {
	toolName := prefixedToolName(prefix, sanitizeToolName(taskName))

	description := taskDef.Desc
	if description == "" {
		description = "Execute task: " + taskName
	}
	if sanitizeToolName(taskName) != taskName {
		description += fmt.Sprintf(" (task: %s)", taskName)
	}

	// Collect all variables (global + task-specific)
	allVars := make(map[string]ast.Var)

	// Add global variables first
	if root.taskfile.Vars != nil && root.taskfile.Vars.Len() > 0 {
		maps.Insert(allVars, root.taskfile.Vars.All())
	}

	// Add task-specific variables (these override global ones)
	if taskDef.Vars != nil && taskDef.Vars.Len() > 0 {
		maps.Insert(allVars, taskDef.Vars.All())
	}

	// Build JSON Schema properties for all variables
	properties := make(map[string]any)
	required := []string{}

	for varName, varDef := range allVars {
		defaultValue := ""
		if strVal, ok := varDef.Value.(string); ok {
			defaultValue = strVal
		}
		properties[varName] = map[string]any{
			"type":        "string",
			"description": fmt.Sprintf("Variable: %s (default: %s)", varName, defaultValue),
		}
	}

	// Add MATCH parameter for wildcard tasks
	if isWildcardTask(taskName) {
		n := countWildcards(taskName)
		matchDesc := "Wildcard value for task pattern " + taskName
		if n > 1 {
			matchDesc += fmt.Sprintf(" (%d comma-separated values)", n)
		}
		properties["MATCH"] = map[string]any{
			"type":        "string",
			"description": matchDesc,
		}
		required = append(required, "MATCH")
	}

	schemaMap := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schemaMap["required"] = required
	}

	schema, err := json.Marshal(schemaMap)
	if err != nil {
		schema = []byte(`{"type":"object"}`)
	}

	return &mcp.Tool{
		Name:        toolName,
		Description: description,
		InputSchema: json.RawMessage(schema),
	}
}

// buildToolSet discovers all tasks across all roots and returns tool definitions
// and handlers without registering them on a server. It also populates each
// root's registeredTools list.
func (s *TaskfileServer) buildToolSet() (map[string]mcp.Tool, map[string]mcp.ToolHandler, error) {
	tools := make(map[string]mcp.Tool)
	handlers := make(map[string]mcp.ToolHandler)

	for _, root := range s.roots {
		root.registeredTools = nil
	}

	for _, root := range s.roots {
		if root.taskfile == nil || root.taskfile.Tasks == nil {
			continue
		}

		prefix := s.rootPrefix(root)

		for taskName, taskDef := range root.taskfile.Tasks.All(nil) {
			if taskDef.Internal {
				continue
			}

			tool := createToolForTask(root, prefix, taskName, taskDef)
			if _, exists := tools[tool.Name]; exists {
				return nil, nil, fmt.Errorf("tool name collision: %q", tool.Name)
			}
			tools[tool.Name] = *tool
			handlers[tool.Name] = createTaskHandler(root, taskName)
			root.registeredTools = append(root.registeredTools, tool.Name)
		}
	}

	return tools, handlers, nil
}

// toolsEqual reports whether two tool definitions are equivalent
// by comparing Name, Description, and InputSchema bytes.
func toolsEqual(a, b *mcp.Tool) bool {
	if a.Name != b.Name || a.Description != b.Description {
		return false
	}
	aSchema, err := json.Marshal(a.InputSchema)
	if err != nil {
		return false
	}
	bSchema, err := json.Marshal(b.InputSchema)
	if err != nil {
		return false
	}
	return bytes.Equal(aSchema, bSchema)
}

// syncTools builds the current tool set, diffs it against previously
// registered tools, and adds/removes tools on the MCP server as needed.
func (s *TaskfileServer) syncTools() error {
	tools, handlers, err := s.buildToolSet()
	if err != nil {
		return err
	}

	// Remove tools that no longer exist or have changed
	var stale []string
	for name, old := range s.registeredTools {
		if newTool, ok := tools[name]; !ok {
			stale = append(stale, name)
		} else if !toolsEqual(&old, &newTool) {
			stale = append(stale, name)
		}
	}
	if len(stale) > 0 {
		s.mcpServer.RemoveTools(stale...)
	}

	// Add tools that are new or were removed above due to changes
	for name, tool := range tools {
		if old, ok := s.registeredTools[name]; ok && toolsEqual(&old, &tool) {
			continue
		}
		t := tool
		s.mcpServer.AddTool(&t, handlers[name])
	}

	s.registeredTools = tools
	return nil
}

// disableRootTools clears the loaded Taskfile state for a root and syncs the
// server so any previously registered tools are withdrawn. The existing watch
// set is preserved so restoring the Taskfile can be detected and reloaded.
func (s *TaskfileServer) disableRootTools(root *rootState) error {
	root.executor = nil
	root.taskfile = nil
	return s.syncTools()
}

// isTaskfile reports whether the given path's basename matches one of the
// supported Taskfile filenames from taskfile.DefaultTaskfiles.
func isTaskfile(path string) bool {
	return slices.Contains(taskfile.DefaultTaskfiles, filepath.Base(path))
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

// rootWatchState returns copies of the current watch configuration for a root.
func (s *TaskfileServer) rootWatchState(root *rootState) ([]string, map[string]struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneStrings(root.watchDirs), cloneStringSet(root.watchTaskfiles)
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
func (s *TaskfileServer) watchRootTaskfiles(ctx context.Context, uri string, root *rootState) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create file watcher: %w", err)
	}

	s.mu.Lock()
	root.watcher = watcher
	s.mu.Unlock()

	defer func() {
		_ = watcher.Close()
		s.mu.Lock()
		if root.watcher == watcher {
			root.watcher = nil
		}
		s.mu.Unlock()
	}()

	watchDirs, watchTaskfiles := s.rootWatchState(root)
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

			watchDirs, watchTaskfiles = s.rootWatchState(root)
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

// rootSnapshot is a URI→rootState pair captured under lock for use by
// watchTaskfiles without holding the mutex.
type rootSnapshot struct {
	uri  string
	root *rootState
}

// watchTaskfiles starts file watchers for the given roots and blocks until the
// context is cancelled. The caller must provide a snapshot captured under lock.
func (s *TaskfileServer) watchTaskfiles(ctx context.Context, roots []rootSnapshot) error {
	var wg sync.WaitGroup
	var firstErr error
	var errOnce sync.Once

	for _, rs := range roots {
		wg.Go(func() {
			if err := s.watchRootTaskfiles(ctx, rs.uri, rs.root); err != nil {
				errOnce.Do(func() { firstErr = err })
			}
		})
	}

	wg.Wait()
	return firstErr
}

// isMethodNotFound reports whether err is a JSON-RPC "method not found" error,
// which indicates the client does not support the requested capability.
func isMethodNotFound(err error) bool {
	var wireErr *jsonrpc.Error
	return errors.As(err, &wireErr) && wireErr.Code == jsonrpc.CodeMethodNotFound
}

// restartWatchers cancels any running file watchers, waits for them to
// exit, then starts new ones for all current roots. The caller must hold
// s.mu; it is temporarily released while waiting for old watchers so
// that watcher goroutines calling reloadRoot can acquire the lock. The
// provided ctx is intentionally detached via context.WithoutCancel
// because callers pass request-scoped contexts that are cancelled after
// the handler returns.
func (s *TaskfileServer) restartWatchers(ctx context.Context) {
	// Capture previous watcher generation's cancel and done channel.
	prevCancel := s.watchCancel
	prevDone := s.watchDone

	// Snapshot roots while we hold the lock.
	snap := make([]rootSnapshot, 0, len(s.roots))
	for uri, root := range s.roots {
		snap = append(snap, rootSnapshot{uri: uri, root: root})
	}

	// Prepare the new watcher generation before releasing the lock.
	// Detach from the caller's request-scoped context which is cancelled
	// after the handler returns; the watcher must outlive the request.
	watchCtx, cancel := context.WithCancel(context.WithoutCancel(ctx)) //nolint:gosec // cancel is stored in s.watchCancel and called on next restart or shutdown
	done := make(chan struct{})
	s.watchCancel = cancel
	s.watchDone = done

	// Release lock while waiting for old watchers so they can acquire
	// it during teardown (e.g. clearing root.watcher).
	s.mu.Unlock()
	if prevCancel != nil {
		prevCancel()
	}
	if prevDone != nil {
		<-prevDone
	}
	s.mu.Lock()

	go func() {
		defer close(done)
		if err := s.watchTaskfiles(watchCtx, snap); err != nil {
			log.Printf("file watcher failed: %v", err)
		}
	}()
}

// loadRootsFromSession queries the client for its root list and loads each
// one. If the client does not support roots, it falls back to os.Getwd().
func (s *TaskfileServer) loadRootsFromSession(ctx context.Context, session *mcp.ServerSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	rootRes, err := session.ListRoots(ctx, nil)
	if err != nil {
		if !isMethodNotFound(err) {
			return fmt.Errorf("failed to list roots: %w", err)
		}
		// Client does not support roots; fall back to working directory.
		workdir, wdErr := os.Getwd()
		if wdErr != nil {
			return fmt.Errorf("failed to get working directory: %w", wdErr)
		}
		uri := dirToURI(workdir)
		root, loadErr := loadRoot(ctx, workdir)
		if loadErr != nil {
			return loadErr
		}
		s.roots[uri] = root
		if err := s.syncTools(); err != nil {
			return err
		}
		s.restartWatchers(ctx)
		return nil
	}

	for _, r := range rootRes.Roots {
		if _, exists := s.roots[r.URI]; exists {
			continue
		}
		dir, parseErr := uriToDir(r.URI)
		if parseErr != nil {
			log.Printf("skipping root with invalid URI %q: %v", r.URI, parseErr)
			continue
		}
		root, loadErr := loadRoot(ctx, dir)
		if loadErr != nil {
			log.Printf("failed to load root %q: %v", r.URI, loadErr)
			continue
		}
		s.roots[r.URI] = root
	}

	if len(s.roots) == 0 {
		return errors.New("no valid roots found")
	}

	if err := s.syncTools(); err != nil {
		return err
	}
	s.restartWatchers(ctx)
	return nil
}

// handleInitialized is called after the client handshake completes.
func (s *TaskfileServer) handleInitialized(ctx context.Context, req *mcp.InitializedRequest) {
	if err := s.loadRootsFromSession(ctx, req.Session); err != nil {
		log.Printf("failed to initialize roots: %v", err)
	}
}

// handleRootsChanged is called when the client sends roots/list_changed.
func (s *TaskfileServer) handleRootsChanged(ctx context.Context, req *mcp.RootsListChangedRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rootRes, err := req.Session.ListRoots(ctx, nil)
	if err != nil {
		log.Printf("failed to list roots after change: %v", err)
		return
	}

	// Build a set of new URIs.
	newURIs := make(map[string]struct{}, len(rootRes.Roots))
	for _, r := range rootRes.Roots {
		newURIs[r.URI] = struct{}{}
	}

	// Remove roots that are no longer present.
	for uri := range s.roots {
		if _, ok := newURIs[uri]; !ok {
			s.unloadRoot(uri)
		}
	}

	// Add roots that are new.
	for _, r := range rootRes.Roots {
		if _, exists := s.roots[r.URI]; exists {
			continue
		}
		dir, parseErr := uriToDir(r.URI)
		if parseErr != nil {
			log.Printf("skipping root with invalid URI %q: %v", r.URI, parseErr)
			continue
		}
		root, loadErr := loadRoot(ctx, dir)
		if loadErr != nil {
			log.Printf("failed to load root %q: %v", r.URI, loadErr)
			continue
		}
		s.roots[r.URI] = root
	}

	if err := s.syncTools(); err != nil {
		log.Printf("failed to sync tools after roots change: %v", err)
	}
	s.restartWatchers(ctx)
}

func run() error {
	// Create taskfile server
	taskfileServer := NewTaskfileServer()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create MCP server with lifecycle handlers
	mcpServer := mcp.NewServer(
		&mcp.Implementation{
			Name:    serverName,
			Version: serverVersion,
		},
		&mcp.ServerOptions{
			InitializedHandler:      taskfileServer.handleInitialized,
			RootsListChangedHandler: taskfileServer.handleRootsChanged,
		},
	)
	taskfileServer.mcpServer = mcpServer
	taskfileServer.registeredTools = make(map[string]mcp.Tool)

	// Start the stdio server
	if err := mcpServer.Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("server error: %w", err)
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Printf("Server error: %v\n", err)
		os.Exit(1)
	}
}

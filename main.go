// Package main implements an MCP server that exposes Taskfile tasks as tools.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/go-task/task/v3"
	"github.com/go-task/task/v3/taskfile"
	"github.com/go-task/task/v3/taskfile/ast"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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
	watcher         *fsnotify.Watcher
}

// TaskfileServer represents our MCP server for Taskfile.yml.
type TaskfileServer struct {
	roots           map[string]*rootState
	mcpServer       *mcp.Server
	registeredTools map[string]mcp.Tool
	mu              sync.Mutex
}

// dirToURI converts an absolute directory path to a file:// URI.
func dirToURI(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	return "file://" + filepath.ToSlash(abs)
}

// uriToDir converts a file:// URI back to a local directory path.
func uriToDir(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("invalid URI %q: %w", uri, err)
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("unsupported URI scheme %q (only file:// is supported)", u.Scheme)
	}
	return filepath.FromSlash(u.Path), nil
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
func loadRoot(dir string) (*rootState, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve path: %w", err)
	}

	executor := task.NewExecutor(
		task.WithDir(abs),
		task.WithSilent(true),
	)

	if err := executor.Setup(); err != nil {
		return nil, fmt.Errorf("failed to setup task executor for %s: %w", abs, err)
	}

	return &rootState{
		executor: executor,
		taskfile: executor.Taskfile,
		workdir:  abs,
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
func NewTaskfileServer() (*TaskfileServer, error) {
	workdir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get working directory: %w", err)
	}

	root, err := loadRoot(workdir)
	if err != nil {
		return nil, err
	}

	uri := dirToURI(workdir)

	return &TaskfileServer{
		roots: map[string]*rootState{uri: root},
	}, nil
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
		if root.taskfile.Tasks == nil {
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

	if len(tools) == 0 {
		return nil, nil, errors.New("no tasks found in any Taskfile")
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

// isTaskfile reports whether the given path's basename matches one of the
// supported Taskfile filenames from taskfile.DefaultTaskfiles.
func isTaskfile(path string) bool {
	return slices.Contains(taskfile.DefaultTaskfiles, filepath.Base(path))
}

// reloadRoot re-creates the task executor for a given root URI and syncs
// the global MCP tool set.
func (s *TaskfileServer) reloadRoot(uri string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	root, ok := s.roots[uri]
	if !ok {
		return fmt.Errorf("unknown root %q", uri)
	}

	executor := task.NewExecutor(
		task.WithDir(root.workdir),
		task.WithSilent(true),
	)
	if err := executor.Setup(); err != nil {
		return fmt.Errorf("failed to setup task executor for %s: %w", root.workdir, err)
	}
	root.executor = executor
	root.taskfile = executor.Taskfile
	return s.syncTools()
}

// loadAndRegisterTools re-creates the task executor from the single root
// working directory and syncs the MCP tool set. This is a convenience
// wrapper for the single-root case.
func (s *TaskfileServer) loadAndRegisterTools() error {
	for uri := range s.roots {
		return s.reloadRoot(uri)
	}
	return errors.New("no roots configured")
}

// watchRootTaskfiles watches a single root's directory tree for Taskfile
// changes and reloads tools when a relevant file is modified.
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
		root.watcher = nil
		s.mu.Unlock()
	}()

	// Recursively add all directories under the root's workdir.
	err = filepath.WalkDir(root.workdir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return watcher.Add(path)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to walk working directory: %w", err)
	}

	const debounce = 200 * time.Millisecond
	var timer *time.Timer

	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return nil
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}

			// Watch for new directories so we stay recursive.
			if event.Has(fsnotify.Create) {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					_ = watcher.Add(event.Name)
				}
			}

			if !isTaskfile(event.Name) {
				continue
			}

			// Debounce rapid events.
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(debounce, func() {
				if err := s.reloadRoot(uri); err != nil {
					log.Printf("failed to reload tools for root %s: %v", uri, err)
				}
			})
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			log.Printf("file watcher error: %v", err)
		}
	}
}

// watchTaskfiles starts file watchers for all roots and blocks until the
// context is cancelled.
func (s *TaskfileServer) watchTaskfiles(ctx context.Context) error {
	var wg sync.WaitGroup
	var firstErr error
	var errOnce sync.Once

	for uri, root := range s.roots {
		wg.Go(func() {
			if err := s.watchRootTaskfiles(ctx, uri, root); err != nil {
				errOnce.Do(func() { firstErr = err })
			}
		})
	}

	wg.Wait()
	return firstErr
}

func run() error {
	// Create taskfile server
	taskfileServer, err := NewTaskfileServer()
	if err != nil {
		return fmt.Errorf("failed to create taskfile server: %w", err)
	}

	// Create MCP server
	mcpServer := mcp.NewServer(
		&mcp.Implementation{
			Name:    "taskfile-mcp-server",
			Version: "1.0.0",
		},
		nil,
	)
	taskfileServer.mcpServer = mcpServer
	taskfileServer.registeredTools = make(map[string]mcp.Tool)

	// Register all tasks as MCP tools
	if err := taskfileServer.syncTools(); err != nil {
		return fmt.Errorf("failed to register tasks: %w", err)
	}

	// Start watching for Taskfile changes
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := taskfileServer.watchTaskfiles(ctx); err != nil {
			log.Printf("file watcher failed: %v", err)
		}
	}()

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

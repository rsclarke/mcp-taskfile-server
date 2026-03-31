// Package main implements an MCP server that exposes Taskfile tasks as tools.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"strings"

	"github.com/go-task/task/v3"
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

// TaskfileServer represents our MCP server for Taskfile.yml.
type TaskfileServer struct {
	executor        *task.Executor
	taskfile        *ast.Taskfile
	workdir         string
	mcpServer       *mcp.Server
	registeredTools map[string]mcp.Tool
}

// NewTaskfileServer creates a new Taskfile MCP server.
func NewTaskfileServer() (*TaskfileServer, error) {
	workdir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get working directory: %w", err)
	}

	// Create a task executor
	executor := task.NewExecutor(
		task.WithDir(workdir),
		task.WithSilent(true),
	)

	// Setup the executor (this loads the Taskfile)
	if err := executor.Setup(); err != nil {
		return nil, fmt.Errorf("failed to setup task executor: %w", err)
	}

	return &TaskfileServer{
		executor: executor,
		taskfile: executor.Taskfile,
		workdir:  workdir,
	}, nil
}

// createTaskHandler creates a handler function for a specific task.
// For wildcard tasks, it reconstructs the full task name from the MATCH argument.
func (s *TaskfileServer) createTaskHandler(taskName string) mcp.ToolHandler {
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
			task.WithDir(s.workdir),
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
// references the original Taskfile task name for clarity.
func (s *TaskfileServer) createToolForTask(taskName string, taskDef *ast.Task) *mcp.Tool {
	toolName := sanitizeToolName(taskName)

	description := taskDef.Desc
	if description == "" {
		description = "Execute task: " + taskName
	}
	if toolName != taskName {
		description += fmt.Sprintf(" (task: %s)", taskName)
	}

	// Collect all variables (global + task-specific)
	allVars := make(map[string]ast.Var)

	// Add global variables first
	if s.taskfile.Vars != nil && s.taskfile.Vars.Len() > 0 {
		maps.Insert(allVars, s.taskfile.Vars.All())
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

// buildToolSet discovers all tasks and returns tool definitions and handlers
// without registering them on a server.
func (s *TaskfileServer) buildToolSet() (map[string]mcp.Tool, map[string]mcp.ToolHandler, error) {
	if s.taskfile.Tasks == nil {
		return nil, nil, errors.New("no tasks found in Taskfile")
	}

	tools := make(map[string]mcp.Tool)
	handlers := make(map[string]mcp.ToolHandler)

	for taskName, taskDef := range s.taskfile.Tasks.All(nil) {
		if taskDef.Internal {
			continue
		}

		tool := s.createToolForTask(taskName, taskDef)
		tools[tool.Name] = *tool
		handlers[tool.Name] = s.createTaskHandler(taskName)
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

func main() {
	// Create taskfile server
	taskfileServer, err := NewTaskfileServer()
	if err != nil {
		fmt.Printf("Failed to create taskfile server: %v\n", err)
		os.Exit(1)
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
		fmt.Printf("Failed to register tasks: %v\n", err)
		os.Exit(1)
	}

	// Start the stdio server
	if err := mcpServer.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Printf("Server error: %v\n", err)
		os.Exit(1)
	}
}

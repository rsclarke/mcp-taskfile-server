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

// toolRegistrar is an interface for registering MCP tools, satisfied by *mcp.Server.
type toolRegistrar interface {
	AddTool(t *mcp.Tool, h mcp.ToolHandler)
}

// TaskfileServer represents our MCP server for Taskfile.yml.
type TaskfileServer struct {
	executor *task.Executor
	taskfile *ast.Taskfile
	workdir  string
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
func (s *TaskfileServer) createTaskHandler(taskName string) mcp.ToolHandler {
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
				Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Task '%s' setup failed: %v", taskName, err)}},
				IsError: true,
			}, nil
		}

		// Create a call for this task
		call := &task.Call{
			Task: taskName,
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
			fmt.Fprintf(&result, "Task '%s' failed with error: %v\n", taskName, taskErr)
		} else {
			fmt.Fprintf(&result, "Task '%s' completed successfully.\n", taskName)
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
func (s *TaskfileServer) createToolForTask(taskName string, taskDef *ast.Task) *mcp.Tool {
	description := taskDef.Desc
	if description == "" {
		description = "Execute task: " + taskName
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

	schema, err := json.Marshal(map[string]any{
		"type":       "object",
		"properties": properties,
	})
	if err != nil {
		schema = []byte(`{"type":"object"}`)
	}

	return &mcp.Tool{
		Name:        taskName,
		Description: description,
		InputSchema: json.RawMessage(schema),
	}
}

// registerTasks discovers all tasks and registers them as MCP tools.
func (s *TaskfileServer) registerTasks(mcpServer toolRegistrar) error {
	if s.taskfile.Tasks == nil {
		return errors.New("no tasks found in Taskfile")
	}

	// Iterate through all tasks and register them
	for taskName, taskDef := range s.taskfile.Tasks.All(nil) {
		// Skip internal tasks
		if taskDef.Internal {
			continue
		}

		// Create tool definition
		tool := s.createToolForTask(taskName, taskDef)

		// Create handler
		handler := s.createTaskHandler(taskName)

		// Register with MCP server
		mcpServer.AddTool(tool, handler)
	}

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

	// Register all tasks as MCP tools
	if err := taskfileServer.registerTasks(mcpServer); err != nil {
		fmt.Printf("Failed to register tasks: %v\n", err)
		os.Exit(1)
	}

	// Start the stdio server
	if err := mcpServer.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Printf("Server error: %v\n", err)
		os.Exit(1)
	}
}

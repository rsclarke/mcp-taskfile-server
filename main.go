package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/go-task/task/v3"
	"github.com/go-task/task/v3/taskfile/ast"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// TaskfileServer represents our MCP server for Taskfile.yml
type TaskfileServer struct {
	executor *task.Executor
	taskfile *ast.Taskfile
	workdir  string
}

// NewTaskfileServer creates a new Taskfile MCP server
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

// createTaskHandler creates a handler function for a specific task
func (s *TaskfileServer) createTaskHandler(taskName string) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Extract variables from request arguments
		arguments := request.GetArguments()
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
			return mcp.NewToolResultError(fmt.Sprintf("Task '%s' setup failed: %v", taskName, err)), nil
		}

		// Create a call for this task
		call := &task.Call{
			Task: taskName,
			Vars: vars,
		}

		// Execute the task
		err := executor.Run(ctx, call)

		// Collect output
		stdoutStr := stdout.String()
		stderrStr := stderr.String()

		// Build result message
		var result strings.Builder

		if err != nil {
			result.WriteString(fmt.Sprintf("Task '%s' failed with error: %v\n", taskName, err))
		} else {
			result.WriteString(fmt.Sprintf("Task '%s' completed successfully.\n", taskName))
		}

		if stdoutStr != "" {
			result.WriteString(fmt.Sprintf("\nOutput:\n%s", stdoutStr))
		}

		if stderrStr != "" {
			result.WriteString(fmt.Sprintf("\nErrors:\n%s", stderrStr))
		}

		if err != nil {
			return mcp.NewToolResultError(result.String()), nil
		}

		return mcp.NewToolResultText(result.String()), nil
	}
}

// createToolForTask creates an MCP tool definition for a given task
func (s *TaskfileServer) createToolForTask(taskName string, taskDef *ast.Task) mcp.Tool {
	description := taskDef.Desc
	if description == "" {
		description = fmt.Sprintf("Execute task: %s", taskName)
	}

	// Start with basic tool definition
	toolOptions := []mcp.ToolOption{
		mcp.WithDescription(description),
	}

	// Collect all variables (global + task-specific)
	allVars := make(map[string]ast.Var)

	// Add global variables first
	if s.taskfile.Vars != nil && s.taskfile.Vars.Len() > 0 {
		for varName, varDef := range s.taskfile.Vars.All() {
			allVars[varName] = varDef
		}
	}

	// Add task-specific variables (these override global ones)
	if taskDef.Vars != nil && taskDef.Vars.Len() > 0 {
		for varName, varDef := range taskDef.Vars.All() {
			allVars[varName] = varDef
		}
	}

	// Add parameters for all variables
	for varName, varDef := range allVars {
		defaultValue := ""
		if strVal, ok := varDef.Value.(string); ok {
			defaultValue = strVal
		}

		// Add string parameter for each variable
		toolOptions = append(toolOptions,
			mcp.WithString(varName,
				mcp.Description(fmt.Sprintf("Variable: %s (default: %s)", varName, defaultValue)),
			),
		)
	}

	return mcp.NewTool(taskName, toolOptions...)
}

// registerTasks discovers all tasks and registers them as MCP tools
func (s *TaskfileServer) registerTasks(mcpServer *server.MCPServer) error {
	if s.taskfile.Tasks == nil {
		return fmt.Errorf("no tasks found in Taskfile")
	}

	// Iterate through all tasks and register them
	for taskName, taskDef := range s.taskfile.Tasks.All(nil) {
		// Skip internal tasks (starting with :)
		if strings.HasPrefix(taskName, ":") {
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
	mcpServer := server.NewMCPServer(
		"taskfile-mcp-server",
		"1.0.0",
		server.WithToolCapabilities(false),
	)

	// Register all tasks as MCP tools
	if err := taskfileServer.registerTasks(mcpServer); err != nil {
		fmt.Printf("Failed to register tasks: %v\n", err)
		os.Exit(1)
	}

	// Start the stdio server
	if err := server.ServeStdio(mcpServer); err != nil {
		fmt.Printf("Server error: %v\n", err)
		os.Exit(1)
	}
}

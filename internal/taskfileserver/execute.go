package taskfileserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/go-task/task/v3"
	"github.com/go-task/task/v3/taskfile/ast"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// createTaskHandlerForWorkdir creates a handler function for a specific task.
// For wildcard tasks, it reconstructs the full task name from the MATCH argument.
func createTaskHandlerForWorkdir(workdir, taskName string) mcp.ToolHandler {
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
			parts := strings.Split(matchVal, ",")
			if len(parts) != wildcardCount {
				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("MATCH requires exactly %d comma-separated value(s), got %d", wildcardCount, len(parts))}},
					IsError: true,
				}, nil
			}
			trimmedParts := make([]string, 0, len(parts))
			for i, p := range parts {
				trimmed := strings.TrimSpace(p)
				if trimmed == "" {
					return &mcp.CallToolResult{
						Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("MATCH value %d cannot be empty", i+1)}},
						IsError: true,
					}, nil
				}
				trimmedParts = append(trimmedParts, trimmed)
			}
			resolvedName = taskName
			for _, p := range trimmedParts {
				resolvedName = strings.Replace(resolvedName, "*", p, 1)
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
			task.WithDir(workdir),
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

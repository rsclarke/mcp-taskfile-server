// Package exec assembles MCP tool handlers that run Taskfile tasks via
// go-task and renders the result as MCP CallToolResult content blocks.
package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/go-task/task/v3"
	taskerrors "github.com/go-task/task/v3/errors"
	"github.com/go-task/task/v3/taskfile/ast"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// isWildcardTask returns true if the task name contains wildcard segments.
func isWildcardTask(taskName string) bool {
	return strings.Contains(taskName, "*")
}

// countWildcards returns the number of wildcard segments in a task name.
func countWildcards(taskName string) int {
	return strings.Count(taskName, "*")
}

// NewHandler creates a handler function for a specific task in the given
// working directory. For wildcard tasks, it reconstructs the full task
// name from the MATCH argument supplied by the caller.
func NewHandler(workdir, taskName string) mcp.ToolHandler {
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
			rawMatch, ok := arguments["MATCH"]
			if !ok {
				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: "MATCH argument is required for wildcard tasks"}},
					IsError: true,
				}, nil
			}
			rawParts, ok := rawMatch.([]any)
			if !ok {
				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("MATCH must be an array of strings, got %T", rawMatch)}},
					IsError: true,
				}, nil
			}
			if len(rawParts) != wildcardCount {
				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("MATCH requires exactly %d value(s), got %d", wildcardCount, len(rawParts))}},
					IsError: true,
				}, nil
			}
			parts := make([]string, 0, len(rawParts))
			for i, raw := range rawParts {
				str, ok := raw.(string)
				if !ok {
					return &mcp.CallToolResult{
						Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("MATCH value %d must be a string, got %T", i+1, raw)}},
						IsError: true,
					}, nil
				}
				if str == "" {
					return &mcp.CallToolResult{
						Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("MATCH value %d cannot be empty", i+1)}},
						IsError: true,
					}, nil
				}
				parts = append(parts, str)
			}
			for _, p := range parts {
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

		return buildTaskResult(resolvedName, taskErr, stdout.String(), stderr.String()), nil
	}
}

// buildTaskResult assembles the structured CallToolResult for a task
// invocation. The first content block is always a status line; stdout
// and stderr (when non-empty) are appended as separate TextContent
// blocks tagged with a "stream" entry in their Meta map so clients can
// route, filter, or render them independently.
func buildTaskResult(taskName string, taskErr error, stdoutStr, stderrStr string) *mcp.CallToolResult {
	content := []mcp.Content{&mcp.TextContent{Text: taskStatusText(taskName, taskErr)}}

	if stdoutStr != "" {
		content = append(content, &mcp.TextContent{
			Text: stdoutStr,
			Meta: mcp.Meta{"stream": "stdout"},
		})
	}
	if stderrStr != "" {
		content = append(content, &mcp.TextContent{
			Text: stderrStr,
			Meta: mcp.Meta{"stream": "stderr"},
		})
	}

	return &mcp.CallToolResult{
		Content: content,
		IsError: taskErr != nil,
	}
}

// taskStatusText returns a one-line status summary for a task invocation.
// On success the line reports an exit status of 0. On failure it prefers
// the underlying task exit code from go-task's *TaskRunError, falling
// back to the raw error message for non-exec failures (e.g. setup errors
// surfaced by the executor).
func taskStatusText(taskName string, taskErr error) string {
	if taskErr == nil {
		return fmt.Sprintf("Task `%s` exited with status 0", taskName)
	}
	var runErr *taskerrors.TaskRunError
	if errors.As(taskErr, &runErr) {
		return fmt.Sprintf("Task `%s` exited with status %d: %v", taskName, runErr.TaskExitCode(), taskErr)
	}
	return fmt.Sprintf("Task `%s` failed: %v", taskName, taskErr)
}

package tools

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/go-task/task/v3/taskfile/ast"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// CreateToolForTask creates an MCP tool definition for a given task.
// The tool name is sanitized for MCP compatibility; the description
// references the original Taskfile task name for clarity. The prefix
// parameter is used in multi-root mode to namespace tool names. The
// taskfile is taken by value rather than via *Root so the planner can
// run on a snapshot without touching live, mutable server state.
func CreateToolForTask(tf *ast.Taskfile, prefix, taskName string, taskDef *ast.Task) *RegisteredTool {
	toolName := SanitizeToolName(prefixedToolName(prefix, taskName))

	description := taskDef.Desc
	if description == "" {
		description = "Execute task: " + taskName
	}
	if toolName != taskName {
		description += fmt.Sprintf(" (task: %s)", taskName)
	}

	// Build JSON Schema properties.
	//
	// Only enumerate global Taskfile vars: task-level `vars:` are applied
	// after caller-supplied values inside go-task's compiler, so advertising
	// them as MCP arguments would be a tool-contract lie. Required caller
	// inputs come exclusively from `requires:` below.
	properties := make(map[string]any)
	required := []string{}

	if tf.Vars != nil {
		for varName, varDef := range tf.Vars.All() {
			prop := map[string]any{
				"type": "string",
			}
			if strVal, ok := varDef.Value.(string); ok {
				prop["default"] = strVal
				prop["description"] = fmt.Sprintf("Variable: %s (default: %s)", varName, strVal)
			} else {
				prop["description"] = "Variable: " + varName
			}
			properties[varName] = prop
		}
	}

	// Honour the task's `requires:` block: each named var becomes a
	// required property, and a static `enum:` translates directly to
	// JSON Schema `enum`. The `enum: { ref: .OTHER }` form is skipped
	// for now since it cannot be resolved without runtime context.
	if taskDef.Requires != nil {
		for _, req := range taskDef.Requires.Vars {
			if req == nil || req.Name == "" {
				continue
			}
			prop, ok := properties[req.Name].(map[string]any)
			if !ok {
				prop = map[string]any{
					"type":        "string",
					"description": "Required variable: " + req.Name,
				}
			}
			if req.Enum != nil && len(req.Enum.Value) > 0 {
				prop["enum"] = slices.Clone(req.Enum.Value)
			}
			properties[req.Name] = prop
			required = append(required, req.Name)
		}
	}

	// Add MATCH parameter for wildcard tasks
	if isWildcardTask(taskName) {
		n := countWildcards(taskName)
		matchDesc := fmt.Sprintf("Wildcard values for task pattern %s (%d value(s) required, one per '*' segment)", taskName, n)
		properties["MATCH"] = map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string"},
			"minItems":    n,
			"maxItems":    n,
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

	return &RegisteredTool{
		Tool: mcp.Tool{
			Name:        toolName,
			Description: description,
			InputSchema: json.RawMessage(schema),
		},
		schemaBytes: schema,
	}
}

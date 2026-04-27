package taskfileserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/go-task/task/v3/taskfile/ast"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registeredTool wraps an mcp.Tool with its serialized InputSchema bytes
// so equality checks can compare schemas as raw bytes without
// re-marshalling on every diff. The mcp.Tool is embedded so callers can
// continue to access Name, Description, and InputSchema directly.
type registeredTool struct {
	mcp.Tool
	schemaBytes []byte
}

// createToolForTask creates an MCP tool definition for a given task.
// The tool name is sanitized for MCP compatibility; the description
// references the original Taskfile task name for clarity. The prefix
// parameter is used in multi-root mode to namespace tool names. The
// taskfile is taken by value rather than via *Root so the planner can
// run on a snapshot without touching live, mutable server state.
func createToolForTask(tf *ast.Taskfile, prefix, taskName string, taskDef *ast.Task) *registeredTool {
	toolName := sanitizeToolName(prefixedToolName(prefix, taskName))

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

	return &registeredTool{
		Tool: mcp.Tool{
			Name:        toolName,
			Description: description,
			InputSchema: json.RawMessage(schema),
		},
		schemaBytes: schema,
	}
}

type toolPlan struct {
	tools    map[string]registeredTool
	handlers map[string]mcp.ToolHandler
}

// buildToolPlan computes the desired tool registration state from a snapshot
// without accessing or mutating the server. logger receives diagnostics
// for plan-level decisions such as colliding tool names; passing a nil
// logger panics.
func buildToolPlan(snap toolStateSnapshot, logger *slog.Logger) toolPlan {
	type toolCandidate struct {
		workdir  string
		taskName string
		tool     registeredTool
		handler  mcp.ToolHandler
	}

	plan := toolPlan{
		tools:    make(map[string]registeredTool),
		handlers: make(map[string]mcp.ToolHandler),
	}
	candidates := make(map[string][]toolCandidate)

	for _, root := range snap.roots {
		if root.taskfile == nil || root.taskfile.Tasks == nil {
			continue
		}

		prefix := rootPrefix(root.workdir, len(snap.roots))

		for taskName, taskDef := range root.taskfile.Tasks.All(nil) {
			if taskDef.Internal {
				continue
			}

			tool := createToolForTask(root.taskfile, prefix, taskName, taskDef)
			candidates[tool.Name] = append(candidates[tool.Name], toolCandidate{
				workdir:  root.workdir,
				taskName: taskName,
				tool:     *tool,
				handler:  createTaskHandlerForWorkdir(root.workdir, taskName),
			})
		}
	}

	names := make([]string, 0, len(candidates))
	for name := range candidates {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		group := candidates[name]
		if len(group) > 1 {
			details := make([]string, 0, len(group))
			for _, candidate := range group {
				details = append(details, fmt.Sprintf("%s (%s)", candidate.taskName, candidate.workdir))
			}
			slices.Sort(details)
			logger.Warn("excluding colliding tool name from MCP exposure",
				slog.String("event", "tools.collision"),
				slog.String("tool_name", name),
				slog.String("candidates", strings.Join(details, ", ")),
			)
			continue
		}

		candidate := group[0]
		plan.tools[name] = candidate.tool
		plan.handlers[name] = candidate.handler
	}

	return plan
}

// toolsEqual reports whether two registered tools are equivalent by
// comparing Name, Description, and the cached InputSchema bytes captured
// when the tool was created.
func toolsEqual(a, b *registeredTool) bool {
	return a.Name == b.Name &&
		a.Description == b.Description &&
		bytes.Equal(a.schemaBytes, b.schemaBytes)
}

// diffTools compares the old registered tool set against the desired set and
// returns the names of tools to remove and tools to add (or re-add due to changes).
func diffTools(old, desired map[string]registeredTool) (stale, added []string) {
	for name, oldTool := range old {
		if newTool, ok := desired[name]; !ok {
			stale = append(stale, name)
		} else if !toolsEqual(&oldTool, &newTool) {
			stale = append(stale, name)
		}
	}
	for name, newTool := range desired {
		if oldTool, ok := old[name]; ok && toolsEqual(&oldTool, &newTool) {
			continue
		}
		added = append(added, name)
	}
	return stale, added
}

// syncTools snapshots state under lock, builds a plan without the lock,
// then re-acquires the lock to validate the generation and apply changes.
// If the generation has advanced while the lock was released (another
// mutator ran concurrently), the stale plan is discarded without touching
// the MCP server, because that mutator will produce its own sync.
func (s *Server) syncTools() error {
	// Phase 1: snapshot under lock.
	s.mu.Lock()
	snap := s.snapshotToolStateLocked()
	oldTools := make(map[string]registeredTool, len(s.registeredTools))
	maps.Copy(oldTools, s.registeredTools)
	s.mu.Unlock()

	// Phase 2: pure planning — no lock held.
	plan := buildToolPlan(snap, s.log())
	stale, added := diffTools(oldTools, plan.tools)

	// Phase 3: validate generation, apply MCP side effects, and commit
	// bookkeeping — all under lock to prevent orphaned registrations.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation != snap.generation {
		return nil
	}
	if len(stale) > 0 {
		s.toolRegistry.RemoveTools(stale...)
	}
	for _, name := range added {
		t := plan.tools[name]
		s.toolRegistry.AddTool(&t.Tool, plan.handlers[name])
	}
	s.registeredTools = plan.tools
	return nil
}

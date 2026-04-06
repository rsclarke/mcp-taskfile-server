package taskfileserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"slices"
	"strings"

	"github.com/go-task/task/v3/taskfile/ast"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// createToolForTask creates an MCP tool definition for a given task.
// The tool name is sanitized for MCP compatibility; the description
// references the original Taskfile task name for clarity. The prefix
// parameter is used in multi-root mode to namespace tool names.
func createToolForTask(root *Root, prefix, taskName string, taskDef *ast.Task) *mcp.Tool {
	toolName := sanitizeToolName(prefixedToolName(prefix, taskName))

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

type toolPlan struct {
	tools    map[string]mcp.Tool
	handlers map[string]mcp.ToolHandler
}

// buildToolPlan computes the desired tool registration state from a snapshot
// without accessing or mutating the server.
func buildToolPlan(snap toolStateSnapshot) toolPlan {
	type toolCandidate struct {
		root     *Root
		taskName string
		tool     mcp.Tool
		handler  mcp.ToolHandler
	}

	plan := toolPlan{
		tools:    make(map[string]mcp.Tool),
		handlers: make(map[string]mcp.ToolHandler),
	}
	candidates := make(map[string][]toolCandidate)

	for _, root := range snap.roots {
		if root.taskfile == nil || root.taskfile.Tasks == nil {
			continue
		}

		prefix := rootPrefix(root, len(snap.roots))

		for taskName, taskDef := range root.taskfile.Tasks.All(nil) {
			if taskDef.Internal {
				continue
			}

			tool := createToolForTask(root, prefix, taskName, taskDef)
			candidates[tool.Name] = append(candidates[tool.Name], toolCandidate{
				root:     root,
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
				details = append(details, fmt.Sprintf("%s (%s)", candidate.taskName, candidate.root.workdir))
			}
			slices.Sort(details)
			log.Printf("excluding colliding tool name %q from MCP exposure: %s", name, strings.Join(details, ", "))
			continue
		}

		candidate := group[0]
		plan.tools[name] = candidate.tool
		plan.handlers[name] = candidate.handler
	}

	return plan
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

// diffTools compares the old registered tool set against the desired set and
// returns the names of tools to remove and tools to add (or re-add due to changes).
func diffTools(old, desired map[string]mcp.Tool) (stale, added []string) {
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
	oldTools := make(map[string]mcp.Tool, len(s.registeredTools))
	maps.Copy(oldTools, s.registeredTools)
	s.mu.Unlock()

	// Phase 2: pure planning — no lock held.
	plan := buildToolPlan(snap)
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
		s.toolRegistry.AddTool(&t, plan.handlers[name])
	}
	s.registeredTools = plan.tools
	return nil
}

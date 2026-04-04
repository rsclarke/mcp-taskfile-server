package main

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
func createToolForTask(root *rootState, prefix, taskName string, taskDef *ast.Task) *mcp.Tool {
	toolName := sanitizeToolName(prefixedToolName(prefix, taskName))

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
func (s *TaskfileServer) buildToolSet() (map[string]mcp.Tool, map[string]mcp.ToolHandler) {
	type toolCandidate struct {
		root     *rootState
		taskName string
		tool     mcp.Tool
		handler  mcp.ToolHandler
	}

	tools := make(map[string]mcp.Tool)
	handlers := make(map[string]mcp.ToolHandler)
	candidates := make(map[string][]toolCandidate)

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
			candidates[tool.Name] = append(candidates[tool.Name], toolCandidate{
				root:     root,
				taskName: taskName,
				tool:     *tool,
				handler:  createTaskHandler(root, taskName),
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
		tools[name] = candidate.tool
		handlers[name] = candidate.handler
		candidate.root.registeredTools = append(candidate.root.registeredTools, name)
	}

	return tools, handlers
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
	tools, handlers := s.buildToolSet()

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

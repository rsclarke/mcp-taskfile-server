// Package tools builds MCP tool registrations from a snapshot of the
// Taskfile state held by the orchestrator. It is responsible for
// translating Taskfile tasks into MCP-shaped tools, naming them in a
// way that satisfies the MCP spec, and producing a plan/diff that the
// orchestrator can apply to the live MCP registry.
package tools

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/go-task/task/v3/taskfile/ast"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rsclarke/mcp-taskfile-server/internal/exec"
)

// RegisteredTool wraps an mcp.Tool with its serialized InputSchema bytes
// so equality checks can compare schemas as raw bytes without
// re-marshalling on every diff. The mcp.Tool is embedded so callers can
// continue to access Name, Description, and InputSchema directly.
type RegisteredTool struct {
	mcp.Tool
	schemaBytes []byte
}

// RootSnapshot is an immutable per-root view captured under lock so the
// planner can run without holding the orchestrator mutex and without
// dereferencing a live root that another goroutine may mutate. The
// taskfile pointer is captured by value: even if the live root's
// taskfile is later swapped out, this snapshot keeps observing the AST
// that was current at snapshot time.
type RootSnapshot struct {
	Workdir  string
	Taskfile *ast.Taskfile
}

// StateSnapshot captures the inputs needed by BuildPlan, frozen at a
// specific generation supplied by the orchestrator.
type StateSnapshot struct {
	Generation uint64
	Roots      map[string]RootSnapshot
}

// Plan is the desired tool registration state computed from a snapshot.
// Tools and Handlers are keyed by tool name and always have matching
// keys.
type Plan struct {
	Tools    map[string]RegisteredTool
	Handlers map[string]mcp.ToolHandler
}

// BuildPlan computes the desired tool registration state from a snapshot
// without accessing or mutating the orchestrator. logger receives
// diagnostics for plan-level decisions such as colliding tool names;
// passing a nil logger panics.
func BuildPlan(snap StateSnapshot, logger *slog.Logger) Plan {
	type toolCandidate struct {
		workdir  string
		taskName string
		tool     RegisteredTool
		handler  mcp.ToolHandler
	}

	plan := Plan{
		Tools:    make(map[string]RegisteredTool),
		Handlers: make(map[string]mcp.ToolHandler),
	}
	candidates := make(map[string][]toolCandidate)

	for _, root := range snap.Roots {
		if root.Taskfile == nil || root.Taskfile.Tasks == nil {
			continue
		}

		prefix := RootPrefix(root.Workdir, len(snap.Roots))

		for taskName, taskDef := range root.Taskfile.Tasks.All(nil) {
			if taskDef.Internal {
				continue
			}

			tool := CreateToolForTask(root.Taskfile, prefix, taskName, taskDef)
			candidates[tool.Name] = append(candidates[tool.Name], toolCandidate{
				workdir:  root.Workdir,
				taskName: taskName,
				tool:     *tool,
				handler:  exec.NewHandler(root.Workdir, taskName),
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
		plan.Tools[name] = candidate.tool
		plan.Handlers[name] = candidate.handler
	}

	return plan
}

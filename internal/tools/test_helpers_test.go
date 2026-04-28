package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/go-task/task/v3/taskfile/ast"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rsclarke/mcp-taskfile-server/internal/roots"
)

// testLogger returns a logger that discards all output. Tests that need
// to assert on log output should construct their own buffer-backed
// handler instead.
func testLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// loadRootFromFixture loads a *roots.Root from a testdata fixture
// directory. It is the planner-side equivalent of the server's fixture
// helper: it does not construct a Server because BuildPlan only needs a
// snapshot.
func loadRootFromFixture(t *testing.T, name string) *roots.Root {
	t.Helper()

	_, filename, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(filename), "..", "..", "testdata", name)

	root, err := roots.Load(t.Context(), dir)
	if err != nil {
		t.Fatalf("failed to load root for fixture %q: %v", name, err)
	}
	return root
}

// snapshotFromRoots builds a StateSnapshot from one or more loaded roots,
// keyed by their canonical URIs. Generation is left at zero because
// tests do not exercise the orchestrator's generation guard.
func snapshotFromRoots(rs ...*roots.Root) StateSnapshot {
	snap := StateSnapshot{
		Roots: make(map[string]RootSnapshot, len(rs)),
	}
	for _, r := range rs {
		snap.Roots[roots.DirToURI(r.Workdir)] = RootSnapshot{
			Workdir:  r.Workdir,
			Taskfile: r.Taskfile,
		}
	}
	return snap
}

// lookupTask finds a task by name in the taskfile or fails the test.
func lookupTask(t *testing.T, tf *ast.Taskfile, name string) *ast.Task {
	t.Helper()

	for taskName, taskDef := range tf.Tasks.All(nil) {
		if taskName == name {
			return taskDef
		}
	}

	t.Fatalf("task %q not found in taskfile", name)
	return nil
}

// toolNames returns the sorted keys from a tool map for use in error messages.
func toolNames[V any](tools map[string]V) []string {
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// schemaProperties marshals a tool's InputSchema to JSON, then unmarshals it
// to return the properties map. This handles InputSchema being any (e.g. json.RawMessage).
func schemaProperties(t *testing.T, tool *RegisteredTool) map[string]any {
	t.Helper()

	b, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("failed to marshal InputSchema: %v", err)
	}

	var schema map[string]any
	if err := json.Unmarshal(b, &schema); err != nil {
		t.Fatalf("failed to unmarshal InputSchema: %v", err)
	}

	props, _ := schema["properties"].(map[string]any)
	return props
}

// schemaRequired extracts the "required" array from a tool's InputSchema.
func schemaRequired(t *testing.T, tool *RegisteredTool) []string {
	t.Helper()

	b, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("failed to marshal InputSchema: %v", err)
	}

	var schema map[string]any
	if err := json.Unmarshal(b, &schema); err != nil {
		t.Fatalf("failed to unmarshal InputSchema: %v", err)
	}

	rawReq, ok := schema["required"]
	if !ok {
		return nil
	}

	arr, ok := rawReq.([]any)
	if !ok {
		return nil
	}

	result := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

func rawToolArguments(t *testing.T, arguments any) json.RawMessage {
	t.Helper()
	if arguments == nil {
		return nil
	}

	raw, err := json.Marshal(arguments)
	if err != nil {
		t.Fatalf("failed to marshal tool arguments: %v", err)
	}
	return raw
}

func callToolHandler(t *testing.T, handler mcp.ToolHandler, name string, arguments any) *mcp.CallToolResult {
	t.Helper()

	request := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: name},
	}
	if raw := rawToolArguments(t, arguments); raw != nil {
		request.Params.Arguments = raw
	}

	result, err := handler(context.Background(), request)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	return result
}

// toolResultText concatenates the text from every TextContent block in a
// CallToolResult, separated by newlines. It is used by tests that want to
// substring-match across the structured status / stdout / stderr blocks
// without caring which block produced the match.
func toolResultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()

	if len(result.Content) == 0 {
		t.Fatal("expected at least 1 content item, got 0")
	}

	parts := make([]string, 0, len(result.Content))
	for i, c := range result.Content {
		text, ok := c.(*mcp.TextContent)
		if !ok {
			t.Fatalf("expected TextContent at index %d, got %T", i, c)
		}
		parts = append(parts, text.Text)
	}
	return strings.Join(parts, "\n")
}

// toolStreamText returns the concatenated text from TextContent blocks
// tagged with Meta["stream"] == stream (e.g. "stdout", "stderr"). Returns
// the empty string if no such block exists.
func toolStreamText(t *testing.T, result *mcp.CallToolResult, stream string) string {
	t.Helper()

	var parts []string
	for i, c := range result.Content {
		text, ok := c.(*mcp.TextContent)
		if !ok {
			t.Fatalf("expected TextContent at index %d, got %T", i, c)
		}
		if text.Meta == nil {
			continue
		}
		if got, _ := text.Meta["stream"].(string); got == stream {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "")
}

// toolStatusText returns the text from the first content block, which by
// convention carries the status summary line for a task invocation.
func toolStatusText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()

	if len(result.Content) == 0 {
		t.Fatal("expected at least 1 content item, got 0")
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected first content to be TextContent, got %T", result.Content[0])
	}
	return text.Text
}

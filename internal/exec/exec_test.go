package exec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fixtureWorkdir returns the absolute path to a testdata fixture directory.
func fixtureWorkdir(t *testing.T, name string) string {
	t.Helper()
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "..", "testdata", name)
}

// tempWorkdir creates a temp directory containing a Taskfile.yml with the
// given content and returns its path.
func tempWorkdir(t *testing.T, taskfileContent []byte) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), taskfileContent, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// callHandler invokes an MCP tool handler with the given arguments and
// returns the result, failing the test on a non-nil error.
func callHandler(t *testing.T, handler mcp.ToolHandler, name string, arguments any) *mcp.CallToolResult {
	t.Helper()

	request := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: name},
	}
	if arguments != nil {
		raw, err := json.Marshal(arguments)
		if err != nil {
			t.Fatalf("failed to marshal tool arguments: %v", err)
		}
		request.Params.Arguments = raw
	}

	result, err := handler(t.Context(), request)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	return result
}

// resultText concatenates the text from every TextContent block in a
// CallToolResult, separated by newlines.
func resultText(t *testing.T, result *mcp.CallToolResult) string {
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

// streamText returns the concatenated text from TextContent blocks
// tagged with Meta["stream"] == stream (e.g. "stdout", "stderr").
func streamText(t *testing.T, result *mcp.CallToolResult, stream string) string {
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

// statusText returns the text from the first content block, which by
// convention carries the status summary line for a task invocation.
func statusText(t *testing.T, result *mcp.CallToolResult) string {
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

func TestNewHandler_WildcardMATCH(t *testing.T) {
	workdir := fixtureWorkdir(t, "wildcard")

	tests := []struct {
		name        string
		taskName    string
		toolName    string
		arguments   map[string]any
		wantError   bool
		wantSubstrs []string
	}{
		{
			name:        "single wildcard success",
			taskName:    "start:*",
			toolName:    "start",
			arguments:   map[string]any{"MATCH": []string{"web"}},
			wantSubstrs: []string{"exited with status 0", "web"},
		},
		{
			name:        "multi wildcard success",
			taskName:    "deploy:*:*",
			toolName:    "deploy",
			arguments:   map[string]any{"MATCH": []string{"api", "production"}},
			wantSubstrs: []string{"exited with status 0", "api", "production"},
		},
		{
			name:        "value containing comma is preserved",
			taskName:    "start:*",
			toolName:    "start",
			arguments:   map[string]any{"MATCH": []string{"a,b"}},
			wantSubstrs: []string{"exited with status 0", "a,b"},
		},
		{
			name:        "missing MATCH",
			taskName:    "start:*",
			toolName:    "start",
			wantError:   true,
			wantSubstrs: []string{"MATCH"},
		},
		{
			name:        "MATCH not an array",
			taskName:    "start:*",
			toolName:    "start",
			arguments:   map[string]any{"MATCH": "web"},
			wantError:   true,
			wantSubstrs: []string{"must be an array"},
		},
		{
			name:        "too few MATCH values",
			taskName:    "deploy:*:*",
			toolName:    "deploy",
			arguments:   map[string]any{"MATCH": []string{"onlyone"}},
			wantError:   true,
			wantSubstrs: []string{"exactly 2 value(s)", "got 1"},
		},
		{
			name:        "too many MATCH values",
			taskName:    "deploy:*:*",
			toolName:    "deploy",
			arguments:   map[string]any{"MATCH": []string{"api", "production", "extra"}},
			wantError:   true,
			wantSubstrs: []string{"exactly 2 value(s)", "got 3"},
		},
		{
			name:        "empty MATCH segment",
			taskName:    "deploy:*:*",
			toolName:    "deploy",
			arguments:   map[string]any{"MATCH": []string{"api", ""}},
			wantError:   true,
			wantSubstrs: []string{"cannot be empty"},
		},
		{
			name:        "non-string MATCH element",
			taskName:    "start:*",
			toolName:    "start",
			arguments:   map[string]any{"MATCH": []any{42}},
			wantError:   true,
			wantSubstrs: []string{"must be a string"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewHandler(workdir, tt.taskName)
			result := callHandler(t, handler, tt.toolName, tt.arguments)

			if result.IsError != tt.wantError {
				t.Fatalf("IsError = %v, want %v", result.IsError, tt.wantError)
			}

			text := resultText(t, result)
			for _, want := range tt.wantSubstrs {
				if !strings.Contains(text, want) {
					t.Fatalf("expected result to contain %q, got %q", want, text)
				}
			}
		})
	}
}

func TestNewHandler_InvalidArguments(t *testing.T) {
	workdir := fixtureWorkdir(t, "basic")

	handler := NewHandler(workdir, "greet")
	args := json.RawMessage(`{invalid json}`)
	result, err := handler(t.Context(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "greet",
			Arguments: args,
		},
	})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for invalid JSON arguments")
	}

	text := resultText(t, result)
	if !strings.Contains(text, "Failed to parse") {
		t.Errorf("expected parse error message, got %q", text)
	}
}

func TestNewHandler_StructuredSuccessContent(t *testing.T) {
	workdir := tempWorkdir(t, []byte("version: '3'\ntasks:\n  noisy:\n    desc: Emit on both streams\n    cmds:\n      - sh -c 'echo out-line; echo err-line 1>&2'\n"))

	handler := NewHandler(workdir, "noisy")
	result := callHandler(t, handler, "noisy", nil)

	if result.IsError {
		t.Fatalf("expected success, got IsError=true: %s", resultText(t, result))
	}
	if got, want := len(result.Content), 3; got != want {
		t.Fatalf("expected %d content blocks (status, stdout, stderr), got %d", want, got)
	}

	status := statusText(t, result)
	if !strings.Contains(status, "Task `noisy` exited with status 0") {
		t.Errorf("status block %q does not match expected exit-0 line", status)
	}

	stdout := streamText(t, result, "stdout")
	if !strings.Contains(stdout, "out-line") {
		t.Errorf("stdout block %q does not contain expected output", stdout)
	}
	if strings.Contains(stdout, "err-line") {
		t.Errorf("stdout block leaked stderr content: %q", stdout)
	}

	stderr := streamText(t, result, "stderr")
	if !strings.Contains(stderr, "err-line") {
		t.Errorf("stderr block %q does not contain expected error output", stderr)
	}
	if strings.Contains(stderr, "out-line") {
		t.Errorf("stderr block leaked stdout content: %q", stderr)
	}
}

func TestNewHandler_StructuredFailureContent(t *testing.T) {
	workdir := tempWorkdir(t, []byte("version: '3'\ntasks:\n  fail:\n    desc: Fail with both streams\n    cmds:\n      - sh -c 'echo before-fail; echo bad-news 1>&2; exit 7'\n"))

	handler := NewHandler(workdir, "fail")
	result := callHandler(t, handler, "fail", nil)

	if !result.IsError {
		t.Fatalf("expected IsError=true for failing task, got success: %s", resultText(t, result))
	}

	status := statusText(t, result)
	if !strings.Contains(status, "Task `fail` exited with status 7") {
		t.Errorf("expected status block to surface exit code 7, got %q", status)
	}

	if stdout := streamText(t, result, "stdout"); !strings.Contains(stdout, "before-fail") {
		t.Errorf("expected stdout block to contain pre-failure output, got %q", stdout)
	}
	if stderr := streamText(t, result, "stderr"); !strings.Contains(stderr, "bad-news") {
		t.Errorf("expected stderr block to contain failing command stderr, got %q", stderr)
	}
}

func TestNewHandler_StructuredSilentSuccess(t *testing.T) {
	workdir := tempWorkdir(t, []byte("version: '3'\ntasks:\n  quiet:\n    desc: A task with no output\n    cmds:\n      - 'true'\n"))

	handler := NewHandler(workdir, "quiet")
	result := callHandler(t, handler, "quiet", nil)

	if result.IsError {
		t.Fatalf("expected success, got IsError=true: %s", resultText(t, result))
	}
	if got, want := len(result.Content), 1; got != want {
		t.Fatalf("expected %d content blocks (status only) when streams are empty, got %d", want, got)
	}
	if status := statusText(t, result); !strings.Contains(status, "exited with status 0") {
		t.Errorf("expected status block to report exit 0, got %q", status)
	}
}

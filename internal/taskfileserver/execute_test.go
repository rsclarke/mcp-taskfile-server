package taskfileserver

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCreateTaskHandlerForWorkdir_WildcardMATCH(t *testing.T) {
	s := loadServerFromFixture(t, "wildcard")
	root := onlyRoot(t, s)

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
			handler := createTaskHandlerForWorkdir(root.workdir, tt.taskName)
			result := callToolHandler(t, handler, tt.toolName, tt.arguments)

			if result.IsError != tt.wantError {
				t.Fatalf("IsError = %v, want %v", result.IsError, tt.wantError)
			}

			text := toolResultText(t, result)
			for _, want := range tt.wantSubstrs {
				if !strings.Contains(text, want) {
					t.Fatalf("expected result to contain %q, got %q", want, text)
				}
			}
		})
	}
}

func TestCreateTaskHandlerForWorkdir_InvalidArguments(t *testing.T) {
	s := loadServerFromFixture(t, "basic")
	root := onlyRoot(t, s)

	handler := createTaskHandlerForWorkdir(root.workdir, "greet")
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

	text := toolResultText(t, result)
	if !strings.Contains(text, "Failed to parse") {
		t.Errorf("expected parse error message, got %q", text)
	}
}

func TestCreateTaskHandlerForWorkdir_StructuredSuccessContent(t *testing.T) {
	s := newTempServer(t, []byte("version: '3'\ntasks:\n  noisy:\n    desc: Emit on both streams\n    cmds:\n      - sh -c 'echo out-line; echo err-line 1>&2'\n"))
	root := onlyRoot(t, s)

	handler := createTaskHandlerForWorkdir(root.workdir, "noisy")
	result := callToolHandler(t, handler, "noisy", nil)

	if result.IsError {
		t.Fatalf("expected success, got IsError=true: %s", toolResultText(t, result))
	}
	if got, want := len(result.Content), 3; got != want {
		t.Fatalf("expected %d content blocks (status, stdout, stderr), got %d", want, got)
	}

	status := toolStatusText(t, result)
	if !strings.Contains(status, "Task `noisy` exited with status 0") {
		t.Errorf("status block %q does not match expected exit-0 line", status)
	}

	stdout := toolStreamText(t, result, "stdout")
	if !strings.Contains(stdout, "out-line") {
		t.Errorf("stdout block %q does not contain expected output", stdout)
	}
	if strings.Contains(stdout, "err-line") {
		t.Errorf("stdout block leaked stderr content: %q", stdout)
	}

	stderr := toolStreamText(t, result, "stderr")
	if !strings.Contains(stderr, "err-line") {
		t.Errorf("stderr block %q does not contain expected error output", stderr)
	}
	if strings.Contains(stderr, "out-line") {
		t.Errorf("stderr block leaked stdout content: %q", stderr)
	}
}

func TestCreateTaskHandlerForWorkdir_StructuredFailureContent(t *testing.T) {
	s := newTempServer(t, []byte("version: '3'\ntasks:\n  fail:\n    desc: Fail with both streams\n    cmds:\n      - sh -c 'echo before-fail; echo bad-news 1>&2; exit 7'\n"))
	root := onlyRoot(t, s)

	handler := createTaskHandlerForWorkdir(root.workdir, "fail")
	result := callToolHandler(t, handler, "fail", nil)

	if !result.IsError {
		t.Fatalf("expected IsError=true for failing task, got success: %s", toolResultText(t, result))
	}

	status := toolStatusText(t, result)
	if !strings.Contains(status, "Task `fail` exited with status 7") {
		t.Errorf("expected status block to surface exit code 7, got %q", status)
	}

	if stdout := toolStreamText(t, result, "stdout"); !strings.Contains(stdout, "before-fail") {
		t.Errorf("expected stdout block to contain pre-failure output, got %q", stdout)
	}
	if stderr := toolStreamText(t, result, "stderr"); !strings.Contains(stderr, "bad-news") {
		t.Errorf("expected stderr block to contain failing command stderr, got %q", stderr)
	}
}

func TestCreateTaskHandlerForWorkdir_StructuredSilentSuccess(t *testing.T) {
	s := newTempServer(t, []byte("version: '3'\ntasks:\n  quiet:\n    desc: A task with no output\n    cmds:\n      - 'true'\n"))
	root := onlyRoot(t, s)

	handler := createTaskHandlerForWorkdir(root.workdir, "quiet")
	result := callToolHandler(t, handler, "quiet", nil)

	if result.IsError {
		t.Fatalf("expected success, got IsError=true: %s", toolResultText(t, result))
	}
	if got, want := len(result.Content), 1; got != want {
		t.Fatalf("expected %d content blocks (status only) when streams are empty, got %d", want, got)
	}
	if status := toolStatusText(t, result); !strings.Contains(status, "exited with status 0") {
		t.Errorf("expected status block to report exit 0, got %q", status)
	}
}

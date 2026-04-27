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
			wantSubstrs: []string{"completed successfully", "web"},
		},
		{
			name:        "multi wildcard success",
			taskName:    "deploy:*:*",
			toolName:    "deploy",
			arguments:   map[string]any{"MATCH": []string{"api", "production"}},
			wantSubstrs: []string{"completed successfully", "api", "production"},
		},
		{
			name:        "value containing comma is preserved",
			taskName:    "start:*",
			toolName:    "start",
			arguments:   map[string]any{"MATCH": []string{"a,b"}},
			wantSubstrs: []string{"completed successfully", "a,b"},
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

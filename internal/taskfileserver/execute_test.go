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
		arguments   map[string]string
		wantError   bool
		wantSubstrs []string
	}{
		{
			name:        "single wildcard success",
			taskName:    "start:*",
			toolName:    "start",
			arguments:   map[string]string{"MATCH": "web"},
			wantSubstrs: []string{"completed successfully", "web"},
		},
		{
			name:        "multi wildcard trims values",
			taskName:    "deploy:*:*",
			toolName:    "deploy",
			arguments:   map[string]string{"MATCH": " api , production "},
			wantSubstrs: []string{"completed successfully", "api", "production"},
		},
		{
			name:        "missing MATCH",
			taskName:    "start:*",
			toolName:    "start",
			wantError:   true,
			wantSubstrs: []string{"MATCH"},
		},
		{
			name:        "wrong MATCH count",
			taskName:    "deploy:*:*",
			toolName:    "deploy",
			arguments:   map[string]string{"MATCH": "onlyone"},
			wantError:   true,
			wantSubstrs: []string{"2 comma-separated"},
		},
		{
			name:        "too many MATCH values",
			taskName:    "deploy:*:*",
			toolName:    "deploy",
			arguments:   map[string]string{"MATCH": "api,production,extra"},
			wantError:   true,
			wantSubstrs: []string{"exactly 2 comma-separated", "got 3"},
		},
		{
			name:        "empty MATCH segment",
			taskName:    "deploy:*:*",
			toolName:    "deploy",
			arguments:   map[string]string{"MATCH": "api, "},
			wantError:   true,
			wantSubstrs: []string{"cannot be empty"},
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

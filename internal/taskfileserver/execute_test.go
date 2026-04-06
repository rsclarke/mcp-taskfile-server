package taskfileserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCreateTaskHandlerForWorkdir_Success(t *testing.T) {
	s := loadServerFromFixture(t, "basic")
	root := onlyRoot(t, s)

	handler := createTaskHandlerForWorkdir(root.workdir, "greet")
	result, err := handler(t.Context(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "greet"},
	})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success, got IsError=true")
	}

	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "completed successfully") {
		t.Errorf("expected success message, got %q", text)
	}
	if !strings.Contains(text, "hello") {
		t.Errorf("expected output to contain 'hello', got %q", text)
	}
}

func TestCreateTaskHandlerForWorkdir_TaskFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  fail:\n    desc: A failing task\n    cmds:\n      - exit 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	handler := createTaskHandlerForWorkdir(dir, "fail")
	result, handlerErr := handler(t.Context(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "fail"},
	})
	if handlerErr != nil {
		t.Fatalf("handler returned Go error: %v", handlerErr)
	}
	if !result.IsError {
		t.Error("expected IsError=true for a failing task")
	}

	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "failed") {
		t.Errorf("expected failure message, got %q", text)
	}
}

func TestCreateTaskHandlerForWorkdir_WithVariables(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  greet:\n    desc: Greet someone\n    cmds:\n      - echo hello {{.NAME}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	handler := createTaskHandlerForWorkdir(dir, "greet")
	args := json.RawMessage(`{"NAME":"world"}`)
	result, handlerErr := handler(t.Context(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "greet",
			Arguments: args,
		},
	})
	if handlerErr != nil {
		t.Fatalf("handler returned error: %v", handlerErr)
	}
	if result.IsError {
		t.Errorf("expected success, got IsError=true")
	}

	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "world") {
		t.Errorf("expected output to contain 'world', got %q", text)
	}
}

func TestCreateTaskHandlerForWorkdir_WildcardMATCH(t *testing.T) {
	s := loadServerFromFixture(t, "wildcard")
	root := onlyRoot(t, s)

	handler := createTaskHandlerForWorkdir(root.workdir, "start:*")
	args := json.RawMessage(`{"MATCH":"web"}`)
	result, err := handler(t.Context(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "start",
			Arguments: args,
		},
	})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if result.IsError {
		text := result.Content[0].(*mcp.TextContent).Text
		t.Errorf("expected success, got IsError=true: %s", text)
	}

	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "web") {
		t.Errorf("expected output to contain 'web', got %q", text)
	}
}

func TestCreateTaskHandlerForWorkdir_WildcardMissingMATCH(t *testing.T) {
	s := loadServerFromFixture(t, "wildcard")
	root := onlyRoot(t, s)

	handler := createTaskHandlerForWorkdir(root.workdir, "start:*")
	result, err := handler(t.Context(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "start"},
	})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true when MATCH is missing")
	}

	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "MATCH") {
		t.Errorf("expected error to mention MATCH, got %q", text)
	}
}

func TestCreateTaskHandlerForWorkdir_WildcardWrongCount(t *testing.T) {
	s := loadServerFromFixture(t, "wildcard")
	root := onlyRoot(t, s)

	handler := createTaskHandlerForWorkdir(root.workdir, "deploy:*:*")
	args := json.RawMessage(`{"MATCH":"onlyone"}`)
	result, err := handler(t.Context(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "deploy",
			Arguments: args,
		},
	})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for wrong MATCH count")
	}

	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "2 comma-separated") {
		t.Errorf("expected error about comma-separated values, got %q", text)
	}
}

func TestCreateTaskHandlerForWorkdir_WildcardMultiMATCH(t *testing.T) {
	s := loadServerFromFixture(t, "wildcard")
	root := onlyRoot(t, s)

	handler := createTaskHandlerForWorkdir(root.workdir, "deploy:*:*")
	args := json.RawMessage(`{"MATCH":" api , production "}`)
	result, err := handler(t.Context(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "deploy",
			Arguments: args,
		},
	})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if result.IsError {
		text := result.Content[0].(*mcp.TextContent).Text
		t.Fatalf("expected success, got IsError=true: %s", text)
	}

	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "api") || !strings.Contains(text, "production") {
		t.Errorf("expected output to contain trimmed wildcard values, got %q", text)
	}
}

func TestCreateTaskHandlerForWorkdir_WildcardTooManyValues(t *testing.T) {
	s := loadServerFromFixture(t, "wildcard")
	root := onlyRoot(t, s)

	handler := createTaskHandlerForWorkdir(root.workdir, "deploy:*:*")
	args := json.RawMessage(`{"MATCH":"api,production,extra"}`)
	result, err := handler(t.Context(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "deploy",
			Arguments: args,
		},
	})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for too many MATCH values")
	}

	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "exactly 2 comma-separated") || !strings.Contains(text, "got 3") {
		t.Errorf("expected error about too many comma-separated values, got %q", text)
	}
}

func TestCreateTaskHandlerForWorkdir_WildcardEmptySegment(t *testing.T) {
	s := loadServerFromFixture(t, "wildcard")
	root := onlyRoot(t, s)

	handler := createTaskHandlerForWorkdir(root.workdir, "deploy:*:*")
	args := json.RawMessage(`{"MATCH":"api, "}`)
	result, err := handler(t.Context(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "deploy",
			Arguments: args,
		},
	})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for empty MATCH segment")
	}

	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "cannot be empty") {
		t.Errorf("expected error about empty MATCH segment, got %q", text)
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

	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "Failed to parse") {
		t.Errorf("expected parse error message, got %q", text)
	}
}

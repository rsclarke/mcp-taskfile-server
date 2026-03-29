package main

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/go-task/task/v3"
	"github.com/go-task/task/v3/taskfile/ast"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// loadServerFromFixture creates a TaskfileServer from a testdata fixture directory.
func loadServerFromFixture(t *testing.T, name string) *TaskfileServer {
	t.Helper()

	_, filename, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(filename), "testdata", name)

	executor := task.NewExecutor(
		task.WithDir(dir),
		task.WithSilent(true),
	)

	if err := executor.Setup(); err != nil {
		t.Fatalf("failed to setup executor for fixture %q: %v", name, err)
	}

	return &TaskfileServer{
		taskfile: executor.Taskfile,
		workdir:  dir,
	}
}

// fakeRegistrar records tools registered via AddTool.
type fakeRegistrar struct {
	tools []mcp.Tool
}

func (f *fakeRegistrar) AddTool(t *mcp.Tool, _ mcp.ToolHandler) {
	f.tools = append(f.tools, *t)
}

// schemaProperties marshals a tool's InputSchema to JSON, then unmarshals it
// to return the properties map. This handles InputSchema being any (e.g. json.RawMessage).
func schemaProperties(t *testing.T, tool *mcp.Tool) map[string]any {
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

func TestCreateToolForTask_Basic(t *testing.T) {
	s := loadServerFromFixture(t, "basic")

	taskDef := lookupTask(t, s.taskfile, "greet")
	tool := s.createToolForTask("greet", taskDef)

	if tool.Name != "greet" {
		t.Errorf("Name = %q, want %q", tool.Name, "greet")
	}
	if tool.Description != "Say hello" {
		t.Errorf("Description = %q, want %q", tool.Description, "Say hello")
	}

	props := schemaProperties(t, tool)
	if len(props) != 0 {
		t.Errorf("expected no properties, got %d", len(props))
	}
}

func TestCreateToolForTask_NoDescription(t *testing.T) {
	s := loadServerFromFixture(t, "no-desc")

	taskDef := lookupTask(t, s.taskfile, "build")
	tool := s.createToolForTask("build", taskDef)

	want := "Execute task: build"
	if tool.Description != want {
		t.Errorf("Description = %q, want %q", tool.Description, want)
	}
}

func TestCreateToolForTask_TaskVars(t *testing.T) {
	s := loadServerFromFixture(t, "task-vars")

	taskDef := lookupTask(t, s.taskfile, "deploy")
	tool := s.createToolForTask("deploy", taskDef)

	props := schemaProperties(t, tool)
	if len(props) != 2 {
		t.Fatalf("expected 2 properties, got %d", len(props))
	}

	for _, varName := range []string{"ENV", "REGION"} {
		prop, ok := props[varName]
		if !ok {
			t.Errorf("missing property %q", varName)
			continue
		}
		propMap, _ := prop.(map[string]any)
		if propMap["type"] != "string" {
			t.Errorf("property %q type = %v, want %q", varName, propMap["type"], "string")
		}
	}
}

func TestCreateToolForTask_GlobalVars(t *testing.T) {
	s := loadServerFromFixture(t, "global-vars")

	taskDef := lookupTask(t, s.taskfile, "info")
	tool := s.createToolForTask("info", taskDef)

	props := schemaProperties(t, tool)
	prop, ok := props["APP_NAME"]
	if !ok {
		t.Fatal("missing property APP_NAME")
	}

	propMap, _ := prop.(map[string]any)
	desc, _ := propMap["description"].(string)
	if desc != "Variable: APP_NAME (default: myapp)" {
		t.Errorf("description = %q, want it to contain default value", desc)
	}
}

func TestCreateToolForTask_OverrideVars(t *testing.T) {
	s := loadServerFromFixture(t, "override-vars")

	taskDef := lookupTask(t, s.taskfile, "deploy")
	tool := s.createToolForTask("deploy", taskDef)

	props := schemaProperties(t, tool)
	prop, ok := props["ENV"]
	if !ok {
		t.Fatal("missing property ENV")
	}

	// Task var should override global var
	propMap, _ := prop.(map[string]any)
	desc, _ := propMap["description"].(string)
	want := "Variable: ENV (default: staging)"
	if desc != want {
		t.Errorf("description = %q, want %q", desc, want)
	}
}

func TestRegisterTasks_SkipsInternal(t *testing.T) {
	s := loadServerFromFixture(t, "internal")

	reg := &fakeRegistrar{}
	if err := s.registerTasks(reg); err != nil {
		t.Fatalf("registerTasks failed: %v", err)
	}

	if len(reg.tools) != 1 {
		t.Fatalf("expected 1 registered tool, got %d", len(reg.tools))
	}
	if reg.tools[0].Name != "public" {
		t.Errorf("registered tool name = %q, want %q", reg.tools[0].Name, "public")
	}
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

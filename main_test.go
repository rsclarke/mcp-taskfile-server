package main

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
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

func TestSanitizeToolName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"greet", "greet"},
		{"build", "build"},
		{"db:migrate", "db_migrate"},
		{"uv:run", "uv_run"},
		{"uv:run:dev:lint-imports", "uv_run_dev_lint-imports"},
		{"uv:.venv", "uv_.venv"},
		{"start:*", "start"},
		{"deploy:*:*", "deploy"},
		{"uv:add:*", "uv_add"},
		{"docs:serve", "docs_serve"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeToolName(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeToolName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestCreateToolForTask_Namespaced(t *testing.T) {
	s := loadServerFromFixture(t, "namespaced")

	tests := []struct {
		taskName string
		wantTool string
		wantDesc string
	}{
		{"db:migrate", "db_migrate", "Run database migrations (task: db:migrate)"},
		{"uv:run", "uv_run", "Run with uv (task: uv:run)"},
		{"uv:run:dev:lint-imports", "uv_run_dev_lint-imports", "Lint imports in dev (task: uv:run:dev:lint-imports)"},
	}

	for _, tt := range tests {
		t.Run(tt.taskName, func(t *testing.T) {
			taskDef := lookupTask(t, s.taskfile, tt.taskName)
			tool := s.createToolForTask(tt.taskName, taskDef)

			if tool.Name != tt.wantTool {
				t.Errorf("Name = %q, want %q", tool.Name, tt.wantTool)
			}
			if tool.Description != tt.wantDesc {
				t.Errorf("Description = %q, want %q", tool.Description, tt.wantDesc)
			}
		})
	}
}

func TestCreateToolForTask_Wildcard(t *testing.T) {
	s := loadServerFromFixture(t, "wildcard")

	t.Run("single wildcard", func(t *testing.T) {
		taskDef := lookupTask(t, s.taskfile, "start:*")
		tool := s.createToolForTask("start:*", taskDef)

		if tool.Name != "start" {
			t.Errorf("Name = %q, want %q", tool.Name, "start")
		}

		props := schemaProperties(t, tool)
		if _, ok := props["MATCH"]; !ok {
			t.Fatal("missing MATCH property for wildcard task")
		}

		required := schemaRequired(t, tool)
		if !slices.Contains(required, "MATCH") {
			t.Errorf("MATCH should be required, got required=%v", required)
		}
	})

	t.Run("double wildcard", func(t *testing.T) {
		taskDef := lookupTask(t, s.taskfile, "deploy:*:*")
		tool := s.createToolForTask("deploy:*:*", taskDef)

		if tool.Name != "deploy" {
			t.Errorf("Name = %q, want %q", tool.Name, "deploy")
		}

		props := schemaProperties(t, tool)
		matchProp, ok := props["MATCH"]
		if !ok {
			t.Fatal("missing MATCH property for wildcard task")
		}

		propMap, _ := matchProp.(map[string]any)
		desc, _ := propMap["description"].(string)
		if !strings.Contains(desc, "2 comma-separated") {
			t.Errorf("MATCH description should mention 2 values, got %q", desc)
		}
	})
}

func TestCreateToolForTask_LeadingDot(t *testing.T) {
	s := loadServerFromFixture(t, "leading-dot")

	taskDef := lookupTask(t, s.taskfile, "uv:.venv")
	tool := s.createToolForTask("uv:.venv", taskDef)

	if tool.Name != "uv_.venv" {
		t.Errorf("Name = %q, want %q", tool.Name, "uv_.venv")
	}
}

func TestRegisterTasks_Namespaced(t *testing.T) {
	s := loadServerFromFixture(t, "namespaced")

	reg := &fakeRegistrar{}
	if err := s.registerTasks(reg); err != nil {
		t.Fatalf("registerTasks failed: %v", err)
	}

	names := make(map[string]bool)
	for _, tool := range reg.tools {
		names[tool.Name] = true
	}

	for _, want := range []string{"db_migrate", "uv_run", "uv_run_dev_lint-imports"} {
		if !names[want] {
			t.Errorf("expected registered tool %q, got tools: %v", want, names)
		}
	}
}

func TestRegisterTasks_Includes(t *testing.T) {
	s := loadServerFromFixture(t, "includes")

	reg := &fakeRegistrar{}
	if err := s.registerTasks(reg); err != nil {
		t.Fatalf("registerTasks failed: %v", err)
	}

	names := make(map[string]bool)
	for _, tool := range reg.tools {
		names[tool.Name] = true
	}

	for _, want := range []string{"build", "docs_serve", "docs_build"} {
		if !names[want] {
			t.Errorf("expected registered tool %q, got tools: %v", want, names)
		}
	}
}

func TestRegisterTasks_Wildcard(t *testing.T) {
	s := loadServerFromFixture(t, "wildcard")

	reg := &fakeRegistrar{}
	if err := s.registerTasks(reg); err != nil {
		t.Fatalf("registerTasks failed: %v", err)
	}

	names := make(map[string]bool)
	for _, tool := range reg.tools {
		names[tool.Name] = true
	}

	for _, want := range []string{"start", "deploy"} {
		if !names[want] {
			t.Errorf("expected registered tool %q, got tools: %v", want, names)
		}
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

// schemaRequired extracts the "required" array from a tool's InputSchema.
func schemaRequired(t *testing.T, tool *mcp.Tool) []string {
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

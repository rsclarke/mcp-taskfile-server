package main

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

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

// newTestServer creates a TaskfileServer from a fixture with a real *mcp.Server attached.
func newTestServer(t *testing.T, fixture string) *TaskfileServer {
	t.Helper()
	s := loadServerFromFixture(t, fixture)
	s.mcpServer = mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil)
	s.registeredTools = make(map[string]mcp.Tool)
	return s
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

func TestBuildToolSet_SkipsInternal(t *testing.T) {
	s := loadServerFromFixture(t, "internal")

	tools, _, err := s.buildToolSet()
	if err != nil {
		t.Fatalf("buildToolSet failed: %v", err)
	}

	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if _, ok := tools["public"]; !ok {
		t.Errorf("expected tool %q, got tools: %v", "public", toolNames(tools))
	}
}

func TestSyncTools_SkipsInternal(t *testing.T) {
	s := newTestServer(t, "internal")

	if err := s.syncTools(); err != nil {
		t.Fatalf("syncTools failed: %v", err)
	}

	if len(s.registeredTools) != 1 {
		t.Fatalf("expected 1 registered tool, got %d", len(s.registeredTools))
	}
	if _, ok := s.registeredTools["public"]; !ok {
		t.Errorf("expected tool %q, got tools: %v", "public", toolNames(s.registeredTools))
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

func TestBuildToolSet_Namespaced(t *testing.T) {
	s := loadServerFromFixture(t, "namespaced")

	tools, _, err := s.buildToolSet()
	if err != nil {
		t.Fatalf("buildToolSet failed: %v", err)
	}

	for _, want := range []string{"db_migrate", "uv_run", "uv_run_dev_lint-imports"} {
		if _, ok := tools[want]; !ok {
			t.Errorf("expected tool %q, got tools: %v", want, toolNames(tools))
		}
	}
}

func TestBuildToolSet_Includes(t *testing.T) {
	s := loadServerFromFixture(t, "includes")

	tools, _, err := s.buildToolSet()
	if err != nil {
		t.Fatalf("buildToolSet failed: %v", err)
	}

	for _, want := range []string{"build", "docs_serve", "docs_build"} {
		if _, ok := tools[want]; !ok {
			t.Errorf("expected tool %q, got tools: %v", want, toolNames(tools))
		}
	}
}

func TestBuildToolSet_Wildcard(t *testing.T) {
	s := loadServerFromFixture(t, "wildcard")

	tools, _, err := s.buildToolSet()
	if err != nil {
		t.Fatalf("buildToolSet failed: %v", err)
	}

	for _, want := range []string{"start", "deploy"} {
		if _, ok := tools[want]; !ok {
			t.Errorf("expected tool %q, got tools: %v", want, toolNames(tools))
		}
	}
}

func TestToolsEqual(t *testing.T) {
	schema1 := json.RawMessage(`{"type":"object","properties":{"FOO":{"type":"string"}}}`)
	schema2 := json.RawMessage(`{"type":"object","properties":{"BAR":{"type":"string"}}}`)

	tests := []struct {
		name string
		a, b *mcp.Tool
		want bool
	}{
		{
			name: "identical",
			a:    &mcp.Tool{Name: "greet", Description: "Say hello", InputSchema: schema1},
			b:    &mcp.Tool{Name: "greet", Description: "Say hello", InputSchema: schema1},
			want: true,
		},
		{
			name: "different name",
			a:    &mcp.Tool{Name: "greet", Description: "Say hello", InputSchema: schema1},
			b:    &mcp.Tool{Name: "build", Description: "Say hello", InputSchema: schema1},
			want: false,
		},
		{
			name: "different description",
			a:    &mcp.Tool{Name: "greet", Description: "Say hello", InputSchema: schema1},
			b:    &mcp.Tool{Name: "greet", Description: "Say goodbye", InputSchema: schema1},
			want: false,
		},
		{
			name: "different schema",
			a:    &mcp.Tool{Name: "greet", Description: "Say hello", InputSchema: schema1},
			b:    &mcp.Tool{Name: "greet", Description: "Say hello", InputSchema: schema2},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toolsEqual(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("toolsEqual() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSyncTools_Idempotent(t *testing.T) {
	s := newTestServer(t, "basic")

	if err := s.syncTools(); err != nil {
		t.Fatalf("first syncTools failed: %v", err)
	}
	first := make(map[string]mcp.Tool)
	maps.Copy(first, s.registeredTools)

	if err := s.syncTools(); err != nil {
		t.Fatalf("second syncTools failed: %v", err)
	}

	if len(s.registeredTools) != len(first) {
		t.Errorf("tool count changed: %d -> %d", len(first), len(s.registeredTools))
	}
	for name, tool := range first {
		cur, ok := s.registeredTools[name]
		if !ok {
			t.Errorf("tool %q disappeared after second sync", name)
			continue
		}
		if !toolsEqual(&tool, &cur) {
			t.Errorf("tool %q changed after second sync", name)
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

// toolNames returns the sorted keys from a tool map for use in error messages.
func toolNames(tools map[string]mcp.Tool) []string {
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func TestIsTaskfile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"Taskfile.yml", true},
		{"taskfile.yml", true},
		{"Taskfile.yaml", true},
		{"taskfile.yaml", true},
		{"Taskfile.dist.yml", true},
		{"taskfile.dist.yml", true},
		{"Taskfile.dist.yaml", true},
		{"taskfile.dist.yaml", true},
		{"/some/dir/Taskfile.yml", true},
		{"README.md", false},
		{"main.go", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := isTaskfile(tt.path); got != tt.want {
				t.Errorf("isTaskfile(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestLoadAndRegisterTools(t *testing.T) {
	s := newTestServer(t, "basic")

	if err := s.loadAndRegisterTools(); err != nil {
		t.Fatalf("loadAndRegisterTools failed: %v", err)
	}

	if len(s.registeredTools) == 0 {
		t.Fatal("expected at least one registered tool")
	}
	if _, ok := s.registeredTools["greet"]; !ok {
		t.Errorf("expected tool %q, got tools: %v", "greet", toolNames(s.registeredTools))
	}
}

func TestWatchTaskfiles_ReloadsOnChange(t *testing.T) {
	// Create a temp directory with a minimal Taskfile.
	dir := t.TempDir()
	initial := []byte("version: '3'\ntasks:\n  hello:\n    desc: Say hello\n    cmds:\n      - echo hello\n")
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), initial, 0o600); err != nil {
		t.Fatal(err)
	}

	// Build a server pointing at the temp dir.
	executor := task.NewExecutor(task.WithDir(dir), task.WithSilent(true))
	if err := executor.Setup(); err != nil {
		t.Fatalf("executor setup: %v", err)
	}
	s := &TaskfileServer{
		executor:        executor,
		taskfile:        executor.Taskfile,
		workdir:         dir,
		mcpServer:       mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil),
		registeredTools: make(map[string]mcp.Tool),
	}
	if err := s.syncTools(); err != nil {
		t.Fatalf("initial syncTools: %v", err)
	}
	if _, ok := s.registeredTools["hello"]; !ok {
		t.Fatal("expected initial tool 'hello'")
	}

	ctx := t.Context()

	go func() {
		_ = s.watchTaskfiles(ctx)
	}()

	// Give the watcher time to start.
	time.Sleep(100 * time.Millisecond)

	// Write an updated Taskfile with a new task.
	updated := []byte("version: '3'\ntasks:\n  hello:\n    desc: Say hello\n    cmds:\n      - echo hello\n  goodbye:\n    desc: Say goodbye\n    cmds:\n      - echo goodbye\n")
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), updated, 0o600); err != nil {
		t.Fatal(err)
	}

	// Wait for debounce + reload.
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for tool reload")
		case <-ticker.C:
			if _, ok := s.registeredTools["goodbye"]; ok {
				return // success
			}
		}
	}
}

func TestWatchTaskfiles_IgnoresNonTaskfile(t *testing.T) {
	dir := t.TempDir()
	initial := []byte("version: '3'\ntasks:\n  hello:\n    desc: Say hello\n    cmds:\n      - echo hello\n")
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), initial, 0o600); err != nil {
		t.Fatal(err)
	}

	executor := task.NewExecutor(task.WithDir(dir), task.WithSilent(true))
	if err := executor.Setup(); err != nil {
		t.Fatalf("executor setup: %v", err)
	}
	s := &TaskfileServer{
		executor:        executor,
		taskfile:        executor.Taskfile,
		workdir:         dir,
		mcpServer:       mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil),
		registeredTools: make(map[string]mcp.Tool),
	}
	if err := s.syncTools(); err != nil {
		t.Fatalf("initial syncTools: %v", err)
	}

	ctx := t.Context()

	go func() {
		_ = s.watchTaskfiles(ctx)
	}()

	time.Sleep(100 * time.Millisecond)

	// Write a non-Taskfile — should NOT trigger reload.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Wait a bit to confirm no spurious reload.
	time.Sleep(400 * time.Millisecond)

	// Tools should remain unchanged.
	if len(s.registeredTools) != 1 {
		t.Errorf("expected 1 tool, got %d", len(s.registeredTools))
	}
}

func TestWatchTaskfiles_CancelStops(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  x:\n    cmds:\n      - echo x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	executor := task.NewExecutor(task.WithDir(dir), task.WithSilent(true))
	if err := executor.Setup(); err != nil {
		t.Fatalf("executor setup: %v", err)
	}
	s := &TaskfileServer{
		executor:        executor,
		taskfile:        executor.Taskfile,
		workdir:         dir,
		mcpServer:       mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil),
		registeredTools: make(map[string]mcp.Tool),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- s.watchTaskfiles(ctx)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("watchTaskfiles returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watchTaskfiles did not stop after context cancellation")
	}
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

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-task/task/v3/taskfile/ast"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// loadServerFromFixture creates a TaskfileServer from a testdata fixture directory.
func loadServerFromFixture(t *testing.T, name string) *TaskfileServer {
	t.Helper()

	_, filename, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(filename), "testdata", name)

	root, err := loadRoot(dir)
	if err != nil {
		t.Fatalf("failed to load root for fixture %q: %v", name, err)
	}

	uri := dirToURI(dir)
	return &TaskfileServer{
		roots: map[string]*rootState{uri: root},
	}
}

// onlyRoot returns the single rootState from a server, or fails the test.
func onlyRoot(t *testing.T, s *TaskfileServer) *rootState {
	t.Helper()
	if len(s.roots) != 1 {
		t.Fatalf("expected 1 root, got %d", len(s.roots))
	}
	for _, root := range s.roots {
		return root
	}
	return nil
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
	root := onlyRoot(t, s)

	taskDef := lookupTask(t, root.taskfile, "greet")
	tool := createToolForTask(root, "", "greet", taskDef)

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
	root := onlyRoot(t, s)

	taskDef := lookupTask(t, root.taskfile, "build")
	tool := createToolForTask(root, "", "build", taskDef)

	want := "Execute task: build"
	if tool.Description != want {
		t.Errorf("Description = %q, want %q", tool.Description, want)
	}
}

func TestCreateToolForTask_TaskVars(t *testing.T) {
	s := loadServerFromFixture(t, "task-vars")
	root := onlyRoot(t, s)

	taskDef := lookupTask(t, root.taskfile, "deploy")
	tool := createToolForTask(root, "", "deploy", taskDef)

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
	root := onlyRoot(t, s)

	taskDef := lookupTask(t, root.taskfile, "info")
	tool := createToolForTask(root, "", "info", taskDef)

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
	root := onlyRoot(t, s)

	taskDef := lookupTask(t, root.taskfile, "deploy")
	tool := createToolForTask(root, "", "deploy", taskDef)

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
	root := onlyRoot(t, s)

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
			taskDef := lookupTask(t, root.taskfile, tt.taskName)
			tool := createToolForTask(root, "", tt.taskName, taskDef)

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
	root := onlyRoot(t, s)

	t.Run("single wildcard", func(t *testing.T) {
		taskDef := lookupTask(t, root.taskfile, "start:*")
		tool := createToolForTask(root, "", "start:*", taskDef)

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
		taskDef := lookupTask(t, root.taskfile, "deploy:*:*")
		tool := createToolForTask(root, "", "deploy:*:*", taskDef)

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
	root := onlyRoot(t, s)

	taskDef := lookupTask(t, root.taskfile, "uv:.venv")
	tool := createToolForTask(root, "", "uv:.venv", taskDef)

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

func TestDirToURI(t *testing.T) {
	uri := dirToURI("/some/path")
	if uri != "file:///some/path" {
		t.Errorf("dirToURI(%q) = %q, want %q", "/some/path", uri, "file:///some/path")
	}
}

func TestURIToDir(t *testing.T) {
	dir, err := uriToDir("file:///some/path")
	if err != nil {
		t.Fatalf("uriToDir failed: %v", err)
	}
	if dir != "/some/path" {
		t.Errorf("uriToDir = %q, want %q", dir, "/some/path")
	}

	_, err = uriToDir("https://example.com")
	if err == nil {
		t.Error("expected error for non-file URI")
	}
}

func TestSanitizeRootPrefix(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"myproject", "myproject"},
		{"my project", "my_project"},
		{"my/project", "my_project"},
		{"___", "root"},
		{"", "root"},
		{"a-b.c_d", "a-b.c_d"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeRootPrefix(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeRootPrefix(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestUnloadRoot(t *testing.T) {
	s := newTestServer(t, "basic")
	if err := s.syncTools(); err != nil {
		t.Fatalf("syncTools failed: %v", err)
	}
	if len(s.roots) != 1 {
		t.Fatalf("expected 1 root, got %d", len(s.roots))
	}

	var uri string
	for u := range s.roots {
		uri = u
	}

	s.unloadRoot(uri)
	if len(s.roots) != 0 {
		t.Errorf("expected 0 roots after unload, got %d", len(s.roots))
	}

	// Unloading a non-existent root should be a no-op.
	s.unloadRoot("file:///nonexistent")
}

func TestMultiRoot_Prefixing(t *testing.T) {
	s := loadServerFromFixture(t, "basic")
	s.mcpServer = mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil)
	s.registeredTools = make(map[string]mcp.Tool)

	// Load a second root from a different fixture.
	_, filename, _, _ := runtime.Caller(0)
	dir2 := filepath.Join(filepath.Dir(filename), "testdata", "no-desc")
	root2, err := loadRoot(dir2)
	if err != nil {
		t.Fatalf("loadRoot: %v", err)
	}
	s.roots[dirToURI(dir2)] = root2

	if err := s.syncTools(); err != nil {
		t.Fatalf("syncTools failed: %v", err)
	}

	// With 2 roots, tools should be prefixed.
	if len(s.registeredTools) < 2 {
		t.Fatalf("expected at least 2 tools, got %d: %v", len(s.registeredTools), toolNames(s.registeredTools))
	}

	// Verify that no tool name is unprefixed "greet" or "build".
	for name := range s.registeredTools {
		if name == "greet" || name == "build" {
			t.Errorf("expected prefixed tool name, got %q", name)
		}
	}
}

func TestMultiRoot_SingleRoot_NoPrefix(t *testing.T) {
	s := newTestServer(t, "basic")
	if err := s.syncTools(); err != nil {
		t.Fatalf("syncTools failed: %v", err)
	}

	// Single root: no prefix.
	if _, ok := s.registeredTools["greet"]; !ok {
		t.Errorf("expected unprefixed tool %q, got: %v", "greet", toolNames(s.registeredTools))
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

	s := newServerForDir(t, dir)
	if _, ok := s.registeredTools["hello"]; !ok {
		t.Fatal("expected initial tool 'hello'")
	}

	ctx := t.Context()

	go func() {
		_ = s.watchTaskfiles(ctx, snapshotRoots(s))
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
			s.mu.Lock()
			_, ok := s.registeredTools["goodbye"]
			s.mu.Unlock()
			if ok {
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

	s := newServerForDir(t, dir)

	ctx := t.Context()

	go func() {
		_ = s.watchTaskfiles(ctx, snapshotRoots(s))
	}()

	time.Sleep(100 * time.Millisecond)

	// Write a non-Taskfile — should NOT trigger reload.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Wait a bit to confirm no spurious reload.
	time.Sleep(400 * time.Millisecond)

	// Tools should remain unchanged.
	s.mu.Lock()
	n := len(s.registeredTools)
	s.mu.Unlock()
	if n != 1 {
		t.Errorf("expected 1 tool, got %d", n)
	}
}

func TestWatchTaskfiles_CancelStops(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  x:\n    cmds:\n      - echo x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := newServerForDir(t, dir)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- s.watchTaskfiles(ctx, snapshotRoots(s))
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

// snapshotRoots returns a rootSnapshot slice for use with watchTaskfiles.
func snapshotRoots(s *TaskfileServer) []rootSnapshot {
	snap := make([]rootSnapshot, 0, len(s.roots))
	for uri, root := range s.roots {
		snap = append(snap, rootSnapshot{uri: uri, root: root})
	}
	return snap
}

// newTempServer creates a TaskfileServer backed by a temp directory containing
// the given Taskfile content, with a real *mcp.Server and initial syncTools.
func newTempServer(t *testing.T, taskfileContent []byte) *TaskfileServer {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), taskfileContent, 0o600); err != nil {
		t.Fatal(err)
	}
	return newServerForDir(t, dir)
}

// newServerForDir creates a TaskfileServer backed by a given directory,
// with a real *mcp.Server and initial syncTools.
func newServerForDir(t *testing.T, dir string) *TaskfileServer {
	t.Helper()
	root, err := loadRoot(dir)
	if err != nil {
		t.Fatalf("loadRoot: %v", err)
	}
	uri := dirToURI(dir)
	s := &TaskfileServer{
		roots:           map[string]*rootState{uri: root},
		mcpServer:       mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil),
		registeredTools: make(map[string]mcp.Tool),
	}
	if err := s.syncTools(); err != nil {
		t.Fatalf("initial syncTools: %v", err)
	}
	return s
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

func TestLoadAndRegisterTools_RemovesTask(t *testing.T) {
	initial := []byte("version: '3'\ntasks:\n  hello:\n    desc: Say hello\n    cmds:\n      - echo hello\n  goodbye:\n    desc: Say goodbye\n    cmds:\n      - echo goodbye\n")
	s := newTempServer(t, initial)

	if _, ok := s.registeredTools["hello"]; !ok {
		t.Fatal("expected initial tool 'hello'")
	}
	if _, ok := s.registeredTools["goodbye"]; !ok {
		t.Fatal("expected initial tool 'goodbye'")
	}

	// Remove the "goodbye" task from the Taskfile.
	updated := []byte("version: '3'\ntasks:\n  hello:\n    desc: Say hello\n    cmds:\n      - echo hello\n")
	if err := os.WriteFile(filepath.Join(onlyRoot(t, s).workdir, "Taskfile.yml"), updated, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := s.loadAndRegisterTools(); err != nil {
		t.Fatalf("loadAndRegisterTools failed: %v", err)
	}

	if _, ok := s.registeredTools["goodbye"]; ok {
		t.Error("tool 'goodbye' should have been removed")
	}
	if _, ok := s.registeredTools["hello"]; !ok {
		t.Error("tool 'hello' should still be registered")
	}
}

func TestLoadAndRegisterTools_UpdatesChangedTask(t *testing.T) {
	initial := []byte("version: '3'\ntasks:\n  greet:\n    desc: Say hello\n    cmds:\n      - echo hello\n")
	s := newTempServer(t, initial)

	origTool := s.registeredTools["greet"]
	if origTool.Description != "Say hello" {
		t.Fatalf("initial description = %q, want %q", origTool.Description, "Say hello")
	}

	// Update the task description.
	updated := []byte("version: '3'\ntasks:\n  greet:\n    desc: Say hi there\n    cmds:\n      - echo hi there\n")
	if err := os.WriteFile(filepath.Join(onlyRoot(t, s).workdir, "Taskfile.yml"), updated, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := s.loadAndRegisterTools(); err != nil {
		t.Fatalf("loadAndRegisterTools failed: %v", err)
	}

	updatedTool, ok := s.registeredTools["greet"]
	if !ok {
		t.Fatal("tool 'greet' should still be registered")
	}
	if updatedTool.Description != "Say hi there" {
		t.Errorf("description = %q, want %q", updatedTool.Description, "Say hi there")
	}
}

func TestWatchTaskfiles_DebounceCoalesces(t *testing.T) {
	initial := []byte("version: '3'\ntasks:\n  hello:\n    desc: Say hello\n    cmds:\n      - echo hello\n")
	s := newTempServer(t, initial)
	root := onlyRoot(t, s)

	// Count reloads by tracking description changes.
	// We'll write multiple rapid updates and verify the final state
	// appears without intermediate states lingering.
	ctx := t.Context()

	go func() {
		_ = s.watchTaskfiles(ctx, snapshotRoots(s))
	}()

	time.Sleep(100 * time.Millisecond)

	// Fire multiple rapid writes within the debounce window (200ms).
	for i := range 5 {
		content := fmt.Appendf(nil, "version: '3'\ntasks:\n  hello:\n    desc: Attempt %d\n    cmds:\n      - echo hello\n", i)
		if err := os.WriteFile(filepath.Join(root.workdir, "Taskfile.yml"), content, 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Wait for debounce + reload to settle.
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for debounced reload")
		case <-ticker.C:
			s.mu.Lock()
			tool, ok := s.registeredTools["hello"]
			s.mu.Unlock()
			if ok && tool.Description == "Attempt 4" {
				return // success — final write was applied
			}
		}
	}
}

func TestHandleInitialized_WithRoots(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  hello:\n    desc: Say hello\n    cmds:\n      - echo hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ts := NewTaskfileServer()
	ctx := t.Context()

	rootURI := dirToURI(dir)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	client.AddRoots(&mcp.Root{URI: rootURI, Name: "test"})

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, &mcp.ServerOptions{
		InitializedHandler:      ts.handleInitialized,
		RootsListChangedHandler: ts.handleRootsChanged,
	})
	ts.mcpServer = server
	ts.registeredTools = make(map[string]mcp.Tool)

	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })

	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	// Wait for initialization to complete.
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for tools to be registered via InitializedHandler")
		case <-ticker.C:
			ts.mu.Lock()
			n := len(ts.registeredTools)
			ts.mu.Unlock()
			if n > 0 {
				ts.mu.Lock()
				_, ok := ts.registeredTools["hello"]
				ts.mu.Unlock()
				if ok {
					goto done
				}
			}
		}
	}
done:

	ts.mu.Lock()
	if len(ts.roots) != 1 {
		t.Errorf("expected 1 root, got %d", len(ts.roots))
	}
	if _, ok := ts.roots[rootURI]; !ok {
		t.Errorf("expected root %q", rootURI)
	}
	ts.mu.Unlock()
}

func TestHandleRootsChanged_AddAndRemove(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir1, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  task1:\n    desc: Task one\n    cmds:\n      - echo one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir2, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  task2:\n    desc: Task two\n    cmds:\n      - echo two\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ts := NewTaskfileServer()
	ctx := t.Context()

	uri1 := dirToURI(dir1)
	uri2 := dirToURI(dir2)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	client.AddRoots(&mcp.Root{URI: uri1, Name: "root1"})

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, &mcp.ServerOptions{
		InitializedHandler:      ts.handleInitialized,
		RootsListChangedHandler: ts.handleRootsChanged,
	})
	ts.mcpServer = server
	ts.registeredTools = make(map[string]mcp.Tool)

	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })

	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	// Wait for initial root to load.
	waitForTools(t, ts, 1)

	// Add a second root.
	client.AddRoots(&mcp.Root{URI: uri2, Name: "root2"})

	// Wait for tools from both roots (prefixed).
	waitForTools(t, ts, 2)

	ts.mu.Lock()
	if len(ts.roots) != 2 {
		t.Errorf("expected 2 roots, got %d", len(ts.roots))
	}
	ts.mu.Unlock()

	// Remove the first root.
	client.RemoveRoots(uri1)

	// Wait until only 1 tool remains.
	waitForToolCount(t, ts, 1)

	ts.mu.Lock()
	if len(ts.roots) != 1 {
		t.Errorf("expected 1 root after removal, got %d", len(ts.roots))
	}
	if _, ok := ts.roots[uri2]; !ok {
		t.Errorf("expected root %q to remain", uri2)
	}
	ts.mu.Unlock()
}

// waitForTools waits until the server has at least minTools registered.
func waitForTools(t *testing.T, ts *TaskfileServer, minTools int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			ts.mu.Lock()
			n := len(ts.registeredTools)
			ts.mu.Unlock()
			t.Fatalf("timed out waiting for %d tools, have %d", minTools, n)
		case <-ticker.C:
			ts.mu.Lock()
			n := len(ts.registeredTools)
			ts.mu.Unlock()
			if n >= minTools {
				return
			}
		}
	}
}

// waitForToolCount waits until the server has exactly count tools registered.
func waitForToolCount(t *testing.T, ts *TaskfileServer, count int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			ts.mu.Lock()
			n := len(ts.registeredTools)
			ts.mu.Unlock()
			t.Fatalf("timed out waiting for %d tools, have %d", count, n)
		case <-ticker.C:
			ts.mu.Lock()
			n := len(ts.registeredTools)
			ts.mu.Unlock()
			if n == count {
				return
			}
		}
	}
}

func TestHandleInitialized_FallbackToWorkdir(t *testing.T) {
	// This test verifies that when the client does not support roots,
	// the server falls back to os.Getwd(). We test the isMethodNotFound
	// helper directly since exercising a client without roots capability
	// requires deeper SDK internals.
	err := &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "not found"}
	if !isMethodNotFound(err) {
		t.Error("expected isMethodNotFound to return true for CodeMethodNotFound")
	}
	if isMethodNotFound(errors.New("some other error")) {
		t.Error("expected isMethodNotFound to return false for a non-wire error")
	}
}

func TestCreateTaskHandler_Success(t *testing.T) {
	s := loadServerFromFixture(t, "basic")
	root := onlyRoot(t, s)

	handler := createTaskHandler(root, "greet")
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

func TestCreateTaskHandler_TaskFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  fail:\n    desc: A failing task\n    cmds:\n      - exit 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	root, err := loadRoot(dir)
	if err != nil {
		t.Fatalf("loadRoot: %v", err)
	}

	handler := createTaskHandler(root, "fail")
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

func TestCreateTaskHandler_WithVariables(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  greet:\n    desc: Greet someone\n    cmds:\n      - echo hello {{.NAME}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	root, err := loadRoot(dir)
	if err != nil {
		t.Fatalf("loadRoot: %v", err)
	}

	handler := createTaskHandler(root, "greet")
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

func TestCreateTaskHandler_WildcardMATCH(t *testing.T) {
	s := loadServerFromFixture(t, "wildcard")
	root := onlyRoot(t, s)

	handler := createTaskHandler(root, "start:*")
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

func TestCreateTaskHandler_WildcardMissingMATCH(t *testing.T) {
	s := loadServerFromFixture(t, "wildcard")
	root := onlyRoot(t, s)

	handler := createTaskHandler(root, "start:*")
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

func TestCreateTaskHandler_WildcardWrongCount(t *testing.T) {
	s := loadServerFromFixture(t, "wildcard")
	root := onlyRoot(t, s)

	handler := createTaskHandler(root, "deploy:*:*")
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

func TestCreateTaskHandler_InvalidArguments(t *testing.T) {
	s := loadServerFromFixture(t, "basic")
	root := onlyRoot(t, s)

	handler := createTaskHandler(root, "greet")
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

func TestBuildToolSet_Collision(t *testing.T) {
	// Create two dirs with the same basename ("dup") containing identically
	// named tasks. With >1 root the prefix is derived from the basename,
	// so both roots produce the same prefixed tool name → collision.
	dir1 := filepath.Join(t.TempDir(), "dup")
	dir2 := filepath.Join(t.TempDir(), "dup")
	if err := os.Mkdir(dir1, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir2, 0o750); err != nil {
		t.Fatal(err)
	}

	taskfile := []byte("version: '3'\ntasks:\n  hello:\n    desc: Say hello\n    cmds:\n      - echo hello\n")
	if err := os.WriteFile(filepath.Join(dir1, "Taskfile.yml"), taskfile, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir2, "Taskfile.yml"), taskfile, 0o600); err != nil {
		t.Fatal(err)
	}

	r1, err := loadRoot(dir1)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := loadRoot(dir2)
	if err != nil {
		t.Fatal(err)
	}

	s := &TaskfileServer{
		roots: map[string]*rootState{
			dirToURI(dir1): r1,
			dirToURI(dir2): r2,
		},
	}

	_, _, err = s.buildToolSet()
	if err == nil {
		t.Fatal("expected collision error, got nil")
	}
	if !strings.Contains(err.Error(), "collision") {
		t.Errorf("expected collision error, got: %v", err)
	}
}

func TestBuildToolSet_NoTasks(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  helper:\n    internal: true\n    cmds:\n      - echo hidden\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	root, err := loadRoot(dir)
	if err != nil {
		t.Fatal(err)
	}

	s := &TaskfileServer{
		roots: map[string]*rootState{dirToURI(dir): root},
	}

	_, _, err = s.buildToolSet()
	if err == nil {
		t.Fatal("expected error for no tasks, got nil")
	}
	if !strings.Contains(err.Error(), "no tasks found") {
		t.Errorf("expected 'no tasks found' error, got: %v", err)
	}
}

func TestHandleRootsChanged_TransitionToUnprefixed(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir1, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  task1:\n    desc: Task one\n    cmds:\n      - echo one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir2, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  task2:\n    desc: Task two\n    cmds:\n      - echo two\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ts := NewTaskfileServer()
	ctx := t.Context()

	uri1 := dirToURI(dir1)
	uri2 := dirToURI(dir2)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	client.AddRoots(
		&mcp.Root{URI: uri1, Name: "root1"},
		&mcp.Root{URI: uri2, Name: "root2"},
	)

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, &mcp.ServerOptions{
		InitializedHandler:      ts.handleInitialized,
		RootsListChangedHandler: ts.handleRootsChanged,
	})
	ts.mcpServer = server
	ts.registeredTools = make(map[string]mcp.Tool)

	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })

	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	// Wait for both roots to be loaded (prefixed tools).
	waitForTools(t, ts, 2)

	// Verify tools are prefixed.
	ts.mu.Lock()
	for name := range ts.registeredTools {
		if name == "task1" || name == "task2" {
			t.Errorf("expected prefixed tool name with 2 roots, got %q", name)
		}
	}
	ts.mu.Unlock()

	// Remove one root to go back to a single root.
	client.RemoveRoots(uri1)

	// Wait until only 1 tool remains.
	waitForToolCount(t, ts, 1)

	// Verify the remaining tool is unprefixed.
	ts.mu.Lock()
	if _, ok := ts.registeredTools["task2"]; !ok {
		t.Errorf("expected unprefixed tool 'task2' after N->1 transition, got: %v", toolNames(ts.registeredTools))
	}
	ts.mu.Unlock()
}

func TestReloadRoot_UnknownURI(t *testing.T) {
	s := newTestServer(t, "basic")

	err := s.reloadRoot("file:///nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown URI, got nil")
	}
	if !strings.Contains(err.Error(), "unknown root") {
		t.Errorf("expected 'unknown root' error, got: %v", err)
	}
}

func TestCreateToolForTask_WithPrefix(t *testing.T) {
	s := loadServerFromFixture(t, "basic")
	root := onlyRoot(t, s)

	taskDef := lookupTask(t, root.taskfile, "greet")
	tool := createToolForTask(root, "myproject", "greet", taskDef)

	if tool.Name != "myproject_greet" {
		t.Errorf("Name = %q, want %q", tool.Name, "myproject_greet")
	}
}

func TestWatchTaskfiles_NewSubdirectory(t *testing.T) {
	dir := t.TempDir()
	initial := []byte("version: '3'\ntasks:\n  hello:\n    desc: Say hello\n    cmds:\n      - echo hello\n")
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), initial, 0o600); err != nil {
		t.Fatal(err)
	}

	s := newServerForDir(t, dir)

	ctx := t.Context()

	go func() {
		_ = s.watchTaskfiles(ctx, snapshotRoots(s))
	}()

	time.Sleep(100 * time.Millisecond)

	// Create a new subdirectory after the watcher started.
	subdir := filepath.Join(dir, "sub")
	if err := os.Mkdir(subdir, 0o750); err != nil {
		t.Fatal(err)
	}

	// Give the watcher time to pick up the new directory.
	time.Sleep(200 * time.Millisecond)

	// Write a Taskfile in the new subdirectory. The main Taskfile includes it
	// indirectly — but we're testing that the watcher picks up the file event
	// in the new subdirectory. Update the root Taskfile to include it.
	subTaskfile := []byte("version: '3'\ntasks:\n  sub-task:\n    desc: From subdirectory\n    cmds:\n      - echo sub\n")
	if err := os.WriteFile(filepath.Join(subdir, "Taskfile.yml"), subTaskfile, 0o600); err != nil {
		t.Fatal(err)
	}

	// Update root Taskfile to include the subdirectory.
	rootWithInclude := []byte("version: '3'\nincludes:\n  sub:\n    taskfile: ./sub\n\ntasks:\n  hello:\n    desc: Say hello\n    cmds:\n      - echo hello\n")
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), rootWithInclude, 0o600); err != nil {
		t.Fatal(err)
	}

	// Wait for reload to register the included task.
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			s.mu.Lock()
			names := toolNames(s.registeredTools)
			s.mu.Unlock()
			t.Fatalf("timed out waiting for sub-task; registered tools: %v", names)
		case <-ticker.C:
			s.mu.Lock()
			_, ok := s.registeredTools["sub_sub-task"]
			s.mu.Unlock()
			if ok {
				return // success
			}
		}
	}
}

func TestToolListChangedNotification_OnFileChange(t *testing.T) {
	dir := t.TempDir()
	initial := []byte("version: '3'\ntasks:\n  hello:\n    desc: Say hello\n    cmds:\n      - echo hello\n")
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), initial, 0o600); err != nil {
		t.Fatal(err)
	}

	ts := NewTaskfileServer()
	ctx := t.Context()

	rootURI := dirToURI(dir)

	// Track notifications received by the client.
	notified := make(chan struct{}, 10)

	client := mcp.NewClient(
		&mcp.Implementation{Name: "test-client", Version: "0.0.0"},
		&mcp.ClientOptions{
			ToolListChangedHandler: func(_ context.Context, _ *mcp.ToolListChangedRequest) {
				notified <- struct{}{}
			},
		},
	)
	client.AddRoots(&mcp.Root{URI: rootURI, Name: "test"})

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, &mcp.ServerOptions{
		InitializedHandler:      ts.handleInitialized,
		RootsListChangedHandler: ts.handleRootsChanged,
	})
	ts.mcpServer = server
	ts.registeredTools = make(map[string]mcp.Tool)

	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })

	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	// Wait for initial tools to be registered.
	waitForTools(t, ts, 1)

	// Drain any notifications from the initial tool registration.
	drainDone := time.After(500 * time.Millisecond)
drain:
	for {
		select {
		case <-notified:
		case <-drainDone:
			break drain
		}
	}

	// Write an updated Taskfile with a new task.
	updated := []byte("version: '3'\ntasks:\n  hello:\n    desc: Say hello\n    cmds:\n      - echo hello\n  goodbye:\n    desc: Say goodbye\n    cmds:\n      - echo goodbye\n")
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), updated, 0o600); err != nil {
		t.Fatal(err)
	}

	// Wait for the server to reload and register the new tool.
	waitForTools(t, ts, 2)

	// The client should receive a tools/list_changed notification.
	select {
	case <-notified:
		// success — notification was delivered
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for tools/list_changed notification from file change")
	}

	// Verify the client can see the new tool via ListTools.
	toolsRes, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}

	toolNames := make(map[string]bool)
	for _, tool := range toolsRes.Tools {
		toolNames[tool.Name] = true
	}
	if !toolNames["goodbye"] {
		t.Errorf("expected tool %q in ListTools response, got: %v", "goodbye", toolNames)
	}
}

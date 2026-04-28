package tools

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rsclarke/mcp-taskfile-server/internal/roots"
)

func TestBuildPlan_SkipsInternal(t *testing.T) {
	root := loadRootFromFixture(t, "internal")

	plan := BuildPlan(snapshotFromRoots(root), testLogger())

	if len(plan.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(plan.Tools))
	}
	if _, ok := plan.Tools["public"]; !ok {
		t.Errorf("expected tool %q, got tools: %v", "public", toolNames(plan.Tools))
	}
}

func TestBuildPlan_Namespaced(t *testing.T) {
	root := loadRootFromFixture(t, "namespaced")

	plan := BuildPlan(snapshotFromRoots(root), testLogger())

	for _, want := range []string{"db_migrate", "uv_run", "uv_run_dev_lint-imports"} {
		if _, ok := plan.Tools[want]; !ok {
			t.Errorf("expected tool %q, got tools: %v", want, toolNames(plan.Tools))
		}
	}
}

func TestBuildPlan_Includes(t *testing.T) {
	root := loadRootFromFixture(t, "includes")

	plan := BuildPlan(snapshotFromRoots(root), testLogger())

	for _, want := range []string{"build", "docs_serve", "docs_build"} {
		if _, ok := plan.Tools[want]; !ok {
			t.Errorf("expected tool %q, got tools: %v", want, toolNames(plan.Tools))
		}
	}
}

func TestBuildPlan_Wildcard(t *testing.T) {
	root := loadRootFromFixture(t, "wildcard")

	plan := BuildPlan(snapshotFromRoots(root), testLogger())

	for _, want := range []string{"start", "deploy"} {
		if _, ok := plan.Tools[want]; !ok {
			t.Errorf("expected tool %q, got tools: %v", want, toolNames(plan.Tools))
		}
	}
}

func TestBuildPlan_HandlerExecutesSelectedTool(t *testing.T) {
	root := loadRootFromFixture(t, "basic")

	plan := BuildPlan(snapshotFromRoots(root), testLogger())
	handler, ok := plan.Handlers["greet"]
	if !ok {
		t.Fatalf("missing handler for greet, got %v", toolNames(plan.Tools))
	}

	result := callToolHandler(t, handler, "greet", nil)
	if result.IsError {
		t.Fatalf("expected success, got IsError=true: %s", toolResultText(t, result))
	}

	if status := toolStatusText(t, result); !strings.Contains(status, "exited with status 0") {
		t.Fatalf("expected status block to report exit 0, got %q", status)
	}
	if stdout := toolStreamText(t, result, "stdout"); !strings.Contains(stdout, "hello") {
		t.Fatalf("expected stdout block to contain hello, got %q", stdout)
	}
}

func TestBuildPlan_HandlerPassesVariables(t *testing.T) {
	root := tempRoot(t, []byte("version: '3'\ntasks:\n  greet:\n    desc: Greet someone\n    cmds:\n      - echo hello {{.NAME}}\n"))

	plan := BuildPlan(snapshotFromRoots(root), testLogger())
	handler, ok := plan.Handlers["greet"]
	if !ok {
		t.Fatalf("missing handler for greet, got %v", toolNames(plan.Tools))
	}

	result := callToolHandler(t, handler, "greet", map[string]string{"NAME": "world"})
	if result.IsError {
		t.Fatalf("expected success, got IsError=true: %s", toolResultText(t, result))
	}

	text := toolResultText(t, result)
	if !strings.Contains(text, "world") {
		t.Fatalf("expected output to contain world, got %q", text)
	}
}

func TestBuildPlan_HandlerReportsTaskFailure(t *testing.T) {
	root := tempRoot(t, []byte("version: '3'\ntasks:\n  fail:\n    desc: A failing task\n    cmds:\n      - exit 1\n"))

	plan := BuildPlan(snapshotFromRoots(root), testLogger())
	handler, ok := plan.Handlers["fail"]
	if !ok {
		t.Fatalf("missing handler for fail, got %v", toolNames(plan.Tools))
	}

	result := callToolHandler(t, handler, "fail", nil)
	if !result.IsError {
		t.Fatal("expected IsError=true for a failing task")
	}

	status := toolStatusText(t, result)
	if !strings.Contains(status, "exited with status") {
		t.Fatalf("expected status block to report exit status, got %q", status)
	}
	if strings.Contains(status, "exited with status 0") {
		t.Fatalf("failing task should not report status 0, got %q", status)
	}
}

func TestBuildPlan_HandlerExecutesWildcardTool(t *testing.T) {
	root := loadRootFromFixture(t, "wildcard")

	plan := BuildPlan(snapshotFromRoots(root), testLogger())
	handler, ok := plan.Handlers["deploy"]
	if !ok {
		t.Fatalf("missing handler for deploy, got %v", toolNames(plan.Tools))
	}

	result := callToolHandler(t, handler, "deploy", map[string]any{"MATCH": []string{"api", "production"}})
	if result.IsError {
		t.Fatalf("expected success, got IsError=true: %s", toolResultText(t, result))
	}

	text := toolResultText(t, result)
	if !strings.Contains(text, "api") || !strings.Contains(text, "production") {
		t.Fatalf("expected output to contain wildcard values, got %q", text)
	}
}

func TestBuildPlan_HandlerSelectsPrefixedRootTool(t *testing.T) {
	parent := t.TempDir()
	frontendDir := filepath.Join(parent, "frontend")
	backendDir := filepath.Join(parent, "backend")
	if err := os.Mkdir(frontendDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(backendDir, 0o750); err != nil {
		t.Fatal(err)
	}

	frontendTaskfile := []byte("version: '3'\ntasks:\n  serve:\n    desc: Serve frontend\n    cmds:\n      - echo frontend\n")
	backendTaskfile := []byte("version: '3'\ntasks:\n  serve:\n    desc: Serve backend\n    cmds:\n      - echo backend\n")
	if err := os.WriteFile(filepath.Join(frontendDir, "Taskfile.yml"), frontendTaskfile, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backendDir, "Taskfile.yml"), backendTaskfile, 0o600); err != nil {
		t.Fatal(err)
	}

	frontendRoot, err := roots.Load(t.Context(), frontendDir)
	if err != nil {
		t.Fatal(err)
	}
	backendRoot, err := roots.Load(t.Context(), backendDir)
	if err != nil {
		t.Fatal(err)
	}

	plan := BuildPlan(snapshotFromRoots(frontendRoot, backendRoot), testLogger())
	frontendHandler, ok := plan.Handlers["frontend_serve"]
	if !ok {
		t.Fatalf("missing frontend handler, got %v", toolNames(plan.Tools))
	}
	backendHandler, ok := plan.Handlers["backend_serve"]
	if !ok {
		t.Fatalf("missing backend handler, got %v", toolNames(plan.Tools))
	}

	frontendResult := callToolHandler(t, frontendHandler, "frontend_serve", nil)
	if frontendResult.IsError {
		t.Fatalf("expected frontend success, got IsError=true: %s", toolResultText(t, frontendResult))
	}
	backendResult := callToolHandler(t, backendHandler, "backend_serve", nil)
	if backendResult.IsError {
		t.Fatalf("expected backend success, got IsError=true: %s", toolResultText(t, backendResult))
	}

	if text := toolResultText(t, frontendResult); !strings.Contains(text, "frontend") {
		t.Fatalf("expected frontend handler output, got %q", text)
	}
	if text := toolResultText(t, backendResult); !strings.Contains(text, "backend") {
		t.Fatalf("expected backend handler output, got %q", text)
	}
}

func TestBuildPlan_ExcludesCollidingToolNamesAcrossRoots(t *testing.T) {
	// Create two dirs with the same basename ("dup") containing identically
	// named tasks. With >1 root the prefix is derived from the basename,
	// so both roots produce the same prefixed tool name and neither should be exposed.
	dir1 := filepath.Join(t.TempDir(), "dup")
	dir2 := filepath.Join(t.TempDir(), "dup")
	if err := os.Mkdir(dir1, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir2, 0o750); err != nil {
		t.Fatal(err)
	}

	taskfile1 := []byte("version: '3'\ntasks:\n  hello:\n    desc: Say hello\n    cmds:\n      - echo hello\n  frontend:\n    desc: Frontend task\n    cmds:\n      - echo frontend\n")
	taskfile2 := []byte("version: '3'\ntasks:\n  hello:\n    desc: Say hello\n    cmds:\n      - echo hello\n  backend:\n    desc: Backend task\n    cmds:\n      - echo backend\n")
	if err := os.WriteFile(filepath.Join(dir1, "Taskfile.yml"), taskfile1, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir2, "Taskfile.yml"), taskfile2, 0o600); err != nil {
		t.Fatal(err)
	}

	r1, err := roots.Load(t.Context(), dir1)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := roots.Load(t.Context(), dir2)
	if err != nil {
		t.Fatal(err)
	}

	plan := BuildPlan(snapshotFromRoots(r1, r2), testLogger())

	if _, ok := plan.Tools["dup_hello"]; ok {
		t.Fatalf("expected colliding tool dup_hello to be excluded, got %v", toolNames(plan.Tools))
	}
	if _, ok := plan.Handlers["dup_hello"]; ok {
		t.Fatal("expected colliding handler dup_hello to be excluded")
	}

	want := []string{"dup_backend", "dup_frontend"}
	if got := toolNames(plan.Tools); !slices.Equal(got, want) {
		t.Fatalf("toolNames = %v, want %v", got, want)
	}
}

func TestBuildPlan_ExcludesCollidingToolNamesWithinRoot(t *testing.T) {
	root := tempRoot(t, []byte("version: '3'\ntasks:\n  build:dev:\n    desc: Build namespaced\n    cmds:\n      - echo namespaced\n  build_dev:\n    desc: Build underscored\n    cmds:\n      - echo underscored\n  lint:\n    desc: Lint\n    cmds:\n      - echo lint\n"))

	plan := BuildPlan(snapshotFromRoots(root), testLogger())
	if _, ok := plan.Tools["build_dev"]; ok {
		t.Fatalf("expected colliding tool build_dev to be excluded, got %v", toolNames(plan.Tools))
	}
	if _, ok := plan.Handlers["build_dev"]; ok {
		t.Fatal("expected colliding handler build_dev to be excluded")
	}
	if got := toolNames(plan.Tools); !slices.Equal(got, []string{"lint"}) {
		t.Fatalf("toolNames = %v, want [lint]", got)
	}
}

func TestBuildPlan_NoTasks(t *testing.T) {
	root := tempRoot(t, []byte("version: '3'\ntasks:\n  helper:\n    internal: true\n    cmds:\n      - echo hidden\n"))

	plan := BuildPlan(snapshotFromRoots(root), testLogger())
	if len(plan.Tools) != 0 {
		t.Fatalf("expected no tools, got %v", toolNames(plan.Tools))
	}
	if len(plan.Handlers) != 0 {
		t.Fatalf("expected no handlers, got %d", len(plan.Handlers))
	}
}

func TestBuildPlan_FromSnapshot(t *testing.T) {
	root := loadRootFromFixture(t, "basic")

	snap := StateSnapshot{
		Generation: 42,
		Roots: map[string]RootSnapshot{
			"file:///test": {Workdir: root.Workdir, Taskfile: root.Taskfile},
		},
	}

	plan := BuildPlan(snap, testLogger())
	if _, ok := plan.Tools["greet"]; !ok {
		t.Fatalf("expected tool greet, got %v", toolNames(plan.Tools))
	}
	if _, ok := plan.Handlers["greet"]; !ok {
		t.Fatal("expected handler for greet")
	}
}

// tempRoot writes the given Taskfile content to a temp directory and
// returns a loaded *roots.Root pointing at it.
func tempRoot(t *testing.T, taskfileContent []byte) *roots.Root {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), taskfileContent, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := roots.Load(t.Context(), dir)
	if err != nil {
		t.Fatalf("roots.Load: %v", err)
	}
	return root
}

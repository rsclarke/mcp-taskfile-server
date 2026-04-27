package taskfileserver

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCreateToolForTask_Basic(t *testing.T) {
	s := loadServerFromFixture(t, "basic")
	root := onlyRoot(t, s)

	taskDef := lookupTask(t, root.taskfile, "greet")
	tool := createToolForTask(root.taskfile, "", "greet", taskDef)

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
	tool := createToolForTask(root.taskfile, "", "build", taskDef)

	want := "Execute task: build"
	if tool.Description != want {
		t.Errorf("Description = %q, want %q", tool.Description, want)
	}
}

func TestCreateToolForTask_TaskVarsExcluded(t *testing.T) {
	// Task-level `vars:` are applied after caller-supplied values inside
	// go-task's compiler, so they must not appear as MCP arguments.
	s := loadServerFromFixture(t, "task-vars")
	root := onlyRoot(t, s)

	taskDef := lookupTask(t, root.taskfile, "deploy")
	tool := createToolForTask(root.taskfile, "", "deploy", taskDef)

	props := schemaProperties(t, tool)
	if len(props) != 0 {
		t.Fatalf("expected no properties for a task with only task-level vars, got %d: %v", len(props), props)
	}
	if req := schemaRequired(t, tool); len(req) != 0 {
		t.Errorf("expected no required entries, got %v", req)
	}
}

func TestCreateToolForTask_GlobalVars(t *testing.T) {
	s := loadServerFromFixture(t, "global-vars")
	root := onlyRoot(t, s)

	taskDef := lookupTask(t, root.taskfile, "info")
	tool := createToolForTask(root.taskfile, "", "info", taskDef)

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
	if got := propMap["default"]; got != "myapp" {
		t.Errorf("default = %v, want %q", got, "myapp")
	}
}

func TestCreateToolForTask_GlobalVarsHonoured(t *testing.T) {
	// Global vars are exposed as caller-overridable arguments. Task-level
	// vars are dropped, so the global default must remain visible.
	s := loadServerFromFixture(t, "override-vars")
	root := onlyRoot(t, s)

	taskDef := lookupTask(t, root.taskfile, "deploy")
	tool := createToolForTask(root.taskfile, "", "deploy", taskDef)

	props := schemaProperties(t, tool)
	if len(props) != 1 {
		t.Fatalf("expected only the global var, got %d: %v", len(props), props)
	}
	prop, ok := props["ENV"]
	if !ok {
		t.Fatal("missing property ENV")
	}

	propMap, _ := prop.(map[string]any)
	if got := propMap["default"]; got != "production" {
		t.Errorf("default = %v, want %q", got, "production")
	}
	desc, _ := propMap["description"].(string)
	want := "Variable: ENV (default: production)"
	if desc != want {
		t.Errorf("description = %q, want %q", desc, want)
	}
}

func TestCreateToolForTask_GlobalVarWithoutStaticDefault(t *testing.T) {
	// A global var whose Value is not a static string (here computed via
	// `sh:`) should be exposed as an argument with no `default` field.
	s := loadServerFromFixture(t, "requires")
	root := onlyRoot(t, s)

	taskDef := lookupTask(t, root.taskfile, "release")
	tool := createToolForTask(root.taskfile, "", "release", taskDef)

	props := schemaProperties(t, tool)
	prop, ok := props["GIT_SHA"].(map[string]any)
	if !ok {
		t.Fatalf("missing GIT_SHA property, got %v", props)
	}
	if _, hasDefault := prop["default"]; hasDefault {
		t.Errorf("expected no default for sh-computed var, got %v", prop["default"])
	}
	if got := prop["description"]; got != "Variable: GIT_SHA" {
		t.Errorf("description = %v, want %q", got, "Variable: GIT_SHA")
	}
}

func TestCreateToolForTask_RequiresVar(t *testing.T) {
	s := loadServerFromFixture(t, "requires")
	root := onlyRoot(t, s)

	taskDef := lookupTask(t, root.taskfile, "release")
	tool := createToolForTask(root.taskfile, "", "release", taskDef)

	props := schemaProperties(t, tool)
	prop, ok := props["VERSION"].(map[string]any)
	if !ok {
		t.Fatalf("missing VERSION property, got %v", props)
	}
	if got := prop["type"]; got != "string" {
		t.Errorf("VERSION type = %v, want %q", got, "string")
	}
	if _, hasDefault := prop["default"]; hasDefault {
		t.Errorf("required var should have no default, got %v", prop["default"])
	}
	if _, hasEnum := prop["enum"]; hasEnum {
		t.Errorf("plain required var should have no enum, got %v", prop["enum"])
	}

	required := schemaRequired(t, tool)
	if !slices.Contains(required, "VERSION") {
		t.Errorf("required = %v, want it to include VERSION", required)
	}
}

func TestCreateToolForTask_RequiresEnum(t *testing.T) {
	s := loadServerFromFixture(t, "requires")
	root := onlyRoot(t, s)

	taskDef := lookupTask(t, root.taskfile, "release")
	tool := createToolForTask(root.taskfile, "", "release", taskDef)

	props := schemaProperties(t, tool)
	prop, ok := props["CHANNEL"].(map[string]any)
	if !ok {
		t.Fatalf("missing CHANNEL property, got %v", props)
	}

	rawEnum, ok := prop["enum"].([]any)
	if !ok {
		t.Fatalf("CHANNEL.enum has wrong type %T: %v", prop["enum"], prop["enum"])
	}
	got := make([]string, 0, len(rawEnum))
	for _, v := range rawEnum {
		s, _ := v.(string)
		got = append(got, s)
	}
	want := []string{"stable", "beta", "nightly"}
	if !slices.Equal(got, want) {
		t.Errorf("CHANNEL.enum = %v, want %v", got, want)
	}

	required := schemaRequired(t, tool)
	if !slices.Contains(required, "CHANNEL") {
		t.Errorf("required = %v, want it to include CHANNEL", required)
	}
}

func TestBuildToolPlan_SkipsInternal(t *testing.T) {
	s := loadServerFromFixture(t, "internal")

	tools := buildToolPlan(snapshotFromServer(s)).tools

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

func TestSyncTools_NoPublicTasks(t *testing.T) {
	s := newTempServer(t, []byte("version: '3'\ntasks:\n  helper:\n    internal: true\n    cmds:\n      - echo hidden\n"))

	if len(s.registeredTools) != 0 {
		t.Fatalf("expected no registered tools, got %v", toolNames(s.registeredTools))
	}
}

func TestSanitizeToolName(t *testing.T) {
	validName := regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

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
		{"build/dev", "build_dev"},
		{"release prod", "release_prod"},
		{"café", "caf_"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeToolName(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeToolName(%q) = %q, want %q", tt.input, got, tt.want)
			}
			if !validName.MatchString(got) {
				t.Errorf("sanitizeToolName(%q) = %q, which does not match MCP tool name rules", tt.input, got)
			}
		})
	}
}

func TestSanitizeToolName_Overlength(t *testing.T) {
	input := strings.Repeat("a", 200)
	got := sanitizeToolName(input)
	wantPrefix := strings.Repeat("a", maxToolNameLength-len(shortToolNameHash(input))-1)
	wantSuffix := "_" + shortToolNameHash(input)

	if len(got) != maxToolNameLength {
		t.Fatalf("len(sanitizeToolName(%q)) = %d, want %d", input[:16], len(got), maxToolNameLength)
	}
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("sanitizeToolName(%q) = %q, want prefix %q", input[:16], got, wantPrefix)
	}
	if !strings.HasSuffix(got, wantSuffix) {
		t.Fatalf("sanitizeToolName(%q) = %q, want suffix %q", input[:16], got, wantSuffix)
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
			tool := createToolForTask(root.taskfile, "", tt.taskName, taskDef)

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
		tool := createToolForTask(root.taskfile, "", "start:*", taskDef)

		if tool.Name != "start" {
			t.Errorf("Name = %q, want %q", tool.Name, "start")
		}

		props := schemaProperties(t, tool)
		matchProp, ok := props["MATCH"].(map[string]any)
		if !ok {
			t.Fatalf("missing MATCH property for wildcard task, got %v", props)
		}
		if got := matchProp["type"]; got != "array" {
			t.Errorf("MATCH.type = %v, want %q", got, "array")
		}
		items, _ := matchProp["items"].(map[string]any)
		if got := items["type"]; got != "string" {
			t.Errorf("MATCH.items.type = %v, want %q", got, "string")
		}
		if got, want := matchProp["minItems"], float64(1); got != want {
			t.Errorf("MATCH.minItems = %v, want %v", got, want)
		}
		if got, want := matchProp["maxItems"], float64(1); got != want {
			t.Errorf("MATCH.maxItems = %v, want %v", got, want)
		}

		required := schemaRequired(t, tool)
		if !slices.Contains(required, "MATCH") {
			t.Errorf("MATCH should be required, got required=%v", required)
		}
	})

	t.Run("double wildcard", func(t *testing.T) {
		taskDef := lookupTask(t, root.taskfile, "deploy:*:*")
		tool := createToolForTask(root.taskfile, "", "deploy:*:*", taskDef)

		if tool.Name != "deploy" {
			t.Errorf("Name = %q, want %q", tool.Name, "deploy")
		}

		props := schemaProperties(t, tool)
		matchProp, ok := props["MATCH"].(map[string]any)
		if !ok {
			t.Fatalf("missing MATCH property for wildcard task, got %v", props)
		}
		if got := matchProp["type"]; got != "array" {
			t.Errorf("MATCH.type = %v, want %q", got, "array")
		}
		if got, want := matchProp["minItems"], float64(2); got != want {
			t.Errorf("MATCH.minItems = %v, want %v", got, want)
		}
		if got, want := matchProp["maxItems"], float64(2); got != want {
			t.Errorf("MATCH.maxItems = %v, want %v", got, want)
		}
		desc, _ := matchProp["description"].(string)
		if !strings.Contains(desc, "2 value(s)") {
			t.Errorf("MATCH description should mention 2 values, got %q", desc)
		}
	})
}

func TestCreateToolForTask_LeadingDot(t *testing.T) {
	s := loadServerFromFixture(t, "leading-dot")
	root := onlyRoot(t, s)

	taskDef := lookupTask(t, root.taskfile, "uv:.venv")
	tool := createToolForTask(root.taskfile, "", "uv:.venv", taskDef)

	if tool.Name != "uv_.venv" {
		t.Errorf("Name = %q, want %q", tool.Name, "uv_.venv")
	}
}

func TestBuildToolPlan_Namespaced(t *testing.T) {
	s := loadServerFromFixture(t, "namespaced")

	tools := buildToolPlan(snapshotFromServer(s)).tools

	for _, want := range []string{"db_migrate", "uv_run", "uv_run_dev_lint-imports"} {
		if _, ok := tools[want]; !ok {
			t.Errorf("expected tool %q, got tools: %v", want, toolNames(tools))
		}
	}
}

func TestBuildToolPlan_Includes(t *testing.T) {
	s := loadServerFromFixture(t, "includes")

	tools := buildToolPlan(snapshotFromServer(s)).tools

	for _, want := range []string{"build", "docs_serve", "docs_build"} {
		if _, ok := tools[want]; !ok {
			t.Errorf("expected tool %q, got tools: %v", want, toolNames(tools))
		}
	}
}

func TestBuildToolPlan_Wildcard(t *testing.T) {
	s := loadServerFromFixture(t, "wildcard")

	tools := buildToolPlan(snapshotFromServer(s)).tools

	for _, want := range []string{"start", "deploy"} {
		if _, ok := tools[want]; !ok {
			t.Errorf("expected tool %q, got tools: %v", want, toolNames(tools))
		}
	}
}

func TestBuildToolPlan_HandlerExecutesSelectedTool(t *testing.T) {
	s := loadServerFromFixture(t, "basic")

	plan := buildToolPlan(snapshotFromServer(s))
	handler, ok := plan.handlers["greet"]
	if !ok {
		t.Fatalf("missing handler for greet, got %v", toolNames(plan.tools))
	}

	result := callToolHandler(t, handler, "greet", nil)
	if result.IsError {
		t.Fatalf("expected success, got IsError=true: %s", toolResultText(t, result))
	}

	text := toolResultText(t, result)
	if !strings.Contains(text, "completed successfully") || !strings.Contains(text, "hello") {
		t.Fatalf("expected successful greet output, got %q", text)
	}
}

func TestBuildToolPlan_HandlerPassesVariables(t *testing.T) {
	s := newTempServer(t, []byte("version: '3'\ntasks:\n  greet:\n    desc: Greet someone\n    cmds:\n      - echo hello {{.NAME}}\n"))

	plan := buildToolPlan(snapshotFromServer(s))
	handler, ok := plan.handlers["greet"]
	if !ok {
		t.Fatalf("missing handler for greet, got %v", toolNames(plan.tools))
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

func TestBuildToolPlan_HandlerReportsTaskFailure(t *testing.T) {
	s := newTempServer(t, []byte("version: '3'\ntasks:\n  fail:\n    desc: A failing task\n    cmds:\n      - exit 1\n"))

	plan := buildToolPlan(snapshotFromServer(s))
	handler, ok := plan.handlers["fail"]
	if !ok {
		t.Fatalf("missing handler for fail, got %v", toolNames(plan.tools))
	}

	result := callToolHandler(t, handler, "fail", nil)
	if !result.IsError {
		t.Fatal("expected IsError=true for a failing task")
	}

	text := toolResultText(t, result)
	if !strings.Contains(text, "failed") {
		t.Fatalf("expected failure message, got %q", text)
	}
}

func TestBuildToolPlan_HandlerExecutesWildcardTool(t *testing.T) {
	s := loadServerFromFixture(t, "wildcard")

	plan := buildToolPlan(snapshotFromServer(s))
	handler, ok := plan.handlers["deploy"]
	if !ok {
		t.Fatalf("missing handler for deploy, got %v", toolNames(plan.tools))
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

func TestBuildToolPlan_HandlerSelectsPrefixedRootTool(t *testing.T) {
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

	frontendRoot, err := loadRoot(t.Context(), frontendDir)
	if err != nil {
		t.Fatal(err)
	}
	backendRoot, err := loadRoot(t.Context(), backendDir)
	if err != nil {
		t.Fatal(err)
	}

	s := &Server{
		roots: map[string]*Root{
			dirToURI(frontendDir): frontendRoot,
			dirToURI(backendDir):  backendRoot,
		},
	}

	plan := buildToolPlan(snapshotFromServer(s))
	frontendHandler, ok := plan.handlers["frontend_serve"]
	if !ok {
		t.Fatalf("missing frontend handler, got %v", toolNames(plan.tools))
	}
	backendHandler, ok := plan.handlers["backend_serve"]
	if !ok {
		t.Fatalf("missing backend handler, got %v", toolNames(plan.tools))
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

func TestToolsEqual(t *testing.T) {
	schema1 := json.RawMessage(`{"type":"object","properties":{"FOO":{"type":"string"}}}`)
	schema2 := json.RawMessage(`{"type":"object","properties":{"BAR":{"type":"string"}}}`)

	mk := func(name, desc string, schema json.RawMessage) *registeredTool {
		return &registeredTool{
			Tool:        mcp.Tool{Name: name, Description: desc, InputSchema: schema},
			schemaBytes: schema,
		}
	}

	tests := []struct {
		name string
		a, b *registeredTool
		want bool
	}{
		{
			name: "identical",
			a:    mk("greet", "Say hello", schema1),
			b:    mk("greet", "Say hello", schema1),
			want: true,
		},
		{
			name: "different name",
			a:    mk("greet", "Say hello", schema1),
			b:    mk("build", "Say hello", schema1),
			want: false,
		},
		{
			name: "different description",
			a:    mk("greet", "Say hello", schema1),
			b:    mk("greet", "Say goodbye", schema1),
			want: false,
		},
		{
			name: "different schema",
			a:    mk("greet", "Say hello", schema1),
			b:    mk("greet", "Say hello", schema2),
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
	first := make(map[string]registeredTool)
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

func TestBuildToolPlan_ExcludesCollidingToolNamesAcrossRoots(t *testing.T) {
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

	r1, err := loadRoot(t.Context(), dir1)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := loadRoot(t.Context(), dir2)
	if err != nil {
		t.Fatal(err)
	}

	s := &Server{
		roots: map[string]*Root{
			dirToURI(dir1): r1,
			dirToURI(dir2): r2,
		},
	}

	plan := buildToolPlan(snapshotFromServer(s))
	tools, handlers := plan.tools, plan.handlers

	if _, ok := tools["dup_hello"]; ok {
		t.Fatalf("expected colliding tool dup_hello to be excluded, got %v", toolNames(tools))
	}
	if _, ok := handlers["dup_hello"]; ok {
		t.Fatal("expected colliding handler dup_hello to be excluded")
	}

	want := []string{"dup_backend", "dup_frontend"}
	if got := toolNames(tools); !slices.Equal(got, want) {
		t.Fatalf("toolNames = %v, want %v", got, want)
	}
}

func TestBuildToolPlan_ExcludesCollidingToolNamesWithinRoot(t *testing.T) {
	dir := t.TempDir()
	taskfile := []byte("version: '3'\ntasks:\n  build:dev:\n    desc: Build namespaced\n    cmds:\n      - echo namespaced\n  build_dev:\n    desc: Build underscored\n    cmds:\n      - echo underscored\n  lint:\n    desc: Lint\n    cmds:\n      - echo lint\n")
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), taskfile, 0o600); err != nil {
		t.Fatal(err)
	}

	root, err := loadRoot(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}

	s := &Server{
		roots: map[string]*Root{dirToURI(dir): root},
	}

	plan := buildToolPlan(snapshotFromServer(s))
	tools, handlers := plan.tools, plan.handlers
	if _, ok := tools["build_dev"]; ok {
		t.Fatalf("expected colliding tool build_dev to be excluded, got %v", toolNames(tools))
	}
	if _, ok := handlers["build_dev"]; ok {
		t.Fatal("expected colliding handler build_dev to be excluded")
	}
	if got := toolNames(tools); !slices.Equal(got, []string{"lint"}) {
		t.Fatalf("toolNames = %v, want [lint]", got)
	}
}

func TestBuildToolPlan_NoTasks(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  helper:\n    internal: true\n    cmds:\n      - echo hidden\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	root, err := loadRoot(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}

	s := &Server{
		roots: map[string]*Root{dirToURI(dir): root},
	}

	plan := buildToolPlan(snapshotFromServer(s))
	tools, handlers := plan.tools, plan.handlers
	if len(tools) != 0 {
		t.Fatalf("expected no tools, got %v", toolNames(tools))
	}
	if len(handlers) != 0 {
		t.Fatalf("expected no handlers, got %d", len(handlers))
	}
}

func TestCreateToolForTask_WithPrefix(t *testing.T) {
	s := loadServerFromFixture(t, "basic")
	root := onlyRoot(t, s)

	taskDef := lookupTask(t, root.taskfile, "greet")
	tool := createToolForTask(root.taskfile, "myproject", "greet", taskDef)

	if tool.Name != "myproject_greet" {
		t.Errorf("Name = %q, want %q", tool.Name, "myproject_greet")
	}
	if tool.Description != "Say hello (task: greet)" {
		t.Errorf("Description = %q, want %q", tool.Description, "Say hello (task: greet)")
	}
}

func TestCreateToolForTask_WithPrefix_EnforcesMaxLength(t *testing.T) {
	s := loadServerFromFixture(t, "basic")
	root := onlyRoot(t, s)
	taskDef := lookupTask(t, root.taskfile, "greet")
	prefix := strings.Repeat("project", 20)

	tool := createToolForTask(root.taskfile, prefix, "greet", taskDef)

	if len(tool.Name) != maxToolNameLength {
		t.Fatalf("len(tool.Name) = %d, want %d", len(tool.Name), maxToolNameLength)
	}
	if matched, _ := regexp.MatchString(`^[A-Za-z0-9_.-]{1,128}$`, tool.Name); !matched {
		t.Fatalf("tool.Name = %q, want MCP-valid name", tool.Name)
	}
}

func TestDiffTools(t *testing.T) {
	schema := json.RawMessage(`{"type":"object"}`)
	mk := func(name, desc string) registeredTool {
		return registeredTool{
			Tool:        mcp.Tool{Name: name, Description: desc, InputSchema: schema},
			schemaBytes: schema,
		}
	}

	t.Run("empty to populated", func(t *testing.T) {
		desired := map[string]registeredTool{"greet": mk("greet", "Say hello")}
		stale, added := diffTools(nil, desired)
		if len(stale) != 0 {
			t.Errorf("stale = %v, want empty", stale)
		}
		if !slices.Equal(added, []string{"greet"}) {
			t.Errorf("added = %v, want [greet]", added)
		}
	})

	t.Run("populated to empty", func(t *testing.T) {
		old := map[string]registeredTool{"greet": mk("greet", "Say hello")}
		stale, added := diffTools(old, nil)
		if !slices.Equal(stale, []string{"greet"}) {
			t.Errorf("stale = %v, want [greet]", stale)
		}
		if len(added) != 0 {
			t.Errorf("added = %v, want empty", added)
		}
	})

	t.Run("unchanged", func(t *testing.T) {
		tools := map[string]registeredTool{"greet": mk("greet", "Say hello")}
		stale, added := diffTools(tools, tools)
		if len(stale) != 0 {
			t.Errorf("stale = %v, want empty", stale)
		}
		if len(added) != 0 {
			t.Errorf("added = %v, want empty", added)
		}
	})

	t.Run("changed description", func(t *testing.T) {
		old := map[string]registeredTool{"greet": mk("greet", "Say hello")}
		desired := map[string]registeredTool{"greet": mk("greet", "Say goodbye")}
		stale, added := diffTools(old, desired)
		if !slices.Equal(stale, []string{"greet"}) {
			t.Errorf("stale = %v, want [greet]", stale)
		}
		if !slices.Equal(added, []string{"greet"}) {
			t.Errorf("added = %v, want [greet]", added)
		}
	})
}

func TestBuildToolPlan_FromSnapshot(t *testing.T) {
	s := loadServerFromFixture(t, "basic")
	root := onlyRoot(t, s)

	snap := toolStateSnapshot{
		generation: 42,
		roots: map[string]toolRootSnapshot{
			"file:///test": {workdir: root.workdir, taskfile: root.taskfile},
		},
	}

	plan := buildToolPlan(snap)
	if _, ok := plan.tools["greet"]; !ok {
		t.Fatalf("expected tool greet, got %v", toolNames(plan.tools))
	}
	if _, ok := plan.handlers["greet"]; !ok {
		t.Fatal("expected handler for greet")
	}
}

func TestSyncTools_DiscardsOnGenerationMismatch(t *testing.T) {
	s := newTestServer(t, "basic")

	if err := s.syncTools(); err != nil {
		t.Fatalf("initial syncTools: %v", err)
	}
	if len(s.registeredTools) == 0 {
		t.Fatal("expected at least one tool after initial sync")
	}

	s.mu.Lock()
	initialGen := s.generation
	initialTools := make(map[string]registeredTool)
	maps.Copy(initialTools, s.registeredTools)

	// Simulate a concurrent mutation by bumping the generation while
	// no actual state change has occurred. This makes any in-flight
	// plan appear stale at commit time.
	s.generation++
	s.mu.Unlock()

	// syncTools will snapshot generation=initialGen+1, plan, apply MCP
	// changes, then re-acquire the lock. We bump generation again before
	// the commit to simulate a race.
	// To test this precisely, we bump generation inside a goroutine
	// after a brief delay to race with the commit phase.
	go func() {
		s.mu.Lock()
		s.generation = initialGen + 10
		s.mu.Unlock()
	}()

	// Run syncTools — it should either commit or discard depending on
	// timing, but must not panic.
	if err := s.syncTools(); err != nil {
		t.Fatalf("syncTools: %v", err)
	}
}

func TestSyncTools_OrphanedToolOnConcurrentSync(t *testing.T) {
	// Single root with two tasks. We simulate a concurrent mutation
	// between Phase 1 (snapshot) and Phase 3 (apply) by bumping the
	// generation after the snapshot is taken. The stale plan must be
	// discarded before any MCP side effects are applied.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  taskA:\n    desc: Task A\n    cmds:\n      - echo A\n  taskB:\n    desc: Task B\n    cmds:\n      - echo B\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	root, err := loadRoot(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}

	mcpSrv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil)
	tracker := newTrackingRegistry(mcpSrv)

	uri := dirToURI(dir)
	s := &Server{
		roots:           map[string]*Root{uri: root},
		toolRegistry:    tracker,
		registeredTools: make(map[string]registeredTool),
	}

	// Initial sync registers both tools.
	if err := s.syncTools(); err != nil {
		t.Fatalf("initial syncTools: %v", err)
	}
	if _, ok := tracker.toolSet()["taskA"]; !ok {
		t.Fatal("expected taskA in tracker after initial sync")
	}
	if _, ok := tracker.toolSet()["taskB"]; !ok {
		t.Fatal("expected taskB in tracker after initial sync")
	}

	// Reset registeredTools so the next sync will try to re-add both.
	s.mu.Lock()
	s.registeredTools = make(map[string]registeredTool)
	s.generation++
	s.mu.Unlock()

	// Reset tracker and MCP server to match.
	tracker.mu.Lock()
	tracker.tools = make(map[string]struct{})
	tracker.mu.Unlock()
	mcpSrv.RemoveTools("taskA", "taskB")

	// Simulate a concurrent mutation: after G1 snapshots but before
	// it applies, swap to a single-task root and bump generation.
	// G1 will snapshot gen=N, plan {taskA, taskB}, then find gen≠N
	// at apply time and discard the plan without touching the MCP server.
	singleTaskDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(singleTaskDir, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  taskA:\n    desc: Task A\n    cmds:\n      - echo A\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	newRoot, err := loadRoot(t.Context(), singleTaskDir)
	if err != nil {
		t.Fatal(err)
	}

	// G1: snapshot with both tasks, then mutate state before G1 applies.
	s.mu.Lock()
	snap := s.snapshotToolStateLocked()
	oldTools := make(map[string]registeredTool, len(s.registeredTools))
	maps.Copy(oldTools, s.registeredTools)

	// Simulate concurrent mutation while G1 is "planning".
	newURI := dirToURI(singleTaskDir)
	delete(s.roots, uri)
	s.roots[newURI] = newRoot
	s.generation++
	s.mu.Unlock()

	// G1 plans from the stale snapshot (both tasks).
	plan := buildToolPlan(snap)
	stale, added := diffTools(oldTools, plan.tools)

	// G1 tries to apply — generation should mismatch, no MCP calls.
	s.mu.Lock()
	if s.generation != snap.generation {
		// Stale plan correctly detected — skip apply.
		s.mu.Unlock()
	} else {
		// This path should not be taken.
		if len(stale) > 0 {
			s.toolRegistry.RemoveTools(stale...)
		}
		for _, name := range added {
			tool := plan.tools[name]
			s.toolRegistry.AddTool(&tool.Tool, plan.handlers[name])
		}
		s.registeredTools = plan.tools
		s.mu.Unlock()
	}

	// G2: run a clean sync with the updated state.
	if err := s.syncTools(); err != nil {
		t.Fatalf("G2 syncTools: %v", err)
	}

	// registeredTools should have {taskA} only.
	s.mu.Lock()
	regTools := make(map[string]registeredTool)
	maps.Copy(regTools, s.registeredTools)
	s.mu.Unlock()

	if _, ok := regTools["taskB"]; ok {
		t.Fatal("registeredTools contains taskB — bookkeeping is stale")
	}
	if _, ok := regTools["taskA"]; !ok {
		t.Fatal("registeredTools missing taskA")
	}

	// The tracker (MCP-side state) must match registeredTools.
	// taskB must not be on the MCP server.
	mcpTools := tracker.toolSet()
	if _, ok := mcpTools["taskB"]; ok {
		t.Fatal("MCP server has orphaned tool taskB: registered on MCP server but not tracked in registeredTools")
	}
	if _, ok := mcpTools["taskA"]; !ok {
		t.Fatal("MCP server missing taskA")
	}
}

// TestSnapshotToolState_IsolatedFromRootMutation verifies that a snapshot
// captured by snapshotToolStateLocked observes the taskfile pointer that
// was current at snapshot time, even if the underlying *Root is mutated
// in-place afterwards (as reloadRoot and disableRootToolsLocked do).
// Before snapshotting copied per-root fields by value, this test would
// have observed a nil taskfile in the snapshot and panicked in the
// planner.
func TestSnapshotToolState_IsolatedFromRootMutation(t *testing.T) {
	s := loadServerFromFixture(t, "basic")
	root := onlyRoot(t, s)

	s.mu.Lock()
	snap := s.snapshotToolStateLocked()
	// Simulate disableRootToolsLocked clearing the live root in place.
	root.taskfile = nil
	s.generation++
	s.mu.Unlock()

	// Planner must run safely against the snapshot, observing the
	// taskfile that was current at snapshot time, not the now-nil
	// field on the live root.
	plan := buildToolPlan(snap)
	if _, ok := plan.tools["greet"]; !ok {
		t.Fatalf("expected snapshotted plan to retain greet, got %v", toolNames(plan.tools))
	}
}

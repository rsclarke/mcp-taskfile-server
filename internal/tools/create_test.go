package tools

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestCreateToolForTask_Basic(t *testing.T) {
	root := loadRootFromFixture(t, "basic")

	taskDef := lookupTask(t, root.Taskfile, "greet")
	tool := CreateToolForTask(root.Taskfile, "", "greet", taskDef)

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
	root := loadRootFromFixture(t, "no-desc")

	taskDef := lookupTask(t, root.Taskfile, "build")
	tool := CreateToolForTask(root.Taskfile, "", "build", taskDef)

	want := "Execute task: build"
	if tool.Description != want {
		t.Errorf("Description = %q, want %q", tool.Description, want)
	}
}

func TestCreateToolForTask_TaskVarsExcluded(t *testing.T) {
	// Task-level `vars:` are applied after caller-supplied values inside
	// go-task's compiler, so they must not appear as MCP arguments.
	root := loadRootFromFixture(t, "task-vars")

	taskDef := lookupTask(t, root.Taskfile, "deploy")
	tool := CreateToolForTask(root.Taskfile, "", "deploy", taskDef)

	props := schemaProperties(t, tool)
	if len(props) != 0 {
		t.Fatalf("expected no properties for a task with only task-level vars, got %d: %v", len(props), props)
	}
	if req := schemaRequired(t, tool); len(req) != 0 {
		t.Errorf("expected no required entries, got %v", req)
	}
}

func TestCreateToolForTask_GlobalVars(t *testing.T) {
	root := loadRootFromFixture(t, "global-vars")

	taskDef := lookupTask(t, root.Taskfile, "info")
	tool := CreateToolForTask(root.Taskfile, "", "info", taskDef)

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
	root := loadRootFromFixture(t, "override-vars")

	taskDef := lookupTask(t, root.Taskfile, "deploy")
	tool := CreateToolForTask(root.Taskfile, "", "deploy", taskDef)

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
	root := loadRootFromFixture(t, "requires")

	taskDef := lookupTask(t, root.Taskfile, "release")
	tool := CreateToolForTask(root.Taskfile, "", "release", taskDef)

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
	root := loadRootFromFixture(t, "requires")

	taskDef := lookupTask(t, root.Taskfile, "release")
	tool := CreateToolForTask(root.Taskfile, "", "release", taskDef)

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
	root := loadRootFromFixture(t, "requires")

	taskDef := lookupTask(t, root.Taskfile, "release")
	tool := CreateToolForTask(root.Taskfile, "", "release", taskDef)

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

func TestCreateToolForTask_Namespaced(t *testing.T) {
	root := loadRootFromFixture(t, "namespaced")

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
			taskDef := lookupTask(t, root.Taskfile, tt.taskName)
			tool := CreateToolForTask(root.Taskfile, "", tt.taskName, taskDef)

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
	root := loadRootFromFixture(t, "wildcard")

	t.Run("single wildcard", func(t *testing.T) {
		taskDef := lookupTask(t, root.Taskfile, "start:*")
		tool := CreateToolForTask(root.Taskfile, "", "start:*", taskDef)

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
		taskDef := lookupTask(t, root.Taskfile, "deploy:*:*")
		tool := CreateToolForTask(root.Taskfile, "", "deploy:*:*", taskDef)

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
	root := loadRootFromFixture(t, "leading-dot")

	taskDef := lookupTask(t, root.Taskfile, "uv:.venv")
	tool := CreateToolForTask(root.Taskfile, "", "uv:.venv", taskDef)

	if tool.Name != "uv_.venv" {
		t.Errorf("Name = %q, want %q", tool.Name, "uv_.venv")
	}
}

func TestCreateToolForTask_WithPrefix(t *testing.T) {
	root := loadRootFromFixture(t, "basic")

	taskDef := lookupTask(t, root.Taskfile, "greet")
	tool := CreateToolForTask(root.Taskfile, "myproject", "greet", taskDef)

	if tool.Name != "myproject_greet" {
		t.Errorf("Name = %q, want %q", tool.Name, "myproject_greet")
	}
	if tool.Description != "Say hello (task: greet)" {
		t.Errorf("Description = %q, want %q", tool.Description, "Say hello (task: greet)")
	}
}

func TestCreateToolForTask_WithPrefix_EnforcesMaxLength(t *testing.T) {
	root := loadRootFromFixture(t, "basic")
	taskDef := lookupTask(t, root.Taskfile, "greet")
	prefix := strings.Repeat("project", 20)

	tool := CreateToolForTask(root.Taskfile, prefix, "greet", taskDef)

	if len(tool.Name) != maxToolNameLength {
		t.Fatalf("len(tool.Name) = %d, want %d", len(tool.Name), maxToolNameLength)
	}
	if matched, _ := regexp.MatchString(`^[A-Za-z0-9_.-]{1,128}$`, tool.Name); !matched {
		t.Fatalf("tool.Name = %q, want MCP-valid name", tool.Name)
	}
}

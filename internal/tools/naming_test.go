package tools

import (
	"regexp"
	"strings"
	"testing"
)

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
			got := SanitizeToolName(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeToolName(%q) = %q, want %q", tt.input, got, tt.want)
			}
			if !validName.MatchString(got) {
				t.Errorf("SanitizeToolName(%q) = %q, which does not match MCP tool name rules", tt.input, got)
			}
		})
	}
}

func TestSanitizeToolName_Overlength(t *testing.T) {
	input := strings.Repeat("a", 200)
	got := SanitizeToolName(input)
	wantPrefix := strings.Repeat("a", maxToolNameLength-len(shortToolNameHash(input))-1)
	wantSuffix := "_" + shortToolNameHash(input)

	if len(got) != maxToolNameLength {
		t.Fatalf("len(SanitizeToolName(%q)) = %d, want %d", input[:16], len(got), maxToolNameLength)
	}
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("SanitizeToolName(%q) = %q, want prefix %q", input[:16], got, wantPrefix)
	}
	if !strings.HasSuffix(got, wantSuffix) {
		t.Fatalf("SanitizeToolName(%q) = %q, want suffix %q", input[:16], got, wantSuffix)
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
			got := SanitizeRootPrefix(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeRootPrefix(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

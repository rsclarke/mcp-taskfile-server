package taskfileserver

import "testing"

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

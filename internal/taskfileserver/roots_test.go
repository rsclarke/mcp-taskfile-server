package taskfileserver

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/go-task/task/v3/taskfile"
)

// isTaskfile reports whether the given path's basename matches one of the
// supported Taskfile filenames from taskfile.DefaultTaskfiles.
func isTaskfile(path string) bool {
	return slices.Contains(taskfile.DefaultTaskfiles, filepath.Base(path))
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
	dir := filepath.Join(t.TempDir(), "path with #hash and ?query")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}

	uri := dirToURI(dir)
	if strings.Contains(uri, "#") || strings.Contains(uri, "?") {
		t.Fatalf("dirToURI(%q) returned unescaped URI %q", dir, uri)
	}

	roundTrip, err := fileURIToPath(uri)
	if err != nil {
		t.Fatalf("fileURIToPath(%q) failed: %v", uri, err)
	}
	if roundTrip != filepath.Clean(dir) {
		t.Errorf("uri round-trip = %q, want %q", roundTrip, filepath.Clean(dir))
	}
}

func TestFileURIToPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "path with #hash and ?query")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	want := filepath.Clean(dir)
	uri := dirToURI(dir)
	localhostURI := strings.Replace(uri, "file://", "file://localhost", 1)

	tests := []struct {
		name    string
		uri     string
		want    string
		wantErr string
	}{
		{name: "local file URI", uri: uri, want: want},
		{name: "localhost file URI", uri: localhostURI, want: want},
		{name: "non-file URI", uri: "https://example.com", wantErr: `unsupported URI scheme "https"`},
		{name: "fragment", uri: "file:///tmp/a#b", wantErr: "must not include query or fragment"},
		{name: "query", uri: "file:///tmp/a?b", wantErr: "must not include query or fragment"},
		{name: "unc", uri: "file://server/share", wantErr: "UNC file URI"},
	}
	if runtime.GOOS == "windows" {
		tests = append(tests, struct {
			name    string
			uri     string
			want    string
			wantErr string
		}{name: "windows drive URI", uri: "file:///C:/repo", want: filepath.Clean(`C:\repo`)})
	} else {
		tests = append(tests, struct {
			name    string
			uri     string
			want    string
			wantErr string
		}{name: "windows drive URI", uri: "file:///C:/repo", wantErr: "windows file URI"})
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := fileURIToPath(tt.uri)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("fileURIToPath(%q) = %q, want error containing %q", tt.uri, got, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("fileURIToPath(%q) error = %q, want substring %q", tt.uri, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("fileURIToPath(%q) failed: %v", tt.uri, err)
			}
			if got != tt.want {
				t.Fatalf("fileURIToPath(%q) = %q, want %q", tt.uri, got, tt.want)
			}
		})
	}
}

func TestCanonicalRootURI(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "path with #hash and ?query")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}

	wantURI := dirToURI(dir)
	wantDir := filepath.Clean(dir)
	aliasURI := strings.Replace(wantURI, "file://", "file://localhost", 1)

	tests := []struct {
		name string
		uri  string
	}{
		{name: "canonical file URI", uri: wantURI},
		{name: "localhost alias", uri: aliasURI},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotURI, gotDir, err := canonicalRootURI(tt.uri)
			if err != nil {
				t.Fatalf("canonicalRootURI(%q) failed: %v", tt.uri, err)
			}
			if gotURI != wantURI {
				t.Fatalf("canonicalRootURI(%q) URI = %q, want %q", tt.uri, gotURI, wantURI)
			}
			if gotDir != wantDir {
				t.Fatalf("canonicalRootURI(%q) dir = %q, want %q", tt.uri, gotDir, wantDir)
			}
		})
	}
}

func TestLoadRoot_DoesNotWalkParentDirectories(t *testing.T) {
	parent := t.TempDir()
	if err := os.WriteFile(filepath.Join(parent, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  parent:\n    cmds:\n      - echo parent\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	child := filepath.Join(parent, "child")
	if err := os.Mkdir(child, 0o750); err != nil {
		t.Fatal(err)
	}

	if _, err := loadRoot(t.Context(), child); err == nil {
		t.Fatal("expected loadRoot to fail when the root has no direct Taskfile")
	}
}

func TestTaskfileLocationToPath_FileURI(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "taskfile with #hash")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}

	got, ok, err := taskfileLocationToPath(dirToURI(dir))
	if err != nil {
		t.Fatalf("taskfileLocationToPath failed: %v", err)
	}
	if !ok {
		t.Fatal("expected file URI to be treated as local")
	}
	if got != filepath.Clean(dir) {
		t.Fatalf("taskfileLocationToPath = %q, want %q", got, filepath.Clean(dir))
	}
}

func TestTaskfileLocationToPath_IgnoresNonLocalURI(t *testing.T) {
	got, ok, err := taskfileLocationToPath("https://example.com/Taskfile.yml")
	if err != nil {
		t.Fatalf("taskfileLocationToPath returned error: %v", err)
	}
	if ok {
		t.Fatalf("expected non-file URI to be ignored, got %q", got)
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

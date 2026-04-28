package server

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rsclarke/mcp-taskfile-server/internal/roots"
	"github.com/rsclarke/mcp-taskfile-server/internal/watch"
)

// loadServerFromFixture creates a Server from a testdata fixture directory.
func loadServerFromFixture(t *testing.T, name string) *Server {
	t.Helper()

	_, filename, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(filename), "..", "..", "testdata", name)

	root, err := roots.Load(t.Context(), dir)
	if err != nil {
		t.Fatalf("failed to load root for fixture %q: %v", name, err)
	}

	uri := roots.DirToURI(dir)
	s := New()
	s.roots[uri] = root
	return s
}

// onlyRoot returns the single Root from a server, or fails the test.
func onlyRoot(t *testing.T, s *Server) *roots.Root {
	t.Helper()
	if len(s.roots) != 1 {
		t.Fatalf("expected 1 root, got %d", len(s.roots))
	}
	for _, root := range s.roots {
		return root
	}
	return nil
}

// onlyRootURI returns the single root URI from a server, or fails the test.
func onlyRootURI(t *testing.T, s *Server) string {
	t.Helper()
	if len(s.roots) != 1 {
		t.Fatalf("expected 1 root, got %d", len(s.roots))
	}
	for uri := range s.roots {
		return uri
	}
	return ""
}

// newTestServer creates a Server from a fixture with a real *mcp.Server attached.
func newTestServer(t *testing.T, fixture string) *Server {
	t.Helper()
	s := loadServerFromFixture(t, fixture)
	mcpSrv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil)
	s.toolRegistry = mcpSrv
	return s
}

// toolNames returns the sorted keys from a tool map for use in error messages.
func toolNames[V any](tools map[string]V) []string {
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// snapshotRoots returns a slice of canonical root URIs. It acquires
// s.mu so it is safe to call while watcher goroutines are reading
// s.roots concurrently.
func snapshotRoots(s *Server) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	uris := make([]string, 0, len(s.roots))
	for uri := range s.roots {
		uris = append(uris, uri)
	}
	return uris
}

// reconcileWatchersForTest drives watch.Manager.Reconcile against the
// server's currently loaded roots. Production code uses the diff-based
// s.watchers.Apply path driven by initializeRoots/replaceRoots; tests
// that populate s.roots directly use this full-set entry point instead.
func reconcileWatchersForTest(ctx context.Context, s *Server) {
	s.watchers.Reconcile(ctx, snapshotRoots(s))
}

// startTestWatchers spawns a per-root watcher goroutine for every loaded
// root. It mimics what the watch.Manager does on a normal startup, but
// without going through the manager so tests can observe the raw
// watch.Watch loop.
func startTestWatchers(ctx context.Context, s *Server) {
	for _, uri := range snapshotRoots(s) {
		go func(uri string) {
			_ = watch.Watch(ctx, s, s.log, uri)
		}(uri)
	}
}

// newTempServer creates a Server backed by a temp directory containing
// the given Taskfile content, with a real *mcp.Server and initial syncTools.
func newTempServer(t *testing.T, taskfileContent []byte) *Server {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), taskfileContent, 0o600); err != nil {
		t.Fatal(err)
	}
	return newServerForDir(t, dir)
}

// newServerForDir creates a Server backed by a given directory,
// with a real *mcp.Server and initial syncTools.
func newServerForDir(t *testing.T, dir string) *Server {
	t.Helper()
	root, err := roots.Load(t.Context(), dir)
	if err != nil {
		t.Fatalf("loadRoot: %v", err)
	}
	uri := roots.DirToURI(dir)
	mcpSrv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil)
	s := New()
	s.toolRegistry = mcpSrv
	s.roots[uri] = root
	if err := s.syncTools(); err != nil {
		t.Fatalf("initial syncTools: %v", err)
	}
	return s
}

// toolResultText concatenates the text from every TextContent block in a
// CallToolResult, separated by newlines. It is used by tests that want to
// substring-match across the structured status / stdout / stderr blocks
// without caring which block produced the match.
func toolResultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()

	if len(result.Content) == 0 {
		t.Fatal("expected at least 1 content item, got 0")
	}

	parts := make([]string, 0, len(result.Content))
	for i, c := range result.Content {
		text, ok := c.(*mcp.TextContent)
		if !ok {
			t.Fatalf("expected TextContent at index %d, got %T", i, c)
		}
		parts = append(parts, text.Text)
	}
	return strings.Join(parts, "\n")
}

// toolStreamText returns the concatenated text from TextContent blocks
// tagged with Meta["stream"] == stream (e.g. "stdout", "stderr"). Returns
// the empty string if no such block exists.
func toolStreamText(t *testing.T, result *mcp.CallToolResult, stream string) string {
	t.Helper()

	var parts []string
	for i, c := range result.Content {
		text, ok := c.(*mcp.TextContent)
		if !ok {
			t.Fatalf("expected TextContent at index %d, got %T", i, c)
		}
		if text.Meta == nil {
			continue
		}
		if got, _ := text.Meta["stream"].(string); got == stream {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "")
}

// toolStatusText returns the text from the first content block, which by
// convention carries the status summary line for a task invocation.
func toolStatusText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()

	if len(result.Content) == 0 {
		t.Fatal("expected at least 1 content item, got 0")
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected first content to be TextContent, got %T", result.Content[0])
	}
	return text.Text
}

// waitForTools waits until the server has at least minTools registered.
func waitForTools(t *testing.T, ts *Server, minTools int) {
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
func waitForToolCount(t *testing.T, ts *Server, count int) {
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

func testRoots(uris ...string) []*mcp.Root {
	roots := make([]*mcp.Root, 0, len(uris))
	for _, uri := range uris {
		roots = append(roots, &mcp.Root{URI: uri})
	}
	return roots
}

// noopRegistry satisfies toolRegistry for tests that only care about the
// server's in-memory tool bookkeeping, not MCP transport side effects.
type noopRegistry struct{}

func (noopRegistry) AddTool(_ *mcp.Tool, _ mcp.ToolHandler) {}

func (noopRegistry) RemoveTools(_ ...string) {}

// trackingRegistry wraps a toolRegistry and tracks the net set of tools
// currently registered, providing an observable view of MCP-side state.
type trackingRegistry struct {
	inner toolRegistry
	mu    sync.Mutex
	tools map[string]struct{}
}

func newTrackingRegistry(inner toolRegistry) *trackingRegistry {
	return &trackingRegistry{
		inner: inner,
		tools: make(map[string]struct{}),
	}
}

func (r *trackingRegistry) AddTool(tool *mcp.Tool, handler mcp.ToolHandler) {
	r.inner.AddTool(tool, handler)
	r.mu.Lock()
	r.tools[tool.Name] = struct{}{}
	r.mu.Unlock()
}

func (r *trackingRegistry) RemoveTools(names ...string) {
	r.inner.RemoveTools(names...)
	r.mu.Lock()
	for _, n := range names {
		delete(r.tools, n)
	}
	r.mu.Unlock()
}

func (r *trackingRegistry) toolSet() map[string]struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make(map[string]struct{}, len(r.tools))
	maps.Copy(result, r.tools)
	return result
}

// waitForRootCount waits until the server has exactly count loaded roots.
func waitForRootCount(t *testing.T, ts *Server, count int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			ts.mu.Lock()
			n := len(ts.roots)
			ts.mu.Unlock()
			t.Fatalf("timed out waiting for %d roots, have %d", count, n)
		case <-ticker.C:
			ts.mu.Lock()
			n := len(ts.roots)
			ts.mu.Unlock()
			if n == count {
				return
			}
		}
	}
}

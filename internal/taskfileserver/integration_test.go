package taskfileserver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rsclarke/mcp-taskfile-server/internal/roots"
	"github.com/rsclarke/mcp-taskfile-server/internal/tools"
)

func TestHandleInitialized_WithRoots(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  hello:\n    desc: Say hello\n    cmds:\n      - echo hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ts := New()
	ctx := t.Context()

	rootURI := roots.DirToURI(dir)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	client.AddRoots(&mcp.Root{URI: rootURI, Name: "test"})

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, &mcp.ServerOptions{
		InitializedHandler:      ts.HandleInitialized,
		RootsListChangedHandler: ts.HandleRootsChanged,
	})
	ts.toolRegistry = server
	ts.registeredTools = make(map[string]tools.RegisteredTool)

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

func TestHandleInitialized_CallToolExecutesSingleRootTool(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  hello:\n    desc: Say hello\n    cmds:\n      - echo hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ts := New()
	ctx := t.Context()

	rootURI := roots.DirToURI(dir)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	client.AddRoots(&mcp.Root{URI: rootURI, Name: "test"})

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, &mcp.ServerOptions{
		InitializedHandler:      ts.HandleInitialized,
		RootsListChangedHandler: ts.HandleRootsChanged,
	})
	ts.toolRegistry = server
	ts.registeredTools = make(map[string]tools.RegisteredTool)

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

	waitForRootCount(t, ts, 1)
	waitForToolCount(t, ts, 1)

	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "hello"})
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}
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

func TestHandleInitialized_DeduplicatesEquivalentRootURIs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  hello:\n    desc: Say hello\n    cmds:\n      - echo hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ts := New()
	ctx := t.Context()

	rootURI := roots.DirToURI(dir)
	aliasURI := strings.Replace(rootURI, "file://", "file://localhost", 1)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	client.AddRoots(
		&mcp.Root{URI: rootURI, Name: "canonical"},
		&mcp.Root{URI: aliasURI, Name: "alias"},
	)

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, &mcp.ServerOptions{
		InitializedHandler:      ts.HandleInitialized,
		RootsListChangedHandler: ts.HandleRootsChanged,
	})
	ts.toolRegistry = server
	ts.registeredTools = make(map[string]tools.RegisteredTool)

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

	waitForRootCount(t, ts, 1)
	waitForToolCount(t, ts, 1)

	ts.mu.Lock()
	defer ts.mu.Unlock()
	if len(ts.roots) != 1 {
		t.Fatalf("expected 1 canonical root, got %d", len(ts.roots))
	}
	if _, ok := ts.roots[rootURI]; !ok {
		t.Fatalf("expected canonical root %q", rootURI)
	}
	if _, ok := ts.roots[aliasURI]; ok {
		t.Fatalf("did not expect alias root %q to be stored separately", aliasURI)
	}
	if _, ok := ts.registeredTools["hello"]; !ok {
		t.Fatalf("expected unprefixed tool 'hello', got %v", toolNames(ts.registeredTools))
	}
	if len(ts.registeredTools) != 1 {
		t.Fatalf("expected exactly 1 tool after deduping aliases, got %d", len(ts.registeredTools))
	}
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

	ts := New()
	ctx := t.Context()

	uri1 := roots.DirToURI(dir1)
	uri2 := roots.DirToURI(dir2)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	client.AddRoots(&mcp.Root{URI: uri1, Name: "root1"})

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, &mcp.ServerOptions{
		InitializedHandler:      ts.HandleInitialized,
		RootsListChangedHandler: ts.HandleRootsChanged,
	})
	ts.toolRegistry = server
	ts.registeredTools = make(map[string]tools.RegisteredTool)

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

func TestHandleRootsChanged_EquivalentURIAliasKeepsSingleRoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  hello:\n    desc: Say hello\n    cmds:\n      - echo hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ts := New()
	ctx := t.Context()

	rootURI := roots.DirToURI(dir)
	aliasURI := strings.Replace(rootURI, "file://", "file://localhost", 1)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	client.AddRoots(&mcp.Root{URI: rootURI, Name: "canonical"})

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, &mcp.ServerOptions{
		InitializedHandler:      ts.HandleInitialized,
		RootsListChangedHandler: ts.HandleRootsChanged,
	})
	ts.toolRegistry = server
	ts.registeredTools = make(map[string]tools.RegisteredTool)

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

	assertCanonicalSingleRoot := func() {
		t.Helper()
		ts.mu.Lock()
		defer ts.mu.Unlock()
		if len(ts.roots) != 1 {
			t.Fatalf("expected 1 canonical root, got %d", len(ts.roots))
		}
		if _, ok := ts.roots[rootURI]; !ok {
			t.Fatalf("expected canonical root %q", rootURI)
		}
		if _, ok := ts.roots[aliasURI]; ok {
			t.Fatalf("did not expect alias root %q to be stored separately", aliasURI)
		}
		if _, ok := ts.registeredTools["hello"]; !ok {
			t.Fatalf("expected unprefixed tool 'hello', got %v", toolNames(ts.registeredTools))
		}
		if len(ts.registeredTools) != 1 {
			t.Fatalf("expected exactly 1 tool, got %d", len(ts.registeredTools))
		}
	}

	waitForRootCount(t, ts, 1)
	waitForToolCount(t, ts, 1)
	assertCanonicalSingleRoot()

	client.AddRoots(&mcp.Root{URI: aliasURI, Name: "alias"})

	waitForRootCount(t, ts, 1)
	waitForToolCount(t, ts, 1)
	assertCanonicalSingleRoot()

	client.RemoveRoots(rootURI)

	waitForRootCount(t, ts, 1)
	waitForToolCount(t, ts, 1)
	assertCanonicalSingleRoot()

	client.RemoveRoots(aliasURI)

	waitForRootCount(t, ts, 0)
	waitForToolCount(t, ts, 0)
}

func TestIsMethodNotFound(t *testing.T) {
	err := &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "not found"}
	if !isMethodNotFound(err) {
		t.Error("expected isMethodNotFound to return true for CodeMethodNotFound")
	}
	if isMethodNotFound(errors.New("some other error")) {
		t.Error("expected isMethodNotFound to return false for a non-wire error")
	}
}

func TestHandleInitialized_NoPublicTasks(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  helper:\n    internal: true\n    cmds:\n      - echo hidden\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ts := New()
	ctx := t.Context()
	uri := roots.DirToURI(dir)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	client.AddRoots(&mcp.Root{URI: uri, Name: "root"})

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, &mcp.ServerOptions{
		InitializedHandler:      ts.HandleInitialized,
		RootsListChangedHandler: ts.HandleRootsChanged,
	})
	ts.toolRegistry = server
	ts.registeredTools = make(map[string]tools.RegisteredTool)

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

	waitForRootCount(t, ts, 1)
	waitForToolCount(t, ts, 0)

	toolsRes, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	if len(toolsRes.Tools) != 0 {
		t.Fatalf("expected no tools, got %d", len(toolsRes.Tools))
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

	ts := New()
	ctx := t.Context()

	uri1 := roots.DirToURI(dir1)
	uri2 := roots.DirToURI(dir2)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	client.AddRoots(
		&mcp.Root{URI: uri1, Name: "root1"},
		&mcp.Root{URI: uri2, Name: "root2"},
	)

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, &mcp.ServerOptions{
		InitializedHandler:      ts.HandleInitialized,
		RootsListChangedHandler: ts.HandleRootsChanged,
	})
	ts.toolRegistry = server
	ts.registeredTools = make(map[string]tools.RegisteredTool)

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

func TestHandleRootsChanged_TransitionToUnprefixedCallTool(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir1, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  task1:\n    desc: Task one\n    cmds:\n      - echo one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir2, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  task2:\n    desc: Task two\n    cmds:\n      - echo two\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ts := New()
	ctx := t.Context()

	uri1 := roots.DirToURI(dir1)
	uri2 := roots.DirToURI(dir2)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	client.AddRoots(
		&mcp.Root{URI: uri1, Name: "root1"},
		&mcp.Root{URI: uri2, Name: "root2"},
	)

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, &mcp.ServerOptions{
		InitializedHandler:      ts.HandleInitialized,
		RootsListChangedHandler: ts.HandleRootsChanged,
	})
	ts.toolRegistry = server
	ts.registeredTools = make(map[string]tools.RegisteredTool)

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

	waitForTools(t, ts, 2)

	client.RemoveRoots(uri1)

	waitForToolCount(t, ts, 1)

	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "task2"})
	if err != nil {
		t.Fatalf("CallTool failed after transition to a single root: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got IsError=true: %s", toolResultText(t, result))
	}

	if status := toolStatusText(t, result); !strings.Contains(status, "exited with status 0") {
		t.Fatalf("expected status block to report exit 0, got %q", status)
	}
	if stdout := toolStreamText(t, result, "stdout"); !strings.Contains(stdout, "two") {
		t.Fatalf("expected stdout block to contain task2 output, got %q", stdout)
	}

	ts.mu.Lock()
	if _, ok := ts.roots[uri2]; !ok {
		ts.mu.Unlock()
		t.Fatalf("expected root %q to remain", uri2)
	}
	ts.mu.Unlock()
}

func TestHandleRootsChanged_RemoveLastRootClearsTools(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  hello:\n    desc: Say hello\n    cmds:\n      - echo hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ts := New()
	ctx := t.Context()
	uri := roots.DirToURI(dir)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	client.AddRoots(&mcp.Root{URI: uri, Name: "root"})

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, &mcp.ServerOptions{
		InitializedHandler:      ts.HandleInitialized,
		RootsListChangedHandler: ts.HandleRootsChanged,
	})
	ts.toolRegistry = server
	ts.registeredTools = make(map[string]tools.RegisteredTool)

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

	waitForRootCount(t, ts, 1)
	waitForTools(t, ts, 1)

	client.RemoveRoots(uri)

	waitForRootCount(t, ts, 0)
	waitForToolCount(t, ts, 0)

	toolsRes, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	if len(toolsRes.Tools) != 0 {
		t.Fatalf("expected no tools after removing last root, got %d", len(toolsRes.Tools))
	}
}

func TestToolListChangedNotification_OnFileChange(t *testing.T) {
	dir := t.TempDir()
	initial := []byte("version: '3'\ntasks:\n  hello:\n    desc: Say hello\n    cmds:\n      - echo hello\n")
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), initial, 0o600); err != nil {
		t.Fatal(err)
	}

	ts := New()
	ctx := t.Context()

	rootURI := roots.DirToURI(dir)

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
		InitializedHandler:      ts.HandleInitialized,
		RootsListChangedHandler: ts.HandleRootsChanged,
	})
	ts.toolRegistry = server
	ts.registeredTools = make(map[string]tools.RegisteredTool)

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

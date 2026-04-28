package server

import (
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rsclarke/mcp-taskfile-server/internal/roots"
	"github.com/rsclarke/mcp-taskfile-server/internal/tools"
)

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

func TestSyncTools_Idempotent(t *testing.T) {
	s := newTestServer(t, "basic")

	if err := s.syncTools(); err != nil {
		t.Fatalf("first syncTools failed: %v", err)
	}
	first := make(map[string]tools.RegisteredTool)
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
		if !tools.Equal(&tool, &cur) {
			t.Errorf("tool %q changed after second sync", name)
		}
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
	initialTools := make(map[string]tools.RegisteredTool)
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

	root, err := roots.Load(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}

	mcpSrv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil)
	tracker := newTrackingRegistry(mcpSrv)

	uri := roots.DirToURI(dir)
	s := New()
	s.toolRegistry = tracker
	s.roots[uri] = root

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
	s.registeredTools = make(map[string]tools.RegisteredTool)
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
	newRoot, err := roots.Load(t.Context(), singleTaskDir)
	if err != nil {
		t.Fatal(err)
	}

	// G1: snapshot with both tasks, then mutate state before G1 applies.
	s.mu.Lock()
	snap := s.snapshotToolStateLocked()
	oldTools := make(map[string]tools.RegisteredTool, len(s.registeredTools))
	maps.Copy(oldTools, s.registeredTools)

	// Simulate concurrent mutation while G1 is "planning".
	newURI := roots.DirToURI(singleTaskDir)
	delete(s.roots, uri)
	s.roots[newURI] = newRoot
	s.generation++
	s.mu.Unlock()

	// G1 plans from the stale snapshot (both tasks).
	plan := tools.BuildPlan(snap, s.log())
	stale, added := tools.Diff(oldTools, plan.Tools)

	// G1 tries to apply — generation should mismatch, no MCP calls.
	s.mu.Lock()
	if s.generation != snap.Generation {
		// Stale plan correctly detected — skip apply.
		s.mu.Unlock()
	} else {
		// This path should not be taken.
		if len(stale) > 0 {
			s.toolRegistry.RemoveTools(stale...)
		}
		for _, name := range added {
			tool := plan.Tools[name]
			s.toolRegistry.AddTool(&tool.Tool, plan.Handlers[name])
		}
		s.registeredTools = plan.Tools
		s.mu.Unlock()
	}

	// G2: run a clean sync with the updated state.
	if err := s.syncTools(); err != nil {
		t.Fatalf("G2 syncTools: %v", err)
	}

	// registeredTools should have {taskA} only.
	s.mu.Lock()
	regTools := make(map[string]tools.RegisteredTool)
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
// in-place afterwards (as ReloadRoot and disableRootToolsLocked do).
// Before snapshotting copied per-root fields by value, this test would
// have observed a nil taskfile in the snapshot and panicked in the
// planner.
func TestSnapshotToolState_IsolatedFromRootMutation(t *testing.T) {
	s := loadServerFromFixture(t, "basic")
	root := onlyRoot(t, s)

	s.mu.Lock()
	snap := s.snapshotToolStateLocked()
	// Simulate disableRootToolsLocked clearing the live root in place.
	root.Taskfile = nil
	s.generation++
	s.mu.Unlock()

	// Planner must run safely against the snapshot, observing the
	// taskfile that was current at snapshot time, not the now-nil
	// field on the live root.
	plan := tools.BuildPlan(snap, s.log())
	if _, ok := plan.Tools["greet"]; !ok {
		t.Fatalf("expected snapshotted plan to retain greet, got %v", toolNames(plan.Tools))
	}
}

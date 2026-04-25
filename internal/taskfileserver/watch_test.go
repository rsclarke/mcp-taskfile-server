package taskfileserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestLoadRoot_WatchTaskfilesIncludesTransitive(t *testing.T) {
	dir := t.TempDir()
	deepDir := filepath.Join(dir, "sub", "deep")
	if err := os.MkdirAll(deepDir, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), []byte("version: '3'\nincludes:\n  sub:\n    taskfile: ./sub\ntasks:\n  root:\n    cmds:\n      - echo root\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "Taskfile.yml"), []byte("version: '3'\nincludes:\n  deep:\n    taskfile: ./deep\ntasks:\n  child:\n    cmds:\n      - echo child\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deepDir, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  leaf:\n    cmds:\n      - echo leaf\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	root, err := loadRoot(t.Context(), dir)
	if err != nil {
		t.Fatalf("loadRoot: %v", err)
	}

	want := []string{
		filepath.Join(dir, "Taskfile.yml"),
		filepath.Join(dir, "sub", "Taskfile.yml"),
		filepath.Join(deepDir, "Taskfile.yml"),
	}

	got := sortedKeys(root.watchTaskfiles)
	if !slices.Equal(got, want) {
		t.Fatalf("watchTaskfiles = %v, want %v", got, want)
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

func TestWatchTaskfiles_IgnoresUnincludedChildTaskfile(t *testing.T) {
	dir := t.TempDir()
	childDir := filepath.Join(dir, "child")
	if err := os.Mkdir(childDir, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  hello:\n    desc: Root task\n    cmds:\n      - echo hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(childDir, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  child:\n    desc: Child task\n    cmds:\n      - echo child\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := newServerForDir(t, dir)
	root := onlyRoot(t, s)
	if _, ok := root.watchTaskfiles[filepath.Join(childDir, "Taskfile.yml")]; ok {
		t.Fatal("unexpected watch on unincluded child Taskfile")
	}

	initialTaskfile := root.taskfile
	ctx := t.Context()

	go func() {
		_ = s.watchTaskfiles(ctx, snapshotRoots(s))
	}()

	time.Sleep(100 * time.Millisecond)

	if err := os.WriteFile(filepath.Join(childDir, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  child:\n    desc: Updated child task\n    cmds:\n      - echo child\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	time.Sleep(400 * time.Millisecond)

	s.mu.Lock()
	defer s.mu.Unlock()
	if onlyRoot(t, s).taskfile != initialTaskfile {
		t.Fatal("editing an unincluded child Taskfile unexpectedly reloaded the root")
	}
}

func TestWatchTaskfiles_ReloadsOnIncludedTaskfileChange(t *testing.T) {
	dir := t.TempDir()
	childDir := filepath.Join(dir, "sub")
	if err := os.Mkdir(childDir, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), []byte("version: '3'\nincludes:\n  sub:\n    taskfile: ./sub\ntasks:\n  hello:\n    desc: Root task\n    cmds:\n      - echo hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(childDir, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  child:\n    desc: First child description\n    cmds:\n      - echo child\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := newServerForDir(t, dir)
	ctx := t.Context()

	go func() {
		_ = s.watchTaskfiles(ctx, snapshotRoots(s))
	}()

	time.Sleep(100 * time.Millisecond)

	if err := os.WriteFile(filepath.Join(childDir, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  child:\n    desc: Updated child description\n    cmds:\n      - echo child\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			s.mu.Lock()
			tool := s.registeredTools["sub_child"]
			s.mu.Unlock()
			t.Fatalf("timed out waiting for included Taskfile reload; last tool state: %+v", tool)
		case <-ticker.C:
			s.mu.Lock()
			tool, ok := s.registeredTools["sub_child"]
			s.mu.Unlock()
			if ok && strings.Contains(tool.Description, "Updated child description") {
				return
			}
		}
	}
}

func TestWatchTaskfiles_ReloadsOnTransitiveIncludedTaskfileChange(t *testing.T) {
	dir := t.TempDir()
	deepDir := filepath.Join(dir, "sub", "deep")
	if err := os.MkdirAll(deepDir, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), []byte("version: '3'\nincludes:\n  sub:\n    taskfile: ./sub\ntasks:\n  hello:\n    desc: Root task\n    cmds:\n      - echo hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "Taskfile.yml"), []byte("version: '3'\nincludes:\n  deep:\n    taskfile: ./deep\ntasks:\n  child:\n    desc: Child task\n    cmds:\n      - echo child\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deepDir, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  leaf:\n    desc: First deep description\n    cmds:\n      - echo leaf\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := newServerForDir(t, dir)
	ctx := t.Context()

	go func() {
		_ = s.watchTaskfiles(ctx, snapshotRoots(s))
	}()

	time.Sleep(100 * time.Millisecond)

	if err := os.WriteFile(filepath.Join(deepDir, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  leaf:\n    desc: Updated deep description\n    cmds:\n      - echo leaf\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			s.mu.Lock()
			tool := s.registeredTools["sub_deep_leaf"]
			s.mu.Unlock()
			t.Fatalf("timed out waiting for transitive include reload; last tool state: %+v", tool)
		case <-ticker.C:
			s.mu.Lock()
			tool, ok := s.registeredTools["sub_deep_leaf"]
			s.mu.Unlock()
			if ok && strings.Contains(tool.Description, "Updated deep description") {
				return
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

func TestServerShutdown_StopsActiveWatchersAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), []byte("version: '3'\ntasks:\n  x:\n    cmds:\n      - echo x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := newServerForDir(t, dir)
	s.restartWatchers(context.Background())

	s.mu.Lock()
	watchDone := s.watchDone
	s.mu.Unlock()
	if watchDone == nil {
		t.Fatal("expected active watcher generation")
	}

	s.Shutdown()

	select {
	case <-watchDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown did not wait for watcher generation to stop")
	}

	s.mu.Lock()
	watchCancel := s.watchCancel
	remainingDone := s.watchDone
	shuttingDown := s.shuttingDown
	s.mu.Unlock()
	if watchCancel != nil || remainingDone != nil {
		t.Fatal("expected watcher state to be cleared after Shutdown")
	}
	if !shuttingDown {
		t.Fatal("expected server to reject watcher restarts after Shutdown")
	}

	done := make(chan struct{})
	go func() {
		s.Shutdown()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("second Shutdown call blocked")
	}

	s.restartWatchers(context.Background())

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.watchCancel != nil || s.watchDone != nil {
		t.Fatal("restartWatchers should be a no-op after Shutdown")
	}
}

func TestReloadRoot_RemovesTask(t *testing.T) {
	initial := []byte("version: '3'\ntasks:\n  hello:\n    desc: Say hello\n    cmds:\n      - echo hello\n  goodbye:\n    desc: Say goodbye\n    cmds:\n      - echo goodbye\n")
	s := newTempServer(t, initial)
	rootURI := onlyRootURI(t, s)

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

	if err := s.reloadRoot(t.Context(), rootURI); err != nil {
		t.Fatalf("reloadRoot failed: %v", err)
	}

	if _, ok := s.registeredTools["goodbye"]; ok {
		t.Error("tool 'goodbye' should have been removed")
	}
	if _, ok := s.registeredTools["hello"]; !ok {
		t.Error("tool 'hello' should still be registered")
	}
}

func TestReloadRoot_RemovesAllPublicTasks(t *testing.T) {
	initial := []byte("version: '3'\ntasks:\n  hello:\n    desc: Say hello\n    cmds:\n      - echo hello\n")
	s := newTempServer(t, initial)
	rootURI := onlyRootURI(t, s)

	if _, ok := s.registeredTools["hello"]; !ok {
		t.Fatal("expected initial tool 'hello'")
	}

	updated := []byte("version: '3'\ntasks:\n  helper:\n    internal: true\n    cmds:\n      - echo hidden\n")
	if err := os.WriteFile(filepath.Join(onlyRoot(t, s).workdir, "Taskfile.yml"), updated, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := s.reloadRoot(t.Context(), rootURI); err != nil {
		t.Fatalf("reloadRoot failed: %v", err)
	}

	if len(s.registeredTools) != 0 {
		t.Fatalf("expected all tools to be removed, got %v", toolNames(s.registeredTools))
	}
}

func TestReloadRoot_UpdatesChangedTask(t *testing.T) {
	initial := []byte("version: '3'\ntasks:\n  greet:\n    desc: Say hello\n    cmds:\n      - echo hello\n")
	s := newTempServer(t, initial)
	rootURI := onlyRootURI(t, s)

	origTool := s.registeredTools["greet"]
	if origTool.Description != "Say hello" {
		t.Fatalf("initial description = %q, want %q", origTool.Description, "Say hello")
	}

	// Update the task description.
	updated := []byte("version: '3'\ntasks:\n  greet:\n    desc: Say hi there\n    cmds:\n      - echo hi there\n")
	if err := os.WriteFile(filepath.Join(onlyRoot(t, s).workdir, "Taskfile.yml"), updated, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := s.reloadRoot(t.Context(), rootURI); err != nil {
		t.Fatalf("reloadRoot failed: %v", err)
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

func TestWatchTaskfiles_InvalidRootTaskfileRemovesToolsUntilRestored(t *testing.T) {
	initial := []byte("version: '3'\ntasks:\n  hello:\n    desc: Say hello\n    cmds:\n      - echo hello\n")
	s := newTempServer(t, initial)
	root := onlyRoot(t, s)

	ctx := t.Context()

	go func() {
		_ = s.watchTaskfiles(ctx, snapshotRoots(s))
	}()

	time.Sleep(100 * time.Millisecond)

	invalid := []byte("version: '3'\ntasks:\n  hello:\n    desc: Broken hello\n    cmds:\n      - echo hello\n    vars: [\n")
	if err := os.WriteFile(filepath.Join(root.workdir, "Taskfile.yml"), invalid, 0o600); err != nil {
		t.Fatal(err)
	}

	waitForToolCount(t, s, 0)

	restored := []byte("version: '3'\ntasks:\n  hello:\n    desc: Restored hello\n    cmds:\n      - echo hello again\n")
	if err := os.WriteFile(filepath.Join(root.workdir, "Taskfile.yml"), restored, 0o600); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			s.mu.Lock()
			tool := s.registeredTools["hello"]
			s.mu.Unlock()
			t.Fatalf("timed out waiting for restored Taskfile reload; last tool state: %+v", tool)
		case <-ticker.C:
			s.mu.Lock()
			tool, ok := s.registeredTools["hello"]
			s.mu.Unlock()
			if ok && tool.Description == "Restored hello" {
				return
			}
		}
	}
}

func TestWatchTaskfiles_DeletedRootTaskfileRemovesToolsUntilRestored(t *testing.T) {
	initial := []byte("version: '3'\ntasks:\n  hello:\n    desc: Say hello\n    cmds:\n      - echo hello\n")
	s := newTempServer(t, initial)
	root := onlyRoot(t, s)

	ctx := t.Context()

	go func() {
		_ = s.watchTaskfiles(ctx, snapshotRoots(s))
	}()

	time.Sleep(100 * time.Millisecond)

	taskfilePath := filepath.Join(root.workdir, "Taskfile.yml")
	if err := os.Remove(taskfilePath); err != nil {
		t.Fatal(err)
	}

	waitForToolCount(t, s, 0)

	restored := []byte("version: '3'\ntasks:\n  hello:\n    desc: Recreated hello\n    cmds:\n      - echo hello again\n")
	if err := os.WriteFile(taskfilePath, restored, 0o600); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			s.mu.Lock()
			tool := s.registeredTools["hello"]
			s.mu.Unlock()
			t.Fatalf("timed out waiting for recreated Taskfile reload; last tool state: %+v", tool)
		case <-ticker.C:
			s.mu.Lock()
			tool, ok := s.registeredTools["hello"]
			s.mu.Unlock()
			if ok && tool.Description == "Recreated hello" {
				return
			}
		}
	}
}

func TestReconcileRoots_MissingInitialTaskfileLoadsWhenCreatedLater(t *testing.T) {
	dir := t.TempDir()
	uri := dirToURI(dir)
	s := New()
	s.toolRegistry = noopRegistry{}
	defer s.Shutdown()

	if err := s.reconcileRoots(t.Context(), testRoots(uri), rootReconcileOptions{requireNonEmpty: true}); err != nil {
		t.Fatalf("reconcileRoots: %v", err)
	}

	root := onlyRoot(t, s)
	if root.taskfile != nil {
		t.Fatal("expected root to be tracked without a loaded Taskfile")
	}
	if !slices.Equal(root.watchDirs, []string{dir}) {
		t.Fatalf("watchDirs = %v, want [%s]", root.watchDirs, dir)
	}
	for _, filename := range []string{"Taskfile.yml", "Taskfile.yaml", "Taskfile.dist.yml", "Taskfile.dist.yaml"} {
		if _, ok := root.watchTaskfiles[filepath.Join(dir, filename)]; !ok {
			t.Fatalf("expected %s to be watched", filename)
		}
	}

	time.Sleep(100 * time.Millisecond)

	taskfilePath := filepath.Join(dir, "Taskfile.yml")
	content := []byte("version: '3'\ntasks:\n  hello:\n    desc: Added after startup\n    cmds:\n      - echo hello\n")
	if err := os.WriteFile(taskfilePath, content, 0o600); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			s.mu.Lock()
			tool := s.registeredTools["hello"]
			s.mu.Unlock()
			t.Fatalf("timed out waiting for created root Taskfile reload; last tool state: %+v", tool)
		case <-ticker.C:
			s.mu.Lock()
			tool, ok := s.registeredTools["hello"]
			loadedRoot := s.roots[uri]
			s.mu.Unlock()
			if ok && tool.Description == "Added after startup" && loadedRoot != nil && loadedRoot.taskfile != nil {
				return
			}
		}
	}
}

func TestReloadRoot_UnknownURI(t *testing.T) {
	s := newTestServer(t, "basic")

	err := s.reloadRoot(t.Context(), "file:///nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown URI, got nil")
	}
	if !strings.Contains(err.Error(), "unknown root") {
		t.Errorf("expected 'unknown root' error, got: %v", err)
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

	// The new subdirectory is not watched yet because it isn't part of the
	// resolved include graph. The root Taskfile edit below should trigger a
	// reload, which then adds the new include path to the watch set.
	time.Sleep(200 * time.Millisecond)

	// Write a Taskfile in the new subdirectory, then update the root Taskfile to
	// include it.
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

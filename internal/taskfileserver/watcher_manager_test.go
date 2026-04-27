package taskfileserver

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeWatcherFn records how many times each URI's watch loop has been
// started, blocks until ctx is cancelled, and exits cleanly.
type fakeWatcherFn struct {
	mu     sync.Mutex
	starts map[string]int
	live   map[string]chan struct{}
}

func newFakeWatcherFn() *fakeWatcherFn {
	return &fakeWatcherFn{
		starts: make(map[string]int),
		live:   make(map[string]chan struct{}),
	}
}

func (f *fakeWatcherFn) run(ctx context.Context, uri string) {
	f.mu.Lock()
	f.starts[uri]++
	exited := make(chan struct{})
	f.live[uri] = exited
	f.mu.Unlock()
	defer close(exited)
	<-ctx.Done()
}

func (f *fakeWatcherFn) startsFor(uri string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts[uri]
}

func (f *fakeWatcherFn) liveChan(uri string) chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.live[uri]
}

// waitForActive polls m.active() until it equals want or the deadline
// expires.
func waitForActive(t *testing.T, m *watcherManager, want int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if got := m.active(); got == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %d active watchers, have %d", want, m.active())
		case <-tick.C:
		}
	}
}

// waitForStart polls until fake.startsFor(uri) is at least 1, i.e. the
// watcher goroutine has entered its run loop after apply spawned it.
func waitForStart(t *testing.T, fake *fakeWatcherFn, uri string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if fake.startsFor(uri) >= 1 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for watcher %q to start, starts=%d", uri, fake.startsFor(uri))
		case <-tick.C:
		}
	}
}

func TestWatcherManager_ApplyAddsAndRemoves(t *testing.T) {
	fake := newFakeWatcherFn()
	m := newWatcherManager(fake.run)
	t.Cleanup(m.shutdown)

	ctx := t.Context()

	m.apply(ctx, []string{"a", "b"}, nil)
	waitForActive(t, m, 2)
	waitForStart(t, fake, "a")
	waitForStart(t, fake, "b")

	// Capture the live channels so we can verify the EXISTING watchers
	// keep running across a subsequent apply (no mass restart).
	liveA := fake.liveChan("a")
	liveB := fake.liveChan("b")

	m.apply(ctx, []string{"c"}, []string{"a"})
	waitForStart(t, fake, "c")

	// "a" should have been cancelled; "b" must NOT be disturbed.
	select {
	case <-liveA:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("watcher for 'a' did not exit after cancellation")
	}

	select {
	case <-liveB:
		t.Fatal("watcher for 'b' was unexpectedly cancelled by an unrelated apply")
	case <-time.After(50 * time.Millisecond):
		// expected: b is still running
	}

	waitForActive(t, m, 2)
	if fake.startsFor("b") != 1 {
		t.Fatalf("watcher for 'b' should not have been restarted, starts=%d", fake.startsFor("b"))
	}
	if fake.startsFor("c") != 1 {
		t.Fatalf("watcher for 'c' should have been started exactly once, starts=%d", fake.startsFor("c"))
	}
}

func TestWatcherManager_ApplyIsIdempotentForExistingURIs(t *testing.T) {
	fake := newFakeWatcherFn()
	m := newWatcherManager(fake.run)
	t.Cleanup(m.shutdown)

	ctx := t.Context()

	m.apply(ctx, []string{"a"}, nil)
	waitForActive(t, m, 1)
	waitForStart(t, fake, "a")

	// Re-applying with the same URI in "added" must not start a second
	// watcher.
	m.apply(ctx, []string{"a"}, nil)
	time.Sleep(50 * time.Millisecond)

	if got := fake.startsFor("a"); got != 1 {
		t.Fatalf("expected exactly one start for 'a', got %d", got)
	}
	if got := m.active(); got != 1 {
		t.Fatalf("expected 1 active watcher, got %d", got)
	}
}

func TestWatcherManager_ShutdownStopsAllAndIsIdempotent(t *testing.T) {
	fake := newFakeWatcherFn()
	m := newWatcherManager(fake.run)

	ctx := t.Context()
	m.apply(ctx, []string{"a", "b", "c"}, nil)
	waitForActive(t, m, 3)
	waitForStart(t, fake, "a")
	waitForStart(t, fake, "b")
	waitForStart(t, fake, "c")

	done := make(chan struct{})
	go func() {
		m.shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not return in time")
	}

	if got := m.active(); got != 0 {
		t.Fatalf("expected 0 active watchers after shutdown, got %d", got)
	}

	// A second shutdown call must not block.
	done2 := make(chan struct{})
	go func() {
		m.shutdown()
		close(done2)
	}()
	select {
	case <-done2:
	case <-time.After(2 * time.Second):
		t.Fatal("second shutdown blocked")
	}

	// apply and reconcile must be no-ops after shutdown.
	m.apply(ctx, []string{"d"}, nil)
	m.reconcile(ctx, []string{"e"})
	time.Sleep(50 * time.Millisecond)
	if got := m.active(); got != 0 {
		t.Fatalf("apply/reconcile should be no-ops after shutdown, got %d active", got)
	}
}

func TestWatcherManager_ReconcileDiffsDesiredSet(t *testing.T) {
	fake := newFakeWatcherFn()
	m := newWatcherManager(fake.run)
	t.Cleanup(m.shutdown)

	ctx := t.Context()
	m.reconcile(ctx, []string{"a", "b"})
	waitForActive(t, m, 2)
	waitForStart(t, fake, "a")
	waitForStart(t, fake, "b")

	liveA := fake.liveChan("a")

	m.reconcile(ctx, []string{"a", "c"})
	waitForActive(t, m, 2)
	waitForStart(t, fake, "c")

	// "a" must still be running (untouched); "b" must have been
	// cancelled; "c" must have been started.
	select {
	case <-liveA:
		t.Fatal("reconcile cancelled watcher for 'a' even though it is in the desired set")
	case <-time.After(50 * time.Millisecond):
		// expected
	}
	if fake.startsFor("a") != 1 {
		t.Fatalf("watcher for 'a' should not have been restarted, starts=%d", fake.startsFor("a"))
	}
	if fake.startsFor("c") != 1 {
		t.Fatalf("watcher for 'c' should have been started, starts=%d", fake.startsFor("c"))
	}
}

func TestWatcherManager_DetachesFromRequestContext(t *testing.T) {
	fake := newFakeWatcherFn()
	m := newWatcherManager(fake.run)
	t.Cleanup(m.shutdown)

	// Cancel the request-scoped context immediately. The watcher's
	// own context must be detached and continue to run.
	reqCtx, cancel := context.WithCancel(context.Background())
	cancel()

	m.apply(reqCtx, []string{"a"}, nil)
	waitForActive(t, m, 1)
	waitForStart(t, fake, "a")

	live := fake.liveChan("a")
	select {
	case <-live:
		t.Fatal("watcher exited because it observed the cancelled request context")
	case <-time.After(50 * time.Millisecond):
		// expected: watcher is still running
	}
}

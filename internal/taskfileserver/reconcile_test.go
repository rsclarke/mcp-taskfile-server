package taskfileserver

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// writeTaskfile creates a directory with a Taskfile that defines a single
// task. It returns the directory's canonical root URI.
func writeTaskfile(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	body := []byte("version: '3'\ntasks:\n  " + name + ":\n    desc: " + name + "\n    cmds:\n      - echo " + name + "\n")
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), body, 0o600); err != nil {
		t.Fatalf("write Taskfile: %v", err)
	}
	return dirToURI(dir)
}

// newReconcileServer returns a Server suitable for reconcile tests: it has
// a registered tool registry but no live watchers.
func newReconcileServer() *Server {
	s := New()
	s.toolRegistry = noopRegistry{}
	return s
}

func TestInitializeRoots_AdditiveAndIdempotent(t *testing.T) {
	uriA := writeTaskfile(t, "alpha")
	uriB := writeTaskfile(t, "beta")

	s := newReconcileServer()

	res, err := s.initializeRoots(t.Context(), testRoots(uriA))
	if err != nil {
		t.Fatalf("initializeRoots first: %v", err)
	}
	if !slices.Equal(res.added, []string{uriA}) {
		t.Fatalf("first added = %v, want [%s]", res.added, uriA)
	}
	if len(res.removed) != 0 {
		t.Fatalf("first removed = %v, want empty", res.removed)
	}

	// A second initializeRoots that omits uriA must NOT remove it; uriB
	// is added on top.
	res, err = s.initializeRoots(t.Context(), testRoots(uriB))
	if err != nil {
		t.Fatalf("initializeRoots second: %v", err)
	}
	if !slices.Equal(res.added, []string{uriB}) {
		t.Fatalf("second added = %v, want [%s]", res.added, uriB)
	}
	if len(res.removed) != 0 {
		t.Fatalf("second removed = %v, want empty (initializeRoots is additive)", res.removed)
	}

	s.mu.Lock()
	_, hasA := s.roots[uriA]
	_, hasB := s.roots[uriB]
	s.mu.Unlock()
	if !hasA || !hasB {
		t.Fatalf("expected both roots present, got hasA=%v hasB=%v", hasA, hasB)
	}
}

func TestInitializeRoots_RequiresNonEmpty(t *testing.T) {
	s := newReconcileServer()

	if _, err := s.initializeRoots(t.Context(), nil); err == nil {
		t.Fatal("expected error from initializeRoots with no roots")
	}

	// Invalid URIs are skipped; if nothing valid remains the call must error.
	if _, err := s.initializeRoots(t.Context(), testRoots("not-a-valid-uri")); err == nil {
		t.Fatal("expected error from initializeRoots when all URIs are invalid")
	}

	s.mu.Lock()
	n := len(s.roots)
	s.mu.Unlock()
	if n != 0 {
		t.Fatalf("expected 0 roots after failed init, got %d", n)
	}
}

func TestReplaceRoots_RemovesMissingAndAddsNew(t *testing.T) {
	uriA := writeTaskfile(t, "alpha")
	uriB := writeTaskfile(t, "beta")
	uriC := writeTaskfile(t, "gamma")

	s := newReconcileServer()

	if _, err := s.initializeRoots(t.Context(), testRoots(uriA, uriB)); err != nil {
		t.Fatalf("initializeRoots: %v", err)
	}

	res := s.replaceRoots(t.Context(), testRoots(uriB, uriC))

	slices.Sort(res.added)
	slices.Sort(res.removed)
	if !slices.Equal(res.added, []string{uriC}) {
		t.Fatalf("added = %v, want [%s]", res.added, uriC)
	}
	if !slices.Equal(res.removed, []string{uriA}) {
		t.Fatalf("removed = %v, want [%s]", res.removed, uriA)
	}

	s.mu.Lock()
	_, hasA := s.roots[uriA]
	_, hasB := s.roots[uriB]
	_, hasC := s.roots[uriC]
	s.mu.Unlock()
	if hasA {
		t.Errorf("uriA should have been removed")
	}
	if !hasB || !hasC {
		t.Errorf("expected uriB and uriC to be present, got hasB=%v hasC=%v", hasB, hasC)
	}
}

func TestReplaceRoots_AllowsEmpty(t *testing.T) {
	uriA := writeTaskfile(t, "alpha")

	s := newReconcileServer()
	if _, err := s.initializeRoots(t.Context(), testRoots(uriA)); err != nil {
		t.Fatalf("initializeRoots: %v", err)
	}

	res := s.replaceRoots(t.Context(), nil)
	if !slices.Equal(res.removed, []string{uriA}) {
		t.Fatalf("removed = %v, want [%s]", res.removed, uriA)
	}
	if len(res.added) != 0 {
		t.Fatalf("added = %v, want empty", res.added)
	}

	s.mu.Lock()
	n := len(s.roots)
	s.mu.Unlock()
	if n != 0 {
		t.Fatalf("expected empty root set after replaceRoots(nil), got %d roots", n)
	}
}

func TestReplaceRoots_NoChangesYieldsEmptyDiff(t *testing.T) {
	uriA := writeTaskfile(t, "alpha")
	uriB := writeTaskfile(t, "beta")

	s := newReconcileServer()
	if _, err := s.initializeRoots(t.Context(), testRoots(uriA, uriB)); err != nil {
		t.Fatalf("initializeRoots: %v", err)
	}

	beforeGen := s.generation
	res := s.replaceRoots(t.Context(), testRoots(uriA, uriB))
	if res.changed() {
		t.Fatalf("expected no diff for identical roots, got added=%v removed=%v", res.added, res.removed)
	}
	if s.generation != beforeGen {
		t.Fatalf("generation should not advance for an unchanged replaceRoots call (before=%d after=%d)", beforeGen, s.generation)
	}
}

func TestLoadDesired_SkipsExistingURIs(t *testing.T) {
	uriA := writeTaskfile(t, "alpha")
	uriB := writeTaskfile(t, "beta")

	existing := map[string]struct{}{uriA: {}}
	s := New()
	desired, loaded := s.loadDesired(t.Context(), testRoots(uriA, uriB), existing)

	if _, ok := desired[uriA]; !ok {
		t.Fatalf("expected uriA in desired set, got %v", desired)
	}
	if _, ok := desired[uriB]; !ok {
		t.Fatalf("expected uriB in desired set, got %v", desired)
	}
	if _, ok := loaded[uriA]; ok {
		t.Fatalf("loadDesired should not reload an existing root, got %v", loaded)
	}
	if _, ok := loaded[uriB]; !ok {
		t.Fatalf("loadDesired should load uriB, got %v", loaded)
	}
}

func TestLoadDesired_DropsInvalidURIs(t *testing.T) {
	uriA := writeTaskfile(t, "alpha")

	s := New()
	desired, loaded := s.loadDesired(
		t.Context(),
		testRoots(uriA, "not-a-valid-uri", "https://example.com"),
		nil,
	)

	if len(desired) != 1 {
		t.Fatalf("desired = %v, want exactly uriA", desired)
	}
	if _, ok := desired[uriA]; !ok {
		t.Fatalf("desired missing uriA: %v", desired)
	}
	if _, ok := loaded[uriA]; !ok {
		t.Fatalf("loaded missing uriA: %v", loaded)
	}
}

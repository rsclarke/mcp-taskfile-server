package taskfileserver

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// TestSetLogger_StructuredFields verifies the server's slog.Logger is
// honoured and emits the expected structured fields for a known event.
func TestSetLogger_StructuredFields(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	s := New()
	s.SetLogger(logger)

	// Invoke loadDesired with a syntactically invalid URI so it logs the
	// "root.invalid_uri" event without performing any disk I/O.
	s.loadDesired(t.Context(), testRoots("not-a-valid-uri"), nil)

	if buf.Len() == 0 {
		t.Fatal("expected at least one log line, got none")
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	var entry map[string]any
	for _, line := range lines {
		var got map[string]any
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("invalid JSON log line %q: %v", line, err)
		}
		if got["event"] == "root.invalid_uri" {
			entry = got
			break
		}
	}
	if entry == nil {
		t.Fatalf("did not find root.invalid_uri event in logs: %s", buf.String())
	}
	if entry["root_uri"] != "not-a-valid-uri" {
		t.Errorf("root_uri = %v, want %q", entry["root_uri"], "not-a-valid-uri")
	}
	if entry["level"] != "WARN" {
		t.Errorf("level = %v, want WARN", entry["level"])
	}
}

// TestSetLogger_NilRestoresDiscard ensures passing a nil logger does
// not panic and silences subsequent calls.
func TestSetLogger_NilRestoresDiscard(t *testing.T) {
	s := New()
	s.SetLogger(nil)

	// Should not panic.
	s.loadDesired(t.Context(), testRoots("not-a-valid-uri"), nil)
}

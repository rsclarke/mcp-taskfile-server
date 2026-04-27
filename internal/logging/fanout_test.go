package logging

import (
	"context"
	"log/slog"
	"sync"
	"testing"
)

// TestFanoutHandler_DispatchesToAllHandlers verifies the fanout passes
// every record to each constituent handler.
func TestFanoutHandler_DispatchesToAllHandlers(t *testing.T) {
	a := &recordingHandler{}
	b := &recordingHandler{}
	logger := slog.New(NewFanout(a, b))

	logger.Warn("ping", slog.String("event", "fanout.test"))

	if got := a.count(); got != 1 {
		t.Errorf("handler a saw %d records, want 1", got)
	}
	if got := b.count(); got != 1 {
		t.Errorf("handler b saw %d records, want 1", got)
	}
}

type recordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}

func (h *recordingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }

func (h *recordingHandler) WithGroup(_ string) slog.Handler { return h }

func (h *recordingHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.records)
}

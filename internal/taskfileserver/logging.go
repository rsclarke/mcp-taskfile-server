package taskfileserver

import (
	"context"
	"log/slog"
)

// fanoutHandler dispatches every record to multiple slog.Handlers,
// allowing the server to emit a single log line to both the stderr JSON
// sink and the SDK's MCP LoggingHandler without coupling their
// lifecycles. Per-handler errors are dropped so a failure in one branch
// does not suppress the others.
//
// slog has no built-in multi-handler; this is the minimum needed.
type fanoutHandler struct {
	handlers []slog.Handler
}

// newFanoutHandler returns a handler that forwards every record to each
// of handlers in order.
func newFanoutHandler(handlers ...slog.Handler) *fanoutHandler {
	return &fanoutHandler{handlers: handlers}
}

// Enabled reports whether any underlying handler is enabled at level.
func (f *fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range f.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

// Handle dispatches r to each underlying handler that is enabled.
// Each handler receives a clone of the record because slog.Record's
// attributes are encoded lazily.
func (f *fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range f.handlers {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		_ = h.Handle(ctx, r.Clone())
	}
	return nil
}

// WithAttrs implements slog.Handler.
func (f *fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cloned := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		cloned[i] = h.WithAttrs(attrs)
	}
	return &fanoutHandler{handlers: cloned}
}

// WithGroup implements slog.Handler.
func (f *fanoutHandler) WithGroup(name string) slog.Handler {
	cloned := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		cloned[i] = h.WithGroup(name)
	}
	return &fanoutHandler{handlers: cloned}
}

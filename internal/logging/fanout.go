// Package logging provides the structured logging primitives shared by
// the Taskfile MCP server: a multi-handler fanout, the MCP "logging"
// arm wiring, and the stderr logger constructor.
package logging

import (
	"context"
	"log/slog"
)

// FanoutHandler dispatches every record to multiple slog.Handlers,
// allowing the server to emit a single log line to both the stderr JSON
// sink and the SDK's MCP LoggingHandler without coupling their
// lifecycles. Per-handler errors are dropped so a failure in one branch
// does not suppress the others.
//
// slog has no built-in multi-handler; this is the minimum needed.
type FanoutHandler struct {
	handlers []slog.Handler
}

// NewFanout returns a handler that forwards every record to each of
// handlers in order.
func NewFanout(handlers ...slog.Handler) *FanoutHandler {
	return &FanoutHandler{handlers: handlers}
}

// Handlers returns the underlying handlers in their original order. The
// returned slice is shared with the receiver and must not be mutated.
func (f *FanoutHandler) Handlers() []slog.Handler {
	return f.handlers
}

// Enabled reports whether any underlying handler is enabled at level.
func (f *FanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
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
func (f *FanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range f.handlers {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		_ = h.Handle(ctx, r.Clone())
	}
	return nil
}

// WithAttrs implements slog.Handler.
func (f *FanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cloned := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		cloned[i] = h.WithAttrs(attrs)
	}
	return &FanoutHandler{handlers: cloned}
}

// WithGroup implements slog.Handler.
func (f *FanoutHandler) WithGroup(name string) slog.Handler {
	cloned := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		cloned[i] = h.WithGroup(name)
	}
	return &FanoutHandler{handlers: cloned}
}

package taskfileserver

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rsclarke/mcp-taskfile-server/internal/logging"
)

// TestServer_NoMCPLoggingBeforeInitialized verifies that a freshly
// constructed Server emits records only to its initially configured sink
// (stderr, here a buffer) and does not panic when no session has been
// installed.
func TestServer_NoMCPLoggingBeforeInitialized(t *testing.T) {
	var buf bytes.Buffer
	stderr := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})

	s := New()
	s.SetLogger(slog.New(stderr))

	s.log().Warn("hello",
		slog.String("event", "test.before_init"),
	)

	if buf.Len() == 0 {
		t.Fatal("expected stderr handler to receive the record")
	}
	if !strings.Contains(buf.String(), `"event":"test.before_init"`) {
		t.Errorf("expected event in stderr buffer, got %s", buf.String())
	}
}

// captureLoggingClient wires up an in-memory client/server pair, lets
// the server's HandleInitialized fire so installMCPLogging runs, and
// returns a client session along with a channel that receives every
// logging/message notification the client is sent.
func captureLoggingClient(t *testing.T, ts *Server) (*mcp.ClientSession, chan *mcp.LoggingMessageParams) {
	t.Helper()

	notes := make(chan *mcp.LoggingMessageParams, 32)

	client := mcp.NewClient(
		&mcp.Implementation{Name: "test-client", Version: "0.0.0"},
		&mcp.ClientOptions{
			LoggingMessageHandler: func(_ context.Context, req *mcp.LoggingMessageRequest) {
				select {
				case notes <- req.Params:
				default:
				}
			},
		},
	)

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, &mcp.ServerOptions{
		InitializedHandler:      ts.HandleInitialized,
		RootsListChangedHandler: ts.HandleRootsChanged,
	})
	ts.SetToolRegistry(server)

	ctx := t.Context()
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

	return cs, notes
}

// TestServer_ForwardsLogsAfterInitialized exercises the end-to-end
// path: connect a client, raise its log level, drive a record through
// the server's logger, and assert the client receives a structured
// logging/message notification.
func TestServer_ForwardsLogsAfterInitialized(t *testing.T) {
	var buf bytes.Buffer
	stderr := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})

	ts := New()
	ts.SetLogger(slog.New(stderr))

	cs, notes := captureLoggingClient(t, ts)

	// Raise the threshold so the SDK's LoggingHandler will forward.
	if err := cs.SetLoggingLevel(t.Context(), &mcp.SetLoggingLevelParams{Level: "debug"}); err != nil {
		t.Fatalf("SetLoggingLevel: %v", err)
	}

	// Wait for HandleInitialized to have run and installed the MCP arm.
	waitForMCPLoggingInstalled(t, ts)

	ts.log().Warn("widget exploded",
		slog.String("event", "test.forward"),
		slog.String("widget", "left"),
	)

	got := waitForLogEvent(t, notes, "test.forward", 2*time.Second)
	if got.Level != "warning" {
		t.Errorf("Level = %q, want warning", got.Level)
	}
	if got.Logger != "mcp-taskfile-server" {
		t.Errorf("Logger = %q, want mcp-taskfile-server", got.Logger)
	}
	payload := decodePayload(t, got.Data)
	if payload["widget"] != "left" {
		t.Errorf("widget = %v, want left", payload["widget"])
	}
	if payload["msg"] != "widget exploded" {
		t.Errorf("msg = %v, want widget exploded", payload["msg"])
	}

	// Stderr arm must also have received the record.
	if !strings.Contains(buf.String(), `"event":"test.forward"`) {
		t.Errorf("stderr buffer missing record; got %s", buf.String())
	}
}

// TestServer_RespectsClientLevel verifies that logging/setLevel from
// the client controls what reaches the MCP arm, while the stderr arm is
// unaffected.
func TestServer_RespectsClientLevel(t *testing.T) {
	var buf bytes.Buffer
	stderr := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})

	ts := New()
	ts.SetLogger(slog.New(stderr))

	cs, notes := captureLoggingClient(t, ts)

	if err := cs.SetLoggingLevel(t.Context(), &mcp.SetLoggingLevelParams{Level: "warning"}); err != nil {
		t.Fatalf("SetLoggingLevel: %v", err)
	}

	waitForMCPLoggingInstalled(t, ts)
	drainNotifications(notes, 100*time.Millisecond)

	ts.log().Info("debugging detail", slog.String("event", "test.suppressed"))
	ts.log().Error("kaboom", slog.String("event", "test.surfaced"))

	got := waitForLogEvent(t, notes, "test.surfaced", 2*time.Second)
	if got.Level != "error" {
		t.Errorf("Level = %q, want error", got.Level)
	}

	// The suppressed info record must not have crossed the MCP arm.
	select {
	case extra := <-notes:
		payload := decodePayload(t, extra.Data)
		if payload["event"] == "test.suppressed" {
			t.Fatalf("info record forwarded despite client level=warning")
		}
	case <-time.After(200 * time.Millisecond):
	}

	// Stderr arm always sees both records, regardless of client level.
	if !strings.Contains(buf.String(), `"event":"test.suppressed"`) {
		t.Errorf("stderr arm missing test.suppressed; got %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"event":"test.surfaced"`) {
		t.Errorf("stderr arm missing test.surfaced; got %s", buf.String())
	}
}

// waitForMCPLoggingInstalled blocks until installMCPLogging has run and
// extended the server's logger with the SDK arm. It detects the swap by
// inspecting the handler chain rather than peeking at private state.
func waitForMCPLoggingInstalled(t *testing.T, ts *Server) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for !hasMCPHandler(ts.log().Handler()) {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for installMCPLogging")
		case <-tick.C:
		}
	}
}

// hasMCPHandler reports whether the given handler tree contains the
// SDK's LoggingHandler. The fanout handler exposes its children via
// Handlers, so we recurse via type assertion only on the types we
// ourselves construct.
func hasMCPHandler(h slog.Handler) bool {
	switch v := h.(type) {
	case *logging.FanoutHandler:
		return slices.ContainsFunc(v.Handlers(), hasMCPHandler)
	case *mcp.LoggingHandler:
		return true
	default:
		return false
	}
}

// drainNotifications consumes any pending notifications for at most d
// before returning.
func drainNotifications(ch <-chan *mcp.LoggingMessageParams, d time.Duration) {
	deadline := time.After(d)
	for {
		select {
		case <-ch:
		case <-deadline:
			return
		}
	}
}

// waitForLogEvent blocks until a notification with payload event=name
// is received or the deadline elapses.
func waitForLogEvent(t *testing.T, ch <-chan *mcp.LoggingMessageParams, name string, d time.Duration) *mcp.LoggingMessageParams {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case got := <-ch:
			payload := decodePayload(t, got.Data)
			if payload["event"] == name {
				return got
			}
		case <-deadline:
			t.Fatalf("timed out waiting for log event %q", name)
			return nil
		}
	}
}

// decodePayload normalises the LoggingMessageParams.Data value into a
// map[string]any so assertions can be written without worrying about
// whether the SDK delivered the original json.RawMessage or a decoded
// map (varies by transport / JSON round-trip).
func decodePayload(t *testing.T, data any) map[string]any {
	t.Helper()
	switch v := data.(type) {
	case map[string]any:
		return v
	case json.RawMessage:
		var out map[string]any
		if err := json.Unmarshal(v, &out); err != nil {
			t.Fatalf("invalid JSON payload %q: %v", v, err)
		}
		return out
	case []byte:
		var out map[string]any
		if err := json.Unmarshal(v, &out); err != nil {
			t.Fatalf("invalid JSON payload %q: %v", v, err)
		}
		return out
	case string:
		var out map[string]any
		if err := json.Unmarshal([]byte(v), &out); err != nil {
			t.Fatalf("invalid JSON payload %q: %v", v, err)
		}
		return out
	default:
		t.Fatalf("unexpected payload type %T (%v)", data, data)
		return nil
	}
}

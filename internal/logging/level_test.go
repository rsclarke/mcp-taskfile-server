package logging

import (
	"log/slog"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := []struct {
		raw  string
		want slog.Level
	}{
		{"", slog.LevelInfo},
		{"info", slog.LevelInfo},
		{"INFO", slog.LevelInfo},
		{"  info  ", slog.LevelInfo},
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"bogus", slog.LevelInfo},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			if got := ParseLevel(tc.raw); got != tc.want {
				t.Errorf("ParseLevel(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

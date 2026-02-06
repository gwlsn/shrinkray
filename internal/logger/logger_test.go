package logger

import (
	"bytes"
	"log/slog"
	"testing"
)

func TestSetLevel(t *testing.T) {
	// Initialize logger with info level
	Init("info")

	// Capture output to verify level changes take effect
	var buf bytes.Buffer
	Log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: &level}))

	// Debug should NOT appear at info level
	buf.Reset()
	Log.Debug("hidden")
	if buf.Len() > 0 {
		t.Error("debug message should not appear at info level")
	}

	// Switch to debug level at runtime
	SetLevel("debug")

	buf.Reset()
	Log.Debug("visible")
	if buf.Len() == 0 {
		t.Error("debug message should appear after SetLevel(debug)")
	}

	// Switch back to error level
	SetLevel("error")

	buf.Reset()
	Log.Info("hidden again")
	if buf.Len() > 0 {
		t.Error("info message should not appear at error level")
	}
}

func TestSetLevelInvalidFallsBackToInfo(t *testing.T) {
	Init("debug")
	SetLevel("garbage")

	var buf bytes.Buffer
	Log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: &level}))

	buf.Reset()
	Log.Debug("should be hidden")
	if buf.Len() > 0 {
		t.Error("invalid level should fall back to info, hiding debug")
	}

	buf.Reset()
	Log.Info("should be visible")
	if buf.Len() == 0 {
		t.Error("info should be visible at info level")
	}
}

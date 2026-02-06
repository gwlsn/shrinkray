package logger

import (
	"log/slog"
	"os"
	"strings"
)

// Log is the global logger instance
var Log *slog.Logger

// level is the dynamic log level, changeable at runtime via SetLevel.
// Uses slog.LevelVar which is backed by atomic.Int64 — safe for concurrent use.
var level slog.LevelVar

// Init initializes the global logger with the specified level.
func Init(levelStr string) {
	SetLevel(levelStr)
	Log = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: &level,
	}))
}

// SetLevel changes the log level at runtime. Valid values: debug, info, warn, error.
// Invalid values fall back to info.
func SetLevel(levelStr string) {
	var lvl slog.Level
	switch strings.ToLower(levelStr) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	level.Set(lvl)
}

// Debug logs a debug message
func Debug(msg string, args ...any) {
	if Log != nil {
		Log.Debug(msg, args...)
	}
}

// Info logs an info message
func Info(msg string, args ...any) {
	if Log != nil {
		Log.Info(msg, args...)
	}
}

// Warn logs a warning message
func Warn(msg string, args ...any) {
	if Log != nil {
		Log.Warn(msg, args...)
	}
}

// Error logs an error message
func Error(msg string, args ...any) {
	if Log != nil {
		Log.Error(msg, args...)
	}
}

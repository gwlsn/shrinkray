# Runtime Log Level Setting

**Date:** 2026-02-06
**Status:** Approved

## Problem

Log level is config-file-only. Changing it requires editing `shrinkray.yaml` and restarting the container. This is tedious on Unraid where the sed command must be remembered each time.

## Solution

Expose log level as a dropdown in the Advanced Settings panel. Changes apply instantly and persist to config.

## Changes

### 1. Logger (`internal/logger/logger.go`)

- Add package-level `slog.LevelVar` (atomic, concurrent-safe)
- Point handler's `Level` option at the `LevelVar` instead of a fixed level
- Add exported `SetLevel(level string)` that parses the string and calls `LevelVar.Set()`
- `Init()` calls `SetLevel()` internally — startup behavior unchanged

### 2. API (`internal/api/handler.go`)

- Add `log_level` to GET `/api/config` response
- Add `LogLevel *string` to `UpdateConfigRequest`
- Validate against allowed values: `debug`, `info`, `warn`, `error`
- Call `logger.SetLevel()` on valid update — takes effect immediately
- Config saved to YAML by existing save logic

### 3. UI (`web/templates/index.html`)

- Add dropdown at top of Advanced Settings section
- Options: Debug, Info, Warn, Error
- Description: "Controls logging verbosity. Use Debug when troubleshooting issues."
- Uses existing `updateSetting('log_level', value)` pattern
- `loadSettings()` populates from `config.log_level`

## Out of Scope

- Separate API endpoint (reuses PUT /api/config)
- SSE notification on change
- Reset-to-default button
- Log level indicator in UI header

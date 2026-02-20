# Post-Refactor Codebase Audit: Master Plan

Full audit of the Shrinkray codebase after major refactor. 63 findings across 9 categories, triaged into 4 waves by effort and dependency order. Codex CLI reviewed and confirmed findings.

## Wave 1: Quick Wins (COMPLETED)

Commit: `4abdcee refactor: post-audit fixes for error handling, deduplication, and API parity`

| # | Finding | What Was Done |
|---|---------|---------------|
| 1 | H8 | Fixed misleading OriginalHandling comment (was inverted vs actual behavior) |
| 2 | H7 | Exposed `keep_larger_files` in GetConfig response and UpdateConfig request |
| 3 | M6 | Centralized HEVC/AV1 quality bounds into `ffmpeg.HEVCQualityMin/Max`, `AV1QualityMin/Max` constants |
| 4 | H2 | Extracted `newJob()` constructor to deduplicate 21 fields between `Add()` and `AddMultiple()` |
| 5 | H5 | Added `logger.Warn` to all 14 silently discarded errors in worker (queue ops + os.Remove) |

## Wave 2: Test Fixes (COMPLETED)

Commit: `9b44f34 test: fix no-op tests and add coverage for untested areas`

| # | Finding | What Was Done |
|---|---------|---------------|
| 6 | M10 | Deleted no-op `TestBinarySearchCRF` (no mock seam for real FFmpeg dependency) |
| 7 | M11 | Converted HDR test `t.Logf` assertions to `t.Errorf` where deterministic |
| 8 | M13 | Added `pushover_test.go` (7 tests via httptest mock) |
| 9 | M13 | Added `limits_test.go` (4 table-driven test functions) |
| 10 | M13 | Added `tonemap_test.go` (2 test functions, 19 cases) |
| 11 | H7+ | Added `TestKeepLargerFilesConfig` (GET/PUT/GET roundtrip) |
| 12 | H2+ | Added `TestNewJobSetsAllFields` (safety net for all 21 fields) |

## Wave 3: Structural Refactors (IN PROGRESS)

These change function signatures and code structure. Each needs a plan, and order matters because they touch overlapping files.

### Step 1: H4 -- TranscodeOptions Struct (COMPLETE)

**Status:** Completed. Commit `4457b8f`. Plan was in Appendix A below.

Replace 17-parameter `Transcode()`, 11-parameter `BuildPresetArgs()`, 12-parameter `attemptTranscode()`, and 12-parameter `tryEncoderFallbacks()` with a single `TranscodeOptions` struct. Pure mechanical refactor, no behavior changes.

**Files:** `internal/ffmpeg/transcode.go`, `internal/ffmpeg/presets.go`, `internal/jobs/worker.go`, `internal/ffmpeg/presets_test.go`, `internal/ffmpeg/transcode_test.go`

**Estimated effort:** 1-2 hours

### Step 2: H3 -- Split processJob Into Phases

**Status:** Plan written at `docs/plans/2026-02-20-split-processjob.md`

Break `processJob()` (273 lines, 5 exit paths) into named phases:
- `prepareJob()`: preset lookup, SmartShrink analysis
- `prepareTranscodeParams()`: quality settings, SW decode, HDR, subtitles (builds `TranscodeOptions`)
- `executeTranscode()`: primary transcode + recovery strategies
- `finalizeJob()`: size check, file move, cache invalidation, completion

**Why H4 first:** After H4, `processJob` passes a single `TranscodeOptions` struct instead of 12+ loose variables. This makes the phase boundaries cleaner because each phase either builds or consumes the struct.

**Files:** `internal/jobs/worker.go`

**Estimated effort:** 2 hours

### Step 3: H1 -- Adopt sqlx for Job Persistence

**Status:** Plan written at `docs/plans/2026-02-20-adopt-sqlx.md`

Replace manual `jobColumns`/`jobPlaceholders`/`scanJob` column-order coupling with `sqlx` struct-tag-based mapping. Eliminates the most dangerous pattern in the codebase (silent data corruption from column order mismatch).

**What sqlx does:** Instead of manually listing columns and scanning them in order, sqlx matches `db:"column_name"` struct tags to SQL columns automatically. Order-independent, type-safe.

**Files:** `internal/store/sqlite.go`, `internal/jobs/job.go` (add db tags), `go.mod` (new dependency)

**Estimated effort:** 1-2 hours

## Wave 4: Dead Code Cleanup (COMPLETED)

### Remove Dead Code (DONE)

| # | Finding | What Was Done |
|---|---------|---------------|
| 13 | Dead `countVideos()` | Removed 69-line function superseded by `recomputeDirCount`; updated 4 test sites |
| 14 | Dead `SamplesUsed` field | Removed write-only field from `vmaf.AnalysisResult` |
| 15 | Dead `Sample.Position/Duration` | Removed write-only fields from `vmaf.Sample` |
| 16 | Dead `statePending` constant | Removed unreachable state; updated test assertions |

### Minor Cleanups (DONE)

| # | Finding | What Was Done |
|---|---------|---------------|
| 17 | Hardcoded `"hable"` | Referenced `config.DefaultTonemapAlgorithm` in `DefaultConfig()` and `BuildPresetArgs` |
| 18 | Duplicate clamping | Removed redundant `MaxConcurrentAnalyses` clamping in `main.go` (already in `config.Load`) |
| 19 | Magic numbers | Extracted browse concurrency limits (8, 16, 256) and worker polling intervals (100ms, 500ms, 30s) into named constants |

### Batch Unexport Sweep (SKIPPED)

Unexport 16+ symbols in `internal/` packages. Deferred per Codex review: diff noise, low runtime impact, all symbols are in `internal/` packages already.

## Skipped (Not Worth the Effort)

Per Codex CLI review, these are theoretical concerns for a project this size:

| Finding | Why Skipped |
|---------|-------------|
| M1: Split ffmpeg into sub-packages | Package-structure purity, high churn |
| M2: Feature envy in processJob | Defensible as orchestration logic |
| M3: Split 13-method Store interface | Only 1 implementation, not worth it |
| M7: SW fallback dedup | Low divergence risk, do opportunistically |
| M12: Global mutable state in ffmpeg | Would require significant restructuring |
| Mass unexporting | Diff noise, low runtime impact |

---

## Appendix A: H4 TranscodeOptions Struct Plan

### The Struct (defined in `internal/ffmpeg/transcode.go`)

```go
type TranscodeOptions struct {
    Preset          *Preset
    SourceBitrate   int64
    SourceWidth     int
    SourceHeight    int
    Duration        time.Duration
    TotalFrames     int64
    QualityHEVC     int
    QualityAV1      int
    QualityMod      float64
    SoftwareDecode  bool
    OutputFormat    string
    Tonemap         *TonemapParams
    SubtitleIndices []int
}
```

`ctx`, `progressCh`, `inputPath`, `outputPath` stay as separate parameters (lifecycle/IO, not config).

### New Signatures

| Function | Before | After |
|----------|--------|-------|
| `Transcode` | 17 params | `(ctx, inputPath, outputPath, opts, progressCh)` |
| `BuildPresetArgs` | 11 params | `(opts)` |
| `BuildSampleEncodeArgs` | 6 params | `(opts)` |
| `attemptTranscode` | 12 params | `(jobCtx, job, opts, tempPath)` |
| `tryEncoderFallbacks` | 12 params | `(jobCtx, job, opts, tempPath, priorError)` |

### Execution (atomic, all at once)

1. Define `TranscodeOptions` struct in `transcode.go`
2. Update `BuildPresetArgs()` to accept struct, read fields
3. Update `BuildSampleEncodeArgs()` to accept struct
4. Update `Transcode()` to accept struct, forward to `BuildPresetArgs`
5. Update `attemptTranscode()` and `tryEncoderFallbacks()` in `worker.go`
6. Update `processJob()` to construct opts once and pass through
7. Update all test call sites in `presets_test.go` and `transcode_test.go`

### Design Decisions

- **Single struct**: params always travel together, no benefit to splitting
- **Pass by value**: `BuildSampleEncodeArgs` safely mutates its copy
- **Zero values are safe**: 0 = use defaults, nil = no tonemapping, nil = map all subtitles

### Verification

```bash
go build ./...
go test ./...
go test -race ./internal/ffmpeg/... ./internal/jobs/...
golangci-lint run
```

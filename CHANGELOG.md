# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [2.1.0] - 2026-02-06

### Added
- **Recursive folder scanning** (#91) — Directory video counts now include files in subdirectories, so TV show folders display total episode count across all seasons
  - Non-blocking: Browse returns instantly using cached results, populates cache in background
  - Startup cache warming pre-computes all counts in a single efficient walk
  - Optimized with singleflight dedup, WalkDir, and semaphore (max 8 concurrent walks)
- **Runtime log level setting** (#92) — Change log verbosity (debug/info/warn/error) from the UI without restarting
  - New dropdown in Advanced Settings
  - `log_level` field on GET/PUT `/api/config` endpoints
  - Uses `slog.LevelVar` for atomic, concurrent-safe level switching

### Fixed
- **API batch job ordering** (#86) — Jobs submitted via API now queue in the order paths are submitted, not alphabetically
  - Replaced non-deterministic goroutine append + alphabetical sort with indexed pre-allocation
  - Each concurrent probe writes to its original position, preserving submission order
- **NVIDIA GPU setup simplified** — Pre-filled `NVIDIA_DRIVER_CAPABILITIES` and `NVIDIA_VISIBLE_DEVICES` env vars; users now just add `--runtime=nvidia`
- **Documentation overhaul** — Rewrote GPU setup guides, fixed stale API docs (browse response schema, encoder API, preset fields)

## [2.0.8] - 2026-02-05

### Fixed
- **4K HDR SmartShrink OOM crash** (#90) — VMAF analysis on 4K+ content no longer causes FFmpeg to be OOM-killed in containers
  - Content >1080p is now downscaled to 1080p before VMAF scoring, reducing memory from ~6GB to ~1.5GB per scorer
  - HDR downscale happens in linear light via combined zscale call (linearize + resize in one pass) for mathematically correct resampling
  - SDR downscale uses standard `scale` filter with aspect-ratio preservation (`-2:1080`)
  - Unknown/zero height defaults to 1080 as safety cap
  - Always uses HD VMAF model (`vmaf_v0.6.1`) since scoring happens at ≤1080p

### Changed
- Removed `SelectModel` function — replaced with `vmafModel` constant since only the HD model is ever used

## [2.0.7] - 2026-02-03

### Fixed
- **HDR SmartShrink support** — SmartShrink presets now work with HDR content regardless of `TonemapHDR` setting
  - VMAF scoring now tonemaps both reference and distorted legs to SDR for valid comparison
  - Previously only the reference leg was tonemapped, assuming distorted was already SDR
  - The `TonemapHDR` config setting only affects final transcode output, not VMAF analysis
  - HDR files with missing `color_transfer` metadata are still blocked (required for tonemapping)

### Changed
- Removed unused `tonemap` parameter from `BuildSampleEncodeArgs` — samples stay in native format during VMAF analysis

## [2.0.6] - 2026-02-03

### Changed
- **Parallel VMAF scoring** — Sample VMAF scores are now computed concurrently (up to 3 workers), significantly reducing scoring wall-clock time
- **Container-aware threading** — Thread allocation now uses `GOMAXPROCS` instead of `NumCPU`, correctly respecting container CPU limits
- **Removed CPU throttling** — VMAF scoring and sample extraction no longer run with `nice -n 19`; full CPU utilization improves iteration speed
- **Removed JSON stdout logging** — VMAF filter no longer outputs verbose JSON to stdout; scores parsed from FFmpeg stderr summary

## [2.0.5] - 2026-01-31

### Added
- **Multi-arch Docker builds** — Release images now include `linux/amd64` and `linux/arm64` architectures
- **Manual Docker build trigger** — Workflow dispatch option for on-demand builds
- **Browser refresh button** — Refresh current directory to detect file changes without full page reload

### Changed
- Documentation updated with FAQ for Mac users regarding Docker vs native builds and VideoToolbox availability
- Removed slow integration tests that required real encoding hardware
- Refactored dead code and consolidated DRY violations across job handling and config modules

### Fixed
- **Probe cache invalidation** — Cache now validates using inode+size to detect when files are replaced; refresh button clears cache and reprobes changed files
- **Refresh button animation** — Button performs smooth single rotation instead of barely-visible twitch

## [2.0.4] - 2026-01-30

### Fixed
- **MKV subtitle compatibility filtering** (#77)
  - Transcoding MP4/TS files with `mov_text` subtitles to MKV no longer fails
  - Subtitle streams are probed before transcoding; incompatible codecs are automatically filtered
  - Compatible codecs (srt, ass, ssa, subrip, webvtt, dvb_subtitle, hdmv_pgs) are preserved
  - Incompatible codecs (mov_text, eia_608, tx3g, ttml, dvb_teletext) are dropped with a warning log
  - Graceful fallback: if subtitle probing fails, transcoding proceeds with default behavior

## [2.0.3] - 2026-01-30

### Added
- **Same-codec re-encoding setting in UI** (#80)
  - New toggle in Transcoding settings to allow HEVC→HEVC or AV1→AV1 re-encoding
  - Useful for re-optimizing files encoded elsewhere with suboptimal settings
  - Previously only configurable via config file

### Changed
- SmartShrink presets now respect `allow_same_codec` setting
  - When disabled (default), SmartShrink skips files already in target codec
  - When enabled, SmartShrink will re-analyze and potentially re-encode for better compression

## [2.0.2] - 2026-01-30

### Added
- **Automatic encoder fallback chain** (#77)
  - If hardware encoder fails mid-job, automatically retries with next available encoder
  - Fallback order: Quick Sync → VAAPI → Software (varies by platform)
  - Per-job fallback—subsequent jobs still try preferred encoder first
  - Improves reliability when specific files trigger encoder issues

### Fixed
- VMAF concurrent analyses log no longer appears mid-splash screen
- Goroutine leak when Transcode errors occur before progress monitoring starts

## [2.0.1] - 2026-01-29

### Added
- **Configurable concurrent VMAF analyses** (#79)
  - New `max_concurrent_analyses` setting (1-3) in Advanced settings
  - Controls how many SmartShrink jobs can analyze simultaneously
  - Each analysis uses ~50% CPU (`numCPU/2` threads) for decode/filter operations
  - Multiple concurrent analyses stack (2 = 100% CPU, 3 = 150% over-subscribed)
- **Low-priority VMAF processing**
  - VMAF FFmpeg processes now run with `nice -n 19` (lowest priority)
  - SmartShrink analysis yields to other processes when system is busy
  - Regular transcoding still runs at normal priority

### Fixed
- VMAF analysis no longer pins CPU to 90%+ on idle systems
- Thread limiting via `-threads` and `-filter_threads` flags on all VMAF FFmpeg calls

## [2.0.0] - 2026-01-28

### Added
- **SmartShrink presets with VMAF-guided quality optimization** (#78)
  - New `smartshrink-hevc` and `smartshrink-av1` presets that analyze video content to find optimal compression
  - Uses VMAF (Video Multi-Method Assessment Fusion) to measure perceptual quality
  - Per-job quality selection: Acceptable (85), Good (90), Excellent (94) VMAF targets
  - Binary search algorithm finds the best CRF/bitrate that meets your quality threshold
  - Sample extraction at 5 positions (10%, 30%, 50%, 70%, 90%) with trimmed mean scoring
  - Supports all hardware encoders (NVENC, QSV, VAAPI, VideoToolbox) and software fallback
  - New job phase indicator: `analyzing` shown during VMAF analysis before encoding
  - SmartShrink results displayed in expandable job details after completion
- **New VMAF package** (`internal/ffmpeg/vmaf/`)
  - Automatic VMAF model detection with fallback support
  - HDR tonemapping support for VMAF analysis
  - Parallel VMAF analysis matching worker count
- **UI improvements**
  - Grouped preset dropdown (SmartShrink / Compress / Downscale categories)
  - Category prefix shown in closed dropdown (e.g., "Compress - HEVC")
  - Quality dropdown for SmartShrink presets
  - Preset and quality selections persist to localStorage
  - HW/SW badge in preset dropdown

### Changed
- Database schema updated to v6 (adds SmartShrink job fields)
- Presets now include `is_smart_shrink` and `skips_codec_check` flags
- Job model includes new fields: `Phase`, `SmartShrinkQuality`, `VMafScore`, `SelectedCRF`, `QualityMod`, `SamplesUsed`, `SearchIterations`

## [1.8.5] - 2026-01-22

### Added
- **Same-codec re-encoding option** (#76)
  - New `allow_same_codec` config option (default: false)
  - When enabled, allows HEVC→HEVC or AV1→AV1 transcoding for bitrate optimization
  - Useful for re-encoding at different quality settings without changing codec
  - Config-file only setting (not exposed in UI)

## [1.8.4] - 2026-01-18

### Added
- **Bit depth test file generation** - Added test vectors for 8-bit and 10-bit H.264/HEVC

## [1.8.3] - 2026-01-18

### Fixed
- **Queue sort options** - Minor UI polish

## [1.8.2] - 2026-01-16

### Fixed
- **Stuck decoder hang with unsupported codecs** (#75)
  - VAAPI hardware decode hangs indefinitely when codec isn't supported (e.g., AV1)
  - Added 10-second first-frame timeout watchdog that kills stuck FFmpeg processes
  - Allows existing software decode retry to trigger instead of hanging forever

## [1.8.1] - 2026-01-16

### Added
- **Queue sort options** (#74)
  - New unified filter/sort dropdown menu in queue header
  - Sort by: Default, File Name, Pre-Transcode Size, Post-Transcode Size, Space Saved
  - Click same sort option to toggle direction (↓ natural order, ↑ reverse)
  - Sort preference persists in localStorage
  - Pending jobs now show pre-transcode file size for sorting visibility

## [1.8.0] - 2026-01-15

### Added
- **HDR detection and tonemapping support** (#65)
  - Automatic HDR detection via color metadata (HDR10/PQ, HLG) with 10-bit BT.2020 fallback
  - Optional HDR-to-SDR tonemapping using software zscale filter (works on all systems)
  - HDR badge displayed on jobs and files in the UI
  - Tonemap toggle in Settings (default: OFF to preserve HDR metadata)
  - When tonemapping is disabled, HDR content preserves 10-bit p010 format and BT.2020 color
  - Hardware encoding still used when tonemapping - only the filter runs on CPU
- **Distinct "Skipped" status for already-encoded files** (#72)
  - Files already in target codec now show "Skipped" (yellow) instead of "Failed" (red)
  - Clearer distinction between actual failures and intentionally skipped files
  - Skipped jobs tracked separately in stats
  - "Clear by status" dropdown includes Skipped option

### Fixed
- **Data race in job event broadcasting** (#73)
  - Job events now use shallow copies to prevent race conditions
  - Fixes potential crashes when multiple subscribers receive events during job updates

### Changed
- Database schema updated to v4 (adds `is_hdr` column with automatic migration)
- SSE events now include HDR metadata for frontend display

## [1.7.5] - 2026-01-15

### Fixed
- **QSV/NVENC hardware acceleration broken in Docker** (#67)
  - QSV now tries direct init first, falls back to VAAPI-derived if needed
  - NVENC tries simple init first, falls back to explicit CUDA device init
  - Init mode detected once at startup instead of runtime retry logic
  - RTX 50 series can now attempt H.264 4:2:2 10-bit hardware decode

### Changed
- Simplified worker.go by removing HW init retry logic (detection handles it)
- Updated comments to clarify H.264 High10 (4:2:0) vs 4:2:2 10-bit support

## [1.7.3] - 2026-01-13

### Fixed
- **iOS Safari scrolling on iPad and iPhone** (#42)
  - Clear Queue and Transcode buttons no longer get pushed off-screen on iPad with many queue items
  - Fixed flex container chain with proper `min-height: 0` for scroll containment
  - iPhone landscape now shows both file list and action buttons (preset/transcode)
  - Uses `100dvh` for dynamic viewport height and `clamp()` for responsive list sizing

## [1.7.2] - 2026-01-13

### Fixed
- **FFmpeg -38 error at 97% progress** (#56)
  - H.264 10-bit content now automatically uses software decode (no GPU supports H.264 10-bit hardware decode)
  - Proactive codec detection skips hardware decode for unsupported profiles: VC-1/WMV3, MPEG-4 ASP
  - Adopted Jellyfin's battle-tested FFmpeg hardware acceleration pipeline
  - Removed problematic `vpp_qsv` filter that caused EOF errors with software decode
  - Hardware decode failures now automatically retry with software decode

### Changed
- QSV on Linux now derives from VAAPI for more reliable device initialization
- Consolidated duplicate `copyFile` implementations into shared `util.CopyFile()`
- Removed dead code (unreachable `vpp_qsv` scaling branch)

## [1.7.1] - 2026-01-11

### Fixed
- **Retry no longer leaves stale job entries in queue**
  - Added SSE "removed" event so all connected clients update when a job is deleted
  - Fixes duplicate/ghost jobs appearing after clicking Retry
- **Clear button now respects queue filter** (#55)
  - When a filter is active (e.g., "Failed"), Clear only removes jobs matching that filter
  - Clear button disabled when viewing Running filter (can't clear running jobs)

## [1.7.0] - 2026-01-11

### Added
- **Stop All / Resume button** (#49)
  - New button in queue footer to pause all running jobs
  - Running jobs are requeued and restart from the beginning when resumed
  - Auto-unpauses when new jobs are added to prevent accidental blocking
  - Button syncs state with queue (shows "Resume" when paused)

### Fixed
- **Queue scroll position preserved during job updates** (#51)
  - Progress updates no longer cause the queue list to jump to top
  - Incremental DOM updates instead of full rebuilds for smoother UI
  - Jobs correctly reposition when status changes (e.g., running → complete)
  - Status badge now updates from "Initializing" to "Running" when encoding starts

## [1.6.2] - 2026-01-09

### Fixed
- **iOS Safari bottom bar no longer clips UI**
  - Added `viewport-fit=cover` and `100dvh` viewport height for proper safe area handling
  - Fixes "Clear Queue" button being hidden behind Safari's redesigned bottom navigation in iOS 26

## [1.6.1] - 2026-01-09

### Fixed
- **Jobs no longer lost when reducing worker count** (#43)
  - When reducing concurrent workers (e.g., 3→2), running jobs are now requeued to the front of the pending queue instead of being lost
  - Added `Requeue()` method to safely return interrupted jobs to pending status

## [1.6.0] - 2026-01-09

### Added
- **MP4 output container option** (#44)
  - New setting in Advanced Settings to output MP4 instead of MKV
  - MP4 mode transcodes audio to AAC stereo and strips subtitles for web/direct play compatibility
  - MKV mode (default) preserves all streams as before
- Version displayed in startup splash screen
- Version field added to "Shrinkray started" log line for easier debugging

### Changed
- Updated CI to golangci-lint v2.8.0
- Refreshed splash screen text

## [1.5.0] - 2026-01-09

### Added
- **SQLite database for job persistence** - Jobs are now stored in SQLite instead of JSON for better performance and reliability
  - Automatic migration from queue.json to shrinkray.db on first run
  - JSON backup created before migration (queue.json.backup)
  - Supports large queues (1000+ jobs) with proper indexing
- **Session and lifetime space saved tracking** (#31)
  - Header shows session saved (resets when you choose)
  - Dropdown shows both session and lifetime totals
  - "Reset Session" button to start fresh count
  - Lifetime total persists forever across restarts and queue clears
- Schema versioning for future database migrations

### Changed
- Job queue now uses SQLite with WAL mode for better concurrent access
- Stats calculation moved from in-memory to database queries
- Updated golangci-lint config for v2 format

### Fixed
- Running jobs correctly reset to pending on restart (#35)

## [1.4.12] - 2026-01-08

### Fixed
- Fix running jobs skipped on container restart (#35)
  - When container restarts while jobs are encoding, those jobs now correctly reset to pending
  - Reset state is immediately persisted to disk so it survives further restarts
  - Jobs restart from the beginning in the correct order

## [1.4.11] - 2026-01-08

### Fixed
- Fix QSV encoding failure on MKVs with embedded cover art (#40)
  - MKV files with attached pictures (cover art) have multiple video streams
  - Previous `-map 0` copied all streams, causing QSV to fail encoding JPEG images
  - Now uses explicit stream mapping: `-map 0:v:0 -map 0:a? -map 0:s?`
  - Only maps primary video, all audio, and all subtitles (skips cover art)

### Changed
- Code cleanup: consolidated duplicate utilities, removed dead code, documented magic numbers

## [1.4.10] - 2026-01-08

### Fixed
- All hardware encoders (QSV, VAAPI, NVENC) now automatically retry with software decode when hardware decode fails (#38, #32)
  - Fixes AV1 files on pre-11th gen Intel which can't hardware decode AV1
  - Fixes VC1, MPEG4-ASP, and other codecs that hardware decoders don't support
  - First attempt uses full hardware acceleration
  - If decode fails, automatically retries with software decode + hardware encode
  - Users with capable hardware still get full HW acceleration
  - Fallback is transparent—no user intervention required

## [1.4.9] - 2026-01-08

### Fixed
- Fix QSV encoding failures when hardware decode falls back to software (#32, #38)
  - Changed filter chain to `format=nv12|qsv,hwupload=extra_hw_frames=64`
  - The pipe syntax accepts either CPU frames (nv12) or GPU frames (qsv) without forced conversion
  - HW decode path: zero-copy passthrough (no performance impact)
  - SW decode path: frames uploaded to GPU via hwupload
  - Matches the pattern used by Jellyfin and our working VAAPI implementation

## [1.4.8] - 2026-01-08

### Fixed
- QSV encoding now properly handles software decode fallback (#32, #38)
  - Previous fix using `vpp_qsv` was incorrect - it can't accept CPU frames
  - Now uses `hwupload=extra_hw_frames=64` like Jellyfin does
  - Added `-init_hw_device qsv=qsv -filter_hw_device qsv` for proper device initialization
  - VC1 and other codecs that QSV can't hardware decode now transcode correctly
  - Note: This fix was incomplete - see 1.4.9

## [1.4.7] - 2026-01-08

### Fixed
- QSV encoding now works for compress presets when hardware decode falls back to software (#38)
  - Added `vpp_qsv` base filter to handle CPU-to-QSV frame upload (note: this fix was incomplete, see 1.4.8)
  - Previously only worked for downscale presets (1080p/720p)

## [1.4.6] - 2026-01-07

### Fixed
- Speed and ETA now display correctly during VAAPI hardware encodes (#29)
  - When FFmpeg reports N/A for time/speed, calculate from frame count and elapsed time

## [1.4.5] - 2026-01-07

### Fixed
- Progress bar now works during VAAPI hardware encodes (#29)
  - FFmpeg reports `N/A` for time-based stats with some hardware encoders
  - Added frame-based progress calculation as fallback when time is unavailable

## [1.4.4] - 2026-01-07

### Added
- `keep_larger_files` config option to keep transcoded files even if larger than original
  - Useful for users who want codec consistency across their library

## [1.4.3] - 2026-01-07

### Added
- Preserve original file modification time after transcoding (#33)
  - Fixes compatibility with Unraid Mover Tuning and similar tools that use mtime for file age

## [1.4.2] - 2026-01-07

### Fixed
- QSV scaling now works on Intel UHD 630 and similar iGPUs (#21)
- Replaced `scale_qsv` with `vpp_qsv` using explicit dimensions for downscale presets

## [1.4.1] - 2026-01-06

### Fixed
- QSV (Intel Quick Sync) encoding now works when hardware decode falls back to software (#21)
- VAAPI encoding more reliable with mixed hardware/software decode scenarios (#21)

## [1.4.0] - 2026-01-06

### Added
- Structured logging system with configurable log levels (debug, info, warn, error)
- `log_level` config option to control logging verbosity
- Job lifecycle logging (started, completed, failed, cancelled)
- FFmpeg command logging at debug level for troubleshooting
- FFmpeg stderr capture for better error diagnostics

### Fixed
- Duplicate "Job started" log entries when running multiple workers

## [1.3.7] - 2026-01-06

### Fixed
- "Processing files..." banner no longer persists after single-file jobs start

## [1.3.6] - 2026-01-05

### Fixed
- VAAPI encoding on AMD GPUs now works when hardware decoding falls back to software (#21)
- Added `hwupload` filter to VAAPI transcode pipeline for proper frame format handling

## [1.3.5] - 2026-01-05

### Fixed
- VAAPI hardware encoder detection now works on AMD GPUs (#21)
- Test encode now properly uploads frames to GPU memory before encoding

## [1.3.4] - 2026-01-05

### Fixed
- Auto-detect `/temp` mount in Docker - no manual config needed (#23)
- Downscale presets (1080p/720p) no longer incorrectly skip files already in HEVC/AV1

### Added
- `TEMP_PATH` environment variable support for explicit temp directory override

## [1.3.3] - 2026-01-05

### Fixed
- Auto-create config file on first run with correct absolute paths
- Default `queue_file` now uses absolute path `/config/queue.json` to prevent permission errors

## [1.3.2] - 2026-01-04

### Fixed
- Breadcrumb navigation now shows full path instead of always collapsing to "..." (#22)
- Users can navigate to any intermediate folder, not just Home or immediate parent
- Deep paths (>3 levels) show last 3 folders: `Home / ... / Parent / GrandParent / Current`

## [1.3.1] - 2026-01-04

### Fixed
- Quality slider labels were backwards - now correctly show "Higher quality" on left and "Smaller file" on right (#24)

## [1.3.0] - 2026-01-01

### Added
- Scheduling feature to restrict transcoding to specific hours (e.g., overnight only)
- Schedule settings in Settings panel with enable toggle and time selection
- Schedule status display near transcode button when enabled
- Advanced settings section for quality control (CRF values for HEVC and AV1)
- Collapsible advanced settings panel (closed by default)

### Fixed
- Quality input fields now validate on change instead of every keystroke

## [1.2.0] - 2025-12-29

### Added
- Dark mode toggle
- Infinite scroll for queue display (handles thousands of jobs smoothly)
- Real-time progress feedback for batch file processing
- Allow selecting folders with nested videos for batch transcoding
- Queue sorted by status (running jobs first, then pending, then completed)

### Fixed
- UI no longer hangs when adding thousands of files to queue

## [1.1.0] - 2025-12-28

### Added
- Skip files already encoded in target codec (HEVC/AV1) to prevent unnecessary transcoding
- Skip files already at target resolution when using downscale presets (1080p/720p)
- Version number displayed in Settings panel

## [1.0.0] - 2025-12-25

### Added
- Initial public release
- Hardware-accelerated transcoding (VideoToolbox, NVENC, QSV, VAAPI)
- HEVC and AV1 compression presets
- 1080p and 720p downscale presets
- Batch folder selection for entire TV series
- Async job creation to prevent UI freezes
- Pushover notifications when queue completes
- Retry button for failed jobs
- Mobile-responsive stats bar
- Queue persistence across restarts

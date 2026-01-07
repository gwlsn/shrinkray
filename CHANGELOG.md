# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/).

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

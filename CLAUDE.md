# Shrinkray

## Overview

Shrinkray is a simple, efficient video transcoding app for Unraid. It lets you select a folder (like a TV series) and transcode it to reduce file size. Unlike Unmanic, it's not automatic or library-wide — it's intentional and manual. "This show is 700GB and I want it smaller."

**Core ethos: It just works.** Abstract complexity away from the user. This should feel like an Apple product — clean, simple, but incredibly polished. The user doesn't need to understand ffmpeg, codecs, or CRF values. They pick a folder, see what they'll save, and click go.

## Design Philosophy

### Product Philosophy
- **It just works**: No configuration anxiety, no wrong choices
- **Simple over configurable**: A few perfect presets, not 50 knobs
- **Manual over automatic**: User chooses what to transcode, when
- **Efficient over featureful**: Fast Go backend, minimal resource usage
- **Transparent over magical**: Show estimated savings and time upfront
- **Opinionated**: We make the hard choices so users don't have to

### Visual Design Philosophy
- **Apple-like aesthetic**: Clean, elegant, intentional industrial design
- **Light mode only**: No dark mode. Restriction breeds creativity. This is how it looks best.
- **Timeless and classy**: Think Claude.ai, Wealthsimple, Notion
- **Typography-first**: Elegant, readable font (Inter, SF Pro, or similar)
- **Single accent color**: One anchor color for actions/progress/savings — something confident but not loud
- **Generous whitespace**: Let the interface breathe
- **Subtle animations**: Meaningful motion that indicates state, not decoration
- **Information hierarchy**: The most important info (savings, progress) should be immediately scannable

### UI Principles
- No cluttered dashboards
- No overwhelming options
- No settings pages with 50 toggles
- Big, clear typography
- Progress and savings front and center
- The interface should feel inevitable — like there's no other way it could have been designed

## Tech Stack

- **Backend**: Go 1.22+
- **Frontend**: Embedded web UI (HTML + minimal JS, possibly Alpine.js or htmx for reactivity)
- **Transcoding**: FFmpeg (shelling out via os/exec, not cgo)
- **Container**: Docker, linuxserver.io style (s6-overlay)
- **Config**: YAML file in /config volume
- **State**: In-memory job queue (no database for v1)

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                    Web UI                           │
│  (file browser, preset picker, queue view)          │
└─────────────────────┬───────────────────────────────┘
                      │ HTTP/JSON API
┌─────────────────────▼───────────────────────────────┐
│                  Go Server                          │
│  ┌─────────────┐ ┌─────────────┐ ┌───────────────┐  │
│  │ File Browser│ │  Job Queue  │ │ Progress SSE  │  │
│  │     API     │ │   Manager   │ │   Endpoint    │  │
│  └─────────────┘ └──────┬──────┘ └───────────────┘  │
│                         │                           │
│                  ┌──────▼──────┐                    │
│                  │   Worker    │                    │
│                  │    Pool     │                    │
│                  └──────┬──────┘                    │
│                         │                           │
│                  ┌──────▼──────┐                    │
│                  │   FFmpeg    │                    │
│                  │   Wrapper   │                    │
│                  └─────────────┘                    │
└─────────────────────────────────────────────────────┘
```

## Core Components

### 1. File Browser API

```
GET /api/browse?path=/media/tv/Friends
```

Returns:
- Directory listing with file metadata
- For video files: size, duration, codec, resolution, bitrate (via ffprobe)
- Estimated size after transcoding per preset
- Estimated time to transcode

The browser should feel fast. Cache ffprobe results aggressively.

### 2. Space & Time Estimation

**This is a key differentiator.** Before the user commits, they should see:

- Current total size of selection
- Estimated size after transcoding (show as range if uncertain)
- Estimated savings in GB and percentage
- Estimated time to complete

**Smart warnings:**
- If estimated savings < 20%: "This content is already well-compressed. Transcoding may not save much space."
- If source is already x265: "Already encoded in x265. Re-encoding will save minimal space and may reduce quality."
- If estimated time is very long: Show a realistic time estimate so users know what they're getting into

**Estimation approach:**
- Use bitrate and codec of source to estimate compressibility
- x264 → x265 typically saves 40-60%
- Already x265 or low bitrate: minimal savings
- Estimate time based on duration and a conservative encode speed (0.5x-1x realtime for software encoding)

### 3. Presets

These should be the **absolute best balanced choice in each category**. The preset you want without knowing what you want. Optimized for streaming (Plex/Jellyfin/Emby Direct Play compatibility).

| ID | Name | Use Case | FFmpeg Flags |
|----|------|----------|--------------|
| `compress` | Compress | Reduce size, keep quality and resolution | `-c:v libx265 -crf 22 -preset medium -c:a copy -c:s copy` |
| `compress-hard` | Compress (Smaller) | Prioritize size over quality | `-c:v libx265 -crf 26 -preset medium -c:a copy -c:s copy` |
| `1080p` | 1080p | Downscale to 1080p max | `-vf "scale=-2:'min(ih,1080)'" -c:v libx265 -crf 22 -preset medium -c:a copy -c:s copy` |
| `720p` | 720p | Downscale to 720p max (big savings) | `-vf "scale=-2:'min(ih,720)'" -c:v libx265 -crf 22 -preset medium -c:a copy -c:s copy` |

**Audio handling**: Copy all audio and subtitle streams unchanged (`-c:a copy -c:s copy`). This is the "just works" choice:
- Preserves surround sound, commentary, alternate languages
- Media servers handle audio transcoding on-the-fly if needed
- Re-encoding audio saves minimal space vs. complexity
- Nobody's ever mad their audio was left alone

**Container**: Output to MKV (best compatibility for keeping all streams).

### 4. Job Queue

```go
type Job struct {
    ID            string
    InputPath     string
    OutputPath    string    // temp file during transcode
    PresetID      string
    Status        JobStatus // pending, running, complete, failed
    Progress      float64   // 0-100
    Speed         string    // e.g., "1.2x"
    ETA           string    // estimated time remaining
    StartedAt     time.Time
    CompletedAt   time.Time
    Error         string
    InputSize     int64
    OutputSize    int64     // populated after completion
    SpaceSaved    int64     // InputSize - OutputSize
}

type JobStatus string

const (
    StatusPending   JobStatus = "pending"
    StatusRunning   JobStatus = "running"
    StatusComplete  JobStatus = "complete"
    StatusFailed    JobStatus = "failed"
    StatusCancelled JobStatus = "cancelled"
)
```

Queue operations:
- Add job(s) — from folder selection
- Cancel job (and clean up temp file)
- Get queue status
- Get job progress (SSE stream)

### 5. FFmpeg Wrapper

Responsibilities:
- Probe files with ffprobe (`-print_format json`)
- Build ffmpeg command from preset
- Parse progress from stderr (`frame=`, `time=`, `speed=`)
- Handle output to temp file, then atomic rename/replace
- Preserve file permissions and timestamps where possible

Key behaviors:
- Output to `{filename}.shrinkray.tmp.mkv` during transcode
- On success: either replace original or keep both (per config)
- On failure: delete temp file, mark job failed
- Always copy all streams (audio, subtitles) unless preset specifies otherwise
- Use `-map 0` to ensure all streams are included

### 6. Progress Tracking

FFmpeg stderr parsing:
```
frame=  123 fps=45 q=28.0 size=    1234kB time=00:01:23.45 bitrate= 123.4kbits/s speed=1.5x
```

Extract:
- Current time → compare to duration → percentage
- Speed → estimate remaining time
- Current output size → live space savings indicator

Deliver via Server-Sent Events (SSE) at `/api/jobs/stream`. Not websockets — SSE is simpler and sufficient.

### 7. Web UI

**Pages/Views:**

1. **Home/Browse**
   - Clean file browser showing media library
   - Folder cards showing total size, file count
   - Click into folder to see contents
   - Video files show: name, size, resolution, codec
   - Already-compressed indicators (x265 badge, etc.)

2. **Selection & Estimation**
   - After selecting a folder/files, show:
     - Total current size
     - Preset selector (simple cards, not dropdown)
     - Estimated size after (with savings %)
     - Estimated time
     - Warnings if applicable
   - Big, clear "Start" button
   - This should feel like a checkout flow — clear what you're getting

3. **Queue/Progress**
   - Currently running job with progress bar, speed, ETA
   - Pending jobs listed below
   - Completed jobs with space saved
   - Cancel button (with confirmation)

4. **Settings** (minimal)
   - Original file handling: Replace / Keep both
   - Worker count (default 1)
   - That's probably it for v1

**UI Implementation:**
- Use Go's `embed` package to bundle static assets
- Vanilla HTML/CSS/JS or Alpine.js for reactivity
- No heavy frameworks — keep it light and fast
- CSS custom properties for theming consistency
- Transitions for state changes (progress updates, job completion)

## API Endpoints

```
GET  /api/browse?path=...          # List directory with video metadata
GET  /api/browse/estimate?path=... # Get size/time estimates for selection
GET  /api/presets                  # List available presets
POST /api/jobs                     # Create job(s) { paths: [...], preset: "..." }
GET  /api/jobs                     # List all jobs
GET  /api/jobs/:id                 # Get single job
DELETE /api/jobs/:id              # Cancel/remove job
GET  /api/jobs/stream              # SSE stream of job updates
GET  /api/config                   # Get current config
PUT  /api/config                   # Update config
```

## Configuration

`/config/shrinkray.yaml`:

```yaml
# Directory to browse (mounted media library)
media_path: /media

# What to do with original files after successful transcode
# Options: replace, keep
original_handling: replace

# Number of concurrent transcode jobs (default 1, usually best for CPU encoding)
workers: 1

# FFmpeg/FFprobe paths (defaults to binaries in PATH)
ffmpeg_path: ffmpeg
ffprobe_path: ffprobe
```

## Docker

Dockerfile based on linuxserver.io patterns:

```dockerfile
FROM ghcr.io/linuxserver/baseimage-alpine:3.19

# Install ffmpeg
RUN apk add --no-cache ffmpeg

# Copy the Go binary
COPY shrinkray /app/shrinkray

# Expose web UI port
EXPOSE 8080

# Config and media volumes
VOLUME /config /media

ENTRYPOINT ["/app/shrinkray"]
```

**Environment variables** (lsio style):
- `PUID` — User ID
- `PGID` — Group ID
- `TZ` — Timezone

## File Structure

```
shrinkray/
├── cmd/
│   └── shrinkray/
│       └── main.go              # Entry point
├── internal/
│   ├── api/
│   │   ├── handler.go           # HTTP handlers
│   │   ├── router.go            # Route setup
│   │   └── sse.go               # SSE implementation
│   ├── config/
│   │   └── config.go            # Config loading/saving
│   ├── ffmpeg/
│   │   ├── probe.go             # ffprobe wrapper
│   │   ├── transcode.go         # ffmpeg wrapper
│   │   ├── progress.go          # Progress parsing
│   │   └── estimate.go          # Size/time estimation
│   ├── jobs/
│   │   ├── job.go               # Job struct and types
│   │   ├── queue.go             # Queue management
│   │   └── worker.go            # Worker pool
│   └── browse/
│       └── browse.go            # File browser logic
├── web/
│   ├── static/
│   │   ├── css/
│   │   │   └── style.css        # Styles (elegant, minimal)
│   │   └── js/
│   │       └── app.js           # UI logic
│   └── templates/
│       └── index.html           # Main template
├── Dockerfile
├── docker-compose.yml           # For local testing
├── go.mod
├── go.sum
├── README.md
└── CLAUDE.md                    # This file
```

## Development

```bash
# Run locally
go run ./cmd/shrinkray

# With a test media folder
MEDIA_PATH=/path/to/test/media go run ./cmd/shrinkray

# Build
go build -o shrinkray ./cmd/shrinkray

# Docker build
docker build -t shrinkray .

# Docker run
docker run -d \
  --name shrinkray \
  -p 8080:8080 \
  -v /path/to/config:/config \
  -v /path/to/media:/media \
  -e PUID=1000 \
  -e PGID=1000 \
  shrinkray
```

## v1 Scope (MVP)

**Must have:**
- [ ] Browse media directory
- [ ] Show video file metadata (size, codec, resolution, duration)
- [ ] Select folder
- [ ] Show estimated space savings before committing
- [ ] Show estimated time before committing
- [ ] Warning for already-compressed content
- [ ] Preset selection (4 presets)
- [ ] Queue jobs for folder
- [ ] Transcode with live progress (percentage, speed, ETA)
- [ ] Replace or keep original
- [ ] Clean, Apple-like web UI
- [ ] Docker container (lsio style)

**Not in v1:**
- Dark mode (intentionally excluded)
- Hardware acceleration (v2)
- Custom presets (v2)
- Job history/persistence (v2 — needs SQLite)
- Total space saved counter (v2 — needs persistence)
- Notifications/webhooks
- Automatic library scanning
- Scheduling
- Plex/Jellyfin integration

## Notes for Claude Code

### General
- This should feel like a premium product despite being a homelab tool
- When in doubt, choose simplicity
- Test with real media files of various codecs/sizes

### Backend
- Use `os/exec` for ffmpeg/ffprobe, not cgo bindings
- Parse ffprobe output as JSON (`-print_format json -v quiet`)
- Use Go's `embed` package to embed all web assets into the binary
- SSE for progress updates — simpler than websockets
- Be careful with path handling — runs in container with mounted volumes
- Always use temp files and atomic rename for safety
- Default to copying all streams (`-map 0 -c:a copy -c:s copy`)
- Handle ffmpeg failures gracefully — clean up temp files

### Frontend
- Light mode only, no theme switching
- Use CSS custom properties for consistent styling
- Smooth transitions for progress updates
- Mobile-responsive but desktop-first
- Test in Chrome, Firefox, Safari
- Keep JS minimal — this isn't a SPA, it's a tool

### Estimation
- Be conservative with time estimates (assume 0.5x-1x realtime)
- Be honest with space estimates (show ranges if uncertain)
- Don't promise savings that won't materialize

### Error Handling
- Clear, human-readable error messages
- If a job fails, show why (disk full, ffmpeg error, etc.)
- Never leave temp files behind on failure

# Shrinkray

Simple, efficient video transcoding for your media library.

Shrinkray lets you select a folder (like a TV series) and transcode it to reduce file size. Unlike automated tools, it's intentional and manual — "This show is 700GB and I want it smaller."

## Features

- **Clean web UI** — Browse your media library, select files, see estimated savings
- **Hardware acceleration** — Automatic VideoToolbox support on macOS (NVENC/VAAPI coming)
- **Smart presets** — Compress, compress smaller, downscale to 1080p/720p
- **Space estimation** — See how much you'll save before committing
- **Progress tracking** — Real-time progress, speed, and ETA
- **Safe by default** — Original files renamed to `.old`, not deleted

## Quick Start

```bash
# Build
go build -o shrinkray ./cmd/shrinkray

# Run (point to your media folder)
./shrinkray -media /path/to/media

# Open http://localhost:8080
```

## Docker

```bash
docker run -d \
  --name shrinkray \
  -p 8080:8080 \
  -v /path/to/config:/config \
  -v /path/to/media:/media \
  -e PUID=1000 \
  -e PGID=1000 \
  ghcr.io/graysonwilson/shrinkray
```

## Presets

| Preset | Description | Target |
|--------|-------------|--------|
| Compress | Reduce size, keep quality | ~50% of source bitrate |
| Compress (Smaller) | Prioritize size over quality | ~35% of source bitrate |
| 1080p | Downscale to 1080p max | ~50% of source bitrate |
| 720p | Downscale to 720p max | ~50% of source bitrate |

All presets:
- Use HEVC (H.265) encoding
- Copy all audio and subtitle streams unchanged
- Output to MKV container

## Configuration

Configuration is stored in `config/shrinkray.yaml`:

```yaml
# Directory to browse
media_path: /media

# What to do with originals: replace or keep
original_handling: replace

# Concurrent transcode jobs (1 recommended)
workers: 1

# FFmpeg paths (defaults to PATH)
ffmpeg_path: ffmpeg
ffprobe_path: ffprobe
```

## How It Works

1. **Browse** — Navigate your media library in the web UI
2. **Select** — Choose files or folders to transcode
3. **Estimate** — See space savings and time estimates
4. **Transcode** — Watch progress in real-time
5. **Done** — Original renamed to `.old`, new file in place

## Requirements

- Go 1.22+ (for building)
- FFmpeg with HEVC support
- For hardware encoding: VideoToolbox (macOS), NVENC (NVIDIA), or VAAPI (Intel/AMD)

## License

MIT

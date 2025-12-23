# Shrinkray

A simple video transcoding tool for Unraid. Select a folder, pick a preset, and shrink your media library.

## Quick Start (Unraid)

1. Install from Community Applications (search "Shrinkray") or add manually:
   - **Repository**: `ghcr.io/gwlsn/shrinkray:latest`
   - **WebUI**: `8080`
   - **Volumes**: `/config` → appdata, `/media` → your media library

2. Open the WebUI, browse to a folder, select files, and click **Start Transcode**

## Quick Start (Docker)

```bash
docker run -d \
  --name shrinkray \
  -p 8080:8080 \
  -e PUID=1000 \
  -e PGID=1000 \
  -v /path/to/config:/config \
  -v /path/to/media:/media \
  ghcr.io/gwlsn/shrinkray:latest
```

For hardware acceleration, add the appropriate device:

```bash
# Intel QSV
--device /dev/dri:/dev/dri

# NVIDIA (requires nvidia-container-toolkit)
--runtime=nvidia --gpus all
```

## Presets

| Preset | Description |
|--------|-------------|
| **Compress (HEVC)** | Re-encode to H.265, typically 50-60% smaller |
| **Compress (AV1)** | Re-encode to AV1, maximum compression |
| **1080p** | Downscale to 1080p + HEVC |
| **720p** | Downscale to 720p + HEVC |

All presets copy audio and subtitles unchanged.

## Hardware Acceleration

Automatically detected and used when available:

| Platform | Encoder |
|----------|---------|
| Intel | Quick Sync (QSV) |
| NVIDIA | NVENC |
| AMD (Linux) | VAAPI |
| macOS | VideoToolbox |

Falls back to software encoding if no hardware is available.

## Settings

Access via the gear icon in the WebUI:

- **Original files**: Delete after transcode, or keep as `.old`
- **Concurrent jobs**: 1-6 simultaneous transcodes
- **Pushover notifications**: Get notified when all jobs complete

## Pushover Notifications

1. Create an app at [pushover.net](https://pushover.net)
2. Enter your **User Key** and **App Token** in Settings
3. Check **"Notify when done"** in the queue header before starting jobs

You'll receive a notification with job counts and total space saved when the queue empties.

## Configuration

Config is stored in `/config/shrinkray.yaml`. Most settings are available in the WebUI, but you can also set:

```yaml
temp_path: /tmp/shrinkray  # Use fast storage for temp files during transcode
```

## Building from Source

```bash
go build -o shrinkray ./cmd/shrinkray
./shrinkray -media /path/to/media
```

Requires Go 1.22+ and FFmpeg with HEVC/AV1 support.

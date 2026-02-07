<div align="center">
  <img src="web/templates/logo.png" alt="Shrinkray" width="128" height="128">
  <h1>Shrinkray</h1>
  <p><strong>Simple, user-friendly video transcoding</strong></p>
  <p>Select a folder. Pick a preset. Shrink your media library.</p>

  ![Version](https://img.shields.io/github/v/release/gwlsn/shrinkray?style=flat-square&label=version)
  ![Go](https://img.shields.io/badge/go-1.25+-00ADD8?style=flat-square&logo=go)
  ![License](https://img.shields.io/badge/license-MIT-green?style=flat-square)
  ![Docker](https://img.shields.io/badge/docker-ghcr.io-2496ED?style=flat-square&logo=docker)
</div>

---

## What is Shrinkray?

Shrinkray is a user-friendly video transcoding tool designed to be simple from the ground up.

### Features

- **SmartShrink** — VMAF-guided auto-optimization finds the smallest file size while maintaining your target quality
- **6 Presets** — SmartShrink (HEVC/AV1), Compress (HEVC/AV1), Downscale (1080p/720p)
- **Full GPU Pipeline** — Hardware decoding AND encoding with software fallback
- **HDR Support** — Automatic HDR detection with optional HDR-to-SDR tonemapping
- **Batch Selection** — Select entire folders to transcode whole seasons or libraries at once
- **Scheduling** — Restrict transcoding to specific hours (e.g., overnight only)
- **Quality Control** — Adjustable CRF for fine-tuned compression, or let SmartShrink decide
- **Queue Management** — Sort by name, size, or date; filter by status; pause/resume
- **Push Notifications** — Pushover alerts when your queue completes
- **Smart Skipping** — Automatically skips files already in target codec/resolution

---

## Screenshot

<div align="center">
  <img src="docs/screenshot.png" alt="Shrinkray UI" width="800">
  <p><em>Clean, focused interface: browse, select, transcode</em></p>
</div>

---

## Quick Start

### Unraid

1. Search **"Shrinkray"** in Community Applications and install
2. Set your paths: `/config` → appdata location, `/media` → media library
3. **GPU setup (recommended):**
   - **NVIDIA:** Install the Nvidia-Driver plugin (reboot after), add `--runtime=nvidia` to Extra Parameters, set `NVIDIA_VISIBLE_DEVICES=all` and `NVIDIA_DRIVER_CAPABILITIES=all` as environment variables
   - **Intel / AMD:** Add `--device=/dev/dri` to Extra Parameters
4. Open the WebUI at port **8080**

### Docker Compose

```yaml
services:
  shrinkray:
    image: ghcr.io/gwlsn/shrinkray:latest
    container_name: shrinkray
    ports:
      - 8080:8080
    volumes:
      - /path/to/config:/config
      - /path/to/media:/media
      - /path/to/fast/storage:/temp  # Optional: SSD for temp files
    environment:
      - PUID=1000
      - PGID=1000  # For Intel/AMD GPU: set to group that owns /dev/dri/renderD128
    # GPU: uncomment the section for your hardware
    # --- Intel / AMD ---
    # devices:
    #   - /dev/dri:/dev/dri
    # --- NVIDIA ---
    # runtime: nvidia
    # environment:
    #   - NVIDIA_VISIBLE_DEVICES=all
    #   - NVIDIA_DRIVER_CAPABILITIES=all
    restart: unless-stopped
```

### Podman

Podman rootless requires `userns_mode: keep-id` for PUID/PGID to work correctly. Without it, UID namespace remapping causes permission errors on mounted volumes. See the [FAQ](docs/FAQ.md#permission-errors-with-podman-rootless) for details.

```yaml
services:
  shrinkray:
    image: ghcr.io/gwlsn/shrinkray:latest
    userns_mode: keep-id
    environment:
      - PUID=1000
      - PGID=1000
    volumes:
      - ./config:/config
      - ./media:/media
      - ./temp:/temp
    devices:
      - /dev/dri:/dev/dri
    restart: unless-stopped
```

### From Source

Requires Go 1.25+ and FFmpeg with HEVC/AV1 encoder support.

```bash
go build -o shrinkray ./cmd/shrinkray
./shrinkray -media /path/to/media
```

Open `http://localhost:8080` in your browser. GPU acceleration is used automatically if available.

---

## Presets

| Preset | Codec | Description |
|--------|-------|-------------|
| **SmartShrink (HEVC)** | H.265 | Auto-optimization via VMAF (SDR only) |
| **SmartShrink (AV1)** | AV1 | Auto-optimization via VMAF (SDR only) |
| **Compress (HEVC)** | H.265 | Re-encode to HEVC |
| **Compress (AV1)** | AV1 | Re-encode to AV1 |
| **1080p** | HEVC | Downscale 4K to 1080p |
| **720p** | HEVC | Downscale to 720p |

### SmartShrink Quality Tiers

SmartShrink analyzes your video using VMAF to find the optimal compression settings. SDR content only — HDR files are skipped (use a Compress preset or enable tonemapping for HDR content).

| Quality | VMAF Target | Description |
|---------|-------------|-------------|
| **Acceptable** | 85 | Noticeable but acceptable compression |
| **Good** | 90 | Minimal perceptible difference (default) |
| **Excellent** | 94 | Visually lossless |

By default (MKV output), audio is copied unchanged and compatible subtitles are preserved (incompatible formats like `mov_text` are automatically filtered with a warning). MP4 output mode converts audio to AAC stereo and strips subtitles for web compatibility.

---

## Hardware Acceleration

Shrinkray automatically detects and uses the best available hardware encoder. No configuration required, just pass through your GPU. If hardware encoding fails mid-job, Shrinkray automatically falls back to the next available encoder (e.g., Quick Sync → VAAPI → Software).

### Supported Hardware

| Platform | Requirements |
|----------|--------------|
| **NVIDIA (NVENC)** | GTX 1050+ / RTX series |
| **Intel (Quick Sync)** | 6th gen+ CPU or Arc GPU |
| **AMD (VAAPI)** | Polaris+ GPU on Linux |
| **Apple (VideoToolbox)** | Any Mac (M1+) |

> **Mac users:** The Docker image works on Apple Silicon, but containers run Linux and cannot access macOS VideoToolbox. For hardware-accelerated encoding, run Shrinkray natively ([From Source](#from-source)).

### Unraid GPU Passthrough

**NVIDIA:**
1. Install the **Nvidia-Driver** plugin from Community Applications and reboot
2. Add `--runtime=nvidia` to Extra Parameters
3. Add environment variables: `NVIDIA_VISIBLE_DEVICES=all` and `NVIDIA_DRIVER_CAPABILITIES=all`

**Intel / AMD:**
1. Add `--device=/dev/dri` to Extra Parameters

### Docker GPU Passthrough

**NVIDIA** (requires [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/install-guide.html)):

```yaml
services:
  shrinkray:
    runtime: nvidia
    environment:
      - NVIDIA_VISIBLE_DEVICES=all
      - NVIDIA_DRIVER_CAPABILITIES=all
```

**Intel / AMD:**

```yaml
services:
  shrinkray:
    devices:
      - /dev/dri:/dev/dri
```

See the [FAQ](docs/FAQ.md#hardware-acceleration) for detailed setup and troubleshooting.

### Verifying Detection

Check the Shrinkray logs at startup to see which encoders were detected. The active encoder is marked with an asterisk. Each job in your queue displays an "HW" or "SW" badge indicating hardware or software encoding.

---

## Scheduling

Restrict transcoding to specific hours to reduce system load during the day.

1. Open **Settings** (gear icon)
2. Enable **Schedule transcoding**
3. Set start and end hours (e.g., 22:00 – 06:00 for overnight)

**Behavior:**
- Jobs can always be added to the queue
- Transcoding only runs during the allowed window
- Running jobs complete even if the window closes
- Jobs automatically resume when the window reopens

---

## Configuration

Configuration is stored in `/config/shrinkray.yaml`. Most settings are available in the WebUI.

| Setting | Default | Description |
|---------|---------|-------------|
| `media_path` | `/media` | Root directory to browse |
| `temp_path` | *(empty)* | Fast storage for temp files (SSD recommended) |
| `original_handling` | `replace` | `replace` = delete original, `keep` = rename to `.old` |
| `workers` | `1` | Concurrent transcode jobs (1–6) |
| `quality_hevc` | `0` | CRF override for HEVC (0 = default, range: 15–40) |
| `quality_av1` | `0` | CRF override for AV1 (0 = default, range: 20–50) |
| `schedule_enabled` | `false` | Enable time-based scheduling |
| `schedule_start_hour` | `22` | Hour transcoding may start (0–23) |
| `schedule_end_hour` | `6` | Hour transcoding must stop (0–23) |
| `pushover_user_key` | *(empty)* | Pushover user key for notifications |
| `pushover_app_token` | *(empty)* | Pushover app token for notifications |
| `log_level` | `info` | Logging verbosity: `debug`, `info`, `warn`, `error` |
| `keep_larger_files` | `false` | Keep transcoded files even if larger than original |
| `allow_same_codec` | `false` | Allow HEVC→HEVC or AV1→AV1 re-encoding |
| `output_format` | `mkv` | Output container: `mkv` (preserves all streams) or `mp4` (web compatible) |
| `tonemap_hdr` | `false` | Convert HDR content to SDR (uses CPU tonemapping) |
| `tonemap_algorithm` | `hable` | Tonemapping algorithm: `hable`, `bt2390`, `reinhard`, `mobius`, `clip`, `linear`, `gamma` |
| `max_concurrent_analyses` | `1` | Simultaneous SmartShrink VMAF analyses (1–3) |

### Example Configuration

```yaml
media_path: /media
temp_path: /tmp/shrinkray
original_handling: replace
workers: 2
quality_hevc: 24
schedule_enabled: true
schedule_start_hour: 22
schedule_end_hour: 6
log_level: info  # Use "debug" for troubleshooting
```

---

## Pushover Notifications

Get push notifications when your transcode queue completes.

1. Create an application at [pushover.net](https://pushover.net)
2. Copy your **User Key** and **API Token**
3. Enter both in Shrinkray → Settings
4. Enable **"Notify when done"** before starting jobs

Notifications include: completed/failed job counts, total space saved.

---

## Documentation

| Topic | Description |
|-------|-------------|
| [FAQ](docs/FAQ.md) | Common questions about CPU usage, skipped files, HDR |
| [API Reference](docs/api/) | REST API endpoints and SSE events |
| [Architecture](docs/architecture/) | System design, hardware acceleration, package structure |

---

## AI Disclosure

This project was developed with AI assistance. Claude intentionally left as a contributor. All generated code is manually reviewed.

---

## License

MIT License - see [LICENSE](LICENSE) for details.

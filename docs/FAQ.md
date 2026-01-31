# FAQ

Quick answers to common questions. Each section covers a topic in depth.

## Contents

- [Getting started](#getting-started)
- [Configuration](#configuration)
- [Hardware acceleration](#hardware-acceleration)
- [Presets and quality](#presets-and-quality)
- [Troubleshooting](#troubleshooting)
- [API and integration](#api-and-integration)

---

## Getting started

### What is Shrinkray?

A simple video transcoding tool with a web UI. Point it at your media library, pick a preset, and compress your files using hardware acceleration when available.

### What are the requirements?

- **Docker** or **Go 1.22+** for building from source
- **FFmpeg** with HEVC/AV1 encoder support (included in Docker image)
- Optional: GPU for hardware acceleration (NVIDIA, Intel, AMD, or Apple Silicon)

### How do I run Shrinkray?

**Docker Compose** (recommended):

```yaml
services:
  shrinkray:
    image: ghcr.io/gwlsn/shrinkray:latest
    ports:
      - 8080:8080
    volumes:
      - /path/to/config:/config
      - /path/to/media:/media
```

**Unraid**: Search "Shrinkray" in Community Applications and configure paths.

**From source**:

```bash
go build -o shrinkray ./cmd/shrinkray
./shrinkray -media /path/to/media -port 8080
```

Open `http://localhost:8080` in your browser.

### Where are config and data stored?

- **Config file**: `/config/shrinkray.yaml`
- **Database**: `/config/shrinkray.db` (SQLite)
- **Temp files**: Same directory as source (configurable via `temp_path`)

### Can I run Shrinkray in Docker on Mac?

Yes, the Docker image supports Apple Silicon (M1/M2/M3/M4). However, Docker containers run Linux and cannot access macOS VideoToolbox, so encoding uses software (CPU) only.

**For hardware-accelerated encoding on Mac**, run natively:

```bash
brew install ffmpeg
go build -o shrinkray ./cmd/shrinkray
./shrinkray -media /path/to/media
```

Docker is still useful if you prefer containerization and don't mind slower software encoding.

---

## Configuration

### Where is the config file?

`/config/shrinkray.yaml` in Docker, or the path specified with `-config` flag when running from source.

### What settings are available?

| Setting | Default | Description |
|---------|---------|-------------|
| `media_path` | `/media` | Root directory to browse |
| `temp_path` | *(empty)* | Fast storage for temp files (SSD recommended) |
| `original_handling` | `replace` | `replace` = delete original, `keep` = rename to `.old` |
| `workers` | `1` | Concurrent transcode jobs (1-6) |
| `max_concurrent_analyses` | `1` | Simultaneous SmartShrink VMAF analyses (1-3) |
| `quality_hevc` | `0` | CRF override for HEVC (0 = encoder default, range: 15-40) |
| `quality_av1` | `0` | CRF override for AV1 (0 = encoder default, range: 20-50) |
| `schedule_enabled` | `false` | Enable time-based scheduling |
| `schedule_start_hour` | `22` | Hour transcoding may start (0-23) |
| `schedule_end_hour` | `6` | Hour transcoding must stop (0-23) |
| `output_format` | `mkv` | Output container: `mkv` or `mp4` |
| `tonemap_hdr` | `false` | Convert HDR to SDR (uses CPU) |
| `tonemap_algorithm` | `hable` | Algorithm: `hable`, `bt2390`, `reinhard`, `mobius` |
| `keep_larger_files` | `false` | Keep output even if larger than original |
| `log_level` | `info` | Logging: `debug`, `info`, `warn`, `error` |

Most settings are editable via the web UI (Settings gear icon).

### Can I use environment variables?

Not directly. Edit `/config/shrinkray.yaml` or use the web UI.

### How do I set up Pushover notifications?

1. Create an application at [pushover.net](https://pushover.net)
2. Copy your **User Key** and **API Token**
3. Enter both in Settings
4. Enable "Notify when done" before starting jobs

Notifications include completed/failed counts and total space saved.

### Can I schedule transcoding for overnight only?

Yes. Enable scheduling in Settings and set start/end hours. Example: start at 22 (10 PM), end at 6 (6 AM).

**Behavior:**
- Jobs can always be added to the queue
- Transcoding only runs during the allowed window
- Running jobs complete even if the window closes
- Jobs automatically resume when the window reopens

---

## Hardware acceleration

### Which encoders does Shrinkray support?

| Platform | Encoder | HEVC | AV1 | Docker Flags |
|----------|---------|------|-----|--------------|
| **NVIDIA** | NVENC | GTX 1050+ | RTX 40+ | `--runtime=nvidia --gpus all` |
| **Intel** | Quick Sync | 6th gen+ | Arc GPUs | `--device /dev/dri:/dev/dri` |
| **AMD** | VAAPI | Polaris+ | RX 7000+ | `--device /dev/dri:/dev/dri` |
| **Apple** | VideoToolbox | Any Mac | M3+ | Native (no Docker) |
| **None** | Software | Always | Always | No flags needed |

### How does encoder detection work?

At startup, Shrinkray:
1. Queries FFmpeg for available encoders
2. Tests each with a 1-frame encode
3. Selects the first working encoder in priority order: VideoToolbox > NVENC > QSV > VAAPI > Software

Check the logs at startup to see which encoders were detected. The active encoder is marked with an asterisk.

### What happens if hardware encoding fails mid-job?

Shrinkray automatically tries the next available encoder. For example, if Quick Sync fails on a specific file, it falls back to VAAPI, then to software encoding. This fallback chain means jobs complete even when specific hardware encoders have issues with certain content.

The fallback happens per-job, not globally—other jobs still try the preferred encoder first.

### How do I verify GPU acceleration is working?

1. **Check startup logs**: Look for encoder detection output
2. **Check job badges**: Each job shows "HW" (hardware) or "SW" (software)
3. **Check GPU utilization**: Use `nvidia-smi`, `radeontop`, or `intel_gpu_top`

### Why is my CPU usage 10-40% with hardware encoding?

This is normal. The GPU handles video encode/decode, but the CPU still handles:
- Demuxing (parsing input container)
- Muxing (writing output container)
- Audio/subtitle stream copying
- FFmpeg process overhead

### Why does my AMD GPU show 0% usage?

Standard monitoring tools may show 0% even when hardware encoding works correctly. AMD GPUs use a dedicated video engine (UVD/VCN) not reported by generic tools. Use `radeontop` which shows UVD/VCN utilization separately.

### Intel Quick Sync (QSV) not working?

Common causes on Linux (non-Unraid):

1. **Missing device passthrough**: Add `--device /dev/dri:/dev/dri`
2. **Permission issues**: Set `PUID`/`PGID` matching your host user
3. **Wrong group**: Your user must be in `video` or `render` group

Troubleshooting:

```bash
# Check device permissions
ls -la /dev/dri

# Check your groups
id

# Add yourself to video group if needed
sudo usermod -aG video $USER
# Then re-login
```

Try `PUID=0` temporarily to confirm it's a permissions issue.

See [Hardware acceleration docs](architecture/hardware.md) for details.

### What hardware supports AV1 encoding?

AV1 hardware encoding requires recent GPUs:

| Platform | Minimum Hardware |
|----------|------------------|
| **NVIDIA** | RTX 40 series (Ada Lovelace) |
| **Intel** | Arc GPUs, Gen 14+ iGPUs |
| **Apple** | M3 chip or newer |
| **AMD** | RX 7000 series (RDNA 3) |

Older hardware falls back to software AV1 encoding (significantly slower but still works).

---

## Presets and quality

### What presets are available?

| Preset | Codec | Description | Typical Savings |
|--------|-------|-------------|-----------------|
| **Compress (HEVC)** | H.265 | Re-encode to HEVC | 40-60% smaller |
| **Compress (AV1)** | AV1 | Maximum compression | 50-70% smaller |
| **1080p** | HEVC | Downscale 4K to 1080p | 60-80% smaller |
| **720p** | HEVC | Downscale to 720p | 70-85% smaller |
| **SmartShrink (HEVC)** | H.265 | VMAF-guided auto-optimization | Varies by content |
| **SmartShrink (AV1)** | AV1 | VMAF-guided auto-optimization | Varies by content |

### Which preset should I use?

- **SmartShrink**: Best results. Analyzes your video to find optimal compression. Uses more CPU during analysis.
- **Compress (HEVC)**: Fast, fixed quality. Best balance of compatibility and savings.
- **Compress (AV1)**: Maximum compression but requires newer playback devices.
- **1080p/720p**: For 4K content you don't need in full resolution.

### Why does SmartShrink use so much CPU?

SmartShrink uses VMAF (Video Multi-Method Assessment Fusion) to analyze video quality. VMAF is CPU-only—there's no GPU acceleration. To minimize impact:

- VMAF processes run at lowest CPU priority (`nice -n 19`) and yield to other work
- Each analysis uses ~50% of CPU cores for decode/filter operations
- Configure concurrent analyses (1-3) in Advanced settings based on your system

### How do quality settings work?

Shrinkray uses CRF (Constant Rate Factor) for quality control. Lower values = higher quality = larger files.

| Codec | Default | Range | Recommendation |
|-------|---------|-------|----------------|
| HEVC | 26 (software) / encoder-specific | 15-40 | 22-28 for most content |
| AV1 | 35 (software) / encoder-specific | 20-50 | 30-38 for most content |

Hardware encoders use their own quality modes (CQ, QP, bitrate) but Shrinkray normalizes the interface.

### What happens if the output is larger than the input?

By default, Shrinkray rejects larger outputs and keeps the original unchanged.

To always keep outputs (for codec consistency across your library), set `keep_larger_files: true` in config.

### Are audio and subtitles preserved?

| Format | Audio | Subtitles |
|--------|-------|-----------|
| **MKV** (default) | Copied unchanged | Compatible codecs preserved* |
| **MP4** | Transcoded to AAC stereo | Stripped (PGS incompatible) |

*MKV preserves most subtitle formats (srt, ass, ssa, pgs, dvb). Some MP4/TS-specific formats (mov_text, eia_608) are automatically filtered with a warning since they're incompatible with MKV containers.

### How does Shrinkray handle HDR content?

Two modes:

**HDR Passthrough** (default, `tonemap_hdr: false`):
- Preserves HDR metadata and 10-bit color
- Uses Main10 profile with BT.2020 color space
- Output plays correctly on HDR displays

**HDR to SDR Tonemapping** (`tonemap_hdr: true`):
- Converts HDR to SDR using CPU (zscale)
- Outputs 8-bit SDR video
- Useful for devices without HDR support

Tonemapping algorithms: `hable` (filmic, default), `bt2390`, `reinhard`, `mobius`.

### Can I create custom presets or FFmpeg settings?

No. Shrinkray is intentionally simple. You can adjust quality via the CRF slider, but full FFmpeg customization is out of scope. Use FFmpeg directly for advanced workflows.

---

## Troubleshooting

### Why did Shrinkray skip some files?

Files are automatically skipped if:
- Already encoded in the target codec (HEVC for HEVC preset, AV1 for AV1 preset)
- Already at or below target resolution (for 1080p/720p presets)

Skipped files show status `skipped` with an explanation.

### A job failed. How do I retry it?

Click the retry button on the failed job, or use the API:

```bash
curl -X POST http://localhost:8080/api/jobs/{id}/retry
```

The file is re-probed and a new job is created.

### Jobs show "SW" badge but I have a GPU. Why?

Software decoding (not encoding) is used when:
- Source codec isn't supported by hardware decoder (exotic codecs, VC-1)
- Source is H.264 10-bit (High10 profile)
- HDR tonemapping is enabled (requires CPU processing)
- AV1 preset on GPU without AV1 hardware support

Encoding still uses GPU when available. Check logs for details.

### Encoding is slow. What can I do?

1. **Verify GPU is detected**: Check startup logs
2. **Reduce quality**: Higher CRF = faster (e.g., HEVC 28 instead of 22)
3. **Use HEVC instead of AV1**: HEVC is faster on most hardware
4. **Use SSD for temp files**: Set `temp_path` to fast storage
5. **Increase workers**: If you have headroom, try 2-4 workers

### A job is stuck at 0% progress

Possible causes:
- FFmpeg failed immediately (check logs)
- Source file is corrupted
- Disk full
- Permission issues

Try cancelling and retrying the job. Enable `log_level: debug` for more details.

### Transcoded file plays incorrectly

- **No audio**: Source may have incompatible audio. Try MP4 mode which transcodes to AAC.
- **Wrong colors**: HDR content may need tonemapping for SDR displays
- **Green/corrupted frames**: Hardware encoder issue. Try software fallback by disabling GPU passthrough temporarily.

### How do I check FFmpeg logs?

Enable debug logging:

```yaml
log_level: debug
```

Docker logs will include FFmpeg stderr output for each job.

### Jobs disappear after restart

Jobs are persisted to `/config/shrinkray.db` (SQLite). Ensure:
- The `/config` volume is mounted correctly
- Container has write permission to the directory

---

## API and integration

### Does Shrinkray have an API?

Yes. Full REST API for automation:

```bash
# List jobs
curl http://localhost:8080/api/jobs

# Create jobs
curl -X POST http://localhost:8080/api/jobs \
  -H "Content-Type: application/json" \
  -d '{"paths": ["/media/Movie.mkv"], "preset_id": "compress-hevc"}'

# Cancel job
curl -X DELETE http://localhost:8080/api/jobs/{id}
```

See [API Reference](api/README.md) for all endpoints.

### How do I get real-time updates?

Subscribe to the SSE stream:

```javascript
const events = new EventSource('/api/jobs/stream');
events.onmessage = (event) => {
  const data = JSON.parse(event.data);
  console.log(data.type, data.job);
};
```

Event types: `init`, `added`, `progress`, `complete`, `failed`, `skipped`, `cancelled`, `removed`.

See [Jobs API](api/jobs.md) for all event types and payloads.

### What is the job lifecycle?

```
pending → running → complete/failed/cancelled
                 ↘ skipped (auto-skip at creation)
```

- **pending**: Waiting in queue
- **running**: FFmpeg processing
- **complete**: Finished successfully
- **failed**: Transcode error
- **cancelled**: User cancelled
- **skipped**: Already meets criteria

See [Job Lifecycle](architecture/job-lifecycle.md) for state diagrams.

### Can I pause and resume the queue?

Yes:

```bash
# Pause (stops running jobs)
curl -X POST http://localhost:8080/api/queue/pause

# Resume
curl -X POST http://localhost:8080/api/queue/resume
```

Paused jobs are requeued at the front and resume when unpaused.

### How do I clear completed jobs?

```bash
# Clear all non-running jobs
curl -X POST http://localhost:8080/api/jobs/clear

# Clear only completed
curl -X POST http://localhost:8080/api/jobs/clear?status=complete

# Clear only failed
curl -X POST http://localhost:8080/api/jobs/clear?status=failed
```

---

## More resources

- [FFmpeg Options](ffmpeg-options.md) - Encoder settings and quality flags
- [API Reference](api/README.md) - REST endpoints and SSE events
- [Architecture](architecture/README.md) - System design and packages
- [Hardware Acceleration](architecture/hardware.md) - GPU detection and codec support

# Presets and encoders

Query available transcoding presets and detected hardware encoders.

## List presets

```
GET /api/presets
```

Returns all available presets with their assigned encoder.

**Response:**

```json
[
  {
    "id": "smartshrink-hevc",
    "name": "SmartShrink (HEVC) [HW]",
    "description": "Auto-optimize with VMAF analysis",
    "encoder": "nvenc",
    "codec": "hevc",
    "max_height": 0,
    "is_smart_shrink": true
  },
  {
    "id": "smartshrink-av1",
    "name": "SmartShrink (AV1) [HW]",
    "description": "Auto-optimize with VMAF analysis",
    "encoder": "nvenc",
    "codec": "av1",
    "max_height": 0,
    "is_smart_shrink": true
  },
  {
    "id": "compress-hevc",
    "name": "Compress (HEVC) [HW]",
    "description": "Reduce size with HEVC encoding",
    "encoder": "nvenc",
    "codec": "hevc",
    "max_height": 0,
    "is_smart_shrink": false
  },
  {
    "id": "compress-av1",
    "name": "Compress (AV1) [HW]",
    "description": "Maximum compression with AV1 encoding",
    "encoder": "nvenc",
    "codec": "av1",
    "max_height": 0,
    "is_smart_shrink": false
  },
  {
    "id": "1080p",
    "name": "Downscale to 1080p [HW]",
    "description": "Downscale to 1080p max (HEVC)",
    "encoder": "nvenc",
    "codec": "hevc",
    "max_height": 1080,
    "is_smart_shrink": false
  },
  {
    "id": "720p",
    "name": "Downscale to 720p [HW]",
    "description": "Downscale to 720p (big savings)",
    "encoder": "nvenc",
    "codec": "hevc",
    "max_height": 720,
    "is_smart_shrink": false
  }
]
```

### Preset fields

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | Preset identifier for API calls |
| `name` | string | Human-readable name (includes [HW]/[SW] suffix) |
| `description` | string | Brief description |
| `encoder` | string | Assigned hardware encoder |
| `codec` | string | Target codec: `hevc` or `av1` |
| `max_height` | int | Max output height (0 = no scaling) |
| `is_smart_shrink` | bool | True for VMAF-based SmartShrink presets |

### SmartShrink presets

SmartShrink presets use VMAF analysis to find optimal compression settings. They require VMAF support in FFmpeg and are hidden if VMAF is not available. **SDR content only** — HDR files are skipped when using SmartShrink presets.

When creating jobs with SmartShrink presets, include the `smartshrink_quality` field:

```json
{
  "paths": ["/media/video.mkv"],
  "preset_id": "smartshrink-hevc",
  "smartshrink_quality": "good"
}
```

Quality options:
- `acceptable` - VMAF 85 (noticeable but acceptable compression)
- `good` - VMAF 90 (minimal perceptible difference, default)
- `excellent` - VMAF 94 (visually lossless)

## List encoders

```
GET /api/encoders
```

Returns all detected hardware encoders and which one is selected as best.

**Response:**

```json
{
  "encoders": [
    {
      "accel": "nvenc",
      "codec": "hevc",
      "name": "NVIDIA NVENC",
      "description": "NVIDIA hardware encoding (HEVC)",
      "encoder": "hevc_nvenc",
      "available": true
    },
    {
      "accel": "nvenc",
      "codec": "av1",
      "name": "NVIDIA NVENC",
      "description": "NVIDIA hardware encoding (AV1)",
      "encoder": "av1_nvenc",
      "available": true
    },
    {
      "accel": "none",
      "codec": "hevc",
      "name": "Software",
      "description": "CPU encoding (HEVC)",
      "encoder": "libx265",
      "available": true
    },
    {
      "accel": "none",
      "codec": "av1",
      "name": "Software",
      "description": "CPU encoding (AV1)",
      "encoder": "libsvtav1",
      "available": true
    }
  ],
  "best": {
    "accel": "nvenc",
    "codec": "hevc",
    "name": "NVIDIA NVENC",
    "description": "NVIDIA hardware encoding (HEVC)",
    "encoder": "hevc_nvenc",
    "available": true
  },
  "vmaf_available": true,
  "vmaf_models": ["vmaf_v0.6.1"]
}
```

The `encoders` array contains one entry per accel+codec combination (e.g., separate entries for NVENC HEVC and NVENC AV1). Only available encoders are included. The `best` field returns the best available HEVC encoder.

### Encoder fields

| Field | Type | Description |
|-------|------|-------------|
| `accel` | string | Acceleration type: `nvenc`, `qsv`, `vaapi`, `videotoolbox`, `none` |
| `codec` | string | Target codec: `hevc` or `av1` |
| `name` | string | Human-readable encoder name |
| `description` | string | Encoder description |
| `encoder` | string | FFmpeg encoder name (e.g., `hevc_nvenc`, `libx265`) |
| `available` | bool | Whether encoder was successfully detected |

## Hardware encoder priority

Shrinkray automatically selects the best available encoder in this order:

1. **Apple VideoToolbox** - Native macOS hardware encoding
2. **NVIDIA NVENC** - Best quality/speed balance, wide GPU support
3. **Intel Quick Sync** - Good for Intel CPUs with integrated graphics
4. **AMD VAAPI** - Linux AMD GPU encoding
5. **Software** - CPU fallback (libx265/libsvtav1)

## Codec support by hardware

| Platform | HEVC | AV1 |
|----------|------|-----|
| NVIDIA NVENC | GTX 1050+ | RTX 40 series+ |
| Intel QSV | 6th gen+ | Arc GPUs, 14th gen+ |
| AMD VAAPI | Polaris+ | RX 7000+ |
| Apple VideoToolbox | Any Mac | M3+ |
| Software | Always | Always |

If hardware AV1 encoding is not available, AV1 presets fall back to software encoding (significantly slower).

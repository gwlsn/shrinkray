# Config API

Read and update Shrinkray configuration at runtime.

## Get configuration

```
GET /api/config
```

Returns current configuration with encoder defaults.

**Response:**

```json
{
  "version": "2.0.8",
  "media_path": "/media",
  "original_handling": "replace",
  "workers": 2,
  "max_concurrent_analyses": 1,
  "has_temp_path": true,
  "pushover_user_key": "u...",
  "pushover_app_token": "a...",
  "pushover_configured": true,
  "notify_on_complete": false,
  "quality_hevc": 0,
  "quality_av1": 0,
  "default_quality_hevc": 28,
  "default_quality_av1": 32,
  "schedule_enabled": true,
  "schedule_start_hour": 22,
  "schedule_end_hour": 6,
  "output_format": "mkv",
  "tonemap_hdr": false,
  "tonemap_algorithm": "hable",
  "allow_same_codec": false
}
```

### Response fields

| Field | Type | Description |
|-------|------|-------------|
| `version` | string | Shrinkray version |
| `media_path` | string | Root media directory |
| `original_handling` | string | `replace` or `keep` |
| `workers` | int | Number of concurrent workers |
| `max_concurrent_analyses` | int | Simultaneous SmartShrink VMAF analyses (1-3) |
| `has_temp_path` | bool | Whether a temp path is configured |
| `pushover_user_key` | string | Pushover user key |
| `pushover_app_token` | string | Pushover app token |
| `pushover_configured` | bool | Whether Pushover credentials are set |
| `notify_on_complete` | bool | Send notification when queue empties |
| `quality_hevc` | int | HEVC CRF override (0 = use default) |
| `quality_av1` | int | AV1 CRF override (0 = use default) |
| `default_quality_hevc` | int | Default CRF for detected HEVC encoder |
| `default_quality_av1` | int | Default CRF for detected AV1 encoder |
| `schedule_enabled` | bool | Time-based scheduling enabled |
| `schedule_start_hour` | int | Hour transcoding starts (0-23) |
| `schedule_end_hour` | int | Hour transcoding stops (0-23) |
| `output_format` | string | Output container: `mkv` or `mp4` |
| `tonemap_hdr` | bool | Convert HDR to SDR |
| `tonemap_algorithm` | string | Tonemapping algorithm |
| `allow_same_codec` | bool | Allow same-codec re-encoding |

## Update configuration

```
PUT /api/config
```

Update configuration settings. Only include fields you want to change.

**Request body:**

```json
{
  "workers": 2,
  "quality_hevc": 24,
  "schedule_enabled": true,
  "schedule_start_hour": 22,
  "schedule_end_hour": 6
}
```

### Updatable fields

| Field | Type | Constraints | Description |
|-------|------|-------------|-------------|
| `original_handling` | string | `replace` or `keep` | What to do with originals |
| `workers` | int | 1-6 | Concurrent transcode jobs |
| `max_concurrent_analyses` | int | 1-3 | Simultaneous VMAF analyses for SmartShrink |
| `pushover_user_key` | string | | Pushover user key |
| `pushover_app_token` | string | | Pushover app token |
| `notify_on_complete` | bool | | Enable completion notification |
| `quality_hevc` | int | 15-40 | CRF for HEVC (lower = higher quality) |
| `quality_av1` | int | 20-50 | CRF for AV1 (lower = higher quality) |
| `schedule_enabled` | bool | | Enable time-based scheduling |
| `schedule_start_hour` | int | 0-23 | When transcoding may start |
| `schedule_end_hour` | int | 0-23 | When transcoding must stop |
| `output_format` | string | `mkv` or `mp4` | Output container format |
| `tonemap_hdr` | bool | | Enable HDR to SDR conversion |
| `tonemap_algorithm` | string | See below | Tonemapping algorithm |
| `allow_same_codec` | bool | | Allow HEVC→HEVC or AV1→AV1 re-encoding |

### Tonemapping algorithms

| Algorithm | Description |
|-----------|-------------|
| `hable` | Filmic curve, good for movies (default) |
| `bt2390` | ITU-R BT.2390, broadcast standard |
| `reinhard` | Classic reinhard, natural look |
| `mobius` | Smooth highlight rolloff |
| `clip` | Simple clipping, may lose highlights |
| `linear` | Linear scaling |
| `gamma` | Gamma-based compression |

**Response:**

```json
{
  "status": "updated"
}
```

**Errors:**
- `400` - Invalid field value (with error message)
- `500` - Failed to save config to disk

## Test Pushover

```
POST /api/pushover/test
```

Send a test notification to verify Pushover credentials.

**Response:**

```json
{
  "status": "Test notification sent"
}
```

**Errors:**
- `400` - Pushover not configured, or invalid credentials

## Notes

- Changes are persisted to `/config/shrinkray.yaml`
- Worker count changes take effect immediately (running jobs complete normally)
- VMAF scoring uses frame subsampling (every 5th frame) and runs samples in parallel for faster analysis; thread allocation respects container CPU limits
- SmartShrink is SDR-only. HDR files are skipped
- Some settings (`media_path`, `temp_path`, `keep_larger_files`) can only be changed in the config file
- Quality value of 0 means "use encoder-specific default"
- `allow_same_codec: true` enables HEVC→HEVC or AV1→AV1 re-encoding for bitrate optimization

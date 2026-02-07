# Jobs API

Manage transcoding jobs and receive real-time updates.

## Create jobs

```
POST /api/jobs
```

Add files or directories to the transcoding queue. The endpoint responds immediately and processes files in the background. New jobs appear via the SSE stream.

**Request body:**

```json
{
  "paths": ["/media/Movies/Movie.mkv", "/media/TV/Show"],
  "preset_id": "compress-hevc"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `paths` | string[] | Yes | File or directory paths to transcode |
| `preset_id` | string | Yes | Preset ID (see below) |
| `smartshrink_quality` | string | For SmartShrink | Quality tier: `acceptable`, `good`, `excellent` |

**Preset IDs:** `compress-hevc`, `compress-av1`, `smartshrink-hevc`, `smartshrink-av1`, `1080p`, `720p`

SmartShrink presets require the `smartshrink_quality` field and only work with SDR content (HDR files are skipped). See [Presets](presets.md#smartshrink-presets) for quality tier details.

**Response** (202 Accepted):

```json
{
  "status": "processing",
  "message": "Processing 5 paths in background..."
}
```

When directories are provided, Shrinkray recursively discovers video files and adds them to the queue. Progress is reported via SSE `discovery_progress` events.

## List jobs

```
GET /api/jobs
```

Returns all jobs in queue order with aggregate statistics.

**Response:**

```json
{
  "jobs": [
    {
      "id": "1705432100000-1",
      "input_path": "/media/Movies/Movie.mkv",
      "output_path": "/media/Movies/Movie.mkv",
      "preset_id": "compress-hevc",
      "encoder": "nvenc",
      "is_hardware": true,
      "status": "complete",
      "progress": 100,
      "speed": 2.5,
      "eta": "",
      "input_size": 4294967296,
      "output_size": 2147483648,
      "space_saved": 2147483648,
      "duration_ms": 7200000,
      "bitrate": 5000000,
      "width": 1920,
      "height": 1080,
      "frame_rate": 23.976,
      "video_codec": "h264",
      "profile": "High",
      "bit_depth": 8,
      "is_hdr": false,
      "transcode_secs": 1200,
      "created_at": "2024-01-16T10:00:00Z",
      "started_at": "2024-01-16T10:05:00Z",
      "completed_at": "2024-01-16T10:25:00Z"
    }
  ],
  "stats": {
    "pending": 3,
    "running": 1,
    "complete": 10,
    "failed": 0,
    "cancelled": 0,
    "skipped": 2,
    "total": 16,
    "total_saved": 10737418240,
    "session_saved": 10737418240,
    "lifetime_saved": 107374182400
  }
}
```

## Get single job

```
GET /api/jobs/{id}
```

**Response:** Single job object (same structure as in list).

**Errors:**
- `404` - Job not found

## Cancel job

```
DELETE /api/jobs/{id}
```

Cancel a pending or running job. Running jobs are stopped mid-transcode and temp files are cleaned up.

**Response:**

```json
{
  "status": "cancelled"
}
```

**Errors:**
- `404` - Job not found
- `409` - Job already in terminal state

## Retry failed job

```
POST /api/jobs/{id}/retry
```

Re-probe the source file and create a new job with the same preset. The failed job is removed.

**Response:** New job object.

**Errors:**
- `404` - Job not found
- `400` - Job is not in failed state, or file no longer exists

## Clear queue

```
POST /api/jobs/clear
```

Remove jobs from the queue. Running jobs are never cleared.

**Query parameters:**

| Parameter | Description |
|-----------|-------------|
| `status` | Optional. Only clear jobs with this status: `pending`, `complete`, `failed`, `skipped`, `cancelled` |

**Examples:**

```bash
# Clear all non-running jobs
curl -X POST http://localhost:8080/api/jobs/clear

# Clear only completed jobs
curl -X POST http://localhost:8080/api/jobs/clear?status=complete
```

**Response:**

```json
{
  "cleared": 5,
  "message": "Cleared 5 jobs"
}
```

## Queue control

### Pause queue

```
POST /api/queue/pause
```

Stop all running jobs and prevent new jobs from starting. Running jobs are cancelled and requeued at the front.

**Response:**

```json
{
  "paused": true,
  "requeued": 2
}
```

### Resume queue

```
POST /api/queue/resume
```

Allow workers to pick up pending jobs again.

**Response:**

```json
{
  "paused": false
}
```

## Real-time updates (SSE)

```
GET /api/jobs/stream
```

Server-Sent Events stream for real-time job updates. Connect with `EventSource` in browsers or any SSE client.

```javascript
const events = new EventSource('/api/jobs/stream');

events.onmessage = (event) => {
  const data = JSON.parse(event.data);
  console.log(data.type, data);
};
```

### Event types

| Type | Description | Payload |
|------|-------------|---------|
| `init` | Initial state on connection | `{ jobs: [...], stats: {...} }` |
| `added` | Single job added | `{ job: {...} }` |
| `jobs_added` | Batch of jobs added | `{ count: 10 }` |
| `discovery_progress` | File discovery progress | `{ probed: 5, total: 20 }` |
| `started` | Job started processing | `{ job: {...} }` |
| `progress` | Job progress update | `{ job: {...} }` |
| `complete` | Job completed | `{ job: {...} }` |
| `failed` | Job failed | `{ job: {...} }` |
| `skipped` | Job skipped (already target codec) | `{ job: {...} }` |
| `cancelled` | Job cancelled | `{ job: {...} }` |
| `requeued` | Job returned to queue | `{ job: {...} }` |
| `removed` | Job removed from queue | `{ job: { id: "..." } }` |
| `notify_sent` | Pushover notification sent | `{}` |

### Job status values

| Status | Description |
|--------|-------------|
| `pending` | Waiting in queue |
| `running` | Currently transcoding |
| `complete` | Finished successfully |
| `failed` | Transcode error |
| `cancelled` | Cancelled by user |
| `skipped` | Skipped (already in target codec/resolution) |

## Statistics

```
GET /api/stats
```

Get current queue statistics.

**Response:**

```json
{
  "pending": 3,
  "running": 1,
  "complete": 10,
  "failed": 0,
  "cancelled": 0,
  "skipped": 2,
  "total": 16,
  "total_saved": 10737418240,
  "session_saved": 10737418240,
  "lifetime_saved": 107374182400
}
```

### Reset session stats

```
POST /api/stats/reset-session
```

Reset the session saved counter (useful after clearing the queue).

**Response:**

```json
{
  "status": "session reset"
}
```

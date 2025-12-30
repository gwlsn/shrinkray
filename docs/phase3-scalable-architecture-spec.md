# Phase 3: Scalable Architecture Specification

**Version**: 1.0
**Status**: AWAITING APPROVAL
**Target Scale**: 20,000 concurrent jobs with smooth UI

---

## 1. END-TO-END JOB LIFECYCLE

### 1.1 Current Lifecycle (Blocking)

```
┌──────────────────────────────────────────────────────────────────────────────┐
│ CURRENT: User clicks "Start Transcode" with 10,000 files                     │
└──────────────────────────────────────────────────────────────────────────────┘

Time=0s      │ POST /api/jobs {paths: [...], preset_id: "hevc"}
             │
             ↓
Time=0.1s    │ HTTP 202 Accepted (immediate)
             │ → Background goroutine starts
             │
             ↓
Time=0.1s    │ discoverMediaFiles() walks directory tree
to 1s        │ Returns: [path1, path2, ... path10000]
             │
             ↓
Time=1s      │ FOR EACH path (in parallel):
to ~17min    │   goroutine: ffprobe $path  (~100ms each)
             │   wg.Wait() ← BLOCKS until ALL 10,000 complete
             │
             │ ⚠️ UI FROZEN: No jobs visible for 17 minutes
             │
             ↓
Time=~17min  │ AddMultiple() called with 10,000 ProbeResults
             │ FOR EACH probe:
             │   queue.Add() → broadcast "added" event
             │
             │ ⚠️ 10,000 SSE events in ~100ms = browser freeze
             │
             ↓
Time=~17min+ │ Workers start processing (1-6 concurrent)
             │ Each job: Start → Progress → Complete/Failed
```

### 1.2 New Lifecycle (Streaming)

```
┌──────────────────────────────────────────────────────────────────────────────┐
│ NEW: User clicks "Start Transcode" with 10,000 files                         │
└──────────────────────────────────────────────────────────────────────────────┘

Time=0s      │ POST /api/jobs {paths: [...], preset_id: "hevc"}
             │
             ↓
Time=0.01s   │ HTTP 202 Accepted (immediate)
             │ → Background discovery starts
             │
             ↓
Time=0.01s   │ filepath.Walk() finds video1.mp4
             │ → AddPendingProbe(path, fileSize) → buffer
             │ filepath.Walk() finds video2.mp4
             │ → AddPendingProbe(path, fileSize) → buffer
             │ ... continues streaming
             │
             ↓
Time=0.11s   │ Batcher timer fires (100ms) OR buffer full (50 jobs)
             │ → broadcast "batch_added" with 50 jobs
             │ → UI shows 50 jobs immediately (status: pending_probe)
             │
             ↓
Time=0.12s   │ Worker 1 picks job (status: pending_probe)
             │ → ffprobe video1.mp4 (~100ms)
             │ → Check skip reason (codec already HEVC?)
             │ → If skip: FailJob("Already HEVC")
             │ → Else: UpdateProbeData() → broadcast "probed"
             │
             ↓
Time=0.22s   │ Worker 1 starts transcode (status: running)
             │ → broadcast "started"
             │ → Progress updates every ~1s
             │ → broadcast "progress" (delta payload)
             │
             │ Meanwhile: Discovery continues in parallel
             │ More batch_added events arrive
             │ Workers 2-6 pick up jobs concurrently
             │
             ↓
Time varies  │ Complete/Failed: broadcast "complete"/"failed"
             │
             │ ✅ First job visible in <200ms
             │ ✅ All jobs visible in ~20s (not 17 min)
             │ ✅ SSE events batched: ~200 events (not 10,000)
```

### 1.3 Lifecycle State Machine

```
                                        ┌────────────────┐
                                        │   (discovered) │
                                        └───────┬────────┘
                                                │ AddPendingProbe()
                                                ↓
┌──────────────────────────────────────────────────────────────────────────────┐
│                              pending_probe                                    │
│  • File path known                                                           │
│  • File size known (from stat)                                               │
│  • Duration, Bitrate, Codec = UNKNOWN                                        │
│  • UI shows: "Queued for processing"                                         │
└─────────────────────────────────┬────────────────────────────────────────────┘
                                  │ Worker picks job
                                  │ → ffprobe()
                                  │
              ┌───────────────────┴───────────────────┐
              │                                       │
              ↓ Probe failed                          ↓ Probe succeeded
┌─────────────────────────┐              ┌─────────────────────────────────────┐
│         failed          │              │ Check skip reason                   │
│  Error: "probe error"   │              │ (codec, resolution)                 │
└─────────────────────────┘              └────────────┬────────────────────────┘
                                                      │
                          ┌───────────────────────────┴───────────────┐
                          │                                           │
                          ↓ Should skip                               ↓ Should transcode
        ┌─────────────────────────────┐                ┌──────────────────────────────────┐
        │           failed            │                │            pending               │
        │  Error: "Already HEVC"      │                │  • Duration, Bitrate now known   │
        │  Error: "Already 720p"      │                │  • UI shows: "Pending"           │
        └─────────────────────────────┘                └───────────────┬──────────────────┘
                                                                       │ StartJob()
                                                                       ↓
                                              ┌────────────────────────────────────────────┐
                                              │                 running                    │
                                              │  • Progress, Speed, ETA updating           │
                                              │  • UI shows: progress bar                  │
                                              └───────────────────────┬────────────────────┘
                                                                      │
                                    ┌─────────────────┬───────────────┴──────────────┐
                                    │                 │                              │
                                    ↓                 ↓                              ↓
                          ┌─────────────────┐ ┌─────────────────┐          ┌─────────────────┐
                          │    complete     │ │     failed      │          │   cancelled     │
                          │  SpaceSaved > 0 │ │  Error message  │          │  User cancelled │
                          └─────────────────┘ └─────────────────┘          └─────────────────┘
```

---

## 2. DATA MODELS

### 2.1 Job Status Enum (Updated)

```go
// internal/jobs/job.go

type Status string

const (
    // NEW: Job discovered but not probed yet
    // File path and size are known; duration, bitrate, codec are unknown
    StatusPendingProbe Status = "pending_probe"

    // Existing statuses (unchanged semantics)
    StatusPending   Status = "pending"    // Probed, waiting for worker
    StatusRunning   Status = "running"    // Transcoding in progress
    StatusComplete  Status = "complete"   // Successfully completed
    StatusFailed    Status = "failed"     // Error occurred
    StatusCancelled Status = "cancelled"  // User cancelled
)

// IsTerminal returns true if the job cannot transition to another state
func (s Status) IsTerminal() bool {
    return s == StatusComplete || s == StatusFailed || s == StatusCancelled
}

// IsActive returns true if the job is being worked on
func (s Status) IsActive() bool {
    return s == StatusPendingProbe || s == StatusPending || s == StatusRunning
}
```

### 2.2 Job Struct (Updated)

```go
// internal/jobs/job.go

type Job struct {
    // Identity
    ID        string `json:"id"`
    InputPath string `json:"input_path"`
    PresetID  string `json:"preset_id"`

    // Output (populated after transcode)
    OutputPath string `json:"output_path,omitempty"`
    TempPath   string `json:"temp_path,omitempty"`

    // Encoder info (known at creation time)
    Encoder    string `json:"encoder"`     // "nvenc", "qsv", "none", etc.
    IsHardware bool   `json:"is_hardware"`

    // Status
    Status   Status  `json:"status"`
    Progress float64 `json:"progress"` // 0-100
    Speed    float64 `json:"speed"`    // Encoding speed multiplier (e.g., 2.5x)
    ETA      string  `json:"eta"`      // Human-readable ETA

    // Error info (for failed jobs)
    Error      string   `json:"error,omitempty"`
    Stderr     string   `json:"stderr,omitempty"`     // Last 64KB of ffmpeg stderr
    ExitCode   int      `json:"exit_code,omitempty"`
    FFmpegArgs []string `json:"ffmpeg_args,omitempty"`

    // File metrics
    InputSize  int64 `json:"input_size"`  // Always known (from stat)
    OutputSize int64 `json:"output_size"` // Known after complete
    SpaceSaved int64 `json:"space_saved"` // InputSize - OutputSize

    // Media metrics (unknown until probed)
    // For pending_probe jobs, these are 0
    Duration int64 `json:"duration"` // Milliseconds
    Bitrate  int64 `json:"bitrate"`  // Bits per second

    // Timestamps
    CreatedAt   time.Time `json:"created_at"`
    StartedAt   time.Time `json:"started_at,omitempty"`
    CompletedAt time.Time `json:"completed_at,omitempty"`

    // Transcode metrics
    TranscodeTime int64 `json:"transcode_secs,omitempty"` // Seconds

    // Software fallback tracking (unchanged)
    IsSoftwareFallback bool   `json:"is_software_fallback,omitempty"`
    OriginalJobID      string `json:"original_job_id,omitempty"`
    FallbackReason     string `json:"fallback_reason,omitempty"`

    // NEW: Probe status tracking
    ProbeError string `json:"probe_error,omitempty"` // Set if probe failed
}
```

### 2.3 JobEvent Struct (Updated)

```go
// internal/jobs/job.go

type JobEvent struct {
    Type string `json:"type"`

    // Single job update (existing events)
    Job *Job `json:"job,omitempty"`

    // NEW: Batch of jobs (for batch_added event)
    Jobs []*Job `json:"jobs,omitempty"`

    // NEW: Lightweight progress update (for progress event)
    ProgressUpdate *ProgressUpdate `json:"progress_update,omitempty"`

    // Stats (for init event)
    Stats *Stats `json:"stats,omitempty"`
}

// NEW: Minimal progress payload (avoids sending full Job struct)
type ProgressUpdate struct {
    ID       string  `json:"id"`
    Progress float64 `json:"progress"`
    Speed    float64 `json:"speed"`
    ETA      string  `json:"eta"`
}
```

### 2.4 Stats Struct (Updated)

```go
// internal/jobs/queue.go

type Stats struct {
    PendingProbe int   `json:"pending_probe"` // NEW: Awaiting probe
    Pending      int   `json:"pending"`       // Probed, waiting for worker
    Running      int   `json:"running"`
    Complete     int   `json:"complete"`
    Failed       int   `json:"failed"`
    Cancelled    int   `json:"cancelled"`
    Total        int   `json:"total"`
    TotalSaved   int64 `json:"total_saved"` // Bytes saved by completed jobs
}
```

---

## 3. SSE EVENT SHAPES

### 3.1 Event Type: `init` (Connection Established)

**Trigger**: Client connects to `/api/jobs/stream`

**Current Payload** (unchanged for backwards compatibility):
```json
{
  "type": "init",
  "jobs": [
    {
      "id": "1735530000000000000-1",
      "input_path": "/media/video1.mp4",
      "preset_id": "hevc",
      "status": "pending",
      "input_size": 1073741824,
      "duration": 7200000,
      "bitrate": 12000000,
      ...
    }
  ],
  "stats": {
    "pending_probe": 0,
    "pending": 5,
    "running": 2,
    "complete": 100,
    "failed": 3,
    "cancelled": 0,
    "total": 110,
    "total_saved": 10737418240
  }
}
```

**Note**: For Phase 3.E (pagination), `jobs` array will be limited to first 100 items. Total count in `stats.total`.

### 3.2 Event Type: `added` (Single Job Added)

**Trigger**: `queue.Add()` for single job (backwards compatibility)

**Payload**:
```json
{
  "type": "added",
  "job": {
    "id": "1735530000000000000-50",
    "input_path": "/media/video50.mp4",
    "preset_id": "hevc",
    "encoder": "nvenc",
    "is_hardware": true,
    "status": "pending",
    "input_size": 2147483648,
    "duration": 3600000,
    "bitrate": 8000000,
    "created_at": "2024-12-30T10:00:00Z"
  }
}
```

### 3.3 Event Type: `batch_added` (NEW - Batch of Jobs)

**Trigger**: `EventBatcher.Flush()` after 50 jobs collected OR 100ms timer

**Payload**:
```json
{
  "type": "batch_added",
  "jobs": [
    {
      "id": "1735530000000000000-1",
      "input_path": "/media/folder/video1.mp4",
      "preset_id": "hevc",
      "encoder": "nvenc",
      "is_hardware": true,
      "status": "pending_probe",
      "input_size": 1073741824,
      "duration": 0,
      "bitrate": 0,
      "created_at": "2024-12-30T10:00:00.001Z"
    },
    {
      "id": "1735530000000000000-2",
      "input_path": "/media/folder/video2.mp4",
      "preset_id": "hevc",
      "encoder": "nvenc",
      "is_hardware": true,
      "status": "pending_probe",
      "input_size": 536870912,
      "duration": 0,
      "bitrate": 0,
      "created_at": "2024-12-30T10:00:00.002Z"
    }
  ]
}
```

**Frontend Handling**:
```javascript
case 'batch_added':
    // Append all jobs at once
    for (const job of data.jobs) {
        cachedJobs.push(job);
        jobIndex.set(job.id, cachedJobs.length - 1);
    }
    cachedStats.pending_probe += data.jobs.length;
    updateStats(cachedStats);
    virtualQueue.addJobs(data.jobs);  // Single DOM update
    break;
```

### 3.4 Event Type: `probed` (NEW - Job Probe Complete)

**Trigger**: Worker successfully probes a `pending_probe` job

**Payload**:
```json
{
  "type": "probed",
  "job": {
    "id": "1735530000000000000-1",
    "input_path": "/media/folder/video1.mp4",
    "preset_id": "hevc",
    "encoder": "nvenc",
    "is_hardware": true,
    "status": "pending",
    "input_size": 1073741824,
    "duration": 7200000,
    "bitrate": 12000000,
    "created_at": "2024-12-30T10:00:00.001Z"
  }
}
```

**Note**: Status transitions from `pending_probe` to `pending`. Duration and Bitrate now populated.

### 3.5 Event Type: `started` (Job Started)

**Trigger**: `queue.StartJob()` (unchanged)

**Payload**:
```json
{
  "type": "started",
  "job": {
    "id": "1735530000000000000-1",
    "status": "running",
    "progress": 0,
    "speed": 0,
    "eta": "",
    "started_at": "2024-12-30T10:00:05Z",
    ...
  }
}
```

### 3.6 Event Type: `progress` (NEW Format - Delta Update)

**Trigger**: `queue.UpdateProgress()` (every ~1 second during transcode)

**NEW Payload** (compact delta):
```json
{
  "type": "progress",
  "progress_update": {
    "id": "1735530000000000000-1",
    "progress": 45.2,
    "speed": 2.35,
    "eta": "00:15:30"
  }
}
```

**Legacy Payload** (still supported for backwards compatibility):
```json
{
  "type": "progress",
  "job": {
    "id": "1735530000000000000-1",
    "status": "running",
    "progress": 45.2,
    "speed": 2.35,
    "eta": "00:15:30",
    ...full job object...
  }
}
```

**Frontend Handling**:
```javascript
case 'progress':
    if (data.progress_update) {
        // NEW: Efficient delta update
        const { id, progress, speed, eta } = data.progress_update;
        updateProgressDOM(id, progress, speed, eta);

        // Update cache
        const job = jobMap.get(id);
        if (job) {
            job.progress = progress;
            job.speed = speed;
            job.eta = eta;
        }
    } else if (data.job) {
        // Legacy: Full job update
        updateActiveJobProgress(data.job);
    }
    break;
```

### 3.7 Event Type: `complete` (Job Completed)

**Trigger**: `queue.CompleteJob()` (unchanged)

**Payload**:
```json
{
  "type": "complete",
  "job": {
    "id": "1735530000000000000-1",
    "status": "complete",
    "progress": 100,
    "output_path": "/media/folder/video1.mp4",
    "output_size": 536870912,
    "space_saved": 536870912,
    "transcode_secs": 1800,
    "completed_at": "2024-12-30T10:30:05Z",
    ...
  }
}
```

### 3.8 Event Type: `failed` (Job Failed)

**Trigger**: `queue.FailJob()` or `queue.FailJobWithDetails()` (unchanged)

**Payload** (may include stderr for debugging):
```json
{
  "type": "failed",
  "job": {
    "id": "1735530000000000000-1",
    "status": "failed",
    "error": "Hardware encoder initialization failed",
    "stderr": "... last 64KB of ffmpeg stderr ...",
    "exit_code": 1,
    "ffmpeg_args": ["-i", "input.mp4", "-c:v", "hevc_nvenc", ...],
    "completed_at": "2024-12-30T10:00:10Z",
    ...
  }
}
```

### 3.9 Event Type: `cancelled` (Job Cancelled)

**Trigger**: `queue.CancelJob()` (unchanged)

**Payload**:
```json
{
  "type": "cancelled",
  "job": {
    "id": "1735530000000000000-1",
    "status": "cancelled",
    "completed_at": "2024-12-30T10:00:08Z",
    ...
  }
}
```

---

## 4. API CONTRACT CHANGES

### 4.1 POST /api/jobs (Create Jobs)

**Request** (unchanged):
```json
{
  "paths": ["/media/folder1", "/media/video.mp4"],
  "preset_id": "hevc",
  "include_subfolders": true,
  "max_depth": null
}
```

**Response** (unchanged):
```json
{
  "status": "processing",
  "message": "Processing 2 paths in background..."
}
```

**Behavior Change**:
- **Before**: Background goroutine probes ALL files, then adds ALL jobs
- **After**: Background goroutine streams file discovery, adds jobs as `pending_probe`

### 4.2 GET /api/jobs (List Jobs)

**Request (NEW parameters)**:
```
GET /api/jobs?offset=0&limit=100&status=pending,running
```

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `offset` | int | 0 | Skip first N jobs |
| `limit` | int | 100 | Return at most N jobs (max 1000) |
| `status` | string | all | Filter by status(es), comma-separated |

**Response (UPDATED)**:
```json
{
  "jobs": [...],
  "stats": {
    "pending_probe": 5000,
    "pending": 100,
    "running": 2,
    "complete": 500,
    "failed": 10,
    "cancelled": 5,
    "total": 5617,
    "total_saved": 107374182400
  },
  "pagination": {
    "offset": 0,
    "limit": 100,
    "total": 5617,
    "has_more": true
  }
}
```

**Backwards Compatibility**: If `offset` and `limit` not provided, returns all jobs (existing behavior).

### 4.3 GET /api/jobs/stream (SSE Stream)

**Behavior Changes**:

| Aspect | Before | After |
|--------|--------|-------|
| Init payload | All jobs | First 100 jobs (Phase 3.E) |
| Bulk add events | N events for N jobs | ceil(N/50) batch events |
| Progress events | Full Job struct | ProgressUpdate delta |
| New event types | None | `batch_added`, `probed` |

---

## 5. FRONTEND RENDERING STRATEGY

### 5.1 Virtual Scrolling Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         VIRTUAL SCROLLING LAYOUT                             │
└─────────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────────┐
│ #queue-container (position: relative; height: 400px; overflow: hidden)      │
│ ┌─────────────────────────────────────────────────────────────────────────┐ │
│ │ .virtual-viewport (height: 100%; overflow-y: auto)                      │ │
│ │ ┌─────────────────────────────────────────────────────────────────────┐ │ │
│ │ │ .virtual-content (height: totalJobs * itemHeight)                   │ │ │
│ │ │                                                                     │ │ │
│ │ │ ┌─────────────────────────────────────────────────────────────────┐ │ │ │
│ │ │ │ .top-spacer (height: firstVisibleIndex * itemHeight)            │ │ │ │
│ │ │ └─────────────────────────────────────────────────────────────────┘ │ │ │
│ │ │                                                                     │ │ │
│ │ │ ┌─────────────────────────────────────────────────────────────────┐ │ │ │
│ │ │ │ .job-item[data-job-id="..."] (visible job 1)                    │ │ │ │
│ │ │ └─────────────────────────────────────────────────────────────────┘ │ │ │
│ │ │ ┌─────────────────────────────────────────────────────────────────┐ │ │ │
│ │ │ │ .job-item[data-job-id="..."] (visible job 2)                    │ │ │ │
│ │ │ └─────────────────────────────────────────────────────────────────┘ │ │ │
│ │ │ ... (10-20 visible items + 5 overscan above/below)                 │ │ │
│ │ │ ┌─────────────────────────────────────────────────────────────────┐ │ │ │
│ │ │ │ .job-item[data-job-id="..."] (visible job N)                    │ │ │ │
│ │ │ └─────────────────────────────────────────────────────────────────┘ │ │ │
│ │ │                                                                     │ │ │
│ │ │ ┌─────────────────────────────────────────────────────────────────┐ │ │ │
│ │ │ │ .bottom-spacer (height: remainingJobs * itemHeight)             │ │ │ │
│ │ │ └─────────────────────────────────────────────────────────────────┘ │ │ │
│ │ └─────────────────────────────────────────────────────────────────────┘ │ │
│ └─────────────────────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 5.2 Data Structures

```javascript
// Primary data store: Map for O(1) lookup by ID
const jobMap = new Map();  // id -> Job object

// Ordered list for rendering
let jobOrder = [];  // Array of job IDs in display order

// Status indexes for O(1) filtering
const statusIndex = {
    pending_probe: new Set(),
    pending: new Set(),
    running: new Set(),
    complete: new Set(),
    failed: new Set(),
    cancelled: new Set()
};

// Virtual scroll state
let visibleRange = { start: 0, end: 20 };
const ITEM_HEIGHT = 80;  // pixels
const OVERSCAN = 5;      // extra items above/below viewport
```

### 5.3 Rendering Functions

```javascript
// Called on scroll (throttled to 60fps)
function updateVisibleRange() {
    const viewport = document.querySelector('.virtual-viewport');
    const scrollTop = viewport.scrollTop;
    const viewportHeight = viewport.clientHeight;

    const newStart = Math.max(0, Math.floor(scrollTop / ITEM_HEIGHT) - OVERSCAN);
    const newEnd = Math.min(
        jobOrder.length,
        Math.ceil((scrollTop + viewportHeight) / ITEM_HEIGHT) + OVERSCAN
    );

    if (newStart !== visibleRange.start || newEnd !== visibleRange.end) {
        visibleRange = { start: newStart, end: newEnd };
        renderVisibleJobs();
    }
}

// Renders only visible jobs
function renderVisibleJobs() {
    const { start, end } = visibleRange;
    const content = document.querySelector('.virtual-content');

    const topHeight = start * ITEM_HEIGHT;
    const bottomHeight = Math.max(0, (jobOrder.length - end) * ITEM_HEIGHT);

    const visibleIds = jobOrder.slice(start, end);
    const visibleJobs = visibleIds.map(id => jobMap.get(id));

    content.innerHTML = `
        <div class="top-spacer" style="height: ${topHeight}px"></div>
        ${visibleJobs.map(job => renderJobHtml(job)).join('')}
        <div class="bottom-spacer" style="height: ${bottomHeight}px"></div>
    `;
}

// Updates single job (only if visible)
function updateJobInView(job) {
    const idx = jobOrder.indexOf(job.id);
    if (idx >= visibleRange.start && idx < visibleRange.end) {
        const localIdx = idx - visibleRange.start;
        const items = document.querySelectorAll('.virtual-content > .job-item');
        if (items[localIdx]) {
            items[localIdx].outerHTML = renderJobHtml(job);
        }
    }
}
```

### 5.4 Status-Specific Rendering

```javascript
// Render job based on status
function renderJobHtml(job) {
    const filename = job.input_path.split('/').pop();
    const safeId = escapeHtml(job.id);
    const safePath = escapeHtml(job.input_path);
    const safeFilename = escapeHtml(filename);

    // Status-specific content
    let statusBadge, detailsHtml, progressHtml = '', actionsHtml = '';

    switch (job.status) {
        case 'pending_probe':
            statusBadge = '<span class="job-badge pending-probe">Queued</span>';
            detailsHtml = `
                <span class="job-detail">${formatBytes(job.input_size)}</span>
                <span class="job-detail status-text">Awaiting probe...</span>
            `;
            actionsHtml = `<button class="btn btn-secondary btn-sm" onclick="cancelJob('${safeId}')">Cancel</button>`;
            break;

        case 'pending':
            statusBadge = '<span class="job-badge pending">Pending</span>';
            detailsHtml = `
                <span class="job-detail">${formatBytes(job.input_size)}</span>
                <span class="job-detail">${formatDuration(job.duration / 1000)}</span>
            `;
            actionsHtml = `<button class="btn btn-secondary btn-sm" onclick="cancelJob('${safeId}')">Cancel</button>`;
            break;

        case 'running':
            const isInit = job.progress === 0 && job.speed === 0;
            statusBadge = isInit
                ? '<span class="job-badge initializing">Initializing</span>'
                : '<span class="job-badge running">Running</span>';
            progressHtml = `
                <div class="job-progress">
                    <div class="progress-bar">
                        <div class="progress-fill ${isInit ? 'initializing' : ''}"
                             style="width: ${isInit ? 0 : job.progress}%"></div>
                    </div>
                </div>
            `;
            detailsHtml = isInit
                ? '<span class="job-detail">Starting encoder...</span>'
                : `
                    <span class="job-detail">${job.progress.toFixed(1)}%</span>
                    <span class="job-detail">${job.speed.toFixed(2)}x</span>
                    <span class="job-detail">ETA: ${job.eta || '...'}</span>
                `;
            actionsHtml = `<button class="btn btn-secondary btn-sm" onclick="cancelJob('${safeId}')">Cancel</button>`;
            break;

        case 'complete':
            statusBadge = '<span class="job-badge complete">Complete</span>';
            detailsHtml = `
                <span class="job-detail job-saved">Saved ${formatBytes(job.space_saved)}</span>
                <span class="job-detail">${formatBytes(job.input_size)} → ${formatBytes(job.output_size)}</span>
                ${job.transcode_secs ? `<span class="job-detail">in ${formatDuration(job.transcode_secs)}</span>` : ''}
            `;
            break;

        case 'failed':
            statusBadge = '<span class="job-badge failed">Failed</span>';
            detailsHtml = `<div class="job-error">${escapeHtml(job.error)}</div>`;
            actionsHtml = `<button class="btn btn-secondary btn-sm" onclick="retryJob('${safeId}')">Retry</button>`;
            break;

        case 'cancelled':
            statusBadge = '<span class="job-badge cancelled">Cancelled</span>';
            detailsHtml = '<span class="job-detail">Cancelled by user</span>';
            break;
    }

    return `
        <div class="job-item" data-job-id="${safeId}" style="height: ${ITEM_HEIGHT}px">
            <div class="job-header">
                <span class="job-name" title="${safePath}">${safeFilename}</span>
                <div class="job-badges">
                    ${job.is_software_fallback ? '<span class="job-badge retry">Retry</span>' : ''}
                    <span class="job-badge ${job.is_hardware ? 'hardware' : 'software'}">
                        ${job.is_hardware ? 'HW' : 'SW'}
                    </span>
                    ${statusBadge}
                </div>
            </div>
            ${progressHtml}
            <div class="job-details">${detailsHtml}</div>
            ${actionsHtml ? `<div class="job-actions">${actionsHtml}</div>` : ''}
        </div>
    `;
}
```

---

## 6. SEPARATION OF CONCERNS

### 6.1 Component Responsibilities

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                          SEPARATION OF CONCERNS                              │
└─────────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────────┐
│ DISCOVERY (internal/browse/browse.go)                                        │
├─────────────────────────────────────────────────────────────────────────────┤
│ Responsibility:                                                              │
│   - Walk directory tree to find video files                                  │
│   - Return file paths and sizes (from stat, not probe)                       │
│   - Respect recursion settings and depth limits                              │
│                                                                              │
│ Does NOT:                                                                    │
│   - Probe files (no ffprobe calls during discovery)                          │
│   - Create jobs (that's Queue's responsibility)                              │
│   - Send SSE events                                                          │
│                                                                              │
│ Interface:                                                                   │
│   DiscoverFiles(ctx, paths, opts) -> chan FileInfo                          │
│   (streams FileInfo{Path, Size} as discovered)                              │
└─────────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────────┐
│ QUEUE (internal/jobs/queue.go)                                               │
├─────────────────────────────────────────────────────────────────────────────┤
│ Responsibility:                                                              │
│   - Store and manage job state                                               │
│   - Persist jobs to disk                                                     │
│   - Broadcast events to SSE subscribers                                      │
│   - Provide next pending job to workers                                      │
│   - Track statistics                                                         │
│                                                                              │
│ Does NOT:                                                                    │
│   - Discover files                                                           │
│   - Probe files                                                              │
│   - Transcode files                                                          │
│                                                                              │
│ Interface:                                                                   │
│   AddPendingProbe(path, presetID, fileSize) -> *Job                         │
│   UpdateProbeData(id, probe) -> error                                        │
│   StartJob(id) -> error                                                      │
│   UpdateProgress(id, progress, speed, eta)                                   │
│   CompleteJob(id, outputPath, outputSize) -> error                          │
│   FailJob(id, errMsg) -> error                                               │
│   GetNext() -> *Job                                                          │
│   Subscribe() -> chan JobEvent                                               │
└─────────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────────┐
│ PROBING (internal/ffmpeg/prober.go)                                          │
├─────────────────────────────────────────────────────────────────────────────┤
│ Responsibility:                                                              │
│   - Execute ffprobe to extract media metadata                                │
│   - Return structured ProbeResult                                            │
│   - Handle probe timeouts and errors                                         │
│                                                                              │
│ Does NOT:                                                                    │
│   - Decide when to probe (that's Worker's decision)                          │
│   - Store results (that's Queue's job via UpdateProbeData)                   │
│   - Check skip reasons (that's checkSkipReason's job)                        │
│                                                                              │
│ Interface:                                                                   │
│   Probe(ctx, path) -> (*ProbeResult, error)                                  │
└─────────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────────┐
│ WORKER (internal/jobs/worker.go)                                             │
├─────────────────────────────────────────────────────────────────────────────┤
│ Responsibility:                                                              │
│   - Pick up next job from queue                                              │
│   - Probe if job is pending_probe                                            │
│   - Check skip reasons after probe                                           │
│   - Execute transcode via ffmpeg                                             │
│   - Report progress, completion, failure                                     │
│   - Handle hardware encoder fallback                                         │
│                                                                              │
│ Does NOT:                                                                    │
│   - Discover files                                                           │
│   - Manage job state directly (uses Queue interface)                         │
│   - Send SSE events directly (Queue broadcasts)                              │
│                                                                              │
│ Interface:                                                                   │
│   processJob(job *Job) - internal method                                     │
└─────────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────────┐
│ SSE (internal/api/sse.go)                                                    │
├─────────────────────────────────────────────────────────────────────────────┤
│ Responsibility:                                                              │
│   - Handle SSE connections                                                   │
│   - Send initial state on connect                                            │
│   - Forward events from Queue to connected clients                           │
│   - Send notifications (Pushover) on queue empty                             │
│                                                                              │
│ Does NOT:                                                                    │
│   - Generate events (Queue does that)                                        │
│   - Modify job state                                                         │
│   - Batch events (EventBatcher does that if enabled)                         │
│                                                                              │
│ Interface:                                                                   │
│   JobStream(w, r) - HTTP handler                                             │
└─────────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────────┐
│ UI STATE (web/templates/index.html)                                          │
├─────────────────────────────────────────────────────────────────────────────┤
│ Responsibility:                                                              │
│   - Maintain local cache of jobs (jobMap, jobOrder)                          │
│   - Maintain status indexes for O(1) filtering                               │
│   - Render visible jobs via virtual scrolling                                │
│   - Handle user interactions (cancel, retry, etc.)                           │
│                                                                              │
│ Does NOT:                                                                    │
│   - Store authoritative state (server is source of truth)                    │
│   - Make decisions about job flow                                            │
│                                                                              │
│ State:                                                                       │
│   jobMap: Map<id, Job>           - O(1) lookup                               │
│   jobOrder: Array<id>            - render order                              │
│   statusIndex: Map<status, Set>  - O(1) status filter                        │
│   visibleRange: {start, end}     - virtual scroll window                     │
│   cachedStats: Stats             - display stats                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 6.2 Data Flow Diagram

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              DATA FLOW                                       │
└─────────────────────────────────────────────────────────────────────────────┘

User Action                  Backend                         Frontend
───────────────────────────────────────────────────────────────────────────────

1. Select folder ─────────────────────────────────────────→ Update selection UI
   + Click Start

2. ─────────────────→ POST /api/jobs
                      │
                      ↓
                   CreateJobs handler
                      │
                      ↓
3. ←───────────────── HTTP 202 Accepted ──────────────────→ Show "Scanning..."
                      │
                      ↓
                   Background goroutine:
                      │
                      ↓
                   Discovery.DiscoverFiles()
                      │ (streams FileInfo)
                      ↓
                   Queue.AddPendingProbe()
                      │
                      ↓
                   EventBatcher.Add(job)
                      │ (collects up to 50 or 100ms)
                      ↓
                   EventBatcher.Flush()
                      │
                      ↓
                   Queue.broadcast("batch_added")
                      │
                      ↓
4. ←─────────────── SSE: batch_added ─────────────────────→ handleBatchAdded()
                                                              │
                                                              ↓
                                                           jobMap.set(...)
                                                           jobOrder.push(...)
                                                           statusIndex.add(...)
                                                           renderVisibleJobs()

5. Worker.processJob()
   │
   ↓
   job.status == pending_probe?
   │ Yes
   ↓
   Prober.Probe(path)
   │
   ├─ Error? ──→ Queue.FailJob() ──→ SSE: failed ─────────→ handleJobFailed()
   │
   ├─ Skip? ───→ Queue.FailJob() ──→ SSE: failed ─────────→ handleJobFailed()
   │
   ↓
   Queue.UpdateProbeData()
   │
   ↓
   Queue.broadcast("probed")
   │
6. ←─────────────── SSE: probed ──────────────────────────→ handleJobProbed()
                                                              │
                                                              ↓
                                                           job.duration = ...
                                                           job.bitrate = ...
                                                           job.status = pending
                                                           updateJobInView(job)

7. Queue.StartJob()
   │
   ↓
   Queue.broadcast("started")
   │
   ←─────────────── SSE: started ─────────────────────────→ handleJobStarted()

8. Transcoder.Transcode()
   │ (progress callback)
   ↓
   Queue.UpdateProgress()
   │
   ↓
   Queue.broadcast("progress")
   │
   ←─────────────── SSE: progress ────────────────────────→ handleProgress()
                         (delta)                              │
                                                              ↓
                                                           updateProgressDOM()
                                                           (targeted update)

9. Transcoder completes
   │
   ↓
   Queue.CompleteJob()
   │
   ↓
   Queue.broadcast("complete")
   │
   ←─────────────── SSE: complete ────────────────────────→ handleJobComplete()
```

---

## 7. MIGRATION PATH

### 7.1 Phased Rollout

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           MIGRATION PHASES                                   │
└─────────────────────────────────────────────────────────────────────────────┘

PHASE M.1: Backend SSE Batching (BACKWARDS COMPATIBLE)
────────────────────────────────────────────────────────
Changes:
  - Add batch_added event type
  - AddMultiple() broadcasts single batch event

Backwards Compat:
  - Old frontend ignores unknown event types → falls back to debounced refresh
  - Old frontend still works, just misses batch optimization

Rollback:
  - Revert AddMultiple() to loop with individual broadcasts
  - No data migration needed


PHASE M.2: Delta Progress Updates (BACKWARDS COMPATIBLE)
─────────────────────────────────────────────────────────
Changes:
  - Add progress_update field to progress events

Backwards Compat:
  - New frontend checks for progress_update first, falls back to job field
  - Old frontend ignores progress_update, uses job field

Rollback:
  - Remove progress_update field, keep sending full job
  - No data migration needed


PHASE M.3: Virtual Scrolling (FRONTEND ONLY)
─────────────────────────────────────────────
Changes:
  - Replace direct DOM rendering with VirtualQueue class

Backwards Compat:
  - Pure frontend change, no backend impact
  - Fallback: can revert to direct rendering if issues

Rollback:
  - Revert index.html to previous version
  - No data migration needed


PHASE M.4: pending_probe Status (REQUIRES MIGRATION)
─────────────────────────────────────────────────────
Changes:
  - Add StatusPendingProbe constant
  - AddPendingProbe() creates jobs without probe data
  - Worker probes on demand
  - UI handles pending_probe status

Migration:
  - Existing queue.json has no pending_probe jobs → no migration needed
  - New jobs created with pending_probe status
  - Stats struct gains pending_probe field

Backwards Compat:
  - Old frontend shows pending_probe as "Unknown" status
  - Acceptable degradation: jobs still work, just unclear status

Rollback:
  - Mark all pending_probe jobs as failed with "Migration rollback"
  - Revert to blocking probe flow
  - OR: Probe all pending_probe jobs synchronously during rollback


PHASE M.5: Paginated Init (REQUIRES CAREFUL ROLLOUT)
─────────────────────────────────────────────────────
Changes:
  - SSE init sends first 100 jobs, not all
  - Frontend requests more on scroll

Migration:
  - None needed for backend
  - Frontend must be updated first

Backwards Compat:
  - Deploy new frontend first (handles both full and paginated init)
  - Then deploy backend change
  - Old frontend without pagination will only see first 100 jobs

Rollback:
  - Revert SSE init to send all jobs
  - Frontend handles both formats
```

### 7.2 Queue.json Schema Compatibility

**Current Schema**:
```json
{
  "jobs": [
    {
      "id": "...",
      "status": "pending|running|complete|failed|cancelled",
      "duration": 7200000,
      "bitrate": 12000000,
      ...
    }
  ],
  "order": ["id1", "id2", ...]
}
```

**New Schema** (backwards compatible):
```json
{
  "jobs": [
    {
      "id": "...",
      "status": "pending_probe|pending|running|complete|failed|cancelled",
      "duration": 0,       // 0 for pending_probe
      "bitrate": 0,        // 0 for pending_probe
      "probe_error": "",   // NEW: only if probe failed
      ...
    }
  ],
  "order": ["id1", "id2", ...]
}
```

**Load Compatibility**:
```go
func (q *Queue) load() error {
    // ... existing load logic ...

    // Handle old schema: jobs without probe_error field
    for _, job := range q.jobs {
        // Old jobs have duration > 0 if they were probed
        // New pending_probe jobs have duration == 0

        // No migration needed: pending_probe is a new state
        // Old queues don't have pending_probe jobs
    }

    return nil
}
```

### 7.3 Feature Flags (Optional)

For risk mitigation, consider feature flags:

```go
// internal/config/config.go
type Config struct {
    // ... existing fields ...

    // Feature flags for gradual rollout
    Features struct {
        BatchedSSE       bool `yaml:"batched_sse" default:"true"`
        DeltaProgress    bool `yaml:"delta_progress" default:"true"`
        DeferredProbing  bool `yaml:"deferred_probing" default:"false"`
        PaginatedInit    bool `yaml:"paginated_init" default:"false"`
    } `yaml:"features"`
}
```

---

## 8. INVARIANTS

The following invariants MUST remain true after all Phase 3 changes:

### 8.1 Job State Invariants

```
INVARIANT 1: Job ID is immutable and unique
─────────────────────────────────────────────
∀ job ∈ Queue: job.ID is set at creation and never changes
∀ job1, job2 ∈ Queue: job1.ID ≠ job2.ID

INVARIANT 2: Status transitions are monotonic toward terminal states
─────────────────────────────────────────────────────────────────────
Valid transitions:
  pending_probe → pending       (after successful probe)
  pending_probe → failed        (probe error or skip reason)
  pending_probe → cancelled     (user cancelled)
  pending → running             (worker picks up)
  pending → cancelled           (user cancelled)
  running → complete            (transcode succeeded)
  running → failed              (transcode error)
  running → cancelled           (user cancelled)

Invalid transitions (MUST NOT occur):
  complete → anything
  failed → anything (except via explicit retry which creates NEW job)
  cancelled → anything
  running → pending
  pending → pending_probe

INVARIANT 3: Terminal states are immutable
──────────────────────────────────────────
∀ job ∈ {complete, failed, cancelled}:
  job.status cannot change
  job.progress, job.speed, job.eta cannot change

INVARIANT 4: Input path never changes
─────────────────────────────────────
∀ job: job.InputPath is set at creation and never changes

INVARIANT 5: Probe data is set exactly once
───────────────────────────────────────────
∀ job:
  IF job.status == pending_probe THEN job.duration == 0 AND job.bitrate == 0
  IF job.status ≠ pending_probe THEN job.duration ≥ 0 AND job.bitrate ≥ 0
  job.duration and job.bitrate are set via UpdateProbeData() exactly once
```

### 8.2 Queue Invariants

```
INVARIANT 6: Order contains exactly the IDs in jobs map
───────────────────────────────────────────────────────
∀ id ∈ queue.order: id ∈ queue.jobs
∀ id ∈ queue.jobs: id ∈ queue.order
len(queue.order) == len(queue.jobs)

INVARIANT 7: GetNext returns jobs in order
──────────────────────────────────────────
GetNext() returns the first job in queue.order where:
  status == pending OR status == pending_probe

INVARIANT 8: Stats are consistent with job states
─────────────────────────────────────────────────
stats.pending_probe == count(job.status == pending_probe)
stats.pending == count(job.status == pending)
stats.running == count(job.status == running)
stats.complete == count(job.status == complete)
stats.failed == count(job.status == failed)
stats.cancelled == count(job.status == cancelled)
stats.total == len(queue.jobs)

INVARIANT 9: Persistence is atomic
──────────────────────────────────
Queue file is always valid JSON
Partial writes never corrupt the queue
```

### 8.3 SSE Invariants

```
INVARIANT 10: Events reflect actual state changes
─────────────────────────────────────────────────
Every state change in Queue triggers exactly one broadcast
Event order matches mutation order

INVARIANT 11: Init event provides consistent snapshot
─────────────────────────────────────────────────────
init event jobs and stats are from same point in time
(mutex held during GetAll() and Stats())

INVARIANT 12: Batch events are atomic
─────────────────────────────────────
If batch_added contains N jobs, all N jobs exist in queue
No partial batches
```

### 8.4 Worker Invariants

```
INVARIANT 13: Workers process jobs in queue order
─────────────────────────────────────────────────
Workers call GetNext() to select jobs
First pending/pending_probe job in order is selected

INVARIANT 14: Only one worker processes each job
─────────────────────────────────────────────────
Job status == running implies exactly one worker holds it
StartJob() fails if status ≠ pending

INVARIANT 15: Probe happens before transcode
─────────────────────────────────────────────
IF job.status == pending_probe at worker pickup:
  Worker MUST probe before calling StartJob()
  Skip reason check happens after probe

INVARIANT 16: Skip reason is checked exactly once
─────────────────────────────────────────────────
checkSkipReason() called after probe, before transcode
If skip reason exists, job fails immediately (no transcode attempt)
```

### 8.5 UI Invariants

```
INVARIANT 17: jobMap and jobOrder are consistent
────────────────────────────────────────────────
∀ id ∈ jobOrder: jobMap.has(id)
∀ id ∈ jobMap: jobOrder.includes(id)

INVARIANT 18: statusIndex is consistent with jobMap
────────────────────────────────────────────────────
∀ job ∈ jobMap: statusIndex[job.status].has(job.id)
∀ status, ∀ id ∈ statusIndex[status]: jobMap.get(id).status == status

INVARIANT 19: Virtual scroll renders correct range
────────────────────────────────────────────────────
Visible DOM elements correspond to jobOrder[visibleRange.start : visibleRange.end]

INVARIANT 20: Stats match server
──────────────────────────────────
cachedStats updated on every SSE event
cachedStats eventually consistent with server Stats()
```

### 8.6 Transcoding Invariants (UNCHANGED)

```
INVARIANT 21: FFmpeg invocation is unchanged
────────────────────────────────────────────
Transcode() receives same parameters as before
FFmpeg command construction unchanged
Progress parsing unchanged

INVARIANT 22: Hardware fallback logic is unchanged
──────────────────────────────────────────────────
IsHardwareEncoderFailure() detection unchanged
AddSoftwareFallback() behavior unchanged
Rate limiting (5 per 5 min) unchanged

INVARIANT 23: Original file handling is unchanged
─────────────────────────────────────────────────
OriginalHandling config respected (replace/keep)
Atomic output file moves unchanged
```

---

## 9. APPROVAL CHECKLIST

Before implementing Phase 3.D (Deferred Probing) or Phase 3.E (Paginated Init), confirm:

- [ ] Forensic analysis reviewed and accepted
- [ ] Job state machine diagram reviewed
- [ ] SSE event shapes reviewed
- [ ] API contract changes reviewed
- [ ] Migration path understood
- [ ] Invariants understood and agreed
- [ ] Rollback plan accepted
- [ ] Test plan defined

**Approver**: ________________
**Date**: ________________
**Notes**: ________________

---

**END OF SPECIFICATION**

*This document must be approved before Phase 3.D or 3.E implementation begins.*
*Phase 3.A (SSE Batching) and 3.B (Delta Progress) are safe to implement incrementally.*

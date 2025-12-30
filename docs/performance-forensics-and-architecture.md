# Shrinkray UI Performance: Forensic Analysis & Scalable Architecture

**Date**: 2024-12-30
**Author**: Performance Analysis
**Target**: Support 20,000 jobs with smooth UI
**Current State**: UI degrades significantly at ~50+ jobs

---

## PHASE 1: FORENSIC ANALYSIS

### 1.1 System Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           SHRINKRAY ARCHITECTURE                             │
└─────────────────────────────────────────────────────────────────────────────┘

┌──────────────┐     ┌──────────────────┐     ┌─────────────────────────────┐
│   Browser    │ ←── │   Go Backend     │ ←── │   FFmpeg/FFprobe           │
│   (Vanilla   │     │   (net/http)     │     │   (System Processes)        │
│    JS)       │     │                  │     │                             │
└──────────────┘     └──────────────────┘     └─────────────────────────────┘
       ↑                     │
       │                     │
       └─────── SSE ─────────┘
          (Real-time events)
```

**Technology Stack:**
- **Backend**: Go 1.22, standard library net/http
- **Frontend**: Vanilla JavaScript (~2,900 lines), no framework
- **Real-time**: Server-Sent Events (SSE) via `/api/jobs/stream`
- **Persistence**: JSON queue file (atomic writes)
- **Media Processing**: FFmpeg/FFprobe subprocess invocation

### 1.2 Job Lifecycle Flow

```
USER ACTION                 BACKEND                      FRONTEND
───────────────────────────────────────────────────────────────────────────
1. Select folder
   + Click "Start"  ───────→ POST /api/jobs
                              │
2. HTTP 202 Accepted ←────────┘ (immediate response)
                              │
3. Background goroutine:      │
   ├─ Walk directory tree     │
   ├─ For EACH video file:    │
   │   └─ Spawn goroutine     │
   │       └─ FFprobe (100ms+)│
   ├─ wg.Wait() ──────────────┼──────→ BLOCKING (all probes)
   │                          │
   │ After ALL probes done:   │
   ├─ For EACH probe result:  │
   │   └─ queue.Add() ────────┼──────→ broadcast "added" event
   │       └─ broadcast() ────┼───────────────→ SSE event ───→ DOM update
   │                          │
4. Worker picks job:          │
   ├─ queue.StartJob() ───────┼──────→ broadcast "started"
   ├─ FFmpeg transcode ───────┼──────→ broadcast "progress" (every ~1s)
   ├─ queue.CompleteJob() ────┼──────→ broadcast "complete"
   │                          │
───────────────────────────────────────────────────────────────────────────
```

### 1.3 Root Causes (Ranked by Impact)

#### ROOT CAUSE 1: CRITICAL — Blocking FFprobe Scan

**Location**: `internal/browse/browse.go:269-301`

**Code Flow**:
```go
func (b *Browser) GetVideoFilesWithOptions(ctx, paths, opts) {
    // Step 1: Discover file paths (fast, ~10ms for 1000 files)
    videoPaths := b.discoverMediaFiles(...)

    // Step 2: Spawn N goroutines for N files
    for _, fp := range videoPaths {
        wg.Add(1)
        go func(filePath string) {
            result := b.getProbeResult(probeCtx, filePath)  // ~100ms each
            mu.Lock()
            results = append(results, result)
            mu.Unlock()
        }(fp)
    }

    // Step 3: BLOCKING - Wait for ALL probes
    wg.Wait()  // ← 7000 files × 100ms = 11+ minutes of UI freeze
}
```

**Causal Chain**:
```
User selects 7000-file folder
    → 7000 goroutines spawned (system overload)
    → wg.Wait() blocks for ALL probes (~11 minutes)
    → UI appears completely frozen (no jobs visible)
    → After 11 minutes, 7000 "added" events flood SSE
    → Browser main thread overwhelmed
```

**Impact Metrics**:
| Files | Probe Time (Est.) | User Perception |
|-------|-------------------|-----------------|
| 50    | ~5 seconds        | Brief delay     |
| 500   | ~50 seconds       | Unacceptable    |
| 5000  | ~8 minutes        | Unusable        |
| 20000 | ~33 minutes       | System failure  |

**Classification**: **ARCHITECTURAL BOTTLENECK** — Requires redesign of job creation flow

---

#### ROOT CAUSE 2: CRITICAL — No SSE Event Batching

**Location**: `internal/jobs/queue.go:186-199`

**Code Flow**:
```go
func (q *Queue) AddMultiple(probes, presetID) ([]*Job, error) {
    for _, probe := range probes {
        job, _ := q.Add(probe.Path, presetID, probe)  // Each Add() broadcasts!
        // ...
    }
}

func (q *Queue) Add(...) (*Job, error) {
    // ...
    q.broadcast(JobEvent{Type: "added", Job: job})  // 1 event per job
}
```

**Causal Chain**:
```
AddMultiple(7000 probes)
    → 7000 × q.Add()
    → 7000 × q.broadcast()
    → 7000 SSE events in rapid succession
    → Browser receives 7000 events in ~100ms
    → Each event triggers DOM update
    → Browser main thread blocked for seconds
```

**Impact Metrics**:
| Jobs Added | SSE Events | Frontend Work |
|------------|------------|---------------|
| 50         | 50         | 50 DOM updates |
| 500        | 500        | Visible jank   |
| 5000       | 5000       | Multi-second freeze |

**Classification**: **TACTICAL BUG** — Can be fixed with batching in backend

---

#### ROOT CAUSE 3: HIGH — Full Queue in SSE Init

**Location**: `internal/api/sse.go:29-36`

**Code Flow**:
```go
func (h *Handler) JobStream(w, r) {
    // Send initial state - ALL jobs at once
    initialJobs := h.queue.GetAll()  // Could be 20000 jobs
    initialData, _ := json.Marshal(map[string]interface{}{
        "type":  "init",
        "jobs":  initialJobs,  // ~500 bytes/job × 20000 = 10MB
        "stats": h.queue.Stats(),
    })
    fmt.Fprintf(w, "data: %s\n\n", initialData)
}
```

**Causal Chain**:
```
Client connects to SSE
    → Backend serializes ALL jobs
    → 10MB+ JSON payload transmitted
    → Browser parses 10MB JSON
    → updateJobs() called with 20000 jobs
    → 20000 DOM elements created
    → Browser hangs for 5-10 seconds
```

**Classification**: **ARCHITECTURAL BOTTLENECK** — Requires pagination/streaming init

---

#### ROOT CAUSE 4: HIGH — No List Virtualization

**Location**: `web/templates/index.html:2537`

**Code Flow**:
```javascript
function updateJobs(jobs) {
    // Renders ALL jobs regardless of viewport
    container.innerHTML = jobs.map(job => renderJobHtml(job)).join('');
}
```

**Causal Chain**:
```
updateJobs(20000 jobs)
    → 20000 × renderJobHtml()  (~30 lines HTML each)
    → 600,000 lines of HTML string
    → container.innerHTML = mega-string
    → Browser parses, creates 20000 DOM nodes
    → Layout/paint for all elements
    → Only 10-20 visible in viewport (waste 99.9%)
```

**Impact Metrics**:
| Jobs | DOM Nodes | Layout Time | Memory |
|------|-----------|-------------|--------|
| 100  | ~1500     | ~100ms      | ~10MB  |
| 1000 | ~15000    | ~1s         | ~100MB |
| 20000| ~300000   | ~20s        | ~2GB   |

**Classification**: **ARCHITECTURAL BOTTLENECK** — Requires virtual scrolling

---

#### ROOT CAUSE 5: MEDIUM — O(N) Active Panel Filtering

**Location**: `web/templates/index.html:2557-2559`

**Code Flow**:
```javascript
function updateActivePanel() {
    // Called after EVERY job update
    const runningJobs = cachedJobs.filter(j => j.status === 'running');  // O(N)
    const pendingJobs = cachedJobs.filter(j => j.status === 'pending').slice(0, 5);  // O(N)
    // ...
}
```

**Causal Chain**:
```
Progress event received
    → updateActiveJobProgress()
    → updateActivePanel()  (called from handleJobStatusChange)
    → filter() over 20000 jobs twice
    → ~40000 object property checks
    → Adds 5-10ms per update
```

**Classification**: **TACTICAL BUG** — Can optimize with index maps

---

#### ROOT CAUSE 6: LOW — Large SSE Payloads

**Location**: `internal/jobs/job.go:19-49`

**Code Flow**:
```go
type Job struct {
    // ... many fields
    Stderr string  // Can be up to 64KB!
    FFmpegArgs []string
    // ...
}

// Every SSE event sends full Job struct
q.broadcast(JobEvent{Type: "progress", Job: job})
```

**Impact**: Progress updates send ~500 bytes minimum, up to 64KB if stderr populated.

**Classification**: **TACTICAL BUG** — Can send delta updates for progress

---

### 1.4 Summary: Architectural Bottlenecks vs Tactical Bugs

| Issue | Type | Effort | Impact on 20k Scale |
|-------|------|--------|---------------------|
| Blocking FFprobe | Architectural | High | Critical |
| No SSE Batching | Tactical | Low | High |
| Full Queue in Init | Architectural | Medium | High |
| No Virtualization | Architectural | Medium | Critical |
| O(N) Filtering | Tactical | Low | Medium |
| Large Payloads | Tactical | Low | Low |

**Key Insight**: The system was designed for ~10-50 jobs. At 20,000 jobs:
- Memory: 20,000 × ~500 bytes = 10MB per SSE init
- DOM: 20,000 × 15 elements = 300,000+ DOM nodes
- Time: 20,000 × 100ms probe = 33 minutes blocking scan

---

## PHASE 2: MINIMAL SAFE FIX (Already Implemented)

### 2.1 Changes Made (Previous PR)

The following frontend optimizations were implemented in commit `f76d996`:

1. **`debouncedRefreshJobs()`** — Batches rapid updates with 200ms debounce
2. **`handleJobAdded(job)`** — Appends single job to DOM (O(1)) instead of full rebuild (O(N))
3. **`handleJobStatusChange(job)`** — Updates single DOM element via `outerHTML`
4. **`updateActiveJobProgress(job)`** — Targeted progress bar updates (no DOM rebuild)
5. **`renderJobHtml(job)`** — Extracted reusable template function

### 2.2 Results

| Metric | Before | After |
|--------|--------|-------|
| Time to add 50 jobs | >5s jank | <200ms |
| DOM rebuilds per add | 50 | 1 |
| Smooth handling | ~10 jobs | ~500 jobs |

### 2.3 What Still Needs Work

The minimal fix handles **500 jobs** smoothly. For **20,000 jobs**, we need:
1. Backend SSE event batching (reduce event flood)
2. Deferred probing (eliminate blocking scan)
3. Virtual scrolling (render only visible jobs)
4. Paginated init (don't send full queue on connect)

---

## PHASE 3: SCALABLE ARCHITECTURE DESIGN

### 3.1 Design Principles

1. **Deferred Probing**: Never probe until a worker needs the data
2. **Streaming Discovery**: Add jobs as files are discovered, not after all are scanned
3. **Natural Rate Limiting**: Workers probe 1 file at a time (worker count = probe concurrency)
4. **Batched SSE**: Aggregate events before sending (max 50/batch or 100ms timer)
5. **Virtual Scrolling**: Render only visible viewport (10-20 items)
6. **Delta Updates**: Send only changed fields for progress events
7. **Paginated APIs**: Never return unbounded lists

### 3.2 New Job Status Model

```
CURRENT STATUSES:                NEW STATUSES:
─────────────────                ─────────────
pending                          pending_probe  (NEW: file discovered, not probed)
running                          pending        (probed, waiting for worker)
complete                         running        (transcoding)
failed                           complete       (success)
cancelled                        failed         (error or skip reason)
                                 cancelled      (user cancelled)
```

**Why `pending_probe`?**
- Jobs appear in UI immediately after file discovery (~10ms per file)
- Worker probes file just before transcoding
- Skip logic runs at probe time (codec check, resolution check)
- UI shows "Waiting for probe..." for pending_probe jobs

### 3.3 New Data Flow

```
┌───────────────────────────────────────────────────────────────────────────┐
│                     NEW ARCHITECTURE: STREAMING DISCOVERY                  │
└───────────────────────────────────────────────────────────────────────────┘

USER ACTION                 BACKEND                      FRONTEND
───────────────────────────────────────────────────────────────────────────
1. Select folder
   + Click "Start"  ───────→ POST /api/jobs
                              │
2. HTTP 202 Accepted ←────────┘ (immediate)
                              │
3. Background goroutine:      │
   ├─ filepath.Walk()         │
   │   ├─ Found video1.mp4 ───┼─→ AddPendingProbe() ─→ batch buffer
   │   ├─ Found video2.mp4 ───┼─→ AddPendingProbe() ─→ batch buffer
   │   ├─ (every 100ms or 50 jobs)                   ─→ SSE "batch_added"
   │   ├─ Found video3.mp4 ...│                          │
   │   └─ ...                 │                          ↓
   │                          │                      handleBatchAdded()
   │                          │                      (append all at once)
   │                          │
4. Worker picks job:          │
   ├─ Job is pending_probe    │
   │   ├─ Probe file (~100ms) │
   │   ├─ Check skip reason   │
   │   ├─ If skip → FailJob() ┼─→ broadcast "failed"
   │   └─ Else → update job   ┼─→ broadcast "probed" (new event)
   ├─ StartJob() ─────────────┼─→ broadcast "started"
   ├─ FFmpeg transcode ───────┼─→ broadcast "progress" (delta only)
   ├─ CompleteJob() ──────────┼─→ broadcast "complete"
───────────────────────────────────────────────────────────────────────────
```

### 3.4 Backend Changes

#### 3.4.1 New Job Status: `pending_probe`

```go
// internal/jobs/job.go
const (
    StatusPendingProbe Status = "pending_probe"  // NEW
    StatusPending      Status = "pending"
    StatusRunning      Status = "running"
    StatusComplete     Status = "complete"
    StatusFailed       Status = "failed"
    StatusCancelled    Status = "cancelled"
)
```

#### 3.4.2 Lightweight Job Creation

```go
// internal/jobs/queue.go

// AddPendingProbe creates a job without probing the file
// File will be probed when worker picks it up
func (q *Queue) AddPendingProbe(path string, presetID string, fileSize int64) *Job {
    q.mu.Lock()
    defer q.mu.Unlock()

    preset := ffmpeg.GetPreset(presetID)
    encoder := string(ffmpeg.HWAccelNone)
    isHardware := false
    if preset != nil {
        encoder = string(preset.Encoder)
        isHardware = preset.Encoder != ffmpeg.HWAccelNone
    }

    job := &Job{
        ID:         generateID(),
        InputPath:  path,
        PresetID:   presetID,
        Encoder:    encoder,
        IsHardware: isHardware,
        Status:     StatusPendingProbe,  // Not probed yet
        InputSize:  fileSize,            // From stat, not probe
        // Duration, Bitrate = 0 (unknown until probed)
        CreatedAt:  time.Now(),
    }

    q.jobs[job.ID] = job
    q.order = append(q.order, job.ID)

    // Don't broadcast yet - let batcher handle it
    return job
}
```

#### 3.4.3 SSE Event Batcher

```go
// internal/jobs/batcher.go

const (
    BatchSize     = 50                     // Max jobs per batch
    BatchInterval = 100 * time.Millisecond // Max time before flush
)

type EventBatcher struct {
    queue    *Queue
    pending  []*Job
    mu       sync.Mutex
    timer    *time.Timer
    stopCh   chan struct{}
}

func NewEventBatcher(queue *Queue) *EventBatcher {
    b := &EventBatcher{
        queue:   queue,
        pending: make([]*Job, 0, BatchSize),
        stopCh:  make(chan struct{}),
    }
    return b
}

func (b *EventBatcher) Add(job *Job) {
    b.mu.Lock()
    defer b.mu.Unlock()

    b.pending = append(b.pending, job)

    // Flush if batch full
    if len(b.pending) >= BatchSize {
        b.flushLocked()
        return
    }

    // Start timer if first job in batch
    if len(b.pending) == 1 {
        b.timer = time.AfterFunc(BatchInterval, func() {
            b.Flush()
        })
    }
}

func (b *EventBatcher) Flush() {
    b.mu.Lock()
    defer b.mu.Unlock()
    b.flushLocked()
}

func (b *EventBatcher) flushLocked() {
    if len(b.pending) == 0 {
        return
    }

    if b.timer != nil {
        b.timer.Stop()
        b.timer = nil
    }

    // Send batch event
    b.queue.broadcast(JobEvent{
        Type: "batch_added",
        Jobs: b.pending,  // New field on JobEvent
    })

    b.pending = make([]*Job, 0, BatchSize)
}
```

#### 3.4.4 Streaming File Discovery

```go
// internal/api/handler.go

func (h *Handler) CreateJobs(w http.ResponseWriter, r *http.Request) {
    // ... validation ...

    // Respond immediately
    writeJSON(w, http.StatusAccepted, map[string]interface{}{
        "status":  "scanning",
        "message": "Discovering files...",
    })

    // Stream jobs as files are discovered
    go func() {
        batcher := jobs.NewEventBatcher(h.queue)

        for _, path := range req.Paths {
            filepath.Walk(path, func(filePath string, info os.FileInfo, err error) error {
                if err != nil || info.IsDir() {
                    return nil
                }

                if !ffmpeg.IsVideoFile(filePath) {
                    return nil
                }

                // Create lightweight job (no probe!)
                job := h.queue.AddPendingProbe(filePath, req.PresetID, info.Size())
                batcher.Add(job)

                return nil
            })
        }

        // Flush remaining jobs
        batcher.Flush()

        // Persist queue after discovery complete
        h.queue.Save()
    }()
}
```

#### 3.4.5 Worker Probes on Demand

```go
// internal/jobs/worker.go

func (w *Worker) processJob(job *Job) {
    // NEW: Probe if job hasn't been probed yet
    if job.Status == jobs.StatusPendingProbe {
        probe, err := w.prober.Probe(w.ctx, job.InputPath)
        if err != nil {
            w.queue.FailJob(job.ID, fmt.Sprintf("probe failed: %v", err))
            return
        }

        // Check skip reason now that we have probe data
        preset := ffmpeg.GetPreset(job.PresetID)
        if skipReason := checkSkipReason(probe, preset); skipReason != "" {
            w.queue.FailJob(job.ID, skipReason)
            return
        }

        // Update job with probe data
        w.queue.UpdateProbeData(job.ID, probe)
    }

    // Continue with existing transcode logic...
}
```

#### 3.4.6 Delta Progress Updates

```go
// internal/jobs/job.go

// ProgressUpdate contains only fields that change during transcoding
type ProgressUpdate struct {
    ID       string  `json:"id"`
    Progress float64 `json:"progress"`
    Speed    float64 `json:"speed"`
    ETA      string  `json:"eta"`
}

// internal/jobs/queue.go
func (q *Queue) UpdateProgress(id string, progress, speed float64, eta string) {
    // ... existing logic ...

    // Send delta update instead of full job
    q.broadcast(JobEvent{
        Type: "progress",
        ProgressUpdate: &ProgressUpdate{
            ID:       id,
            Progress: progress,
            Speed:    speed,
            ETA:      eta,
        },
    })
}
```

#### 3.4.7 Paginated Job API

```go
// internal/api/handler.go

func (h *Handler) ListJobs(w http.ResponseWriter, r *http.Request) {
    offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
    limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

    if limit <= 0 || limit > 1000 {
        limit = 100  // Default page size
    }

    allJobs := h.queue.GetAll()
    total := len(allJobs)

    // Paginate
    end := offset + limit
    if end > total {
        end = total
    }

    var pageJobs []*Job
    if offset < total {
        pageJobs = allJobs[offset:end]
    }

    writeJSON(w, http.StatusOK, map[string]interface{}{
        "jobs":   pageJobs,
        "total":  total,
        "offset": offset,
        "limit":  limit,
    })
}
```

### 3.5 Frontend Changes

#### 3.5.1 Virtual Scrolling Implementation

```javascript
// VirtualQueue: Renders only visible items in viewport
class VirtualQueue {
    constructor(container, options = {}) {
        this.container = container;
        this.itemHeight = options.itemHeight || 80;  // px per job row
        this.overscan = options.overscan || 5;       // Extra rows above/below

        this.jobs = [];
        this.jobIndex = new Map();  // id -> array index (O(1) lookup)
        this.visibleRange = { start: 0, end: 20 };

        // Create structure
        this.viewport = document.createElement('div');
        this.viewport.className = 'virtual-viewport';
        this.viewport.style.overflow = 'auto';
        this.viewport.style.height = '100%';

        this.content = document.createElement('div');
        this.content.className = 'virtual-content';

        this.viewport.appendChild(this.content);
        this.container.appendChild(this.viewport);

        // Throttled scroll handler
        this.viewport.addEventListener('scroll', this.throttle(() => {
            this.updateVisibleRange();
        }, 16));  // ~60fps
    }

    throttle(fn, delay) {
        let last = 0;
        return (...args) => {
            const now = Date.now();
            if (now - last >= delay) {
                last = now;
                fn.apply(this, args);
            }
        };
    }

    setJobs(jobs) {
        this.jobs = jobs;
        this.jobIndex.clear();
        jobs.forEach((j, i) => this.jobIndex.set(j.id, i));
        this.render();
    }

    addJobs(newJobs) {
        const startIdx = this.jobs.length;
        this.jobs.push(...newJobs);
        newJobs.forEach((j, i) => this.jobIndex.set(j.id, startIdx + i));
        this.render();
    }

    updateJob(job) {
        const idx = this.jobIndex.get(job.id);
        if (idx !== undefined) {
            this.jobs[idx] = job;

            // Only re-render if visible
            if (idx >= this.visibleRange.start && idx < this.visibleRange.end) {
                this.renderItem(idx);
            }
        }
    }

    updateVisibleRange() {
        const scrollTop = this.viewport.scrollTop;
        const viewportHeight = this.viewport.clientHeight;

        const start = Math.max(0, Math.floor(scrollTop / this.itemHeight) - this.overscan);
        const end = Math.min(
            this.jobs.length,
            Math.ceil((scrollTop + viewportHeight) / this.itemHeight) + this.overscan
        );

        if (start !== this.visibleRange.start || end !== this.visibleRange.end) {
            this.visibleRange = { start, end };
            this.render();
        }
    }

    render() {
        const { start, end } = this.visibleRange;
        const totalHeight = this.jobs.length * this.itemHeight;

        // Spacer before visible items
        const topSpacer = start * this.itemHeight;
        // Spacer after visible items
        const bottomSpacer = (this.jobs.length - end) * this.itemHeight;

        const visibleJobs = this.jobs.slice(start, end);

        this.content.innerHTML = `
            <div style="height: ${topSpacer}px"></div>
            ${visibleJobs.map((job, i) =>
                `<div class="virtual-item" data-index="${start + i}" style="height: ${this.itemHeight}px">
                    ${renderJobHtml(job)}
                </div>`
            ).join('')}
            <div style="height: ${bottomSpacer}px"></div>
        `;
    }

    renderItem(index) {
        const { start, end } = this.visibleRange;
        if (index < start || index >= end) return;

        const localIndex = index - start;
        const items = this.content.querySelectorAll('.virtual-item');
        if (items[localIndex]) {
            items[localIndex].innerHTML = renderJobHtml(this.jobs[index]);
        }
    }

    // Scroll to specific job
    scrollToJob(jobId) {
        const idx = this.jobIndex.get(jobId);
        if (idx !== undefined) {
            this.viewport.scrollTop = idx * this.itemHeight;
        }
    }
}
```

#### 3.5.2 Updated SSE Handler

```javascript
// Handle new event types
eventSource.onmessage = (event) => {
    const data = JSON.parse(event.data);

    switch (data.type) {
        case 'init':
            // Initial load (paginated)
            virtualQueue.setJobs(data.jobs);
            updateStats(data.stats);
            break;

        case 'batch_added':
            // Batch of new jobs
            virtualQueue.addJobs(data.jobs);
            cachedStats.pending += data.jobs.length;
            updateStats(cachedStats);
            break;

        case 'added':
            // Single job (backwards compat)
            virtualQueue.addJobs([data.job]);
            cachedStats.pending++;
            updateStats(cachedStats);
            break;

        case 'progress':
            // Delta update (new format)
            if (data.progress_update) {
                updateProgressDelta(data.progress_update);
            } else if (data.job) {
                // Legacy format
                virtualQueue.updateJob(data.job);
            }
            break;

        case 'probed':
            // Job was probed, now has duration/bitrate
            virtualQueue.updateJob(data.job);
            break;

        case 'started':
        case 'complete':
        case 'failed':
        case 'cancelled':
            virtualQueue.updateJob(data.job);
            updateStatsFromEvent(data.type, data.job);
            break;
    }
};

// Efficient progress update (no job lookup in array)
function updateProgressDelta(update) {
    // Update only the specific DOM elements
    const el = document.querySelector(`[data-job-id="${update.id}"]`);
    if (el) {
        const progressBar = el.querySelector('.progress-fill');
        if (progressBar) {
            progressBar.style.width = update.progress + '%';
        }
        const details = el.querySelector('.job-details');
        if (details) {
            details.innerHTML = `
                <span class="job-detail">${update.progress.toFixed(1)}%</span>
                <span class="job-detail">${update.speed.toFixed(2)}x</span>
                <span class="job-detail">ETA: ${update.eta || '...'}</span>
            `;
        }
    }

    // Update active panel too
    const activeEl = document.querySelector(`#now-processing-content [data-job-id="${update.id}"]`);
    if (activeEl) {
        // Similar updates...
    }
}
```

#### 3.5.3 Indexed Status Lookups

```javascript
// Maintain indexes for O(1) status filtering
class JobStateIndex {
    constructor() {
        this.byStatus = {
            pending_probe: new Set(),
            pending: new Set(),
            running: new Set(),
            complete: new Set(),
            failed: new Set(),
            cancelled: new Set()
        };
    }

    add(job) {
        this.byStatus[job.status]?.add(job.id);
    }

    remove(job) {
        Object.values(this.byStatus).forEach(set => set.delete(job.id));
    }

    update(job, oldStatus) {
        if (oldStatus && this.byStatus[oldStatus]) {
            this.byStatus[oldStatus].delete(job.id);
        }
        this.byStatus[job.status]?.add(job.id);
    }

    getRunning() {
        return Array.from(this.byStatus.running);
    }

    getPending() {
        return Array.from(this.byStatus.pending);
    }
}

// Use in updateActivePanel
function updateActivePanel() {
    const runningIds = jobIndex.getRunning();  // O(1)
    const runningJobs = runningIds.map(id => jobMap.get(id));
    // ... render ...
}
```

### 3.6 Implementation Phases

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        IMPLEMENTATION ROADMAP                                │
└─────────────────────────────────────────────────────────────────────────────┘

PHASE 3.A: SSE Batching (SAFE, INCREMENTAL)
────────────────────────────────────────────
Target: Reduce event flood for bulk adds
Risk: Low
Effort: ~2 hours
Files:
  - internal/jobs/queue.go (add batch broadcast)
  - internal/jobs/job.go (add Jobs field to JobEvent)
  - web/templates/index.html (handle batch_added event)

Changes:
  1. Add Jobs []*Job field to JobEvent struct
  2. Modify AddMultiple to collect jobs, broadcast once
  3. Add handleBatchAdded() in frontend
  4. Keep individual Add() unchanged (backwards compat)

PHASE 3.B: Delta Progress Updates (SAFE, INCREMENTAL)
────────────────────────────────────────────────────────
Target: Reduce SSE payload size for progress events
Risk: Low
Effort: ~1 hour
Files:
  - internal/jobs/job.go (add ProgressUpdate struct)
  - internal/jobs/queue.go (modify UpdateProgress broadcast)
  - web/templates/index.html (handle progress_update)

Changes:
  1. Add ProgressUpdate struct with ID, Progress, Speed, ETA
  2. Change progress broadcast to use ProgressUpdate
  3. Update frontend to handle both formats

PHASE 3.C: Virtual Scrolling (FRONTEND ONLY)
─────────────────────────────────────────────
Target: Render only visible jobs
Risk: Medium (changes UI behavior)
Effort: ~4 hours
Files:
  - web/templates/index.html (VirtualQueue class)

Changes:
  1. Implement VirtualQueue class
  2. Replace direct DOM updates with VirtualQueue methods
  3. Add CSS for virtual viewport
  4. Test scroll behavior, keyboard nav

PHASE 3.D: Deferred Probing (ARCHITECTURAL)
───────────────────────────────────────────
Target: Eliminate blocking scan
Risk: High (changes job lifecycle)
Effort: ~8 hours
Files:
  - internal/jobs/job.go (add StatusPendingProbe)
  - internal/jobs/queue.go (add AddPendingProbe, UpdateProbeData)
  - internal/jobs/worker.go (probe on demand)
  - internal/api/handler.go (streaming discovery)
  - web/templates/index.html (pending_probe status display)

REQUIRES APPROVAL BEFORE IMPLEMENTATION

Changes:
  1. Add pending_probe status
  2. Create AddPendingProbe method
  3. Modify worker to probe before transcode
  4. Stream files during discovery
  5. Update UI to show "Awaiting probe" state

PHASE 3.E: Paginated Init (ARCHITECTURAL)
─────────────────────────────────────────
Target: Don't send full queue on SSE connect
Risk: Medium
Effort: ~4 hours
Files:
  - internal/api/sse.go (paginated init)
  - internal/api/handler.go (pagination support)
  - web/templates/index.html (lazy loading)

REQUIRES APPROVAL BEFORE IMPLEMENTATION
```

### 3.7 What Does NOT Need to Change

The following components are **stable and should not be modified**:

1. **FFmpeg invocation logic** (`internal/ffmpeg/transcode.go`)
   - Proven transcoding pipeline
   - Hardware encoder detection and fallback
   - Progress parsing from stderr

2. **Preset system** (`internal/ffmpeg/presets.go`)
   - Codec definitions
   - Quality settings
   - Encoder selection

3. **Queue persistence** (`internal/jobs/queue.go:save()`)
   - Atomic JSON writes
   - Crash recovery

4. **Worker pool lifecycle** (`internal/jobs/worker.go:Start/Stop`)
   - Graceful shutdown
   - Context cancellation

5. **Hardware encoder fallback** (`internal/jobs/worker.go:341-354`)
   - Rate limiting
   - Automatic retry

### 3.8 Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Virtual scroll breaks keyboard nav | Medium | Add keyboard handler for arrow keys |
| Deferred probe misses skip reasons | High | Probe early in worker, before queue slot taken |
| Batch events lost on disconnect | Low | SSE reconnect fetches full state |
| pending_probe confuses users | Low | Clear UI label "Scanning..." |
| Memory leak in VirtualQueue | Medium | Clean up on job removal |

### 3.9 Performance Targets

| Metric | Current (50 jobs) | Target (20,000 jobs) |
|--------|-------------------|----------------------|
| Time to first job visible | ~5s | <100ms |
| DOM nodes | ~750 | ~300 (viewport only) |
| SSE events per bulk add | N | ceil(N/50) |
| Memory usage | 10MB | 50MB |
| Scroll FPS | 60 | 60 |
| Progress update latency | <50ms | <50ms |

### 3.10 Backwards Compatibility

All changes maintain backwards compatibility:

1. **SSE Events**: Old event types still work; `batch_added` is additive
2. **API**: Pagination parameters are optional; defaults to current behavior
3. **Job Status**: `pending_probe` is a new status; old statuses unchanged
4. **Frontend**: Fallback handlers for old event formats

---

## PHASE 4: IMPLEMENTATION CHECKLIST

### Phase 3.A: SSE Batching (Ready for Implementation)

- [ ] Add `Jobs []*Job` field to `JobEvent` struct
- [ ] Create `AddMultipleBatched()` method that collects jobs
- [ ] Broadcast single `batch_added` event after AddMultiple
- [ ] Add `handleBatchAdded(jobs)` in frontend
- [ ] Test with 100, 500, 1000 job adds
- [ ] Verify existing single-add still works

### Phase 3.B: Delta Progress (Ready for Implementation)

- [ ] Create `ProgressUpdate` struct
- [ ] Add `ProgressUpdate` field to `JobEvent`
- [ ] Modify `UpdateProgress()` to use delta format
- [ ] Update frontend to handle `progress_update` field
- [ ] Test progress updates still work
- [ ] Measure payload size reduction

### Phase 3.C: Virtual Scrolling (Needs Design Review)

- [ ] Design VirtualQueue class API
- [ ] Implement basic virtual scrolling
- [ ] Handle job updates within viewport
- [ ] Add scroll-to-job functionality
- [ ] Test with 1000, 5000, 20000 jobs
- [ ] Verify keyboard navigation works

### Phase 3.D: Deferred Probing (REQUIRES EXPLICIT APPROVAL)

⚠️ **STOP**: This phase changes core job lifecycle. Do not implement without approval.

- [ ] Get explicit approval from stakeholder
- [ ] Add `pending_probe` status constant
- [ ] Create `AddPendingProbe()` method
- [ ] Modify worker to probe on demand
- [ ] Update skip reason checking
- [ ] Stream files during discovery
- [ ] Update UI for new status
- [ ] Full integration test

### Phase 3.E: Paginated Init (REQUIRES EXPLICIT APPROVAL)

⚠️ **STOP**: This phase changes SSE protocol. Do not implement without approval.

- [ ] Get explicit approval from stakeholder
- [ ] Add pagination to ListJobs API
- [ ] Modify SSE init to send first page
- [ ] Implement lazy loading in frontend
- [ ] Test reconnect behavior
- [ ] Verify stats still accurate

---

## APPENDIX: Quick Reference

### A.1 File Locations

| Component | File | Lines |
|-----------|------|-------|
| Job struct | `internal/jobs/job.go` | 19-49 |
| Queue.Add | `internal/jobs/queue.go` | 130-184 |
| Queue.AddMultiple | `internal/jobs/queue.go` | 186-199 |
| Queue.broadcast | `internal/jobs/queue.go` | 521-533 |
| SSE handler | `internal/api/sse.go` | 10-63 |
| CreateJobs API | `internal/api/handler.go` | 99-159 |
| Frontend SSE | `web/templates/index.html` | 2739-2774 |
| handleJobAdded | `web/templates/index.html` | 2459-2479 |
| updateJobs | `web/templates/index.html` | 2514-2539 |

### A.2 Test Commands

```bash
# Run unit tests
go test ./...

# Build and run
go run ./cmd/shrinkray

# Profile memory
go tool pprof http://localhost:8080/debug/pprof/heap

# Frontend console stress test
stressTest(500);  // See forensics doc for function
```

---

**END OF DOCUMENT**

*This document must be approved before any Phase 3.D or 3.E implementation begins.*

# Job lifecycle

Jobs flow through a defined set of states from creation to completion.

## State machine

```mermaid
stateDiagram-v2
    [*] --> pending: Job created
    pending --> running: Worker picks up
    pending --> cancelled: User cancels
    pending --> skipped: Already target codec

    running --> complete: Transcode success
    running --> failed: Transcode error
    running --> cancelled: User cancels
    running --> pending: Requeued (pause)

    complete --> [*]
    failed --> [*]
    cancelled --> [*]
    skipped --> [*]

    note right of pending: Waiting in queue
    note right of running: FFmpeg processing
    note right of skipped: Already meets target codec/resolution
```

## Status descriptions

| Status | Description |
|--------|-------------|
| `pending` | Job is waiting in the queue for a worker |
| `running` | FFmpeg is actively transcoding |
| `complete` | Transcode finished successfully |
| `failed` | Transcode error (codec issue, disk full, etc.) |
| `cancelled` | User cancelled the job |
| `skipped` | Automatically skipped (file already meets criteria) |

## Creation flow

```mermaid
sequenceDiagram
    participant UI as Web UI
    participant API as Handler
    participant B as Browser
    participant Q as Queue
    participant SSE as Subscribers

    UI->>API: POST /api/jobs
    API-->>UI: 202 Accepted

    loop For each path
        API->>B: Probe file
        B-->>API: Metadata
        API->>Q: Add job
        Q->>SSE: Broadcast "added"
    end
```

1. User selects files/folders and preset in UI
2. API responds immediately with 202
3. Background goroutine probes each file
4. Jobs added to queue with metadata
5. SSE broadcasts notify UI of new jobs

## Execution flow

```mermaid
sequenceDiagram
    participant W as Worker
    participant Q as Queue
    participant FF as FFmpeg
    participant SSE as Subscribers

    W->>Q: GetNext()
    Q-->>W: Pending job
    W->>Q: StartJob()
    Q->>SSE: Broadcast "started"

    W->>FF: Spawn process

    loop Progress updates
        FF-->>W: Progress line
        W->>Q: UpdateProgress()
        Q->>SSE: Broadcast "progress"
    end

    alt Success
        FF-->>W: Exit 0
        W->>Q: CompleteJob()
        Q->>SSE: Broadcast "complete"
    else Failure
        FF-->>W: Exit non-zero
        W->>Q: FailJob()
        Q->>SSE: Broadcast "failed"
    end
```

## SmartShrink execution flow

SmartShrink jobs have an additional VMAF analysis phase before encoding:

```mermaid
sequenceDiagram
    participant W as Worker
    participant Q as Queue
    participant V as VMAF
    participant FF as FFmpeg
    participant SSE as Subscribers

    W->>Q: GetNext()
    Q-->>W: SmartShrink job
    W->>Q: StartJob()
    W->>Q: UpdatePhase("analyzing")
    Q->>SSE: Broadcast "progress"

    W->>V: Analyze(samples, threshold)

    loop Binary search
        V->>FF: Encode sample at CRF
        FF-->>V: Sample file
        V->>FF: Calculate VMAF score
        FF-->>V: Score
    end

    V-->>W: Optimal CRF/modifier
    W->>Q: UpdateVMAFResult()
    W->>Q: UpdatePhase("encoding")
    Q->>SSE: Broadcast "progress"

    W->>FF: Transcode with optimal settings

    loop Progress updates
        FF-->>W: Progress line
        W->>Q: UpdateProgress()
        Q->>SSE: Broadcast "progress"
    end

    FF-->>W: Exit 0
    W->>Q: CompleteJob()
    Q->>SSE: Broadcast "complete"
```

The VMAF analysis phase:
1. Extracts 3 samples at fixed positions (25%, 50%, 75%)
2. Uses binary search to find optimal CRF/bitrate meeting the quality threshold
3. Scores are averaged across all samples
4. Analysis runs in parallel (limited by worker count)

## Skip logic

Jobs are automatically skipped when:

1. **Compression presets** (`compress-hevc`, `compress-av1`):
   - File is already encoded in target codec

2. **Downscale presets** (`1080p`, `720p`):
   - File height is already at or below target resolution

Skip checking happens at job creation time. Skipped jobs appear in the queue with status `skipped` and an explanation in the error field.

## Cancellation

Cancelling a running job:

1. Worker pool receives cancel request
2. FFmpeg process is killed (SIGKILL)
3. Temp file is deleted
4. Job status set to `cancelled`
5. SSE broadcasts cancellation

## Requeue (pause)

When the queue is paused:

1. Running jobs are interrupted
2. Jobs reset to `pending` status
3. Jobs moved to front of queue
4. Workers stop picking up new jobs

Resume allows workers to continue.

## Retry

Failed jobs can be retried:

1. Original file is re-probed
2. New job created with same preset
3. Failed job removed from queue
4. New job starts in `pending` state

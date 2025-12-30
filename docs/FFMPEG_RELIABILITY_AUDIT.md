# FFmpeg Reliability Audit — Shrinkray

**Date:** 2025-12-30
**Status:** Investigation Complete

---

## Executive Summary

This audit examines FFmpeg usage in Shrinkray to identify failure modes, evaluate hardware acceleration behavior, and recommend improvements for reliability. The codebase already has solid foundations including bounded stderr capture, error pattern matching in the UI, and hardware encoder preflight testing. This document identifies remaining gaps and proposes targeted improvements.

---

## Step 1: Current FFmpeg Usage

### Invocation Points

| Location | Tool | Purpose |
|----------|------|---------|
| `internal/ffmpeg/transcode.go:161` | `ffmpeg` | Main video transcoding |
| `internal/ffmpeg/probe.go:64` | `ffprobe` | File metadata extraction |
| `internal/ffmpeg/hwaccel.go:154` | `ffmpeg` | Encoder availability detection |
| `internal/ffmpeg/hwaccel.go:254` | `ffmpeg` | Hardware encoder test (1-frame encode) |

### Command Structure

#### ffprobe (probe.go:64-70)
```
ffprobe -v quiet -print_format json -show_format -show_streams <path>
```
- Simple, safe invocation
- JSON output parsing with proper error handling
- 30-second timeout via context

#### ffmpeg transcode (transcode.go:148-159)
```
ffmpeg [inputArgs] -i <input> -y -progress pipe:1 -nostats [outputArgs] <output>
```

**Input Args** (from preset, goes before -i):
- Hardware acceleration: `-hwaccel videotoolbox`, `-hwaccel cuda -hwaccel_output_format cuda`, etc.
- VAAPI device: `-vaapi_device /dev/dri/renderD128`

**Output Args** (from preset):
- Encoder: `-c:v hevc_videotoolbox`, `-c:v libx265`, etc.
- Quality: `-crf 26`, `-b:v 1234k`, `-cq 28`, `-global_quality 27`, `-qp 27`
- Preset-specific: `-preset medium`, `-preset p4 -tune hq -rc vbr`, `-allow_sw 1`
- Scaling (if applicable): `-vf scale=-2:'min(ih,1080)'`
- Stream handling: `-map 0 -c:a copy -c:s copy`

#### ffmpeg encoder test (hwaccel.go:229-258)
```
ffmpeg [-vaapi_device <path>] -f lavfi -i color=c=black:s=256x256:d=0.1 -frames:v 1 -c:v <encoder> -f null -
```
- 10-second timeout
- Tests if hardware encoder actually works (not just listed)
- 256x256 resolution chosen for QSV minimum requirements

### Codec Defaults

| Encoder | Quality Flag | Default Value | Notes |
|---------|--------------|---------------|-------|
| libx265 | `-crf` | 26 | Software HEVC |
| hevc_videotoolbox | `-b:v` | 35% of source | Dynamic bitrate (500k-15000k) |
| hevc_nvenc | `-cq` | 28 | VBR mode |
| hevc_qsv | `-global_quality` | 27 | Intel Quick Sync |
| hevc_vaapi | `-qp` | 27 | Linux VA-API |
| libsvtav1 | `-crf` | 35 | Software AV1 |
| av1_videotoolbox | `-b:v` | 25% of source | Dynamic bitrate |
| av1_nvenc | `-cq` | 32 | RTX 40+ |
| av1_qsv | `-global_quality` | 32 | Intel Arc |
| av1_vaapi | `-qp` | 32 | Linux VA-API |

### Failure Timing

| Phase | When | Current Handling |
|-------|------|------------------|
| Pre-FFmpeg | File missing, permissions | Caught by `os.Stat()` before transcode |
| Initialization | Codec not found, HW init fail | FFmpeg exits quickly, stderr captured |
| Mid-transcode | Corrupt frames, memory issues | FFmpeg exits, stderr captured |
| Finalization | Disk full, permissions | FFmpeg exits, but output file may exist |
| Post-FFmpeg | Output larger than input | Checked in worker.go:332-338 |

---

## Step 2: Failure Taxonomy

### Existing UI Pattern Recognition

The frontend (`web/templates/index.html:1678-1876`) already recognizes 13 error patterns:

| ID | Pattern | Confidence | Avoidable? | Notes |
|----|---------|------------|------------|-------|
| no_such_file | `No such file or directory` | High | Race condition | File moved after queue add |
| moov_atom | `moov atom not found` | High | No | Incomplete MP4 downloads |
| invalid_data | `Invalid data found` | High | No | Corrupted input |
| codec_params | `Could not find codec parameters` | High | No | Unknown streams |
| unknown_decoder | `Decoder .* not found` | High | No | Missing codec support |
| decode_error | `decode_slice_header error` | Medium | No | Corrupted frames |
| too_many_packets | `Too many packets buffered` | Medium | **Yes** | Missing `-max_muxing_queue_size` |
| permission_denied | `Permission denied` | High | Yes | Should preflight |
| hwaccel_init | `NVENC\|CUDA\|VAAPI.*failed` | High | **Yes** | Should fallback |
| out_of_memory | `Out of memory` | High | Partial | Worker count limit |
| disk_full | `No space left on device` | High | Partial | Could preflight |
| dts_error | `non-monotonous DTS` | Medium | **Yes** | Missing timestamp flags |
| output_larger | `larger than original` | High | Expected | Already handled |

### Failure Classes Analysis

#### Unavoidable Failures (Accept)
- Corrupt/incomplete source files (moov_atom, invalid_data, decode_error)
- Unsupported codecs (unknown_decoder, codec_params)
- Insufficient system resources (out_of_memory, disk_full - can only mitigate)

#### Avoidable Failures (Fix)

**1. Hardware Acceleration Failures**
- **Problem:** HW encoder passes preflight test but fails mid-transcode
- **Cause:** GPU contention, thermal throttling, driver bugs
- **Fix:** Automatic software fallback on HW failure

**2. Muxing Buffer Exhaustion**
- **Problem:** `Too many packets buffered for output stream`
- **Cause:** Default muxing queue size (8192 packets) insufficient for some inputs
- **Fix:** Add `-max_muxing_queue_size 4096` as default

**3. Timestamp Issues**
- **Problem:** `non-monotonous DTS in output stream` warnings/errors
- **Cause:** Source has broken timestamps, MKV container strict about DTS ordering
- **Fix:** Add timestamp correction flags

**4. Subtitle Stream Failures**
- **Problem:** Subtitle codecs incompatible with MKV container
- **Cause:** `-c:s copy` fails for bitmap subtitles when mapping all streams
- **Fix:** Consider `-sn` or conditional subtitle handling

**5. Audio Stream Failures**
- **Problem:** Some audio codecs fail to remux into MKV
- **Cause:** `-c:a copy` with incompatible container/codec combinations
- **Current Risk:** Low - MKV supports most audio codecs

---

## Step 3: Hardware Acceleration Behavior

### Current Implementation (hwaccel.go)

**Strengths:**
- Tests actual encoding capability, not just FFmpeg's encoder list
- Uses realistic test parameters (256x256 for QSV compatibility)
- 10-second timeout prevents hangs
- Caches results globally
- Auto-detects VAAPI device path

**Weaknesses:**
1. **No runtime fallback:** If HW encoder fails during actual transcode, job fails completely
2. **Stale cache:** Detection happens once at startup, no re-detection if GPU state changes
3. **No transient error detection:** GPU memory full, encoder busy, thermal throttling

### Hardware Encoder Failure Modes

| Encoder | Common Failures | Detectable? | Fallback? |
|---------|-----------------|-------------|-----------|
| NVENC | Session limit (2-3 concurrent) | At runtime | No |
| NVENC | Memory exhaustion | At runtime | No |
| QSV | Minimum resolution not met | At preflight | Yes |
| QSV | Unsupported pixel format | At runtime | No |
| VAAPI | Device permission denied | At preflight | Yes |
| VAAPI | GPU memory exhaustion | At runtime | No |
| VideoToolbox | `-allow_sw 1` already handles fallback | N/A | Built-in |

### Recommendations for HW Acceleration

1. **Implement software fallback on HW failure**
   - Detect specific HW failure patterns in stderr
   - Re-queue job with software encoder
   - Mark as "retried with software"

2. **Add NVENC session awareness (nice-to-have)**
   - Track concurrent NVENC jobs
   - Limit to 2 concurrent for consumer GPUs

3. **Consider removing QSV/VAAPI from auto-detection (alternative)**
   - These have more edge cases than NVENC/VideoToolbox
   - Could offer as manual opt-in

---

## Step 4: Command Robustness Review

### Current Flags Assessment

| Flag | Current | Issue |
|------|---------|-------|
| `-y` | Present | Good - overwrites without prompt |
| `-progress pipe:1` | Present | Good - progress parsing |
| `-nostats` | Present | Good - clean output |
| `-map 0` | Present | **Risk** - maps ALL streams including problematic ones |
| `-c:a copy` | Present | Generally safe |
| `-c:s copy` | Present | **Risk** - bitmap subtitles can fail |
| `-max_muxing_queue_size` | Missing | **Should add** |

### Proposed Safer Defaults

#### 1. Add muxing queue size (Recommended)
```go
// In presets.go, add to all encoder outputArgs:
"-max_muxing_queue_size", "4096",
```
**Why:** Prevents `Too many packets buffered` errors on files with unusual timing. Default of 8192 packets is sometimes insufficient. Cost: none.

#### 2. Handle subtitle stream failures (Recommended)
Option A: Drop subtitles entirely
```go
"-sn", // No subtitles
```

Option B: Keep but be defensive (preferred)
```go
"-c:s", "copy",
"-disposition:s", "0", // Mark as non-default to reduce issues
```

**Current risk level:** Low-medium. MKV is very permissive, but DVD/Blu-ray bitmap subtitles (`dvd_subtitle`, `hdmv_pgs_subtitle`) occasionally fail.

#### 3. Consider timestamp repair flags (Optional)
```go
"-fflags", "+genpts",      // Generate PTS if missing
"-avoid_negative_ts", "make_zero", // Fix negative timestamps
```
**Trade-off:** May hide genuine source issues. Only add if DTS errors are frequent.

### Flags NOT to Add
- `-err_detect ignore_err` — Hides corruption, produces bad output
- `-vsync passthrough` — Can cause A/V sync issues
- `-async 1` — Deprecated, causes audio glitches
- `-fflags +discardcorrupt` — Drops frames unpredictably

---

## Step 5: Fallback & Retry Strategy

### Current Behavior
- Job fails → Status=failed, stderr captured
- User can manually retry via UI
- No automatic retry or fallback

### Proposed Retry Decision Matrix

| Error Pattern | Action | Retries | Notes |
|---------------|--------|---------|-------|
| HW encoder init failed | Retry with software | 1 | Auto-fallback |
| HW encoder mid-transcode fail | Retry with software | 1 | Auto-fallback |
| NVENC session limit | Wait and retry | 1 | After other job finishes |
| Too many packets buffered | Retry with larger queue | 1 | Add `-max_muxing_queue_size 16384` |
| Permission denied | Fail immediately | 0 | User action required |
| Disk full | Fail immediately | 0 | User action required |
| File not found | Fail immediately | 0 | File gone |
| Corrupt/invalid data | Fail immediately | 0 | Source issue |
| Unknown errors | Fail immediately | 0 | Don't guess |

### Implementation Design

```go
// In worker.go, after transcode fails:

type RetryDecision struct {
    ShouldRetry bool
    NewPreset   *Preset  // nil = same preset
    Reason      string
}

func analyzeFailure(err *ffmpeg.TranscodeError, preset *Preset) RetryDecision {
    stderr := strings.ToLower(err.Stderr)

    // Hardware encoder failures -> retry with software
    hwPatterns := []string{
        "nvenc", "cuda", "vaapi", "qsv", "videotoolbox",
        "hardware", "device", "initialization failed",
    }
    if preset.Encoder != ffmpeg.HWAccelNone {
        for _, p := range hwPatterns {
            if strings.Contains(stderr, p) {
                return RetryDecision{
                    ShouldRetry: true,
                    NewPreset:   softwarePresetFor(preset),
                    Reason:      "hardware encoder failed, retrying with software",
                }
            }
        }
    }

    // Muxing queue exhaustion -> retry with larger buffer
    if strings.Contains(stderr, "too many packets") {
        return RetryDecision{
            ShouldRetry: true,
            NewPreset:   nil, // Same preset, different flag
            Reason:      "muxing buffer exhausted, retrying with larger queue",
        }
    }

    // Everything else: don't retry
    return RetryDecision{ShouldRetry: false}
}
```

### Retry Visibility

When a job retries:
1. Original job marked as "failed (retrying with software)"
2. New job created with reference to original
3. SSE event: `{type: "retry", originalJob: {...}, newJob: {...}}`
4. UI shows retry indicator and reason

---

## Step 6: Prioritized Recommendations

### Must-Fix (High confidence, low effort, clear benefit)

#### 1. Add `-max_muxing_queue_size 4096` to all presets
**File:** `internal/ffmpeg/presets.go`
**Effort:** 5 minutes
**Benefit:** Eliminates "Too many packets buffered" failures
**Risk:** None

```go
// Add to all encoder configs in outputArgs:
outputArgs = append(outputArgs, "-max_muxing_queue_size", "4096")
```

#### 2. Implement software fallback for hardware encoder failures
**Files:** `internal/jobs/worker.go`, `internal/ffmpeg/transcode.go`
**Effort:** 1-2 hours
**Benefit:** Eliminates class of failures where HW works at detection but fails at runtime
**Risk:** Low - fallback to known-working path

**Implementation:**
- Detect HW-related patterns in stderr
- Create new job with software preset
- Mark original as "failed (auto-retried)"
- Limit to 1 retry attempt

### Nice-to-Have (Good improvements, more effort)

#### 3. Add preflight permission check
**Effort:** 30 minutes
Before starting transcode, verify:
- Input file is readable
- Output directory is writable
- Sufficient disk space (~2x input size)

#### 4. Add pattern for subtitle copy failures
**File:** `web/templates/index.html`
**Pattern:** `/Subtitle codec .* is not supported in MKV|Cannot determine format of subtitle/i`

#### 5. Track retry history on jobs
**File:** `internal/jobs/job.go`
Add fields:
```go
RetryCount    int      `json:"retry_count,omitempty"`
RetryReason   string   `json:"retry_reason,omitempty"`
OriginalJobID string   `json:"original_job_id,omitempty"`
```

#### 6. Add NVENC concurrent session tracking
Track how many NVENC jobs are running, warn if approaching limit (typically 2-3 for consumer GPUs).

### Not Recommended

- **Adding `-err_detect ignore_err`:** Hides real problems, produces corrupt output
- **Automatic retry for all errors:** Would waste resources on unfixable issues
- **Removing subtitle copy:** Most files work fine, would be user-unfriendly
- **Adding complex timestamp repair:** Masks source issues, may cause sync problems

---

## Appendix: Error Pattern Gaps

Patterns NOT currently in the UI that could be added:

| Pattern | Regex | Summary |
|---------|-------|---------|
| NVENC Session Limit | `OpenEncodeSessionEx failed.*0x7\|maximum simultaneous` | NVENC concurrent encode limit reached |
| Subtitle Copy Fail | `Subtitle codec.*not supported\|cannot convert subtitle` | Subtitle remux failed |
| Pixel Format | `Incompatible pixel format\|pixel format.*not supported` | HW encoder doesn't support source format |
| B-frame Error | `dpb_can_reencode_with_bframes\|reference picture missing` | Complex GOP structure issues |

---

## Conclusion

Shrinkray's FFmpeg usage is already well-structured with good error capture and UI pattern matching. The main opportunities for improvement are:

1. **Add one flag** (`-max_muxing_queue_size`) to prevent a class of avoidable failures
2. **Add software fallback** when hardware encoding fails at runtime
3. **Minor UI additions** for subtitle and NVENC session errors

These changes would meaningfully reduce unexpected failures while keeping the codebase simple and avoiding over-engineering.

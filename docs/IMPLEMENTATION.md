# UI Performance Optimization Implementation

**Version**: 1.0
**Date**: 2024-12-30
**Status**: IMPLEMENTED

---

## Summary

This implementation addresses UI performance degradation when handling large queues (5k-20k jobs) in Shrinkray. The solution uses feature flags for safe, phased rollout.

## Implemented Features

### Phase 3.A: Batch SSE Events ✅
- `batch_added` event type for adding many jobs at once
- Reduces SSE event flood from N events to 1 event when adding multiple jobs
- **Commit**: `27496a2`

### Phase 3.B: Delta Progress Updates ✅
- `ProgressUpdate` struct with only ID, progress, speed, ETA
- Reduces progress event payload from ~500 bytes to ~80 bytes
- **Commit**: `27496a2`

### Phase 3.C: Virtual Scrolling ✅
- Renders only visible jobs + overscan (~40 items) instead of entire queue
- Maintains jobMap, jobOrder, statusIndex for O(1) lookups
- Falls back to full rendering when flag is off
- **Feature Flag**: `SHRINKRAY_FEATURE_VIRTUAL_SCROLL=1`
- **Commit**: `bd1df6e`

### Phase 3.D: Deferred Probing ✅
- Jobs added immediately as `pending_probe` without waiting for ffprobe
- Workers probe files when picking up jobs
- Streaming discovery: UI shows jobs within ~100ms instead of waiting minutes
- **Feature Flag**: `SHRINKRAY_FEATURE_DEFERRED_PROBING=1`
- **Commit**: `32a6eb4`

### Completed Jobs Section ✅
- Moves completed jobs to collapsible "Recently Completed" section
- Shows compact view: filename + space saved
- Keeps main queue focused on actionable items
- **Commit**: `5a85c98`

## Feature Flags

All new features are behind feature flags with safe defaults (off):

| Flag | Environment Variable | Default | Effect |
|------|---------------------|---------|--------|
| Virtual Scroll | `SHRINKRAY_FEATURE_VIRTUAL_SCROLL` | off | Only render visible jobs |
| Deferred Probing | `SHRINKRAY_FEATURE_DEFERRED_PROBING` | off | Probe on worker pickup |
| Batched SSE | `SHRINKRAY_FEATURE_BATCHED_SSE` | on | Batch add events |
| Delta Progress | `SHRINKRAY_FEATURE_DELTA_PROGRESS` | on | Small progress payloads |

### Enabling Features

```bash
# Enable virtual scrolling
export SHRINKRAY_FEATURE_VIRTUAL_SCROLL=1

# Enable deferred probing
export SHRINKRAY_FEATURE_DEFERRED_PROBING=1

# Or in config.yaml:
features:
  virtual_scroll: true
  deferred_probing: true
```

## Rollback Instructions

### Quick Rollback (Feature Flags)
Disable any feature without code changes:

```bash
# Disable virtual scrolling
export SHRINKRAY_FEATURE_VIRTUAL_SCROLL=0

# Disable deferred probing
export SHRINKRAY_FEATURE_DEFERRED_PROBING=0
```

Restart the service for changes to take effect.

### Code Rollback
If needed, revert to before performance optimizations:

```bash
# Revert to pre-Phase 3 state
git revert HEAD~4..HEAD

# Or reset to specific commit
git reset --hard 27496a2  # Before virtual scroll
git reset --hard f76d996  # Before batch SSE
```

### Rollback Considerations
- **Batch SSE**: Always safe - only affects how events are grouped
- **Delta Progress**: Always safe - frontend handles both formats
- **Virtual Scroll**: Safe - frontend gracefully falls back
- **Deferred Probing**: Consider pending_probe jobs in queue
  - Existing pending_probe jobs will still work (workers will probe them)
  - New jobs will use immediate probing

## Testing Recommendations

### Simulating Large Queues
```javascript
// In browser console: Add 5000 mock jobs for UI testing
(async () => {
  for (let i = 0; i < 100; i++) {
    await fetch('/api/jobs', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        paths: ['/media/test'],
        preset_id: 'hevc-sw'
      })
    });
  }
})();
```

### Performance Verification
1. Add 5k+ jobs to queue
2. Verify scrolling is smooth (no jank)
3. Verify progress updates work
4. Verify completed jobs move to "Recently Completed"
5. Verify job status changes update correctly

## Architecture Changes

### New Status: `pending_probe`
```
pending_probe → pending       (after successful probe)
pending_probe → failed        (probe error or skip reason found)
pending_probe → cancelled     (user cancelled)
```

### New Queue Methods
- `AddWithoutProbe(path, preset, size)` - Add job in pending_probe status
- `AddMultipleWithoutProbe(files, preset)` - Batch add pending_probe jobs
- `UpdateJobAfterProbe(id, probe)` - Update job with probe results

### New Browse Methods
- `DiscoverVideoFiles(paths, opts)` - Fast discovery without probing

## Files Changed

### Backend
- `internal/config/config.go` - FeatureFlags struct
- `internal/jobs/job.go` - StatusPendingProbe, IsWorkable(), NeedsProbe()
- `internal/jobs/queue.go` - AddWithoutProbe, UpdateJobAfterProbe, Stats.PendingProbe
- `internal/jobs/worker.go` - Probe pending_probe jobs before processing
- `internal/api/handler.go` - Feature flag routing, expose flags to frontend
- `internal/browse/browse.go` - DiscoverVideoFiles for fast discovery

### Frontend
- `web/templates/index.html`
  - Virtual scroll implementation
  - Completed jobs section
  - pending_probe status handling
  - probed event handling

## Future Work (Phase 3.E - Optional)

Paginated SSE init and lazy loading is documented in the spec but not implemented. The current features provide significant improvement for large queues. Pagination can be added if needed for even larger scales (50k+ jobs).

package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gwlsn/shrinkray/internal/ffmpeg"
	"github.com/gwlsn/shrinkray/internal/jobs"
)

// TestIssue35_RunningJobsNotSkippedAfterRestart verifies the fix for issue #35:
// When container restarts while jobs are running, those jobs should NOT be skipped.
// They should be reset to pending and processed in their original order.
//
// Scenario from issue:
// 1. Queue has JOB1, JOB2, JOB3, JOB4
// 2. JOB1 and JOB2 are running (25-50% progress)
// 3. Container restarts (simulated by closing and reopening store)
// 4. Expected: JOB1, JOB2, JOB3, JOB4 process in order
// 5. Bug was: JOB3, JOB4 processed first, JOB1 & JOB2 skipped
func TestIssue35_RunningJobsNotSkippedAfterRestart(t *testing.T) {
	tmpDir := t.TempDir()

	// === SESSION 1: Create queue with 4 jobs, start 2 of them ===
	store1, err := InitStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to init store: %v", err)
	}

	queue1, err := jobs.NewQueueWithStore(store1)
	if err != nil {
		t.Fatalf("failed to create queue: %v", err)
	}

	// Create 4 jobs representing episodes
	probe := &struct {
		Path     string
		Size     int64
		Duration time.Duration
	}{Size: 1000000, Duration: 10 * time.Second}

	// Add jobs in order: episode1, episode2, episode3, episode4
	ep1, _ := queue1.Add("/media/TV/Show/S01E01.mkv", "compress-hevc", &ffmpeg.ProbeResult{
		Path: "/media/TV/Show/S01E01.mkv", Size: probe.Size, Duration: probe.Duration,
	}, "")
	ep2, _ := queue1.Add("/media/TV/Show/S01E02.mkv", "compress-hevc", &ffmpeg.ProbeResult{
		Path: "/media/TV/Show/S01E02.mkv", Size: probe.Size, Duration: probe.Duration,
	}, "")
	ep3, _ := queue1.Add("/media/TV/Show/S01E03.mkv", "compress-hevc", &ffmpeg.ProbeResult{
		Path: "/media/TV/Show/S01E03.mkv", Size: probe.Size, Duration: probe.Duration,
	}, "")
	ep4, _ := queue1.Add("/media/TV/Show/S01E04.mkv", "compress-hevc", &ffmpeg.ProbeResult{
		Path: "/media/TV/Show/S01E04.mkv", Size: probe.Size, Duration: probe.Duration,
	}, "")

	t.Logf("Created 4 jobs: %s, %s, %s, %s", ep1.ID, ep2.ID, ep3.ID, ep4.ID)

	// Start episodes 1 and 2 (simulating encoding at 25-50%)
	queue1.StartJob(ep1.ID, "/tmp/ep1.tmp.mkv")
	queue1.UpdateProgress(ep1.ID, 25.0, 1.5, "5m remaining")

	queue1.StartJob(ep2.ID, "/tmp/ep2.tmp.mkv")
	queue1.UpdateProgress(ep2.ID, 50.0, 1.5, "3m remaining")

	// Verify state before "restart"
	if queue1.Get(ep1.ID).Status != jobs.StatusRunning {
		t.Error("ep1 should be running")
	}
	if queue1.Get(ep2.ID).Status != jobs.StatusRunning {
		t.Error("ep2 should be running")
	}
	if queue1.Get(ep3.ID).Status != jobs.StatusPending {
		t.Error("ep3 should be pending")
	}
	if queue1.Get(ep4.ID).Status != jobs.StatusPending {
		t.Error("ep4 should be pending")
	}

	// Close store (simulating container shutdown)
	store1.Close()

	// === SESSION 2: Restart - this is what InitStore does on container start ===
	t.Log("=== Simulating container restart ===")

	store2, err := InitStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to reinit store: %v", err)
	}
	defer store2.Close()

	queue2, err := jobs.NewQueueWithStore(store2)
	if err != nil {
		t.Fatalf("failed to recreate queue: %v", err)
	}

	// === VERIFY: The fix for issue #35 ===

	// 1. All 4 jobs should still exist
	allJobs := queue2.GetAll()
	if len(allJobs) != 4 {
		t.Fatalf("Expected 4 jobs after restart, got %d", len(allJobs))
	}
	t.Logf("✓ All 4 jobs present after restart")

	// 2. Episodes 1 and 2 should be reset to PENDING (not running or skipped)
	gotEp1 := queue2.Get(ep1.ID)
	gotEp2 := queue2.Get(ep2.ID)

	if gotEp1.Status != jobs.StatusPending {
		t.Errorf("ep1 should be PENDING after restart, got %s", gotEp1.Status)
	}
	if gotEp2.Status != jobs.StatusPending {
		t.Errorf("ep2 should be PENDING after restart, got %s", gotEp2.Status)
	}
	t.Logf("✓ Running jobs reset to pending")

	// 3. Progress should be reset to 0
	if gotEp1.Progress != 0 {
		t.Errorf("ep1 progress should be 0 after restart, got %.0f%%", gotEp1.Progress)
	}
	if gotEp2.Progress != 0 {
		t.Errorf("ep2 progress should be 0 after restart, got %.0f%%", gotEp2.Progress)
	}
	t.Logf("✓ Progress reset to 0")

	// 4. CRITICAL: GetNext() should return ep1, NOT ep3
	// This was the actual bug - the queue was skipping reset jobs
	next := queue2.GetNext()
	if next == nil {
		t.Fatal("GetNext() returned nil")
	}
	if next.ID != ep1.ID {
		t.Errorf("GetNext() should return ep1 (first reset job), but got %s (%s)",
			next.ID, next.InputPath)
	}
	t.Logf("✓ GetNext() returns ep1 (not skipping to ep3)")

	// 5. Verify the full processing order would be correct
	// Simulate processing the queue to verify order
	expectedOrder := []string{ep1.ID, ep2.ID, ep3.ID, ep4.ID}
	for i, expectedID := range expectedOrder {
		next := queue2.GetNext()
		if next == nil {
			t.Fatalf("GetNext() returned nil at position %d", i)
		}
		if next.ID != expectedID {
			t.Errorf("Position %d: expected %s, got %s", i, expectedID, next.ID)
		}
		// Mark as running so GetNext returns the next pending job
		queue2.StartJob(next.ID, "/tmp/temp.mkv")
	}
	t.Logf("✓ Full processing order is correct: ep1 → ep2 → ep3 → ep4")

	// 6. No more pending jobs
	if queue2.GetNext() != nil {
		t.Error("Should be no more pending jobs")
	}
	t.Logf("✓ All jobs accounted for")
}

// TestIssue35_APIResponseAfterRestart verifies the API returns correct data
// after restart, which is what the UI relies on.
func TestIssue35_APIResponseAfterRestart(t *testing.T) {
	tmpDir := t.TempDir()

	// Session 1: Create and start jobs
	store1, _ := InitStore(tmpDir)
	queue1, _ := jobs.NewQueueWithStore(store1)

	probe := &ffmpeg.ProbeResult{Path: "/test.mkv", Size: 1000000, Duration: 10 * time.Second}
	job1, _ := queue1.Add("/media/video1.mkv", "compress-hevc", probe, "")
	_, _ = queue1.Add("/media/video2.mkv", "compress-hevc", probe, "")

	queue1.StartJob(job1.ID, "/tmp/temp1.mkv")
	queue1.UpdateProgress(job1.ID, 50.0, 1.5, "5m")

	store1.Close()

	// Session 2: Restart
	store2, _ := InitStore(tmpDir)
	defer store2.Close()
	queue2, _ := jobs.NewQueueWithStore(store2)

	// Simulate what the /api/jobs endpoint returns
	allJobs := queue2.GetAll()
	stats := queue2.Stats()

	// Build API response (matches api/handler.go ListJobs)
	type APIResponse struct {
		Jobs  []*jobs.Job `json:"jobs"`
		Stats jobs.Stats  `json:"stats"`
	}
	response := APIResponse{Jobs: allJobs, Stats: stats}

	// Serialize to JSON (what the UI receives)
	jsonData, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Failed to marshal response: %v", err)
	}

	// Parse it back (what the UI would do)
	var parsed APIResponse
	json.Unmarshal(jsonData, &parsed)

	// Verify UI would see correct state
	if len(parsed.Jobs) != 2 {
		t.Errorf("UI should see 2 jobs, got %d", len(parsed.Jobs))
	}

	// Find job1 in response
	var foundJob1 *jobs.Job
	for _, j := range parsed.Jobs {
		if j.ID == job1.ID {
			foundJob1 = j
			break
		}
	}

	if foundJob1 == nil {
		t.Fatal("Job1 not found in API response")
	}

	// UI should see job1 as PENDING (not running, not missing)
	if foundJob1.Status != jobs.StatusPending {
		t.Errorf("UI should see job1 as PENDING, got %s", foundJob1.Status)
	}

	// Progress should be 0 (not 50%)
	if foundJob1.Progress != 0 {
		t.Errorf("UI should see job1 progress as 0, got %.0f", foundJob1.Progress)
	}

	// Stats should show 2 pending
	if parsed.Stats.Pending != 2 {
		t.Errorf("Stats should show 2 pending, got %d", parsed.Stats.Pending)
	}
	if parsed.Stats.Running != 0 {
		t.Errorf("Stats should show 0 running, got %d", parsed.Stats.Running)
	}

	t.Log("✓ API response (and thus UI) shows correct state after restart")
}

// TestIssue35_MultipleRestarts verifies behavior survives multiple restarts
func TestIssue35_MultipleRestarts(t *testing.T) {
	tmpDir := t.TempDir()

	// Create initial jobs
	store1, _ := InitStore(tmpDir)
	queue1, _ := jobs.NewQueueWithStore(store1)

	probe := &ffmpeg.ProbeResult{Path: "/test.mkv", Size: 1000000, Duration: 10 * time.Second}
	job1, _ := queue1.Add("/media/video1.mkv", "compress-hevc", probe, "")
	_, _ = queue1.Add("/media/video2.mkv", "compress-hevc", probe, "")

	queue1.StartJob(job1.ID, "/tmp/temp.mkv")
	store1.Close()

	// First restart
	store2, _ := InitStore(tmpDir)
	queue2, _ := jobs.NewQueueWithStore(store2)

	if queue2.Get(job1.ID).Status != jobs.StatusPending {
		t.Error("After first restart: job1 should be pending")
	}

	// Start job1 again, then restart again
	queue2.StartJob(job1.ID, "/tmp/temp.mkv")
	store2.Close()

	// Second restart
	store3, _ := InitStore(tmpDir)
	defer store3.Close()
	queue3, _ := jobs.NewQueueWithStore(store3)

	if queue3.Get(job1.ID).Status != jobs.StatusPending {
		t.Error("After second restart: job1 should be pending")
	}

	// Should still be first in queue
	next := queue3.GetNext()
	if next == nil || next.ID != job1.ID {
		t.Error("After multiple restarts: job1 should still be first")
	}

	t.Log("✓ Behavior correct after multiple restarts")
}

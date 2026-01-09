package jobs_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/gwlsn/shrinkray/internal/ffmpeg"
	"github.com/gwlsn/shrinkray/internal/jobs"
	"github.com/gwlsn/shrinkray/internal/store"
)

func TestQueue(t *testing.T) {
	queue := jobs.NewQueue()

	// Create a test probe result
	probe := &ffmpeg.ProbeResult{
		Path:     "/media/video.mkv",
		Size:     1000000,
		Duration: 10 * time.Second,
	}

	// Add a job
	job, err := queue.Add(probe.Path, "compress", probe)
	if err != nil {
		t.Fatalf("failed to add job: %v", err)
	}

	if job.ID == "" {
		t.Error("job ID should not be empty")
	}

	if job.Status != jobs.StatusPending {
		t.Errorf("expected status pending, got %s", job.Status)
	}

	// Get the job back
	got := queue.Get(job.ID)
	if got == nil {
		t.Fatal("failed to get job")
	}

	if got.InputPath != probe.Path {
		t.Errorf("expected input path %s, got %s", probe.Path, got.InputPath)
	}

	t.Logf("Created job: %+v", job)
}

func TestQueueLifecycle(t *testing.T) {
	queue := jobs.NewQueue()

	probe := &ffmpeg.ProbeResult{
		Path:     "/media/video.mkv",
		Size:     1000000,
		Duration: 10 * time.Second,
	}

	// Add job
	job, _ := queue.Add(probe.Path, "compress", probe)

	// Start job
	err := queue.StartJob(job.ID, "/tmp/video.tmp.mkv")
	if err != nil {
		t.Fatalf("failed to start job: %v", err)
	}

	got := queue.Get(job.ID)
	if got.Status != jobs.StatusRunning {
		t.Errorf("expected status running, got %s", got.Status)
	}

	// Update progress
	queue.UpdateProgress(job.ID, 50.0, 1.5, "5m remaining")

	got = queue.Get(job.ID)
	if got.Progress != 50.0 {
		t.Errorf("expected progress 50, got %f", got.Progress)
	}

	// Complete job
	err = queue.CompleteJob(job.ID, "/media/video.mkv", 500000)
	if err != nil {
		t.Fatalf("failed to complete job: %v", err)
	}

	got = queue.Get(job.ID)
	if got.Status != jobs.StatusComplete {
		t.Errorf("expected status complete, got %s", got.Status)
	}

	if got.SpaceSaved != 500000 {
		t.Errorf("expected space saved 500000, got %d", got.SpaceSaved)
	}

	t.Logf("Completed job: %+v", got)
}

func TestQueuePersistence(t *testing.T) {
	tmpDir := t.TempDir()
	dbFile := filepath.Join(tmpDir, "test.db")

	// Create store and queue
	store1, err := store.NewSQLiteStore(dbFile)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	queue1, err := jobs.NewQueueWithStore(store1)
	if err != nil {
		t.Fatalf("failed to create queue: %v", err)
	}

	probe := &ffmpeg.ProbeResult{
		Path:     "/media/video.mkv",
		Size:     1000000,
		Duration: 10 * time.Second,
	}

	// For 1080p preset, use a 4K video that actually needs downscaling
	probe4K := &ffmpeg.ProbeResult{
		Path:     "/media/video2.mkv",
		Size:     1000000,
		Duration: 10 * time.Second,
		Height:   2160, // 4K needs downscaling to 1080p
	}

	job1, _ := queue1.Add(probe.Path, "compress", probe)
	job2, _ := queue1.Add(probe4K.Path, "1080p", probe4K)

	// Complete one job
	queue1.StartJob(job1.ID, "/tmp/temp.mkv")
	queue1.CompleteJob(job1.ID, "/media/video.mkv", 500000)

	// Close store
	store1.Close()

	// Create a new store and queue from the same file
	store2, err := store.NewSQLiteStore(dbFile)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store2.Close()

	queue2, err := jobs.NewQueueWithStore(store2)
	if err != nil {
		t.Fatalf("failed to load queue: %v", err)
	}

	// Verify jobs were persisted
	all := queue2.GetAll()
	if len(all) != 2 {
		t.Errorf("expected 2 jobs, got %d", len(all))
	}

	got1 := queue2.Get(job1.ID)
	if got1 == nil || got1.Status != jobs.StatusComplete {
		t.Errorf("job1 not persisted correctly: %+v", got1)
	}

	got2 := queue2.Get(job2.ID)
	if got2 == nil || got2.Status != jobs.StatusPending {
		t.Errorf("job2 not persisted correctly: %+v", got2)
	}

	t.Log("Queue persisted and loaded successfully")
}

func TestQueueRunningJobsResetOnLoad(t *testing.T) {
	tmpDir := t.TempDir()
	dbFile := filepath.Join(tmpDir, "test.db")

	// Create store and queue, start a job
	store1, err := store.NewSQLiteStore(dbFile)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	queue1, err := jobs.NewQueueWithStore(store1)
	if err != nil {
		t.Fatalf("failed to create queue: %v", err)
	}

	probe := &ffmpeg.ProbeResult{
		Path:     "/media/video.mkv",
		Size:     1000000,
		Duration: 10 * time.Second,
	}

	job, _ := queue1.Add(probe.Path, "compress", probe)
	queue1.StartJob(job.ID, "/tmp/temp.mkv")

	// Verify it's running
	if queue1.Get(job.ID).Status != jobs.StatusRunning {
		t.Fatal("job should be running")
	}

	// Close store
	store1.Close()

	// Simulate restart - create new store (which resets running jobs)
	store2, err := store.NewSQLiteStore(dbFile)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store2.Close()

	// Reset running jobs (this is normally done by InitStore)
	count, err := store2.ResetRunningJobs()
	if err != nil {
		t.Fatalf("failed to reset running jobs: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 job reset, got %d", count)
	}

	queue2, err := jobs.NewQueueWithStore(store2)
	if err != nil {
		t.Fatalf("failed to load queue: %v", err)
	}

	// Running job should be reset to pending
	got := queue2.Get(job.ID)
	if got.Status != jobs.StatusPending {
		t.Errorf("expected running job to be reset to pending, got %s", got.Status)
	}

	t.Log("Running jobs reset to pending on load")
}

func TestQueueRunningJobsResetAndVisible(t *testing.T) {
	// This test verifies the fix for issue #35:
	// Running jobs should be reset to pending AND appear correctly in GetNext()
	tmpDir := t.TempDir()
	dbFile := filepath.Join(tmpDir, "test.db")

	// Create store and queue with 4 jobs
	store1, err := store.NewSQLiteStore(dbFile)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	queue1, err := jobs.NewQueueWithStore(store1)
	if err != nil {
		t.Fatalf("failed to create queue: %v", err)
	}

	probe := &ffmpeg.ProbeResult{
		Path:     "/media/video.mkv",
		Size:     1000000,
		Duration: 10 * time.Second,
	}

	job1, _ := queue1.Add("/media/video1.mkv", "compress", probe)
	job2, _ := queue1.Add("/media/video2.mkv", "compress", probe)
	job3, _ := queue1.Add("/media/video3.mkv", "compress", probe)
	_, _ = queue1.Add("/media/video4.mkv", "compress", probe) // job4

	// Start jobs 1 and 2 (simulate running at 25-50%)
	queue1.StartJob(job1.ID, "/tmp/temp1.mkv")
	queue1.StartJob(job2.ID, "/tmp/temp2.mkv")

	// Verify jobs 1&2 are running, 3&4 are pending
	if queue1.Get(job1.ID).Status != jobs.StatusRunning {
		t.Fatal("job1 should be running")
	}
	if queue1.Get(job3.ID).Status != jobs.StatusPending {
		t.Fatal("job3 should be pending")
	}

	// Close store
	store1.Close()

	// Simulate container restart
	store2, err := store.NewSQLiteStore(dbFile)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store2.Close()

	// Reset running jobs
	store2.ResetRunningJobs()

	queue2, err := jobs.NewQueueWithStore(store2)
	if err != nil {
		t.Fatalf("failed to load queue: %v", err)
	}

	// Verify ALL jobs appear in GetAll()
	all := queue2.GetAll()
	if len(all) != 4 {
		t.Errorf("expected 4 jobs in GetAll(), got %d", len(all))
	}

	// Verify jobs 1&2 are now pending (reset from running)
	if queue2.Get(job1.ID).Status != jobs.StatusPending {
		t.Errorf("job1 should be reset to pending, got %s", queue2.Get(job1.ID).Status)
	}
	if queue2.Get(job2.ID).Status != jobs.StatusPending {
		t.Errorf("job2 should be reset to pending, got %s", queue2.Get(job2.ID).Status)
	}

	// Critical: GetNext() should return job1 (first in order, now pending)
	// NOT job3 - that was the bug in issue #35
	next := queue2.GetNext()
	if next == nil {
		t.Fatal("GetNext() returned nil, expected job1")
	}
	if next.ID != job1.ID {
		t.Errorf("GetNext() should return job1 (first reset job), got job with path %s", next.InputPath)
	}

	// Close and reopen to verify reset was persisted
	store2.Close()

	store3, _ := store.NewSQLiteStore(dbFile)
	defer store3.Close()
	queue3, _ := jobs.NewQueueWithStore(store3)

	if queue3.Get(job1.ID).Status != jobs.StatusPending {
		t.Error("reset status was not persisted to disk")
	}

	t.Log("Running jobs correctly reset, visible, and returned by GetNext()")
}

func TestQueueGetNext(t *testing.T) {
	queue := jobs.NewQueue()

	probe := &ffmpeg.ProbeResult{
		Path:     "/media/video.mkv",
		Size:     1000000,
		Duration: 10 * time.Second,
	}

	// No jobs - should return nil
	if queue.GetNext() != nil {
		t.Error("expected nil for empty queue")
	}

	// Add jobs
	job1, _ := queue.Add("/media/video1.mkv", "compress", probe)
	job2, _ := queue.Add("/media/video2.mkv", "compress", probe)
	job3, _ := queue.Add("/media/video3.mkv", "compress", probe)

	// Should return first pending job
	next := queue.GetNext()
	if next == nil || next.ID != job1.ID {
		t.Errorf("expected job1, got %+v", next)
	}

	// Start job1 - next should return job2
	queue.StartJob(job1.ID, "/tmp/temp.mkv")
	next = queue.GetNext()
	if next == nil || next.ID != job2.ID {
		t.Errorf("expected job2, got %+v", next)
	}

	// Complete job1, start job2
	queue.CompleteJob(job1.ID, "/media/video1.mkv", 500000)
	queue.StartJob(job2.ID, "/tmp/temp.mkv")

	// Next should be job3
	next = queue.GetNext()
	if next == nil || next.ID != job3.ID {
		t.Errorf("expected job3, got %+v", next)
	}
}

func TestQueueCancel(t *testing.T) {
	queue := jobs.NewQueue()

	probe := &ffmpeg.ProbeResult{
		Path:     "/media/video.mkv",
		Size:     1000000,
		Duration: 10 * time.Second,
	}

	job, _ := queue.Add(probe.Path, "compress", probe)

	// Cancel pending job
	err := queue.CancelJob(job.ID)
	if err != nil {
		t.Fatalf("failed to cancel job: %v", err)
	}

	got := queue.Get(job.ID)
	if got.Status != jobs.StatusCancelled {
		t.Errorf("expected status cancelled, got %s", got.Status)
	}

	// Try to cancel again - should fail
	err = queue.CancelJob(job.ID)
	if err == nil {
		t.Error("expected error when cancelling already cancelled job")
	}
}

func TestQueueStats(t *testing.T) {
	queue := jobs.NewQueue()

	probe := &ffmpeg.ProbeResult{
		Path:     "/media/video.mkv",
		Size:     1000000,
		Duration: 10 * time.Second,
	}

	// Add some jobs in various states
	job1, _ := queue.Add("/media/v1.mkv", "compress", probe)
	queue.Add("/media/v2.mkv", "compress", probe)
	queue.Add("/media/v3.mkv", "compress", probe)

	queue.StartJob(job1.ID, "/tmp/temp.mkv")
	queue.CompleteJob(job1.ID, "/media/v1.mkv", 500000)

	stats := queue.Stats()

	if stats.Total != 3 {
		t.Errorf("expected total 3, got %d", stats.Total)
	}

	if stats.Pending != 2 {
		t.Errorf("expected pending 2, got %d", stats.Pending)
	}

	if stats.Complete != 1 {
		t.Errorf("expected complete 1, got %d", stats.Complete)
	}

	if stats.TotalSaved != 500000 {
		t.Errorf("expected total saved 500000, got %d", stats.TotalSaved)
	}

	t.Logf("Queue stats: %+v", stats)
}

func TestQueueSubscription(t *testing.T) {
	queue := jobs.NewQueue()

	// Subscribe
	ch := queue.Subscribe()

	probe := &ffmpeg.ProbeResult{
		Path:     "/media/video.mkv",
		Size:     1000000,
		Duration: 10 * time.Second,
	}

	// Add job - should receive event
	job, _ := queue.Add(probe.Path, "compress", probe)

	select {
	case event := <-ch:
		if event.Type != "added" {
			t.Errorf("expected event type 'added', got %s", event.Type)
		}
		if event.Job.ID != job.ID {
			t.Error("event job ID mismatch")
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for event")
	}

	// Start job
	queue.StartJob(job.ID, "/tmp/temp.mkv")

	select {
	case event := <-ch:
		if event.Type != "started" {
			t.Errorf("expected event type 'started', got %s", event.Type)
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for event")
	}

	// Unsubscribe
	queue.Unsubscribe(ch)

	t.Log("Subscription working correctly")
}

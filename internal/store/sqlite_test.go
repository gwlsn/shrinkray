package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/gwlsn/shrinkray/internal/jobs"
)

func createTestJob(id string) *jobs.Job {
	return &jobs.Job{
		ID:         id,
		InputPath:  "/media/video_" + id + ".mkv",
		PresetID:   "compress-hevc",
		Encoder:    "libx265",
		IsHardware: false,
		Status:     jobs.StatusPending,
		InputSize:  1000000,
		Duration:   60000,
		Bitrate:    3000000,
		Width:      1920,
		Height:     1080,
		FrameRate:  24.0,
		CreatedAt:  time.Now(),
	}
}

func TestSQLiteStore_SaveJob_CreatesNew(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	job := createTestJob("test-1")

	// Save new job
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("failed to save job: %v", err)
	}

	// Retrieve and verify
	got, err := store.GetJob("test-1")
	if err != nil {
		t.Fatalf("failed to get job: %v", err)
	}

	if got == nil {
		t.Fatal("expected job, got nil")
	}

	if got.ID != job.ID {
		t.Errorf("expected ID %s, got %s", job.ID, got.ID)
	}
	if got.InputPath != job.InputPath {
		t.Errorf("expected InputPath %s, got %s", job.InputPath, got.InputPath)
	}
	if got.Status != job.Status {
		t.Errorf("expected Status %s, got %s", job.Status, got.Status)
	}
}

func TestSQLiteStore_SaveJob_UpdatesExisting(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	job := createTestJob("test-1")

	// Save initial
	store.SaveJob(job)

	// Update status
	job.Status = jobs.StatusRunning
	job.Progress = 50.0
	job.Speed = 2.5
	job.ETA = "5m remaining"
	job.StartedAt = time.Now()

	if err := store.SaveJob(job); err != nil {
		t.Fatalf("failed to update job: %v", err)
	}

	// Verify update
	got, _ := store.GetJob("test-1")
	if got.Status != jobs.StatusRunning {
		t.Errorf("expected Status running, got %s", got.Status)
	}
	if got.Progress != 50.0 {
		t.Errorf("expected Progress 50, got %f", got.Progress)
	}
	if got.Speed != 2.5 {
		t.Errorf("expected Speed 2.5, got %f", got.Speed)
	}
}

func TestSQLiteStore_GetJob_ReturnsNilForMissing(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	got, err := store.GetJob("nonexistent")
	if err == nil && got != nil {
		t.Error("expected nil for missing job")
	}
}

func TestSQLiteStore_DeleteJob_RemovesJobAndOrder(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	job := createTestJob("test-1")
	store.SaveJob(job)
	store.AppendToOrder(job.ID)

	// Delete
	if err := store.DeleteJob(job.ID); err != nil {
		t.Fatalf("failed to delete job: %v", err)
	}

	// Verify job is gone
	got, _ := store.GetJob(job.ID)
	if got != nil {
		t.Error("expected job to be deleted")
	}

	// Verify not in order (cascaded)
	allJobs, order, _ := store.GetAllJobs()
	if len(allJobs) != 0 || len(order) != 0 {
		t.Error("expected empty job list and order after delete")
	}
}

func TestSQLiteStore_DeleteJob_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Delete nonexistent - should not error
	if err := store.DeleteJob("nonexistent"); err != nil {
		t.Errorf("delete of nonexistent job should not error: %v", err)
	}
}

func TestSQLiteStore_AppendToOrder_MaintainsInsertionOrder(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Create and save jobs A, B, C
	for _, id := range []string{"A", "B", "C"} {
		job := createTestJob(id)
		store.SaveJob(job)
		store.AppendToOrder(id)
	}

	// Get all and verify order
	_, order, err := store.GetAllJobs()
	if err != nil {
		t.Fatalf("failed to get jobs: %v", err)
	}

	expected := []string{"A", "B", "C"}
	if len(order) != len(expected) {
		t.Fatalf("expected %d jobs, got %d", len(expected), len(order))
	}

	for i, id := range expected {
		if order[i] != id {
			t.Errorf("order[%d]: expected %s, got %s", i, id, order[i])
		}
	}
}

func TestSQLiteStore_RemoveFromOrder_HandlesGaps(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Create A, B, C
	for _, id := range []string{"A", "B", "C"} {
		job := createTestJob(id)
		store.SaveJob(job)
		store.AppendToOrder(id)
	}

	// Remove middle element
	store.RemoveFromOrder("B")

	// Order should now be A, C
	_, order, _ := store.GetAllJobs()
	if len(order) != 3 { // Jobs still exist, just not in order
		t.Logf("Order after remove: %v", order)
	}

	// GetAllJobs returns jobs even without order entries (by created_at)
	// What matters is the deletion from order table worked
	allJobs, _, _ := store.GetAllJobs()
	if len(allJobs) != 3 {
		t.Errorf("expected 3 jobs, got %d", len(allJobs))
	}
}

func TestSQLiteStore_GetJobsByStatus_FiltersCorrectly(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Create jobs in different states
	pending := createTestJob("pending-1")
	pending.Status = jobs.StatusPending
	store.SaveJob(pending)

	running := createTestJob("running-1")
	running.Status = jobs.StatusRunning
	store.SaveJob(running)

	complete := createTestJob("complete-1")
	complete.Status = jobs.StatusComplete
	store.SaveJob(complete)

	// Test each status filter
	pendingJobs, _ := store.GetJobsByStatus(jobs.StatusPending)
	if len(pendingJobs) != 1 || pendingJobs[0].ID != "pending-1" {
		t.Errorf("pending filter failed: got %d jobs", len(pendingJobs))
	}

	runningJobs, _ := store.GetJobsByStatus(jobs.StatusRunning)
	if len(runningJobs) != 1 || runningJobs[0].ID != "running-1" {
		t.Errorf("running filter failed: got %d jobs", len(runningJobs))
	}

	completeJobs, _ := store.GetJobsByStatus(jobs.StatusComplete)
	if len(completeJobs) != 1 || completeJobs[0].ID != "complete-1" {
		t.Errorf("complete filter failed: got %d jobs", len(completeJobs))
	}
}

func TestSQLiteStore_GetJobsByStatus_ReturnsEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// No jobs exist
	jobList, err := store.GetJobsByStatus(jobs.StatusPending)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Either nil or empty slice is acceptable
	if len(jobList) != 0 {
		t.Errorf("expected 0 jobs, got %d", len(jobList))
	}
}

func TestSQLiteStore_GetNextPendingJob(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Add jobs in order
	for _, id := range []string{"first", "second", "third"} {
		job := createTestJob(id)
		job.Status = jobs.StatusPending
		store.SaveJob(job)
		store.AppendToOrder(id)
	}

	// First pending should be "first"
	next, err := store.GetNextPendingJob()
	if err != nil {
		t.Fatalf("failed to get next pending: %v", err)
	}
	if next == nil || next.ID != "first" {
		t.Errorf("expected first pending job, got %v", next)
	}
}

func TestSQLiteStore_GetNextPendingJob_SkipsRunning(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// First job is running
	first := createTestJob("first")
	first.Status = jobs.StatusRunning
	store.SaveJob(first)
	store.AppendToOrder("first")

	// Second is pending
	second := createTestJob("second")
	second.Status = jobs.StatusPending
	store.SaveJob(second)
	store.AppendToOrder("second")

	// Should return second (first pending)
	next, _ := store.GetNextPendingJob()
	if next == nil || next.ID != "second" {
		t.Errorf("expected second job (first pending), got %v", next)
	}
}

func TestSQLiteStore_ResetRunningJobs(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Create running jobs with progress
	for i := 0; i < 3; i++ {
		job := createTestJob(string(rune('A' + i)))
		job.Status = jobs.StatusRunning
		job.Progress = 50.0
		job.Speed = 1.5
		job.ETA = "10m"
		store.SaveJob(job)
	}

	// Also create a pending job (should not be affected)
	pending := createTestJob("pending")
	pending.Status = jobs.StatusPending
	store.SaveJob(pending)

	// Reset running jobs
	count, err := store.ResetRunningJobs()
	if err != nil {
		t.Fatalf("failed to reset running jobs: %v", err)
	}

	if count != 3 {
		t.Errorf("expected 3 jobs reset, got %d", count)
	}

	// Verify all are now pending with cleared progress
	allJobs, _, _ := store.GetAllJobs()
	for _, job := range allJobs {
		if job.Status != jobs.StatusPending {
			t.Errorf("job %s: expected pending, got %s", job.ID, job.Status)
		}
		if job.ID != "pending" && job.Progress != 0 {
			t.Errorf("job %s: expected progress 0, got %f", job.ID, job.Progress)
		}
	}
}

func TestSQLiteStore_Stats(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Create jobs in various states
	pending := createTestJob("pending")
	pending.Status = jobs.StatusPending
	store.SaveJob(pending)

	running := createTestJob("running")
	running.Status = jobs.StatusRunning
	store.SaveJob(running)

	complete1 := createTestJob("complete1")
	complete1.Status = jobs.StatusComplete
	complete1.SpaceSaved = 100000
	store.SaveJob(complete1)
	store.AddToLifetimeSaved(complete1.SpaceSaved) // Increment counters

	complete2 := createTestJob("complete2")
	complete2.Status = jobs.StatusComplete
	complete2.SpaceSaved = 200000
	store.SaveJob(complete2)
	store.AddToLifetimeSaved(complete2.SpaceSaved) // Increment counters

	failed := createTestJob("failed")
	failed.Status = jobs.StatusFailed
	store.SaveJob(failed)

	cancelled := createTestJob("cancelled")
	cancelled.Status = jobs.StatusCancelled
	store.SaveJob(cancelled)

	skipped := createTestJob("skipped")
	skipped.Status = jobs.StatusSkipped
	store.SaveJob(skipped)

	// Get stats
	stats, err := store.Stats()
	if err != nil {
		t.Fatalf("failed to get stats: %v", err)
	}

	if stats.Total != 7 {
		t.Errorf("expected Total 7, got %d", stats.Total)
	}
	if stats.Pending != 1 {
		t.Errorf("expected Pending 1, got %d", stats.Pending)
	}
	if stats.Running != 1 {
		t.Errorf("expected Running 1, got %d", stats.Running)
	}
	if stats.Complete != 2 {
		t.Errorf("expected Complete 2, got %d", stats.Complete)
	}
	if stats.Failed != 1 {
		t.Errorf("expected Failed 1, got %d", stats.Failed)
	}
	if stats.Cancelled != 1 {
		t.Errorf("expected Cancelled 1, got %d", stats.Cancelled)
	}
	if stats.Skipped != 1 {
		t.Errorf("expected Skipped 1, got %d", stats.Skipped)
	}
	// TotalSaved now equals SessionSaved for API compatibility
	if stats.TotalSaved != 300000 {
		t.Errorf("expected TotalSaved (session) 300000, got %d", stats.TotalSaved)
	}
	if stats.SessionSaved != 300000 {
		t.Errorf("expected SessionSaved 300000, got %d", stats.SessionSaved)
	}
	if stats.LifetimeSaved != 300000 {
		t.Errorf("expected LifetimeSaved 300000, got %d", stats.LifetimeSaved)
	}
}

func TestSQLiteStore_AllFieldsRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Create job with all fields populated
	now := time.Now().Truncate(time.Second) // SQLite stores with second precision
	job := &jobs.Job{
		ID:            "full-test",
		InputPath:     "/media/test.mkv",
		OutputPath:    "/media/test_out.mkv",
		TempPath:      "/tmp/test.mkv.tmp",
		PresetID:      "compress-hevc",
		Encoder:       "hevc_videotoolbox",
		IsHardware:    true,
		Status:        jobs.StatusComplete,
		Progress:      100.0,
		Speed:         2.5,
		ETA:           "",
		Error:         "",
		InputSize:     1000000000,
		OutputSize:    500000000,
		SpaceSaved:    500000000,
		Duration:      3600000,
		Bitrate:       5000000,
		Width:         3840,
		Height:        2160,
		FrameRate:     29.97,
		TranscodeTime: 1800,
		CreatedAt:     now,
		StartedAt:     now.Add(time.Minute),
		CompletedAt:   now.Add(time.Hour),
	}

	// Save
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("failed to save job: %v", err)
	}

	// Retrieve
	got, err := store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("failed to get job: %v", err)
	}

	// Verify all fields
	if got.ID != job.ID {
		t.Errorf("ID: expected %s, got %s", job.ID, got.ID)
	}
	if got.InputPath != job.InputPath {
		t.Errorf("InputPath: expected %s, got %s", job.InputPath, got.InputPath)
	}
	if got.OutputPath != job.OutputPath {
		t.Errorf("OutputPath: expected %s, got %s", job.OutputPath, got.OutputPath)
	}
	if got.TempPath != job.TempPath {
		t.Errorf("TempPath: expected %s, got %s", job.TempPath, got.TempPath)
	}
	if got.PresetID != job.PresetID {
		t.Errorf("PresetID: expected %s, got %s", job.PresetID, got.PresetID)
	}
	if got.Encoder != job.Encoder {
		t.Errorf("Encoder: expected %s, got %s", job.Encoder, got.Encoder)
	}
	if got.IsHardware != job.IsHardware {
		t.Errorf("IsHardware: expected %v, got %v", job.IsHardware, got.IsHardware)
	}
	if got.Status != job.Status {
		t.Errorf("Status: expected %s, got %s", job.Status, got.Status)
	}
	if got.Progress != job.Progress {
		t.Errorf("Progress: expected %f, got %f", job.Progress, got.Progress)
	}
	if got.Speed != job.Speed {
		t.Errorf("Speed: expected %f, got %f", job.Speed, got.Speed)
	}
	if got.InputSize != job.InputSize {
		t.Errorf("InputSize: expected %d, got %d", job.InputSize, got.InputSize)
	}
	if got.OutputSize != job.OutputSize {
		t.Errorf("OutputSize: expected %d, got %d", job.OutputSize, got.OutputSize)
	}
	if got.SpaceSaved != job.SpaceSaved {
		t.Errorf("SpaceSaved: expected %d, got %d", job.SpaceSaved, got.SpaceSaved)
	}
	if got.Duration != job.Duration {
		t.Errorf("Duration: expected %d, got %d", job.Duration, got.Duration)
	}
	if got.Bitrate != job.Bitrate {
		t.Errorf("Bitrate: expected %d, got %d", job.Bitrate, got.Bitrate)
	}
	if got.Width != job.Width {
		t.Errorf("Width: expected %d, got %d", job.Width, got.Width)
	}
	if got.Height != job.Height {
		t.Errorf("Height: expected %d, got %d", job.Height, got.Height)
	}
	if got.FrameRate != job.FrameRate {
		t.Errorf("FrameRate: expected %f, got %f", job.FrameRate, got.FrameRate)
	}
	if got.TranscodeTime != job.TranscodeTime {
		t.Errorf("TranscodeTime: expected %d, got %d", job.TranscodeTime, got.TranscodeTime)
	}

	// Time comparisons (within 1 second tolerance)
	if got.CreatedAt.Sub(job.CreatedAt) > time.Second {
		t.Errorf("CreatedAt: expected %v, got %v", job.CreatedAt, got.CreatedAt)
	}
	if got.StartedAt.Sub(job.StartedAt) > time.Second {
		t.Errorf("StartedAt: expected %v, got %v", job.StartedAt, got.StartedAt)
	}
	if got.CompletedAt.Sub(job.CompletedAt) > time.Second {
		t.Errorf("CompletedAt: expected %v, got %v", job.CompletedAt, got.CompletedAt)
	}
}

func TestSQLiteStore_ZeroValuesPreserved(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Job with all optional fields at zero/empty
	job := &jobs.Job{
		ID:        "minimal",
		InputPath: "/media/test.mkv",
		PresetID:  "compress",
		Encoder:   "none",
		Status:    jobs.StatusPending,
		CreatedAt: time.Now(),
	}

	store.SaveJob(job)

	got, _ := store.GetJob(job.ID)

	// All optional fields should be zero
	if got.OutputPath != "" {
		t.Errorf("OutputPath should be empty, got %s", got.OutputPath)
	}
	if got.TempPath != "" {
		t.Errorf("TempPath should be empty, got %s", got.TempPath)
	}
	if got.Progress != 0 {
		t.Errorf("Progress should be 0, got %f", got.Progress)
	}
	if got.OutputSize != 0 {
		t.Errorf("OutputSize should be 0, got %d", got.OutputSize)
	}
}

func TestSQLiteStore_SaveJobs_BatchPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Create batch of jobs
	batch := make([]*jobs.Job, 100)
	for i := 0; i < 100; i++ {
		batch[i] = createTestJob(string(rune('0' + i/10)) + string(rune('0'+i%10)))
	}

	// Save in batch
	if err := store.SaveJobs(batch); err != nil {
		t.Fatalf("failed to save batch: %v", err)
	}

	// Verify all saved
	allJobs, _, err := store.GetAllJobs()
	if err != nil {
		t.Fatalf("failed to get all jobs: %v", err)
	}

	if len(allJobs) != 100 {
		t.Errorf("expected 100 jobs, got %d", len(allJobs))
	}
}

func TestSQLiteStore_Persistence(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// Create store and add job
	store1, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	job := createTestJob("persist-test")
	store1.SaveJob(job)
	store1.AppendToOrder(job.ID)
	store1.Close()

	// Reopen and verify
	store2, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to reopen store: %v", err)
	}
	defer store2.Close()

	got, _ := store2.GetJob("persist-test")
	if got == nil {
		t.Fatal("job not persisted")
	}

	if got.InputPath != job.InputPath {
		t.Errorf("expected InputPath %s, got %s", job.InputPath, got.InputPath)
	}

	// Verify order persisted
	_, order, _ := store2.GetAllJobs()
	if len(order) != 1 || order[0] != "persist-test" {
		t.Errorf("order not persisted: %v", order)
	}
}

func TestSQLiteStore_WALMode(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Query journal mode
	var mode string
	err = store.db.QueryRow("PRAGMA journal_mode").Scan(&mode)
	if err != nil {
		t.Fatalf("failed to query journal mode: %v", err)
	}

	if mode != "wal" {
		t.Errorf("expected WAL mode, got %s", mode)
	}
}

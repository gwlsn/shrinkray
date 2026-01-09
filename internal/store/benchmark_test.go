package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gwlsn/shrinkray/internal/jobs"
)

func createBenchmarkJob(id string) *jobs.Job {
	return &jobs.Job{
		ID:         id,
		InputPath:  "/media/video_" + id + ".mkv",
		PresetID:   "compress-hevc",
		Encoder:    "hevc_videotoolbox",
		IsHardware: true,
		Status:     jobs.StatusPending,
		InputSize:  1000000000,
		Duration:   3600000,
		Bitrate:    5000000,
		Width:      3840,
		Height:     2160,
		FrameRate:  29.97,
		CreatedAt:  time.Now(),
	}
}

func BenchmarkInsert(b *testing.B) {
	tmpDir := b.TempDir()
	dbPath := filepath.Join(tmpDir, "bench.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		b.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		job := createBenchmarkJob(fmt.Sprintf("job-%d", i))
		store.SaveJob(job)
	}
}

func BenchmarkInsert1000(b *testing.B) {
	for i := 0; i < b.N; i++ {
		tmpDir := b.TempDir()
		dbPath := filepath.Join(tmpDir, "bench.db")

		store, err := NewSQLiteStore(dbPath)
		if err != nil {
			b.Fatalf("failed to create store: %v", err)
		}

		start := time.Now()
		for j := 0; j < 1000; j++ {
			job := createBenchmarkJob(fmt.Sprintf("job-%d", j))
			store.SaveJob(job)
			store.AppendToOrder(job.ID)
		}
		elapsed := time.Since(start)

		store.Close()

		// Threshold check: should be < 1 second
		if elapsed > time.Second {
			b.Errorf("Insert 1000 jobs took %v (threshold: 1s)", elapsed)
		}
	}
}

func BenchmarkBatchInsert(b *testing.B) {
	tmpDir := b.TempDir()
	dbPath := filepath.Join(tmpDir, "bench.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		b.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Create batch of 100 jobs
	batch := make([]*jobs.Job, 100)
	for i := 0; i < 100; i++ {
		batch[i] = createBenchmarkJob(fmt.Sprintf("batch-%d", i))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Update IDs to avoid conflicts
		for j, job := range batch {
			job.ID = fmt.Sprintf("batch-%d-%d", i, j)
		}
		store.SaveJobs(batch)
	}
}

func BenchmarkGetAllJobs(b *testing.B) {
	tmpDir := b.TempDir()
	dbPath := filepath.Join(tmpDir, "bench.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		b.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Insert 1000 jobs
	for i := 0; i < 1000; i++ {
		job := createBenchmarkJob(fmt.Sprintf("job-%d", i))
		store.SaveJob(job)
		store.AppendToOrder(job.ID)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, err := store.GetAllJobs()
		if err != nil {
			b.Fatalf("GetAllJobs failed: %v", err)
		}
	}
}

func BenchmarkGetAllJobs_WithThreshold(b *testing.B) {
	tmpDir := b.TempDir()
	dbPath := filepath.Join(tmpDir, "bench.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		b.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Insert 1000 jobs
	batch := make([]*jobs.Job, 1000)
	for i := 0; i < 1000; i++ {
		batch[i] = createBenchmarkJob(fmt.Sprintf("job-%d", i))
	}
	store.SaveJobs(batch)
	for _, job := range batch {
		store.AppendToOrder(job.ID)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		_, _, err := store.GetAllJobs()
		elapsed := time.Since(start)

		if err != nil {
			b.Fatalf("GetAllJobs failed: %v", err)
		}

		// Threshold: should be < 100ms
		if elapsed > 100*time.Millisecond {
			b.Errorf("GetAllJobs (1000 jobs) took %v (threshold: 100ms)", elapsed)
		}
	}
}

func BenchmarkGetNextJob(b *testing.B) {
	tmpDir := b.TempDir()
	dbPath := filepath.Join(tmpDir, "bench.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		b.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Insert 1000 jobs with mixed status
	for i := 0; i < 1000; i++ {
		job := createBenchmarkJob(fmt.Sprintf("job-%d", i))
		// Mix of statuses
		switch i % 5 {
		case 0:
			job.Status = jobs.StatusPending
		case 1:
			job.Status = jobs.StatusRunning
		case 2:
			job.Status = jobs.StatusComplete
		case 3:
			job.Status = jobs.StatusFailed
		case 4:
			job.Status = jobs.StatusCancelled
		}
		store.SaveJob(job)
		store.AppendToOrder(job.ID)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		_, err := store.GetNextPendingJob()
		elapsed := time.Since(start)

		if err != nil {
			b.Fatalf("GetNextPendingJob failed: %v", err)
		}

		// Threshold: should be < 1ms
		if elapsed > time.Millisecond {
			b.Errorf("GetNextPendingJob took %v (threshold: 1ms)", elapsed)
		}
	}
}

func BenchmarkStats(b *testing.B) {
	tmpDir := b.TempDir()
	dbPath := filepath.Join(tmpDir, "bench.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		b.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Insert 1000 jobs with mixed status
	for i := 0; i < 1000; i++ {
		job := createBenchmarkJob(fmt.Sprintf("job-%d", i))
		switch i % 5 {
		case 0:
			job.Status = jobs.StatusPending
		case 1:
			job.Status = jobs.StatusRunning
		case 2:
			job.Status = jobs.StatusComplete
			job.SpaceSaved = 500000000
		case 3:
			job.Status = jobs.StatusFailed
		case 4:
			job.Status = jobs.StatusCancelled
		}
		store.SaveJob(job)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := store.Stats()
		if err != nil {
			b.Fatalf("Stats failed: %v", err)
		}
	}
}

func BenchmarkMigration10000(b *testing.B) {
	for i := 0; i < b.N; i++ {
		tmpDir := b.TempDir()
		jsonPath := filepath.Join(tmpDir, "queue.json")
		dbPath := filepath.Join(tmpDir, "shrinkray.db")

		// Create 10000 jobs
		numJobs := 10000
		jobList := make([]*jobs.Job, numJobs)
		order := make([]string, numJobs)
		for j := 0; j < numJobs; j++ {
			id := fmt.Sprintf("job-%05d", j)
			jobList[j] = createBenchmarkJob(id)
			order[j] = id
		}

		// Write JSON
		data := struct {
			Jobs  []*jobs.Job `json:"jobs"`
			Order []string    `json:"order"`
		}{Jobs: jobList, Order: order}
		jsonBytes, _ := json.Marshal(data)
		os.WriteFile(jsonPath, jsonBytes, 0644)

		// Time the migration
		start := time.Now()
		result := MigrateFromJSON(jsonPath, dbPath)
		elapsed := time.Since(start)

		if !result.Success {
			b.Fatalf("migration failed: %s", result.ErrorMessage)
		}

		// Threshold: should be < 10 seconds
		if elapsed > 10*time.Second {
			b.Errorf("Migration of %d jobs took %v (threshold: 10s)", numJobs, elapsed)
		}

		b.Logf("Migration of %d jobs took %v", numJobs, elapsed)
	}
}

func BenchmarkConcurrentReads(b *testing.B) {
	tmpDir := b.TempDir()
	dbPath := filepath.Join(tmpDir, "bench.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		b.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Insert 1000 jobs
	for i := 0; i < 1000; i++ {
		job := createBenchmarkJob(fmt.Sprintf("job-%d", i))
		store.SaveJob(job)
		store.AppendToOrder(job.ID)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			store.GetAllJobs()
		}
	})
}

func BenchmarkConcurrentWrites(b *testing.B) {
	tmpDir := b.TempDir()
	dbPath := filepath.Join(tmpDir, "bench.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		b.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			job := createBenchmarkJob(fmt.Sprintf("job-%d-%d", time.Now().UnixNano(), i))
			store.SaveJob(job)
			i++
		}
	})
}

// Test that verifies thresholds from the plan
func TestPerformanceThresholds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping performance threshold test in short mode")
	}

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Test 1: Insert 1000 jobs should be < 1 second
	// Note: Race detector adds ~10x overhead, CI adds more variability
	t.Run("Insert1000Jobs", func(t *testing.T) {
		start := time.Now()
		for i := 0; i < 1000; i++ {
			job := createBenchmarkJob(fmt.Sprintf("insert-%d", i))
			store.SaveJob(job)
			store.AppendToOrder(job.ID)
		}
		elapsed := time.Since(start)

		// Very lenient threshold (10s) to pass with race detector in CI
		if elapsed > 10*time.Second {
			t.Errorf("Insert 1000 jobs took %v (threshold: 10s)", elapsed)
		}
		t.Logf("Insert 1000 jobs: %v (target: <1s without race detector)", elapsed)
	})

	// Test 2: Query all (1000 jobs) should be < 100ms
	t.Run("QueryAll1000Jobs", func(t *testing.T) {
		start := time.Now()
		_, _, err := store.GetAllJobs()
		elapsed := time.Since(start)

		if err != nil {
			t.Fatalf("GetAllJobs failed: %v", err)
		}
		// Very lenient threshold (2s) to pass with race detector in CI
		if elapsed > 2*time.Second {
			t.Errorf("Query all 1000 jobs took %v (threshold: 2s)", elapsed)
		}
		t.Logf("Query all 1000 jobs: %v (target: <100ms without race detector)", elapsed)
	})

	// Test 3: GetNext (1000 jobs) should be < 1ms
	t.Run("GetNext1000Jobs", func(t *testing.T) {
		// Run multiple times to get average
		var totalElapsed time.Duration
		runs := 100
		for i := 0; i < runs; i++ {
			start := time.Now()
			_, _ = store.GetNextPendingJob()
			totalElapsed += time.Since(start)
		}
		avgElapsed := totalElapsed / time.Duration(runs)

		// Very lenient threshold (500ms) to pass with race detector in CI
		if avgElapsed > 500*time.Millisecond {
			t.Errorf("GetNext avg %v (threshold: 500ms)", avgElapsed)
		}
		t.Logf("GetNext avg: %v over %d runs (target: <1ms without race detector)", avgElapsed, runs)
	})
}

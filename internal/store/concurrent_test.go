package store

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gwlsn/shrinkray/internal/jobs"
)

func TestConcurrency_MultipleWriters(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// 20 goroutines × 50 ops each = 1000 concurrent writes
	numWorkers := 20
	opsPerWorker := 50

	var wg sync.WaitGroup
	errors := make(chan error, numWorkers*opsPerWorker)

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < opsPerWorker; i++ {
				job := &jobs.Job{
					ID:        fmt.Sprintf("w%d-j%d", workerID, i),
					InputPath: fmt.Sprintf("/media/video_%d_%d.mkv", workerID, i),
					PresetID:  "compress",
					Encoder:   "libx265",
					Status:    jobs.StatusPending,
					InputSize: int64(1000000 + i),
					CreatedAt: time.Now(),
				}
				if err := store.SaveJob(job); err != nil {
					errors <- fmt.Errorf("worker %d job %d: %w", workerID, i, err)
					return
				}
			}
		}(w)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Error(err)
	}

	// Verify all jobs saved
	allJobs, _, err := store.GetAllJobs()
	if err != nil {
		t.Fatalf("failed to get all jobs: %v", err)
	}

	expected := numWorkers * opsPerWorker
	if len(allJobs) != expected {
		t.Errorf("expected %d jobs, got %d", expected, len(allJobs))
	}

	t.Logf("Successfully wrote %d jobs from %d concurrent workers", len(allJobs), numWorkers)
}

func TestConcurrency_ReadWhileWriting(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Start writer
	writerDone := make(chan bool)
	go func() {
		for i := 0; i < 100; i++ {
			job := &jobs.Job{
				ID:        fmt.Sprintf("job-%d", i),
				InputPath: fmt.Sprintf("/media/video_%d.mkv", i),
				PresetID:  "compress",
				Encoder:   "libx265",
				Status:    jobs.StatusPending,
				CreatedAt: time.Now(),
			}
			store.SaveJob(job)
			store.AppendToOrder(job.ID)
			time.Sleep(time.Millisecond) // Small delay
		}
		writerDone <- true
	}()

	// Start 10 readers
	var readWg sync.WaitGroup
	readErrors := make(chan error, 100)

	for r := 0; r < 10; r++ {
		readWg.Add(1)
		go func(readerID int) {
			defer readWg.Done()
			for i := 0; i < 50; i++ {
				_, _, err := store.GetAllJobs()
				if err != nil {
					readErrors <- fmt.Errorf("reader %d iteration %d: %w", readerID, i, err)
					return
				}
				time.Sleep(2 * time.Millisecond)
			}
		}(r)
	}

	// Wait for writer
	<-writerDone

	// Wait for readers
	readWg.Wait()
	close(readErrors)

	// Check for errors
	for err := range readErrors {
		t.Error(err)
	}

	t.Log("Read-while-write test passed")
}

func TestConcurrency_StatusUpdateRace(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Create pending job
	job := &jobs.Job{
		ID:        "contested",
		InputPath: "/media/contested.mkv",
		PresetID:  "compress",
		Encoder:   "libx265",
		Status:    jobs.StatusPending,
		CreatedAt: time.Now(),
	}
	store.SaveJob(job)
	store.AppendToOrder(job.ID)

	// 10 workers try to claim same pending job
	numWorkers := 10
	var wg sync.WaitGroup
	claims := make(chan int, numWorkers)

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			// Try to get next pending and claim it
			next, _ := store.GetNextPendingJob()
			if next != nil && next.Status == jobs.StatusPending {
				// Try to update to running
				next.Status = jobs.StatusRunning
				next.StartedAt = time.Now()
				if err := store.SaveJob(next); err == nil {
					claims <- workerID
				}
			}
		}(w)
	}

	wg.Wait()
	close(claims)

	// Count claims
	var claimers []int
	for workerID := range claims {
		claimers = append(claimers, workerID)
	}

	// All workers may have "claimed" it due to no transaction isolation
	// but the final state should be consistent
	got, _ := store.GetJob("contested")
	if got.Status != jobs.StatusRunning {
		t.Errorf("expected status running, got %s", got.Status)
	}

	t.Logf("Job claimed by %d workers (expected some overlap without transactions)", len(claimers))
}

func TestConcurrency_OrderAppendRace(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// 100 concurrent AppendToOrder calls
	numAppends := 100
	var wg sync.WaitGroup

	for i := 0; i < numAppends; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			jobID := fmt.Sprintf("job-%03d", id)
			job := &jobs.Job{
				ID:        jobID,
				InputPath: fmt.Sprintf("/media/video_%03d.mkv", id),
				PresetID:  "compress",
				Encoder:   "libx265",
				Status:    jobs.StatusPending,
				CreatedAt: time.Now(),
			}
			store.SaveJob(job)
			store.AppendToOrder(jobID)
		}(i)
	}

	wg.Wait()

	// Verify no duplicates in order
	allJobs, order, _ := store.GetAllJobs()

	if len(allJobs) != numAppends {
		t.Errorf("expected %d jobs, got %d", numAppends, len(allJobs))
	}

	// Check for duplicates in order
	seen := make(map[string]bool)
	for _, id := range order {
		if seen[id] {
			t.Errorf("duplicate in order: %s", id)
		}
		seen[id] = true
	}

	t.Logf("Order append race test passed with %d unique entries", len(order))
}

func TestConcurrency_MixedOperations(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Pre-populate some jobs
	for i := 0; i < 50; i++ {
		job := &jobs.Job{
			ID:        fmt.Sprintf("init-%d", i),
			InputPath: fmt.Sprintf("/media/init_%d.mkv", i),
			PresetID:  "compress",
			Encoder:   "libx265",
			Status:    jobs.StatusPending,
			CreatedAt: time.Now(),
		}
		store.SaveJob(job)
		store.AppendToOrder(job.ID)
	}

	// Run mixed operations concurrently
	var wg sync.WaitGroup

	// Writers adding new jobs
	for w := 0; w < 5; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				job := &jobs.Job{
					ID:        fmt.Sprintf("new-%d-%d", workerID, i),
					InputPath: fmt.Sprintf("/media/new_%d_%d.mkv", workerID, i),
					PresetID:  "compress",
					Encoder:   "libx265",
					Status:    jobs.StatusPending,
					CreatedAt: time.Now(),
				}
				store.SaveJob(job)
				store.AppendToOrder(job.ID)
			}
		}(w)
	}

	// Readers getting all jobs
	for r := 0; r < 5; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				store.GetAllJobs()
			}
		}()
	}

	// Status updaters
	for u := 0; u < 5; u++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				jobID := fmt.Sprintf("init-%d", (workerID*10+i)%50)
				if job, err := store.GetJob(jobID); err == nil && job != nil {
					job.Status = jobs.StatusRunning
					job.Progress = float64(i * 10)
					store.SaveJob(job)
				}
			}
		}(u)
	}

	// Stats readers
	for s := 0; s < 3; s++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				store.Stats()
			}
		}()
	}

	wg.Wait()

	// Verify final state is consistent
	allJobs, _, err := store.GetAllJobs()
	if err != nil {
		t.Fatalf("final GetAllJobs failed: %v", err)
	}

	// Should have 50 initial + (5 workers × 20 jobs) = 150 jobs
	expected := 50 + (5 * 20)
	if len(allJobs) != expected {
		t.Errorf("expected %d jobs, got %d", expected, len(allJobs))
	}

	t.Logf("Mixed operations test passed with %d total jobs", len(allJobs))
}

func TestConcurrency_HighContention(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping high contention test in short mode")
	}

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Create single job that all workers will try to update
	job := &jobs.Job{
		ID:        "hot-spot",
		InputPath: "/media/hot.mkv",
		PresetID:  "compress",
		Encoder:   "libx265",
		Status:    jobs.StatusRunning,
		Progress:  0,
		CreatedAt: time.Now(),
	}
	store.SaveJob(job)

	// 50 workers all trying to update same job's progress
	numWorkers := 50
	updatesPerWorker := 20
	var wg sync.WaitGroup
	errors := make(chan error, numWorkers*updatesPerWorker)

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < updatesPerWorker; i++ {
				got, err := store.GetJob("hot-spot")
				if err != nil {
					errors <- err
					return
				}
				got.Progress = float64(workerID*100 + i)
				if err := store.SaveJob(got); err != nil {
					errors <- err
					return
				}
			}
		}(w)
	}

	wg.Wait()
	close(errors)

	// Count errors (should be none due to SQLite locking)
	errorCount := 0
	for err := range errors {
		t.Logf("Error: %v", err)
		errorCount++
	}

	if errorCount > 0 {
		t.Errorf("%d errors during high contention test", errorCount)
	}

	// Final state should be valid
	got, _ := store.GetJob("hot-spot")
	if got == nil {
		t.Fatal("job disappeared")
	}

	t.Logf("High contention test passed. Final progress: %.0f", got.Progress)
}

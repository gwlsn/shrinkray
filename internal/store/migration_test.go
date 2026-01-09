package store

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gwlsn/shrinkray/internal/jobs"
)

// jsonPersistenceData matches the structure used in migration
type testPersistenceData struct {
	Jobs  []*jobs.Job `json:"jobs"`
	Order []string    `json:"order"`
}

func createTestJobForMigration(id string, status jobs.Status) *jobs.Job {
	return &jobs.Job{
		ID:         id,
		InputPath:  "/media/video_" + id + ".mkv",
		PresetID:   "compress-hevc",
		Encoder:    "libx265",
		IsHardware: false,
		Status:     status,
		InputSize:  1000000,
		Duration:   60000,
		CreatedAt:  time.Now(),
	}
}

func TestNeedsMigration(t *testing.T) {
	tmpDir := t.TempDir()
	jsonPath := filepath.Join(tmpDir, "queue.json")
	dbPath := filepath.Join(tmpDir, "shrinkray.db")

	// Neither exists - no migration needed
	if NeedsMigration(jsonPath, dbPath) {
		t.Error("expected no migration when neither exists")
	}

	// Create JSON file - migration needed
	os.WriteFile(jsonPath, []byte("{}"), 0644)
	if !NeedsMigration(jsonPath, dbPath) {
		t.Error("expected migration needed when JSON exists but DB doesn't")
	}

	// Create DB - no migration needed (already migrated)
	os.WriteFile(dbPath, []byte(""), 0644)
	if NeedsMigration(jsonPath, dbPath) {
		t.Error("expected no migration when both exist")
	}

	// Remove JSON - no migration needed
	os.Remove(jsonPath)
	if NeedsMigration(jsonPath, dbPath) {
		t.Error("expected no migration when only DB exists")
	}
}

func TestMigration_EmptyQueue(t *testing.T) {
	tmpDir := t.TempDir()
	jsonPath := filepath.Join(tmpDir, "queue.json")
	dbPath := filepath.Join(tmpDir, "shrinkray.db")

	// Create empty queue JSON
	data := testPersistenceData{Jobs: []*jobs.Job{}, Order: []string{}}
	jsonBytes, _ := json.Marshal(data)
	os.WriteFile(jsonPath, jsonBytes, 0644)

	// Migrate
	result := MigrateFromJSON(jsonPath, dbPath)

	if !result.Success {
		t.Errorf("migration failed: %s", result.ErrorMessage)
	}

	if !result.WasEmpty {
		t.Error("expected WasEmpty to be true")
	}

	// Backup should exist
	if result.BackupPath == "" {
		t.Error("expected backup path")
	}

	// Original JSON should be gone
	if _, err := os.Stat(jsonPath); !os.IsNotExist(err) {
		t.Error("original JSON should be removed")
	}
}

func TestMigration_ValidQueue(t *testing.T) {
	tmpDir := t.TempDir()
	jsonPath := filepath.Join(tmpDir, "queue.json")
	dbPath := filepath.Join(tmpDir, "shrinkray.db")

	// Create queue with jobs
	job1 := createTestJobForMigration("job1", jobs.StatusComplete)
	job1.SpaceSaved = 500000
	job2 := createTestJobForMigration("job2", jobs.StatusPending)
	job3 := createTestJobForMigration("job3", jobs.StatusRunning)

	data := testPersistenceData{
		Jobs:  []*jobs.Job{job1, job2, job3},
		Order: []string{"job1", "job2", "job3"},
	}
	jsonBytes, _ := json.Marshal(data)
	os.WriteFile(jsonPath, jsonBytes, 0644)

	// Migrate
	result := MigrateFromJSON(jsonPath, dbPath)

	if !result.Success {
		t.Fatalf("migration failed: %s", result.ErrorMessage)
	}

	if result.JobsImported != 3 {
		t.Errorf("expected 3 jobs imported, got %d", result.JobsImported)
	}

	// Verify DB contents
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to open migrated DB: %v", err)
	}
	defer store.Close()

	allJobs, order, _ := store.GetAllJobs()
	if len(allJobs) != 3 {
		t.Errorf("expected 3 jobs in DB, got %d", len(allJobs))
	}

	if len(order) != 3 {
		t.Errorf("expected 3 jobs in order, got %d", len(order))
	}

	// Verify job data
	got, _ := store.GetJob("job1")
	if got == nil {
		t.Fatal("job1 not found")
	}
	if got.Status != jobs.StatusComplete {
		t.Errorf("job1 status: expected complete, got %s", got.Status)
	}
	if got.SpaceSaved != 500000 {
		t.Errorf("job1 space_saved: expected 500000, got %d", got.SpaceSaved)
	}
}

func TestMigration_OrderPreserved(t *testing.T) {
	tmpDir := t.TempDir()
	jsonPath := filepath.Join(tmpDir, "queue.json")
	dbPath := filepath.Join(tmpDir, "shrinkray.db")

	// Create jobs with specific order
	jobA := createTestJobForMigration("A", jobs.StatusPending)
	jobB := createTestJobForMigration("B", jobs.StatusPending)
	jobC := createTestJobForMigration("C", jobs.StatusPending)

	data := testPersistenceData{
		Jobs:  []*jobs.Job{jobC, jobA, jobB}, // Jobs in different order than Order array
		Order: []string{"A", "B", "C"},        // This is the correct processing order
	}
	jsonBytes, _ := json.Marshal(data)
	os.WriteFile(jsonPath, jsonBytes, 0644)

	// Migrate
	MigrateFromJSON(jsonPath, dbPath)

	// Verify order
	store, _ := NewSQLiteStore(dbPath)
	defer store.Close()

	_, order, _ := store.GetAllJobs()
	expected := []string{"A", "B", "C"}
	for i, id := range expected {
		if i >= len(order) || order[i] != id {
			t.Errorf("order[%d]: expected %s, got %v", i, id, order)
			break
		}
	}
}

func TestMigration_CorruptJSON_Truncated(t *testing.T) {
	tmpDir := t.TempDir()
	jsonPath := filepath.Join(tmpDir, "queue.json")
	dbPath := filepath.Join(tmpDir, "shrinkray.db")

	// Write truncated JSON
	os.WriteFile(jsonPath, []byte(`{"jobs":[{"id":"1"`), 0644)

	result := MigrateFromJSON(jsonPath, dbPath)

	if result.Success {
		t.Error("expected migration to fail for truncated JSON")
	}

	// Corrupt file should be renamed
	if _, err := os.Stat(jsonPath + ".corrupt"); os.IsNotExist(err) {
		t.Error("corrupt file should be renamed to .corrupt")
	}
}

func TestMigration_CorruptJSON_InvalidSyntax(t *testing.T) {
	tmpDir := t.TempDir()
	jsonPath := filepath.Join(tmpDir, "queue.json")
	dbPath := filepath.Join(tmpDir, "shrinkray.db")

	// Write invalid JSON
	os.WriteFile(jsonPath, []byte(`{invalid}`), 0644)

	result := MigrateFromJSON(jsonPath, dbPath)

	if result.Success {
		t.Error("expected migration to fail for invalid JSON")
	}

	// Should be renamed to .corrupt
	if _, err := os.Stat(jsonPath + ".corrupt"); os.IsNotExist(err) {
		t.Error("corrupt file should be renamed")
	}
}

func TestMigration_CorruptJSON_BinaryGarbage(t *testing.T) {
	tmpDir := t.TempDir()
	jsonPath := filepath.Join(tmpDir, "queue.json")
	dbPath := filepath.Join(tmpDir, "shrinkray.db")

	// Write binary garbage
	garbage := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE}
	os.WriteFile(jsonPath, garbage, 0644)

	result := MigrateFromJSON(jsonPath, dbPath)

	if result.Success {
		t.Error("expected migration to fail for binary garbage")
	}
}

func TestMigration_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	jsonPath := filepath.Join(tmpDir, "queue.json")
	dbPath := filepath.Join(tmpDir, "shrinkray.db")

	// Create empty file
	os.WriteFile(jsonPath, []byte{}, 0644)

	result := MigrateFromJSON(jsonPath, dbPath)

	if !result.Success {
		t.Errorf("empty file should succeed: %s", result.ErrorMessage)
	}
	if !result.WasEmpty {
		t.Error("expected WasEmpty to be true")
	}
}

func TestMigration_BackupCreated(t *testing.T) {
	tmpDir := t.TempDir()
	jsonPath := filepath.Join(tmpDir, "queue.json")
	dbPath := filepath.Join(tmpDir, "shrinkray.db")

	// Create valid queue
	data := testPersistenceData{
		Jobs:  []*jobs.Job{createTestJobForMigration("test", jobs.StatusPending)},
		Order: []string{"test"},
	}
	jsonBytes, _ := json.Marshal(data)
	os.WriteFile(jsonPath, jsonBytes, 0644)

	result := MigrateFromJSON(jsonPath, dbPath)

	if !result.Success {
		t.Fatalf("migration failed: %s", result.ErrorMessage)
	}

	// Backup should exist
	backupPath := jsonPath + ".backup"
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		t.Error("backup file should exist")
	}

	// Backup should have same content
	backupContent, _ := os.ReadFile(backupPath)
	if !bytes.Equal(backupContent, jsonBytes) {
		t.Error("backup content doesn't match original")
	}
}

func TestMigration_LargeQueue(t *testing.T) {
	tmpDir := t.TempDir()
	jsonPath := filepath.Join(tmpDir, "queue.json")
	dbPath := filepath.Join(tmpDir, "shrinkray.db")

	// Create 1000 jobs
	numJobs := 1000
	jobList := make([]*jobs.Job, numJobs)
	order := make([]string, numJobs)
	for i := 0; i < numJobs; i++ {
		id := string(rune('A' + i/26/26%26)) + string(rune('A'+i/26%26)) + string(rune('A'+i%26))
		jobList[i] = createTestJobForMigration(id, jobs.StatusPending)
		order[i] = id
	}

	data := testPersistenceData{Jobs: jobList, Order: order}
	jsonBytes, _ := json.Marshal(data)
	os.WriteFile(jsonPath, jsonBytes, 0644)

	start := time.Now()
	result := MigrateFromJSON(jsonPath, dbPath)
	elapsed := time.Since(start)

	if !result.Success {
		t.Fatalf("migration failed: %s", result.ErrorMessage)
	}

	if result.JobsImported != numJobs {
		t.Errorf("expected %d jobs, got %d", numJobs, result.JobsImported)
	}

	t.Logf("Migrated %d jobs in %v", numJobs, elapsed)

	// Should complete in reasonable time (< 5 seconds)
	if elapsed > 5*time.Second {
		t.Errorf("migration too slow: %v", elapsed)
	}
}

func TestMigration_OrderReferencesNonexistentJob(t *testing.T) {
	tmpDir := t.TempDir()
	jsonPath := filepath.Join(tmpDir, "queue.json")
	dbPath := filepath.Join(tmpDir, "shrinkray.db")

	// Create queue with orphan order entry
	job := createTestJobForMigration("exists", jobs.StatusPending)
	data := testPersistenceData{
		Jobs:  []*jobs.Job{job},
		Order: []string{"exists", "ghost"}, // "ghost" doesn't exist
	}
	jsonBytes, _ := json.Marshal(data)
	os.WriteFile(jsonPath, jsonBytes, 0644)

	result := MigrateFromJSON(jsonPath, dbPath)

	if !result.Success {
		t.Fatalf("migration should succeed: %s", result.ErrorMessage)
	}

	if result.JobsSkipped != 1 {
		t.Errorf("expected 1 job skipped (ghost), got %d", result.JobsSkipped)
	}
}

func TestInitStore_FreshStart(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := InitStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to init store: %v", err)
	}
	defer store.Close()

	// Verify DB was created
	dbPath := filepath.Join(tmpDir, "shrinkray.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("database file should exist")
	}
}

func TestInitStore_WithMigration(t *testing.T) {
	tmpDir := t.TempDir()
	jsonPath := filepath.Join(tmpDir, "queue.json")

	// Create JSON queue
	job := createTestJobForMigration("migrate-me", jobs.StatusPending)
	data := testPersistenceData{
		Jobs:  []*jobs.Job{job},
		Order: []string{"migrate-me"},
	}
	jsonBytes, _ := json.Marshal(data)
	os.WriteFile(jsonPath, jsonBytes, 0644)

	// Init store (should migrate)
	store, err := InitStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to init store: %v", err)
	}
	defer store.Close()

	// Verify job was migrated
	got, _ := store.GetJob("migrate-me")
	if got == nil {
		t.Error("job should have been migrated")
	}

	// JSON should be backed up
	if _, err := os.Stat(jsonPath); !os.IsNotExist(err) {
		t.Error("original JSON should be removed after migration")
	}
	if _, err := os.Stat(jsonPath + ".backup"); os.IsNotExist(err) {
		t.Error("backup should exist")
	}
}

func TestInitStore_ResetsRunningJobs(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "shrinkray.db")

	// Create store with running job
	store1, _ := NewSQLiteStore(dbPath)
	job := createTestJobForMigration("running", jobs.StatusRunning)
	job.Progress = 50.0
	store1.SaveJob(job)
	store1.AppendToOrder(job.ID)
	store1.Close()

	// Reopen via InitStore (simulates restart)
	store2, err := InitStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to init store: %v", err)
	}
	defer store2.Close()

	// Job should be reset to pending
	got, _ := store2.GetJob("running")
	if got.Status != jobs.StatusPending {
		t.Errorf("expected status pending after restart, got %s", got.Status)
	}
	if got.Progress != 0 {
		t.Errorf("expected progress 0 after restart, got %f", got.Progress)
	}
}

func TestGetDBPath(t *testing.T) {
	tests := []struct {
		jsonPath string
		expected string
	}{
		{"/config/queue.json", "/config/shrinkray.db"},
		{"/data/shrinkray/queue.json", "/data/shrinkray/shrinkray.db"},
		{"./queue.json", "shrinkray.db"}, // filepath.Join normalizes ./
	}

	for _, tt := range tests {
		got := GetDBPath(tt.jsonPath)
		if got != tt.expected {
			t.Errorf("GetDBPath(%s): expected %s, got %s", tt.jsonPath, tt.expected, got)
		}
	}
}

func TestGetJSONPath(t *testing.T) {
	tests := []struct {
		configDir string
		expected  string
	}{
		{"/config", "/config/queue.json"},
		{"/data/shrinkray", "/data/shrinkray/queue.json"},
		{".", "queue.json"}, // filepath.Join normalizes ./
	}

	for _, tt := range tests {
		got := GetJSONPath(tt.configDir)
		if got != tt.expected {
			t.Errorf("GetJSONPath(%s): expected %s, got %s", tt.configDir, tt.expected, got)
		}
	}
}

package store

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gwlsn/shrinkray/internal/jobs"
	"github.com/gwlsn/shrinkray/internal/logger"
)

// jsonPersistenceData matches the structure in internal/jobs/queue.go
type jsonPersistenceData struct {
	Jobs  []*jobs.Job `json:"jobs"`
	Order []string    `json:"order"`
}

// MigrationResult contains the outcome of a migration attempt.
type MigrationResult struct {
	Success       bool
	JobsImported  int
	JobsSkipped   int
	BackupPath    string
	WasEmpty      bool
	ErrorMessage  string
}

// NeedsMigration checks if migration from JSON to SQLite is needed.
// Returns true if jsonPath exists and dbPath does not exist.
func NeedsMigration(jsonPath, dbPath string) bool {
	_, jsonErr := os.Stat(jsonPath)
	_, dbErr := os.Stat(dbPath)

	jsonExists := jsonErr == nil
	dbExists := dbErr == nil

	return jsonExists && !dbExists
}

// MigrateFromJSON migrates data from a JSON queue file to SQLite.
// On success, the JSON file is renamed to .backup.
// On failure, the JSON file is renamed to .corrupt and an empty DB is created.
func MigrateFromJSON(jsonPath, dbPath string) *MigrationResult {
	result := &MigrationResult{}

	// Read JSON file
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		result.ErrorMessage = fmt.Sprintf("read JSON: %v", err)
		handleMigrationFailure(jsonPath, result)
		return result
	}

	// Check for empty file
	if len(data) == 0 {
		result.WasEmpty = true
		result.Success = true
		renameToBackup(jsonPath, result)
		return result
	}

	// Parse JSON
	var pd jsonPersistenceData
	if err := json.Unmarshal(data, &pd); err != nil {
		result.ErrorMessage = fmt.Sprintf("parse JSON: %v", err)
		handleMigrationFailure(jsonPath, result)
		return result
	}

	// Check for empty queue
	if len(pd.Jobs) == 0 {
		result.WasEmpty = true
		result.Success = true
		renameToBackup(jsonPath, result)
		return result
	}

	// Create backup before proceeding
	backupPath := jsonPath + ".backup"
	if err := copyFile(jsonPath, backupPath); err != nil {
		result.ErrorMessage = fmt.Sprintf("create backup: %v", err)
		handleMigrationFailure(jsonPath, result)
		return result
	}
	result.BackupPath = backupPath

	// Create SQLite store
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		result.ErrorMessage = fmt.Sprintf("create SQLite store: %v", err)
		// Remove partial DB if it was created
		os.Remove(dbPath)
		os.Remove(dbPath + "-wal")
		os.Remove(dbPath + "-shm")
		handleMigrationFailure(jsonPath, result)
		return result
	}
	defer store.Close()

	// Build a map of job IDs for order validation
	jobMap := make(map[string]*jobs.Job)
	for _, job := range pd.Jobs {
		jobMap[job.ID] = job
	}

	// Import jobs
	if err := store.SaveJobs(pd.Jobs); err != nil {
		result.ErrorMessage = fmt.Sprintf("save jobs: %v", err)
		store.Close()
		os.Remove(dbPath)
		os.Remove(dbPath + "-wal")
		os.Remove(dbPath + "-shm")
		handleMigrationFailure(jsonPath, result)
		return result
	}
	result.JobsImported = len(pd.Jobs)

	// Import order (only for jobs that exist)
	for _, id := range pd.Order {
		if _, exists := jobMap[id]; exists {
			if err := store.AppendToOrder(id); err != nil {
				logger.Warn("Failed to add job to order during migration", "job_id", id, "error", err)
				result.JobsSkipped++
			}
		} else {
			// Order references non-existent job, skip it
			result.JobsSkipped++
		}
	}

	// Success - remove the original JSON (backup already exists)
	os.Remove(jsonPath)
	result.Success = true

	logger.Info("Migration complete",
		"jobs_imported", result.JobsImported,
		"jobs_skipped", result.JobsSkipped,
		"backup", result.BackupPath,
	)

	return result
}

// handleMigrationFailure renames the JSON file to .corrupt and logs the error.
func handleMigrationFailure(jsonPath string, result *MigrationResult) {
	corruptPath := jsonPath + ".corrupt"

	// If file exists, rename it
	if _, err := os.Stat(jsonPath); err == nil {
		if err := os.Rename(jsonPath, corruptPath); err != nil {
			logger.Error("Failed to rename corrupt JSON file", "error", err)
		} else {
			logger.Warn("Renamed corrupt queue file", "path", corruptPath)
		}
	}

	logger.Error("Migration failed, starting with empty queue", "error", result.ErrorMessage)
}

// renameToBackup renames the JSON file to .backup.
func renameToBackup(jsonPath string, result *MigrationResult) {
	backupPath := jsonPath + ".backup"
	if err := os.Rename(jsonPath, backupPath); err != nil {
		logger.Warn("Failed to rename JSON to backup", "error", err)
	} else {
		result.BackupPath = backupPath
	}
}

// copyFile copies a file from src to dst.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}

// GetDBPath converts a JSON queue path to the corresponding SQLite path.
// e.g., /config/queue.json -> /config/shrinkray.db
func GetDBPath(jsonPath string) string {
	dir := filepath.Dir(jsonPath)
	return filepath.Join(dir, "shrinkray.db")
}

// GetJSONPath returns the expected JSON queue path for a config directory.
func GetJSONPath(configDir string) string {
	return filepath.Join(configDir, "queue.json")
}

// CleanupDBFiles removes SQLite database files (main, WAL, and SHM).
func CleanupDBFiles(dbPath string) {
	os.Remove(dbPath)
	os.Remove(dbPath + "-wal")
	os.Remove(dbPath + "-shm")
}

// InitStore initializes the store, performing migration if necessary.
// This is the main entry point for store initialization.
func InitStore(configDir string) (*SQLiteStore, error) {
	jsonPath := GetJSONPath(configDir)
	dbPath := GetDBPath(jsonPath)

	// Check if we need to migrate
	if NeedsMigration(jsonPath, dbPath) {
		logger.Info("Migrating from JSON to SQLite", "json", jsonPath, "db", dbPath)
		result := MigrateFromJSON(jsonPath, dbPath)
		if !result.Success {
			// Migration failed, but we continue with empty DB
			logger.Warn("Migration failed, starting fresh", "error", result.ErrorMessage)
		}
	}

	// Open or create SQLite store
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}

	// Reset any running jobs (crash recovery)
	count, err := store.ResetRunningJobs()
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("reset running jobs: %w", err)
	}
	if count > 0 {
		logger.Info("Reset interrupted jobs to pending", "count", count)
	}

	return store, nil
}

// IsDBPath checks if a path looks like a SQLite database path.
func IsDBPath(path string) bool {
	return strings.HasSuffix(path, ".db")
}

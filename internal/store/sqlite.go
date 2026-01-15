package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/gwlsn/shrinkray/internal/jobs"
	_ "modernc.org/sqlite"
)

const schemaVersion = 3

const schema = `
CREATE TABLE IF NOT EXISTS jobs (
	id TEXT PRIMARY KEY,
	input_path TEXT NOT NULL,
	output_path TEXT,
	temp_path TEXT,
	preset_id TEXT NOT NULL,
	encoder TEXT NOT NULL,
	is_hardware INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL,
	progress REAL NOT NULL DEFAULT 0,
	speed REAL NOT NULL DEFAULT 0,
	eta TEXT,
	error TEXT,
	input_size INTEGER NOT NULL DEFAULT 0,
	output_size INTEGER,
	space_saved INTEGER,
	duration_ms INTEGER,
	bitrate INTEGER,
	width INTEGER,
	height INTEGER,
	frame_rate REAL,
	video_codec TEXT,
	profile TEXT,
	bit_depth INTEGER,
	transcode_secs INTEGER,
	created_at TEXT NOT NULL,
	started_at TEXT,
	completed_at TEXT
);

CREATE TABLE IF NOT EXISTS job_order (
	position INTEGER PRIMARY KEY AUTOINCREMENT,
	job_id TEXT NOT NULL UNIQUE REFERENCES jobs(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS schema_version (
	version INTEGER NOT NULL,
	applied_at TEXT DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS stats_metadata (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL,
	updated_at TEXT DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);
CREATE INDEX IF NOT EXISTS idx_jobs_created_at ON jobs(created_at);
CREATE INDEX IF NOT EXISTS idx_jobs_status_created ON jobs(status, created_at);
`

// SQLiteStore implements Store using SQLite.
type SQLiteStore struct {
	db   *sql.DB
	mu   sync.RWMutex // Protects concurrent access
	path string
}

// NewSQLiteStore creates a new SQLite-backed store.
// The database file is created if it doesn't exist.
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	// Open database with WAL mode for better concurrency
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Enable foreign keys
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	// Create schema
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}

	// Check/set schema version
	var version int
	err = db.QueryRow("SELECT version FROM schema_version ORDER BY version DESC LIMIT 1").Scan(&version)
	if err == sql.ErrNoRows {
		// Fresh database, insert version and initialize stats_metadata
		_, err = db.Exec("INSERT INTO schema_version (version) VALUES (?)", schemaVersion)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("insert schema version: %w", err)
		}
		// Initialize stats_metadata with default values
		_, err = db.Exec(`
			INSERT OR IGNORE INTO stats_metadata (key, value) VALUES
				('session_saved', '0'),
				('lifetime_saved', '0')
		`)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("init stats metadata: %w", err)
		}
	} else if err != nil {
		db.Close()
		return nil, fmt.Errorf("check schema version: %w", err)
	} else if version < schemaVersion {
		// Run migrations
		if version < 2 {
			// Migrate v1 -> v2: add stats_metadata table and initialize
			_, err = db.Exec(`
				CREATE TABLE IF NOT EXISTS stats_metadata (
					key TEXT PRIMARY KEY,
					value TEXT NOT NULL,
					updated_at TEXT DEFAULT CURRENT_TIMESTAMP
				)
			`)
			if err != nil {
				db.Close()
				return nil, fmt.Errorf("create stats_metadata table: %w", err)
			}
			_, err = db.Exec(`
				INSERT OR IGNORE INTO stats_metadata (key, value) VALUES
					('session_saved', '0'),
					('lifetime_saved', '0')
			`)
			if err != nil {
				db.Close()
				return nil, fmt.Errorf("init stats metadata: %w", err)
			}
		}
		if version < 3 {
			// Migrate v2 -> v3: add video_codec, profile, bit_depth columns
			_, err = db.Exec(`ALTER TABLE jobs ADD COLUMN video_codec TEXT`)
			if err != nil {
				db.Close()
				return nil, fmt.Errorf("add video_codec column: %w", err)
			}
			_, err = db.Exec(`ALTER TABLE jobs ADD COLUMN profile TEXT`)
			if err != nil {
				db.Close()
				return nil, fmt.Errorf("add profile column: %w", err)
			}
			_, err = db.Exec(`ALTER TABLE jobs ADD COLUMN bit_depth INTEGER`)
			if err != nil {
				db.Close()
				return nil, fmt.Errorf("add bit_depth column: %w", err)
			}
		}
		// Update version
		_, err = db.Exec("INSERT INTO schema_version (version) VALUES (?)", schemaVersion)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("update schema version: %w", err)
		}
	}

	return &SQLiteStore{db: db, path: dbPath}, nil
}

// SaveJob persists a job using INSERT OR REPLACE.
func (s *SQLiteStore) SaveJob(job *jobs.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.saveJobLocked(job)
}

func (s *SQLiteStore) saveJobLocked(job *jobs.Job) error {
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO jobs (
			id, input_path, output_path, temp_path, preset_id, encoder, is_hardware,
			status, progress, speed, eta, error, input_size, output_size, space_saved,
			duration_ms, bitrate, width, height, frame_rate, video_codec, profile, bit_depth,
			transcode_secs, created_at, started_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		job.ID, job.InputPath, nullString(job.OutputPath), nullString(job.TempPath),
		job.PresetID, job.Encoder, boolToInt(job.IsHardware),
		string(job.Status), job.Progress, job.Speed, nullString(job.ETA), nullString(job.Error),
		job.InputSize, nullInt64(job.OutputSize), nullInt64(job.SpaceSaved),
		nullInt64(job.Duration), nullInt64(job.Bitrate), nullInt(job.Width), nullInt(job.Height),
		nullFloat64(job.FrameRate), nullString(job.VideoCodec), nullString(job.Profile), nullInt(job.BitDepth),
		nullInt64(job.TranscodeTime),
		formatTime(job.CreatedAt), formatTimePtr(job.StartedAt), formatTimePtr(job.CompletedAt),
	)
	return err
}

// GetJob retrieves a job by ID.
func (s *SQLiteStore) GetJob(id string) (*jobs.Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.getJobLocked(id)
}

func (s *SQLiteStore) getJobLocked(id string) (*jobs.Job, error) {
	row := s.db.QueryRow(`
		SELECT id, input_path, output_path, temp_path, preset_id, encoder, is_hardware,
			status, progress, speed, eta, error, input_size, output_size, space_saved,
			duration_ms, bitrate, width, height, frame_rate, video_codec, profile, bit_depth,
			transcode_secs, created_at, started_at, completed_at
		FROM jobs WHERE id = ?
	`, id)

	return scanJob(row)
}

// DeleteJob removes a job by ID.
func (s *SQLiteStore) DeleteJob(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Delete from jobs (cascade will remove from job_order)
	_, err := s.db.Exec("DELETE FROM jobs WHERE id = ?", id)
	return err
}

// SaveJobs persists multiple jobs in a transaction.
func (s *SQLiteStore) SaveJobs(jobList []*jobs.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`
		INSERT OR REPLACE INTO jobs (
			id, input_path, output_path, temp_path, preset_id, encoder, is_hardware,
			status, progress, speed, eta, error, input_size, output_size, space_saved,
			duration_ms, bitrate, width, height, frame_rate, video_codec, profile, bit_depth,
			transcode_secs, created_at, started_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, job := range jobList {
		_, err := stmt.Exec(
			job.ID, job.InputPath, nullString(job.OutputPath), nullString(job.TempPath),
			job.PresetID, job.Encoder, boolToInt(job.IsHardware),
			string(job.Status), job.Progress, job.Speed, nullString(job.ETA), nullString(job.Error),
			job.InputSize, nullInt64(job.OutputSize), nullInt64(job.SpaceSaved),
			nullInt64(job.Duration), nullInt64(job.Bitrate), nullInt(job.Width), nullInt(job.Height),
			nullFloat64(job.FrameRate), nullString(job.VideoCodec), nullString(job.Profile), nullInt(job.BitDepth),
			nullInt64(job.TranscodeTime),
			formatTime(job.CreatedAt), formatTimePtr(job.StartedAt), formatTimePtr(job.CompletedAt),
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetAllJobs returns all jobs in queue order.
func (s *SQLiteStore) GetAllJobs() ([]*jobs.Job, []string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Get jobs in order
	rows, err := s.db.Query(`
		SELECT j.id, j.input_path, j.output_path, j.temp_path, j.preset_id, j.encoder, j.is_hardware,
			j.status, j.progress, j.speed, j.eta, j.error, j.input_size, j.output_size, j.space_saved,
			j.duration_ms, j.bitrate, j.width, j.height, j.frame_rate, j.video_codec, j.profile, j.bit_depth,
			j.transcode_secs, j.created_at, j.started_at, j.completed_at
		FROM jobs j
		LEFT JOIN job_order o ON j.id = o.job_id
		ORDER BY o.position ASC, j.created_at ASC
	`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var jobList []*jobs.Job
	var order []string

	for rows.Next() {
		job, err := scanJobRows(rows)
		if err != nil {
			return nil, nil, err
		}
		jobList = append(jobList, job)
		order = append(order, job.ID)
	}

	return jobList, order, rows.Err()
}

// GetJobsByStatus returns all jobs with the given status.
func (s *SQLiteStore) GetJobsByStatus(status jobs.Status) ([]*jobs.Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT j.id, j.input_path, j.output_path, j.temp_path, j.preset_id, j.encoder, j.is_hardware,
			j.status, j.progress, j.speed, j.eta, j.error, j.input_size, j.output_size, j.space_saved,
			j.duration_ms, j.bitrate, j.width, j.height, j.frame_rate, j.video_codec, j.profile, j.bit_depth,
			j.transcode_secs, j.created_at, j.started_at, j.completed_at
		FROM jobs j
		LEFT JOIN job_order o ON j.id = o.job_id
		WHERE j.status = ?
		ORDER BY o.position ASC, j.created_at ASC
	`, string(status))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobList []*jobs.Job
	for rows.Next() {
		job, err := scanJobRows(rows)
		if err != nil {
			return nil, err
		}
		jobList = append(jobList, job)
	}

	return jobList, rows.Err()
}

// GetNextPendingJob returns the first pending job in queue order.
func (s *SQLiteStore) GetNextPendingJob() (*jobs.Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRow(`
		SELECT j.id, j.input_path, j.output_path, j.temp_path, j.preset_id, j.encoder, j.is_hardware,
			j.status, j.progress, j.speed, j.eta, j.error, j.input_size, j.output_size, j.space_saved,
			j.duration_ms, j.bitrate, j.width, j.height, j.frame_rate, j.video_codec, j.profile, j.bit_depth,
			j.transcode_secs, j.created_at, j.started_at, j.completed_at
		FROM jobs j
		LEFT JOIN job_order o ON j.id = o.job_id
		WHERE j.status = 'pending'
		ORDER BY o.position ASC, j.created_at ASC
		LIMIT 1
	`)

	job, err := scanJob(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return job, err
}

// AppendToOrder adds a job ID to the end of the queue.
func (s *SQLiteStore) AppendToOrder(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec("INSERT OR IGNORE INTO job_order (job_id) VALUES (?)", id)
	return err
}

// RemoveFromOrder removes a job ID from the queue order.
func (s *SQLiteStore) RemoveFromOrder(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec("DELETE FROM job_order WHERE job_id = ?", id)
	return err
}

// SetOrder persists the full job order, replacing any existing order.
func (s *SQLiteStore) SetOrder(order []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Clear existing order
	if _, err := tx.Exec("DELETE FROM job_order"); err != nil {
		return err
	}

	// Insert in new order (autoincrement gives sequential positions)
	for _, jobID := range order {
		if _, err := tx.Exec("INSERT INTO job_order (job_id) VALUES (?)", jobID); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// ResetRunningJobs resets all running jobs to pending.
func (s *SQLiteStore) ResetRunningJobs() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec(`
		UPDATE jobs
		SET status = 'pending', progress = 0, speed = 0, eta = NULL
		WHERE status = 'running'
	`)
	if err != nil {
		return 0, err
	}

	count, err := result.RowsAffected()
	return int(count), err
}

// Stats returns queue statistics including session and lifetime savings.
func (s *SQLiteStore) Stats() (Stats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var stats Stats

	// Get session_saved and lifetime_saved counters from stats_metadata
	var sessionSavedStr, lifetimeSavedStr string
	err := s.db.QueryRow(`SELECT value FROM stats_metadata WHERE key = 'session_saved'`).Scan(&sessionSavedStr)
	if err != nil && err != sql.ErrNoRows {
		return stats, fmt.Errorf("get session saved: %w", err)
	}
	err = s.db.QueryRow(`SELECT value FROM stats_metadata WHERE key = 'lifetime_saved'`).Scan(&lifetimeSavedStr)
	if err != nil && err != sql.ErrNoRows {
		return stats, fmt.Errorf("get lifetime saved: %w", err)
	}

	sessionSaved, _ := strconv.ParseInt(sessionSavedStr, 10, 64)
	lifetimeSaved, _ := strconv.ParseInt(lifetimeSavedStr, 10, 64)

	// Get job counts
	row := s.db.QueryRow(`
		SELECT
			COUNT(*) as total,
			SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END) as pending,
			SUM(CASE WHEN status = 'running' THEN 1 ELSE 0 END) as running,
			SUM(CASE WHEN status = 'complete' THEN 1 ELSE 0 END) as complete,
			SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END) as failed,
			SUM(CASE WHEN status = 'cancelled' THEN 1 ELSE 0 END) as cancelled,
			SUM(CASE WHEN status = 'skipped' THEN 1 ELSE 0 END) as skipped
		FROM jobs
	`)

	err = row.Scan(&stats.Total, &stats.Pending, &stats.Running, &stats.Complete,
		&stats.Failed, &stats.Cancelled, &stats.Skipped)
	if err != nil {
		return stats, err
	}

	stats.SessionSaved = sessionSaved
	stats.LifetimeSaved = lifetimeSaved
	stats.TotalSaved = stats.SessionSaved // Header shows session saved for API compatibility

	return stats, nil
}

// ResetSession resets the session saved counter to 0.
func (s *SQLiteStore) ResetSession() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		UPDATE stats_metadata SET value = '0', updated_at = datetime('now')
		WHERE key = 'session_saved'
	`)
	return err
}

// AddToLifetimeSaved increments both session and lifetime saved counters.
// Call this when a job completes successfully.
func (s *SQLiteStore) AddToLifetimeSaved(bytes int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Increment both session and lifetime counters
	_, err := s.db.Exec(`
		UPDATE stats_metadata
		SET value = CAST((CAST(value AS INTEGER) + ?) AS TEXT),
		    updated_at = datetime('now')
		WHERE key IN ('session_saved', 'lifetime_saved')
	`, bytes)
	return err
}

// SessionLifetimeStats returns the session and lifetime saved bytes.
// This implements the jobs.StoreWithStats interface.
func (s *SQLiteStore) SessionLifetimeStats() (sessionSaved, lifetimeSaved int64, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Get session_saved and lifetime_saved counters from stats_metadata
	var sessionSavedStr, lifetimeSavedStr string
	err = s.db.QueryRow(`SELECT value FROM stats_metadata WHERE key = 'session_saved'`).Scan(&sessionSavedStr)
	if err != nil && err != sql.ErrNoRows {
		return 0, 0, fmt.Errorf("get session saved: %w", err)
	}
	err = s.db.QueryRow(`SELECT value FROM stats_metadata WHERE key = 'lifetime_saved'`).Scan(&lifetimeSavedStr)
	if err != nil && err != sql.ErrNoRows {
		return 0, 0, fmt.Errorf("get lifetime saved: %w", err)
	}

	sessionSaved, _ = strconv.ParseInt(sessionSavedStr, 10, 64)
	lifetimeSaved, _ = strconv.ParseInt(lifetimeSavedStr, 10, 64)

	return sessionSaved, lifetimeSaved, nil
}

// Close closes the database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// Path returns the database file path.
func (s *SQLiteStore) Path() string {
	return s.path
}

// Helper functions for scanning rows

type rowScanner interface {
	Scan(dest ...interface{}) error
}

func scanJob(row rowScanner) (*jobs.Job, error) {
	var job jobs.Job
	var outputPath, tempPath, eta, errStr sql.NullString
	var videoCodec, profile sql.NullString
	var outputSize, spaceSaved, duration, bitrate, transcodeTime sql.NullInt64
	var width, height, bitDepth sql.NullInt64
	var frameRate sql.NullFloat64
	var isHardware int
	var status string
	var createdAt, startedAt, completedAt sql.NullString

	err := row.Scan(
		&job.ID, &job.InputPath, &outputPath, &tempPath,
		&job.PresetID, &job.Encoder, &isHardware,
		&status, &job.Progress, &job.Speed, &eta, &errStr,
		&job.InputSize, &outputSize, &spaceSaved,
		&duration, &bitrate, &width, &height, &frameRate,
		&videoCodec, &profile, &bitDepth,
		&transcodeTime, &createdAt, &startedAt, &completedAt,
	)
	if err != nil {
		return nil, err
	}

	job.OutputPath = outputPath.String
	job.TempPath = tempPath.String
	job.ETA = eta.String
	job.Error = errStr.String
	job.IsHardware = isHardware != 0
	job.Status = jobs.Status(status)
	job.OutputSize = outputSize.Int64
	job.SpaceSaved = spaceSaved.Int64
	job.Duration = duration.Int64
	job.Bitrate = bitrate.Int64
	job.Width = int(width.Int64)
	job.Height = int(height.Int64)
	job.FrameRate = frameRate.Float64
	job.VideoCodec = videoCodec.String
	job.Profile = profile.String
	job.BitDepth = int(bitDepth.Int64)
	job.TranscodeTime = transcodeTime.Int64
	job.CreatedAt = parseTime(createdAt.String)
	job.StartedAt = parseTime(startedAt.String)
	job.CompletedAt = parseTime(completedAt.String)

	return &job, nil
}

func scanJobRows(rows *sql.Rows) (*jobs.Job, error) {
	return scanJob(rows)
}

// Helper functions for SQL values

func nullString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(i int) interface{} {
	if i == 0 {
		return nil
	}
	return i
}

func nullInt64(i int64) interface{} {
	if i == 0 {
		return nil
	}
	return i
}

func nullFloat64(f float64) interface{} {
	if f == 0 {
		return nil
	}
	return f
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func formatTimePtr(t time.Time) interface{} {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

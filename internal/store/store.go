package store

import (
	"github.com/gwlsn/shrinkray/internal/jobs"
)

// Store defines the persistence interface for job data.
// Implementations must be safe for concurrent use.
type Store interface {
	// SaveJob persists a job. If the job already exists (by ID), it is updated.
	SaveJob(job *jobs.Job) error

	// GetJob retrieves a job by ID. Returns nil if not found.
	GetJob(id string) (*jobs.Job, error)

	// DeleteJob removes a job by ID. Also removes it from the order.
	// Returns nil if the job doesn't exist.
	DeleteJob(id string) error

	// SaveJobs persists multiple jobs in a single transaction.
	SaveJobs(jobList []*jobs.Job) error

	// GetAllJobs returns all jobs and their order.
	// Jobs are returned in queue order (first = next to process).
	GetAllJobs() ([]*jobs.Job, []string, error)

	// GetJobsByStatus returns all jobs with the given status.
	GetJobsByStatus(status jobs.Status) ([]*jobs.Job, error)

	// GetNextPendingJob returns the first pending job in queue order.
	// Returns nil if no pending jobs exist.
	GetNextPendingJob() (*jobs.Job, error)

	// AppendToOrder adds a job ID to the end of the queue order.
	AppendToOrder(id string) error

	// RemoveFromOrder removes a job ID from the queue order.
	RemoveFromOrder(id string) error

	// ResetRunningJobs changes all jobs with status "running" to "pending"
	// and clears their progress. Used on startup to recover from crashes.
	// Returns the number of jobs reset.
	ResetRunningJobs() (int, error)

	// Stats returns queue statistics.
	Stats() (Stats, error)

	// ResetSession resets the session start time to now.
	// After reset, SessionSaved will start counting from 0.
	ResetSession() error

	// AddToLifetimeSaved increments the lifetime saved counter.
	// Call this when a job completes successfully.
	AddToLifetimeSaved(bytes int64) error

	// Close closes the store and releases resources.
	Close() error
}

// Stats holds queue statistics.
type Stats struct {
	Pending       int   `json:"pending"`
	Running       int   `json:"running"`
	Complete      int   `json:"complete"`
	Failed        int   `json:"failed"`
	Cancelled     int   `json:"cancelled"`
	Total         int   `json:"total"`
	TotalSaved    int64 `json:"total_saved"`    // For API compatibility (= session_saved)
	SessionSaved  int64 `json:"session_saved"`  // Bytes saved this session
	LifetimeSaved int64 `json:"lifetime_saved"` // All-time bytes saved
}

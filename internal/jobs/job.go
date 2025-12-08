package jobs

import (
	"time"
)

// Status represents the current state of a job
type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusComplete  Status = "complete"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Job represents a transcoding job
type Job struct {
	ID          string    `json:"id"`
	InputPath   string    `json:"input_path"`
	OutputPath  string    `json:"output_path,omitempty"` // Set after completion
	TempPath    string    `json:"temp_path,omitempty"`   // Temp file during transcode
	PresetID    string    `json:"preset_id"`
	Encoder     string    `json:"encoder"`              // "videotoolbox", "nvenc", "none", etc.
	IsHardware  bool      `json:"is_hardware"`          // True if using hardware acceleration
	Status      Status    `json:"status"`
	Progress    float64   `json:"progress"`     // 0-100
	Speed       float64   `json:"speed"`        // Encoding speed (1.0 = realtime)
	ETA         string    `json:"eta"`          // Human-readable ETA
	Error       string    `json:"error,omitempty"`
	InputSize   int64     `json:"input_size"`
	OutputSize  int64     `json:"output_size,omitempty"`  // Populated after completion
	SpaceSaved  int64     `json:"space_saved,omitempty"`  // InputSize - OutputSize
	Duration    int64     `json:"duration_ms,omitempty"`  // Video duration in ms
	Bitrate     int64     `json:"bitrate,omitempty"`      // Source video bitrate in bits/s
	CreatedAt   time.Time `json:"created_at"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
}

// IsTerminal returns true if the job is in a terminal state
func (j *Job) IsTerminal() bool {
	return j.Status == StatusComplete || j.Status == StatusFailed || j.Status == StatusCancelled
}

// JobUpdate represents an update to a job's progress
type JobUpdate struct {
	JobID    string  `json:"job_id"`
	Progress float64 `json:"progress"`
	Speed    float64 `json:"speed"`
	ETA      string  `json:"eta"`
}

// JobEvent represents an event for SSE streaming
type JobEvent struct {
	Type string      `json:"type"` // "update", "complete", "failed", "cancelled"
	Job  *Job        `json:"job"`
}

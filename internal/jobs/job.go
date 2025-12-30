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
	Stderr      string    `json:"stderr,omitempty"`       // Last ~64KB of ffmpeg stderr for diagnostics
	ExitCode    int       `json:"exit_code,omitempty"`    // FFmpeg exit code (0 = success)
	FFmpegArgs  []string  `json:"ffmpeg_args,omitempty"`  // FFmpeg command arguments used
	InputSize   int64     `json:"input_size"`
	OutputSize  int64     `json:"output_size,omitempty"`  // Populated after completion
	SpaceSaved  int64     `json:"space_saved,omitempty"`  // InputSize - OutputSize
	Duration    int64     `json:"duration_ms,omitempty"`  // Video duration in ms
	Bitrate     int64     `json:"bitrate,omitempty"`      // Source video bitrate in bits/s
	TranscodeTime int64   `json:"transcode_secs,omitempty"` // Time to transcode in seconds
	CreatedAt   time.Time `json:"created_at"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	CompletedAt time.Time `json:"completed_at,omitempty"`

	// Software fallback fields - populated when HW encoding fails and retries with SW
	IsSoftwareFallback bool   `json:"is_software_fallback,omitempty"` // True if this job is a SW retry
	OriginalJobID      string `json:"original_job_id,omitempty"`      // ID of the failed HW job
	FallbackReason     string `json:"fallback_reason,omitempty"`      // Why HW encoding failed
}

// IsTerminal returns true if the job is in a terminal state
func (j *Job) IsTerminal() bool {
	return j.Status == StatusComplete || j.Status == StatusFailed || j.Status == StatusCancelled
}

// JobEvent represents an event for SSE streaming
type JobEvent struct {
	Type string      `json:"type"` // "update", "complete", "failed", "cancelled"
	Job  *Job        `json:"job"`
}

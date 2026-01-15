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
	StatusSkipped   Status = "skipped"
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
	Width       int       `json:"width,omitempty"`        // Source video width
	Height      int       `json:"height,omitempty"`       // Source video height
	FrameRate   float64   `json:"frame_rate,omitempty"`   // Source video frame rate
	VideoCodec  string    `json:"video_codec,omitempty"`  // Source codec (h264, hevc, etc.)
	Profile     string    `json:"profile,omitempty"`      // Codec profile (High, High 10, Main, etc.)
	BitDepth    int       `json:"bit_depth,omitempty"`    // Color depth (8, 10, 12)
	TranscodeTime int64   `json:"transcode_secs,omitempty"` // Time to transcode in seconds
	CreatedAt   time.Time `json:"created_at"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
}

// IsTerminal returns true if the job is in a terminal state
func (j *Job) IsTerminal() bool {
	return j.Status == StatusComplete || j.Status == StatusFailed || j.Status == StatusCancelled || j.Status == StatusSkipped
}

// JobEvent represents an event for SSE streaming
type JobEvent struct {
	Type   string `json:"type"`            // "added", "jobs_added", "discovery_progress", "complete", "failed", "skipped", "cancelled", "progress"
	Job    *Job   `json:"job,omitempty"`   // Single job for most events
	Count  int    `json:"count,omitempty"` // Number of jobs for batch events (jobs_added)
	Probed int    `json:"probed,omitempty"` // Files probed so far (discovery_progress)
	Total  int    `json:"total,omitempty"`  // Total files to probe (discovery_progress)
}

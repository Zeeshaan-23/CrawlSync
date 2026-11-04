package server

import (
	"time"
)

// Standard JSON response envelopes

// APIResponse is the standard success response envelope.
type APIResponse struct {
	Success bool `json:"success"`
	Data    any  `json:"data"`
}

// APIErrorDetail represents structured error information.
type APIErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// APIErrorResponse is the standard error response envelope.
type APIErrorResponse struct {
	Success bool           `json:"success"`
	Error   APIErrorDetail `json:"error"`
}

// Client Request DTOs

// StartCrawlRequestDTO represents the client payload to start or resume a crawl.
// Note: allow_local is intentionally omitted to maintain server-side SSRF safety.
type StartCrawlRequestDTO struct {
	URL            string `json:"url"`
	Workers        *int   `json:"workers,omitempty"`
	MaxPages       *int   `json:"max_pages,omitempty"`
	MaxDepth       *int   `json:"max_depth,omitempty"`
	TimeoutSeconds *int   `json:"timeout_seconds,omitempty"`
	SameDomain     *bool  `json:"same_domain,omitempty"`
	Resume         *bool  `json:"resume,omitempty"`
	RetryFailed    *bool  `json:"retry_failed,omitempty"`
	PauseOnStart   *bool  `json:"pause_on_start,omitempty"`
}

// API Response Data DTOs

// CrawlStatusResponseDTO represents the current crawler lifecycle and session status.
type CrawlStatusResponseDTO struct {
	State             string `json:"state"`
	SessionID         string `json:"session_id,omitempty"`
	StartURL          string `json:"start_url,omitempty"`
	WorkerCount       int    `json:"worker_count,omitempty"`
	MaxPages          int    `json:"max_pages,omitempty"`
	MaxDepth          int    `json:"max_depth,omitempty"`
	CumulativeElapsed int64  `json:"cumulative_elapsed_ms"`
	IsResumed         bool   `json:"is_resumed"`
	IsPaused          bool   `json:"is_paused"`
	LastRunError      string `json:"last_run_error,omitempty"`
}

// CrawlStatsResponseDTO represents aggregated URL counts.
type CrawlStatsResponseDTO struct {
	TotalDiscovered int `json:"total_discovered"`
	Pending         int `json:"pending"`
	InProgress      int `json:"in_progress"`
	Completed       int `json:"completed"`
	Failed          int `json:"failed"`
}

// CrawlSessionResponseDTO is returned when a crawl session starts or resumes.
type CrawlSessionResponseDTO struct {
	SessionID          string `json:"session_id"`
	State              string `json:"state"`
	StartURL           string `json:"start_url"`
	WorkerCount        int    `json:"worker_count"`
	MaxPages           int    `json:"max_pages"`
	MaxDepth           int    `json:"max_depth"`
	SameDomain         bool   `json:"same_domain"`
	StorageDir         string `json:"storage_dir"`
	CheckpointDir      string `json:"checkpoint_dir"`
	CheckpointInterval string `json:"checkpoint_interval"`
	IsResumed          bool   `json:"is_resumed"`
}

// ControlActionResponseDTO is returned for lifecycle mutations (pause, resume, stop).
type ControlActionResponseDTO struct {
	State     string `json:"state"`
	SessionID string `json:"session_id,omitempty"`
	Message   string `json:"message"`
}

// CheckpointResponseDTO holds metadata about a created checkpoint.
type CheckpointResponseDTO struct {
	SessionID string `json:"session_id"`
	Sequence  int64  `json:"sequence"`
	Timestamp string `json:"timestamp"`
	Stats     any    `json:"stats"`
}

// URLItemResponseDTO represents safe metadata for an individual URL.
type URLItemResponseDTO struct {
	Sequence     int64     `json:"sequence"`
	URL          string    `json:"url"`
	Normalized   string    `json:"normalized"`
	Status       string    `json:"status"`
	Depth        int       `json:"depth"`
	DiscoveredAt time.Time `json:"discovered_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	StatusCode   int       `json:"status_code,omitempty"`
	ErrorMessage string    `json:"error_message,omitempty"`
	Size         int64     `json:"size,omitempty"`
	DurationMs   int64     `json:"duration_ms,omitempty"`
	SavedPath    string    `json:"saved_path,omitempty"`
}

// URLListResponseDTO represents a paginated and filtered list of URLs.
type URLListResponseDTO struct {
	Total    int                  `json:"total"`
	Filtered int                  `json:"filtered"`
	Limit    int                  `json:"limit"`
	Offset   int                  `json:"offset"`
	Items    []URLItemResponseDTO `json:"items"`
}

// WorkerInfoResponseDTO represents a single worker's state snapshot.
type WorkerInfoResponseDTO struct {
	ID         int       `json:"id"`
	State      string    `json:"state"`
	CurrentURL string    `json:"current_url"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// WorkerListResponseDTO represents worker pool monitoring information.
type WorkerListResponseDTO struct {
	WorkerCount int                     `json:"worker_count"`
	Workers     []WorkerInfoResponseDTO `json:"workers"`
}

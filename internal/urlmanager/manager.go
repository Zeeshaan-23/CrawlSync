package urlmanager

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// URLStatus represents the lifecycle state of a URL during crawling.
type URLStatus string

const (
	StatusPending    URLStatus = "pending"
	StatusInProgress URLStatus = "in_progress"
	StatusCompleted  URLStatus = "completed"
	StatusFailed     URLStatus = "failed"
)

// URLItem holds the tracking metadata for a single discovered URL.
// OS CONCEPT - MONOTONIC SEQUENCE & AUDIT TRAIL:
// Sequence provides a deterministic, monotonically increasing index reflecting
// exact discovery order, compensating for non-deterministic map iteration in Go.
type URLItem struct {
	Sequence     int64     `json:"sequence"`
	URL          string    `json:"url"`
	Normalized   string    `json:"normalized"`
	Status       URLStatus `json:"status"`
	Depth        int       `json:"depth"`
	DiscoveredAt time.Time `json:"discovered_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	StatusCode   int       `json:"status_code,omitempty"`
	ErrorMessage string    `json:"error_message,omitempty"`
	Size         int64     `json:"size,omitempty"`
	DurationMs   int64     `json:"duration_ms,omitempty"`
	SavedPath    string    `json:"saved_path,omitempty"`
}

// Stats represents aggregated crawl status counts.
type Stats struct {
	TotalDiscovered int `json:"total_discovered"`
	Pending         int `json:"pending"`
	InProgress      int `json:"in_progress"`
	Completed       int `json:"completed"`
	Failed          int `json:"failed"`
}

// Snapshot represents a point-in-time, internally consistent copy of all URL records and statistics.
type Snapshot struct {
	Stats        Stats     `json:"stats"`
	URLs         []URLItem `json:"urls"`
	LastSequence int64     `json:"last_sequence"`
	CapturedAt   time.Time `json:"captured_at"`
}

// Manager manages URL tracking, duplicate elimination, and status transitions.
// OS CONCEPT:
// The URL Manager acts as a monitor protecting shared state across concurrent goroutines.
// An internal Mutex guarantees mutual exclusion to prevent race conditions during scheduling
// and state transitions.
type Manager struct {
	mu sync.RWMutex

	// URLs maps normalized URL -> *URLItem
	// CRITICAL SECTION: guarded by mu
	urls map[string]*URLItem

	// nextSequence is a monotonically increasing counter for unique URLs.
	// Starts at 1; guarded by mu.
	nextSequence int64
}

// NewManager creates an empty URL Manager.
func NewManager() *Manager {
	return &Manager{
		urls:         make(map[string]*URLItem),
		nextSequence: 1,
	}
}

// TrySchedule attempts to atomically normalize, check for duplicate, and schedule a URL.
// Returns true if the URL was newly scheduled (status = pending), false if it was already known.
//
// OS CONCEPT - ATOMIC TEST-AND-SET / CRITICAL SECTION:
// Checking if a URL exists, assigning a sequence number, and inserting it happens inside
// a single critical section protected by mu.Lock().
// Duplicates do NOT consume sequence numbers.
func (m *Manager) TrySchedule(rawURL string, depth int) (bool, string, error) {
	normURL, err := NormalizeURL(rawURL)
	if err != nil {
		return false, "", fmt.Errorf("normalization failed: %w", err)
	}

	// CRITICAL SECTION START: Mutual Exclusion
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.urls[normURL]; exists {
		// Duplicate detected: already scheduled or crawled.
		// Sequence counter is NOT incremented.
		return false, normURL, nil
	}

	seq := m.nextSequence
	m.nextSequence++

	now := time.Now()
	m.urls[normURL] = &URLItem{
		Sequence:     seq,
		URL:          rawURL,
		Normalized:   normURL,
		Status:       StatusPending,
		Depth:        depth,
		DiscoveredAt: now,
		UpdatedAt:    now,
	}

	return true, normURL, nil
	// CRITICAL SECTION END
}

// MarkInProgress transitions a URL from pending to in_progress.
func (m *Manager) MarkInProgress(normURL string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	item, exists := m.urls[normURL]
	if !exists {
		return fmt.Errorf("url not found: %s", normURL)
	}
	item.Status = StatusInProgress
	item.UpdatedAt = time.Now()
	return nil
}

// MarkCompleted transitions a URL to completed state and records HTTP status code.
// Rule 1: A URL is marked completed ONLY after its HTML has been written to disk.
func (m *Manager) MarkCompleted(normURL string, statusCode int) error {
	return m.MarkCompletedWithMetadata(normURL, statusCode, 0, 0, "")
}

// MarkCompletedWithMetadata transitions a URL to completed state and records HTTP status code and download metadata.
func (m *Manager) MarkCompletedWithMetadata(normURL string, statusCode int, size int64, duration time.Duration, savedPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	item, exists := m.urls[normURL]
	if !exists {
		return fmt.Errorf("url not found: %s", normURL)
	}
	item.Status = StatusCompleted
	item.StatusCode = statusCode
	item.Size = size
	item.DurationMs = duration.Milliseconds()
	item.SavedPath = savedPath
	item.UpdatedAt = time.Now()
	return nil
}

// MarkFailed transitions a URL to failed state and records error details.
func (m *Manager) MarkFailed(normURL string, statusCode int, err error) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	item, exists := m.urls[normURL]
	if !exists {
		return fmt.Errorf("url not found: %s", normURL)
	}
	item.Status = StatusFailed
	item.StatusCode = statusCode
	if err != nil {
		item.ErrorMessage = err.Error()
	}
	item.UpdatedAt = time.Now()
	return nil
}

// GetStatus returns the current status and metadata of a URL.
func (m *Manager) GetStatus(normURL string) (*URLItem, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	item, exists := m.urls[normURL]
	if !exists {
		return nil, false
	}
	// Return a copy to avoid data race outside lock
	copied := *item
	return &copied, true
}

// GetStats calculates current crawl statistics.
// OS CONCEPT:
// Crawl-completion condition: pending == 0 AND in_progress == 0.
func (m *Manager) GetStats() Stats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var stats Stats
	stats.TotalDiscovered = len(m.urls)

	for _, item := range m.urls {
		switch item.Status {
		case StatusPending:
			stats.Pending++
		case StatusInProgress:
			stats.InProgress++
		case StatusCompleted:
			stats.Completed++
		case StatusFailed:
			stats.Failed++
		}
	}
	return stats
}

// Snapshot captures a consistent, point-in-time deep copy of all URL records,
// statistics, and the current sequence counter under a single read lock.
//
// OS CONCEPT - CONSISTENT GLOBAL STATE / SNAPSHOTTING:
// Capturing statistics and URL items together under the same lock avoids "torn reads"
// where statistics describe state at time T1 while records describe state at time T2.
// The lock is released immediately after copying before any I/O or JSON encoding.
func (m *Manager) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	urlsCopy := make([]URLItem, 0, len(m.urls))
	var stats Stats
	stats.TotalDiscovered = len(m.urls)

	for _, item := range m.urls {
		urlsCopy = append(urlsCopy, *item) // Value copy: item is a struct with non-pointer fields
		switch item.Status {
		case StatusPending:
			stats.Pending++
		case StatusInProgress:
			stats.InProgress++
		case StatusCompleted:
			stats.Completed++
		case StatusFailed:
			stats.Failed++
		}
	}

	// Sort URLs deterministically by sequence number
	sort.Slice(urlsCopy, func(i, j int) bool {
		return urlsCopy[i].Sequence < urlsCopy[j].Sequence
	})

	var lastSeq int64
	if m.nextSequence > 1 {
		lastSeq = m.nextSequence - 1
	}

	return Snapshot{
		Stats:        stats,
		URLs:         urlsCopy,
		LastSequence: lastSeq,
		CapturedAt:   time.Now(),
	}
}

// ResetInProgressToPending resets all in_progress URLs back to pending.
// Used during crash recovery so interrupted downloads get retried safely.
func (m *Manager) ResetInProgressToPending() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	count := 0
	now := time.Now()
	for _, item := range m.urls {
		if item.Status == StatusInProgress {
			item.Status = StatusPending
			item.UpdatedAt = now
			count++
		}
	}
	return count
}

// GetAllItems returns a snapshot of all URLs (safe copy).
func (m *Manager) GetAllItems() []URLItem {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]URLItem, 0, len(m.urls))
	for _, item := range m.urls {
		result = append(result, *item)
	}
	return result
}

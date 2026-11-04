package urlmanager

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

// RestoreOptions configures the restoration policy when recovering from a checkpoint.
type RestoreOptions struct {
	StorageDir  string
	RetryFailed bool
}

// Restore reconstructs URLManager state from persisted URL records.
//
// OS CONCEPT - STATE RECONSTRUCTION & IDEMPOTENT RECOVERY:
// 1. Rebuilds the deduplication set and metadata map.
// 2. Sets nextSequence = max(existing sequence) + 1.
// 3. Demotes in_progress URLs to pending (interrupted jobs).
// 4. Verifies completed URLs have matching saved HTML files on disk; demotes missing/empty files to pending.
// 5. Preserves failed URLs unless RetryFailed is true.
func (m *Manager) Restore(records []URLItem, opts RestoreOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.urls = make(map[string]*URLItem, len(records))
	maxSeq := int64(0)
	now := time.Now()

	for _, rec := range records {
		if rec.Sequence > maxSeq {
			maxSeq = rec.Sequence
		}

		item := rec
		switch item.Status {
		case StatusInProgress:
			// Unfinished or interrupted jobs are demoted to pending for retry
			item.Status = StatusPending
			item.UpdatedAt = now

		case StatusCompleted:
			// Ensure HTML page file actually exists on storage media and cannot escape via symlink
			demote := false
			if opts.StorageDir != "" {
				if item.SavedPath == "" {
					demote = true
				} else {
					normalizedSaved := filepath.FromSlash(strings.ReplaceAll(item.SavedPath, "\\", "/"))
					absStorage, err1 := filepath.Abs(opts.StorageDir)
					absSaved, err2 := filepath.Abs(normalizedSaved)
					if err1 != nil || err2 != nil {
						demote = true
					} else {
						rel, errRel := filepath.Rel(absStorage, absSaved)
						if errRel != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) || filepath.Dir(absSaved) != absStorage {
							demote = true
						} else {
							realStorage, errS := filepath.EvalSymlinks(absStorage)
							if errS != nil {
								realStorage = absStorage
							}
							if realSaved, errSym := filepath.EvalSymlinks(absSaved); errSym == nil {
								relTarget, errTarget := filepath.Rel(realStorage, realSaved)
								if errTarget != nil || strings.HasPrefix(relTarget, "..") || filepath.IsAbs(relTarget) {
									log.Printf("[URLManager SECURITY] SavedPath symlink %s escapes storage dir; demoting to pending", item.SavedPath)
									demote = true
								}
							}
						}
					}
					if !demote {
						fi, err := os.Stat(item.SavedPath)
						if err != nil || fi.Size() == 0 {
							demote = true
						}
					}
				}
			}

			if demote {
				log.Printf("[URLManager NOTICE] Completed page %s missing from storage; demoting to pending", item.Normalized)
				item.Status = StatusPending
				item.SavedPath = ""
				item.Size = 0
				item.DurationMs = 0
				item.StatusCode = 0
				item.UpdatedAt = now
			}

		case StatusFailed:
			if opts.RetryFailed {
				log.Printf("[URLManager NOTICE] Retrying failed URL %s; demoting to pending", item.Normalized)
				item.Status = StatusPending
				item.ErrorMessage = ""
				item.StatusCode = 0
				item.UpdatedAt = now
			}

		case StatusPending:
			// Retain pending
		}

		m.urls[item.Normalized] = &item
	}

	m.nextSequence = maxSeq + 1
	return nil
}

// GetPendingItems returns all URLs currently in pending state, sorted strictly by discovery sequence ascending.
func (m *Manager) GetPendingItems() []URLItem {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var pending []URLItem
	for _, item := range m.urls {
		if item.Status == StatusPending {
			pending = append(pending, *item)
		}
	}

	sort.Slice(pending, func(i, j int) bool {
		return pending[i].Sequence < pending[j].Sequence
	})

	return pending
}

// GetBySequence returns a copy of the URL item with the matching sequence number,
// or false if no record exists with that sequence ID.
//
// OS CONCEPT - SAFE DATA ISOLATION:
// Returns a deep copy of the URLItem struct so callers cannot mutate internal
// state or create race conditions outside the read lock.
func (m *Manager) GetBySequence(seq int64) (*URLItem, bool) {
	if seq <= 0 {
		return nil, false
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, item := range m.urls {
		if item.Sequence == seq {
			copied := *item
			return &copied, true
		}
	}
	return nil, false
}

// Pagination configuration constants
const (
	// DefaultPaginationLimit is the default page size when limit is unspecified.
	DefaultPaginationLimit = 20
	// MaxPaginationLimit is the maximum permissible page size.
	MaxPaginationLimit = 100
	// DefaultPaginationOffset is the default 0-based offset.
	DefaultPaginationOffset = 0
	// MinPaginationOffset is the minimum allowable offset.
	MinPaginationOffset = 0
)

// URLFilterOptions configures filtering, sorting, and pagination for URL queries.
type URLFilterOptions struct {
	Status *URLStatus // Optional status filter
	Depth  *int       // Optional depth filter
	Offset int        // Pagination offset (0-based)
	Limit  int        // Pagination limit (max items returned)
	SortBy string     // Sort criterion: "seq_asc" (default), "seq_desc", "depth_asc", "depth_desc", "time_desc"
}

// GetFilteredURLs retrieves a paginated and filtered list of URL items.
//
// OS CONCEPT - BOUNDED LOCK RETENTION & MEMORY ISOLATION:
// The read lock is held only long enough to evaluate filtering predicates and copy matching
// items. Sorting, pagination slicing, and response formatting are performed outside the lock,
// preventing concurrent worker threads from stalling on lock contention.
func (m *Manager) GetFilteredURLs(opts URLFilterOptions) (items []URLItem, total int, filtered int) {
	// Normalize pagination bounds
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultPaginationLimit
	}
	if limit > MaxPaginationLimit {
		limit = MaxPaginationLimit
	}
	offset := opts.Offset
	if offset < MinPaginationOffset {
		offset = MinPaginationOffset
	}

	m.mu.RLock()
	total = len(m.urls)
	var matching []URLItem

	for _, item := range m.urls {
		if opts.Status != nil && item.Status != *opts.Status {
			continue
		}
		if opts.Depth != nil && item.Depth != *opts.Depth {
			continue
		}
		matching = append(matching, *item)
	}
	m.mu.RUnlock()

	filtered = len(matching)

	// Sort outside the read lock
	switch opts.SortBy {
	case "seq_desc":
		sort.Slice(matching, func(i, j int) bool {
			return matching[i].Sequence > matching[j].Sequence
		})
	case "depth_asc":
		sort.Slice(matching, func(i, j int) bool {
			if matching[i].Depth == matching[j].Depth {
				return matching[i].Sequence < matching[j].Sequence
			}
			return matching[i].Depth < matching[j].Depth
		})
	case "depth_desc":
		sort.Slice(matching, func(i, j int) bool {
			if matching[i].Depth == matching[j].Depth {
				return matching[i].Sequence < matching[j].Sequence
			}
			return matching[i].Depth > matching[j].Depth
		})
	case "time_desc":
		sort.Slice(matching, func(i, j int) bool {
			return matching[i].UpdatedAt.After(matching[j].UpdatedAt)
		})
	default: // "seq_asc"
		sort.Slice(matching, func(i, j int) bool {
			return matching[i].Sequence < matching[j].Sequence
		})
	}

	// Apply pagination window safely
	if offset >= filtered {
		return []URLItem{}, total, filtered
	}

	end := offset + limit
	if end > filtered {
		end = filtered
	}

	paginated := matching[offset:end]
	result := make([]URLItem, len(paginated))
	copy(result, paginated)

	return result, total, filtered
}

package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"crawlsync/internal/fetcher"
	"crawlsync/internal/parser"
)

// WorkerState indicates the current operational phase of a worker goroutine.
type WorkerState string

const (
	WorkerStateIdle        WorkerState = "IDLE"
	WorkerStateDownloading WorkerState = "DOWNLOADING"
)

// WorkerInfo represents the snapshot of a single worker's state for dashboard monitoring.
type WorkerInfo struct {
	ID         int         `json:"id"`
	State      WorkerState `json:"state"`
	CurrentURL string      `json:"current_url"`
	UpdatedAt  time.Time   `json:"updated_at"`
}

// Job encapsulates a unit of crawling work assigned to a worker.
type Job struct {
	URL   string `json:"url"`
	Depth int    `json:"depth"`
}

// CrawlResult encapsulates the output of a worker's fetch and parse phase.
type CrawlResult struct {
	URL             string        `json:"url"`
	Depth           int           `json:"depth"`
	StatusCode      int           `json:"status_code"`
	Size            int64         `json:"size"`
	Duration        time.Duration `json:"duration"`
	SavedPath       string        `json:"saved_path"`
	DiscoveredLinks []string      `json:"discovered_links"`
	Error           error         `json:"error"`
}

// WorkerPool manages a fixed set of worker goroutines.
// OS CONCEPT:
// Thread Pool pattern: Reusable worker threads avoid the overhead of continuous thread creation
// and destruction. The pool bounds CPU and memory consumption to a predictable limit.
type WorkerPool struct {
	workerCount int
	fetcher     *fetcher.Fetcher
	storageDir  string
	sameDomain  bool

	mu      sync.RWMutex
	workers []WorkerInfo
}

// NewWorkerPool initializes the pool structure with default IDLE states.
func NewWorkerPool(count int, f *fetcher.Fetcher, storageDir string, sameDomain bool) *WorkerPool {
	if count < 1 {
		count = 1
	}
	if count > 20 {
		count = 20
	}

	wp := &WorkerPool{
		workerCount: count,
		fetcher:     f,
		storageDir:  storageDir,
		sameDomain:  sameDomain,
		workers:     make([]WorkerInfo, count),
	}

	now := time.Now()
	for i := 0; i < count; i++ {
		wp.workers[i] = WorkerInfo{
			ID:        i + 1,
			State:     WorkerStateIdle,
			UpdatedAt: now,
		}
	}

	return wp
}

// Start launches the worker goroutines.
// Each worker reads jobs from jobsChan and emits CrawlResult to resultsChan.
func (wp *WorkerPool) Start(ctx context.Context, jobsChan <-chan Job, resultsChan chan<- CrawlResult, wg *sync.WaitGroup) {
	for i := 0; i < wp.workerCount; i++ {
		workerID := i + 1
		wg.Add(1)
		go wp.runWorker(ctx, workerID, jobsChan, resultsChan, wg)
	}
}

// runWorker is the continuous execution loop for an individual worker goroutine.
func (wp *WorkerPool) runWorker(
	ctx context.Context,
	workerID int,
	jobsChan <-chan Job,
	resultsChan chan<- CrawlResult,
	wg *sync.WaitGroup,
) {
	// OS CONCEPT: Guaranteed thread termination and resource cleanup.
	// wg.Done() is deferred to execute exactly once when the goroutine exits.
	defer wg.Done()

	// Guarantee that worker monitor state is reset to IDLE upon goroutine exit,
	// preventing stale DOWNLOADING indicators after cancellation or shutdown.
	defer wp.setWorkerState(workerID, WorkerStateIdle, "")

	for {
		// Worker is waiting for assignment
		wp.setWorkerState(workerID, WorkerStateIdle, "")

		select {
		case <-ctx.Done():
			// Context canceled: terminate worker cleanly
			return

		case job, ok := <-jobsChan:
			if !ok {
				// Job channel closed: all work finished, worker terminates
				return
			}

			// Transition state to DOWNLOADING
			wp.setWorkerState(workerID, WorkerStateDownloading, job.URL)

			// Execute fetch, save, and parse
			result := wp.processJob(ctx, workerID, job)

			// Send result to controller via the results channel
			// NOTE: Worker NEVER modifies URL manager directly or touches jobsChan.
			select {
			case resultsChan <- result:
			case <-ctx.Done():
				// Interrupted while sending result; exit cleanly.
				// URL remains in_progress; Phase 6 recovery will return in_progress URLs to pending.
				return
			}
		}
	}
}

// processJob executes network fetch, disk persistence, and link extraction for a single job.
func (wp *WorkerPool) processJob(ctx context.Context, workerID int, job Job) CrawlResult {
	res := CrawlResult{
		URL:   job.URL,
		Depth: job.Depth,
	}

	// 1. Fetch webpage (passes ctx to respect cancellation / timeouts)
	fetchRes := wp.fetcher.Fetch(ctx, job.URL)
	res.StatusCode = fetchRes.StatusCode
	res.Size = fetchRes.Size
	res.Duration = fetchRes.Duration

	if fetchRes.Error != nil {
		res.Error = fetchRes.Error
		return res
	}

	// Check if context was canceled immediately after fetch
	if err := ctx.Err(); err != nil {
		res.Error = err
		return res
	}

	// 2. Persist HTML to disk BEFORE declaring success (Rule 1: Completed only after disk write)
	savedPath, err := wp.savePageToDisk(job.URL, fetchRes.Body)
	if err != nil {
		res.Error = fmt.Errorf("failed saving HTML to disk: %w", err)
		return res
	}
	res.SavedPath = savedPath

	// 3. Extract hyperlinks from downloaded body
	links, err := parser.ExtractLinks(bytes.NewReader(fetchRes.Body), job.URL, parser.ParseOptions{
		SameDomainOnly: wp.sameDomain,
	})
	if err != nil {
		// Non-fatal parse warning: Malformed HTML does not invalidate a successfully downloaded page
		log.Printf("[Worker %d] Non-fatal HTML parsing warning for %s: %v", workerID, job.URL, err)
		res.DiscoveredLinks = nil
	} else {
		res.DiscoveredLinks = links
	}

	return res
}

// setWorkerState safely updates a worker's status under a mutex lock.
// OS CONCEPT: Mutual exclusion for thread status reporting.
func (wp *WorkerPool) setWorkerState(workerID int, state WorkerState, currentURL string) {
	wp.mu.Lock()
	defer wp.mu.Unlock()

	idx := workerID - 1
	if idx >= 0 && idx < len(wp.workers) {
		wp.workers[idx].State = state
		wp.workers[idx].CurrentURL = currentURL
		wp.workers[idx].UpdatedAt = time.Now()
	}
}

// GetWorkerStates returns a thread-safe snapshot of all workers.
func (wp *WorkerPool) GetWorkerStates() []WorkerInfo {
	wp.mu.RLock()
	defer wp.mu.RUnlock()

	snapshot := make([]WorkerInfo, len(wp.workers))
	copy(snapshot, wp.workers)
	return snapshot
}

var invalidFileChars = regexp.MustCompile(`[^a-zA-Z0-9_\-\.]`)

// savePageToDisk saves downloaded HTML content to the designated storage directory.
// Uses a sanitised slug + SHA-256 hash to ensure collision-free, cross-platform valid filenames.
func (wp *WorkerPool) savePageToDisk(pageURL string, body []byte) (string, error) {
	if wp.storageDir == "" {
		wp.storageDir = filepath.Join("data", "pages")
	}

	if err := os.MkdirAll(wp.storageDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create storage directory: %w", err)
	}

	parsed, err := url.Parse(pageURL)
	var slug string
	if err == nil {
		slug = parsed.Host + "_" + strings.Trim(parsed.Path, "/")
	} else {
		slug = "page"
	}

	slug = invalidFileChars.ReplaceAllString(slug, "_")
	if len(slug) > 40 {
		slug = slug[:40]
	}

	// Compute hash of URL for collision resistance
	hash := sha256.Sum256([]byte(pageURL))
	hashHex := hex.EncodeToString(hash[:])[:12]

	filename := fmt.Sprintf("%s_%s.html", slug, hashHex)
	fullPath := filepath.Join(wp.storageDir, filename)

	// OS CONCEPT - ATOMIC FILE WRITING:
	// Prevent partial/corrupted writes if interrupted during I/O.
	// 1. Write content to a temporary file in the same directory.
	// 2. Explicitly flush disk buffers to storage media via Sync().
	// 3. Atomically rename the temporary file to its final destination.
	tmpPattern := fmt.Sprintf("%s_%s-*.tmp", slug, hashHex)
	tmpFile, err := os.CreateTemp(wp.storageDir, tmpPattern)
	if err != nil {
		return "", fmt.Errorf("failed creating temp file for page: %w", err)
	}
	tmpPath := tmpFile.Name()

	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmpFile.Write(body); err != nil {
		_ = tmpFile.Close()
		return "", fmt.Errorf("write error for %s: %w", tmpPath, err)
	}

	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return "", fmt.Errorf("sync error for %s: %w", tmpPath, err)
	}

	if err := tmpFile.Close(); err != nil {
		return "", fmt.Errorf("close error for %s: %w", tmpPath, err)
	}

	// Commit atomically: preserve existing destination if commit fails
	if err := replaceFileWithBackup(tmpPath, fullPath, wp.storageDir); err != nil {
		return "", fmt.Errorf("atomic replacement failed for %s: %w", fullPath, err)
	}

	committed = true
	return fullPath, nil
}

// replaceFileWithBackup atomically replaces targetPath with tmpPath.
// If targetPath already exists, it is backed up to a temporary file in dir.
// If renaming tmpPath to targetPath fails, targetPath is restored from the backup to preserve existing data.
func replaceFileWithBackup(tmpPath, targetPath, dir string) error {
	targetExists := false
	if _, err := os.Stat(targetPath); err == nil {
		targetExists = true
	}

	var bakPath string
	if targetExists {
		bakPattern := fmt.Sprintf("replace-bak-%d-*.tmp", time.Now().UnixNano())
		bakFile, err := os.CreateTemp(dir, bakPattern)
		if err != nil {
			return fmt.Errorf("failed creating backup file: %w", err)
		}
		bakPath = bakFile.Name()
		_ = bakFile.Close()
		_ = os.Remove(bakPath) // Free the filename for rename

		if err := os.Rename(targetPath, bakPath); err != nil {
			return fmt.Errorf("failed backing up existing file %s: %w", targetPath, err)
		}
	}

	if err := os.Rename(tmpPath, targetPath); err != nil {
		// Rollback: restore original file from backup if it existed
		if targetExists {
			_ = os.Rename(bakPath, targetPath)
		}
		return fmt.Errorf("failed committing new file %s: %w", targetPath, err)
	}

	// Success: clean up temporary backup file
	if targetExists {
		_ = os.Remove(bakPath)
	}
	return nil
}

// IsInterruptedError checks whether an error is caused by context cancellation or deadline expiration.
func IsInterruptedError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

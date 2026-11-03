package controller

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"

	"crawlsync/internal/fetcher"
	"crawlsync/internal/state"
	"crawlsync/internal/urlmanager"
)

// Config specifies the runtime configuration for a crawl session.
type Config struct {
	StartURL           string
	WorkerCount        int
	MaxPages           int
	MaxDepth           int
	RequestTimeout     time.Duration
	StorageDir         string
	CheckpointDir      string
	CheckpointInterval time.Duration
	SameDomainOnly     bool
	AllowLocal         bool
}

// DefaultConfig provides sensible defaults for an academic crawling session.
func DefaultConfig(startURL string) Config {
	return Config{
		StartURL:           startURL,
		WorkerCount:        5,
		MaxPages:           50,
		MaxDepth:           3,
		RequestTimeout:     10 * time.Second,
		StorageDir:         filepath.Join("data", "pages"),
		CheckpointDir:      filepath.Join("data", "checkpoints"),
		CheckpointInterval: 30 * time.Second,
		SameDomainOnly:     true,
		AllowLocal:         false,
	}
}

// Controller coordinates the crawling session, acts as the single scheduler,
// manages the worker pool, triggers periodic checkpoints, and detects crawl completion.
//
// OS CONCEPT - SINGLE OWNER SCHEDULER & DEADLOCK AVOIDANCE:
// In classical multithreaded systems, deadlock occurs when producer and consumer mutually
// block on each other's full buffers (Controller blocked sending to full Job channel while
// Workers are blocked sending to full Results channel).
//
// CrawlSync prevents this deadlock using an internal pending slice combined with a dynamic
// select multiplexer. If the jobs channel is full, the controller does not block; it remains
// continuously ready to drain the results channel.
type Controller struct {
	config            Config
	sessionID         string
	urlManager        *urlmanager.Manager
	checkpointManager *state.CheckpointManager
	workerPool        *WorkerPool
	fetcher           *fetcher.Fetcher

	// Synchronization & Goroutine management
	wg sync.WaitGroup
}

// NewController creates and initializes a Controller with its components.
func NewController(cfg Config, mgr *urlmanager.Manager) *Controller {
	if cfg.WorkerCount < 1 {
		cfg.WorkerCount = 1
	}
	if cfg.WorkerCount > 20 {
		cfg.WorkerCount = 20
	}
	if cfg.StorageDir == "" {
		cfg.StorageDir = filepath.Join("data", "pages")
	}
	if cfg.CheckpointDir == "" {
		cfg.CheckpointDir = filepath.Join("data", "checkpoints")
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 10 * time.Second
	}
	if mgr == nil {
		mgr = urlmanager.NewManager()
	}

	f := fetcher.New(fetcher.Config{
		Timeout:     cfg.RequestTimeout,
		AllowLocal:  cfg.AllowLocal,
		MaxBodySize: 5 * 1024 * 1024,
	})

	wp := NewWorkerPool(cfg.WorkerCount, f, cfg.StorageDir, cfg.SameDomainOnly)
	cm := state.NewCheckpointManager(cfg.CheckpointDir)

	return &Controller{
		config:            cfg,
		sessionID:         state.GenerateSessionID(),
		urlManager:        mgr,
		checkpointManager: cm,
		workerPool:        wp,
		fetcher:           f,
	}
}

// Run executes the complete crawl lifecycle from start URL until completion condition is met.
//
// OS CONCEPTS DEMONSTRATED:
// 1. Thread Pool Lifecycle: Worker pool spawned, jobs dispatched, channels drained and closed.
// 2. Preemption & Cancellation: Respects context.Context cancellation signals.
// 3. Periodic Checkpointing & Durability: Checkpoint taken at clean event-loop boundaries.
// 4. Termination Detection: Crawl ends when: (pending in queue == 0) AND (in-flight jobs == 0).
func (c *Controller) Run(ctx context.Context) error {
	// 1. Normalize and schedule initial seed URL
	sched, normStartURL, err := c.urlManager.TrySchedule(c.config.StartURL, 0)
	if err != nil {
		return fmt.Errorf("failed to schedule seed URL: %w", err)
	}
	if !sched {
		// If seed URL was already scheduled or completed (e.g. after restore), proceed
		normStartURL, _ = urlmanager.NormalizeURL(c.config.StartURL)
	}

	// 2. Setup communication channels (Producer-Consumer Buffer)
	jobsChan := make(chan Job, c.config.WorkerCount*2)
	resultsChan := make(chan CrawlResult, c.config.WorkerCount*2)

	// 3. Start worker pool
	c.workerPool.Start(ctx, jobsChan, resultsChan, &c.wg)

	// 4. Setup periodic checkpoint ticker
	var checkpointChan <-chan time.Time
	if c.config.CheckpointInterval > 0 {
		ticker := time.NewTicker(c.config.CheckpointInterval)
		defer ticker.Stop()
		checkpointChan = ticker.C
	}

	// 5. Initialize internal scheduling state
	// Clear separation between:
	// - pendingJobs: URLs queued in controller memory waiting for an open slot in jobsChan
	// - inFlight: URLs dispatched to jobsChan or currently executing on a worker
	var pendingJobs []Job
	pendingJobs = append(pendingJobs, Job{URL: normStartURL, Depth: 0})

	inFlight := 0

	// 6. Main Event Loop
	for {
		// Check completion condition:
		// Both the internal queue must be empty AND no jobs may be in-flight.
		// OS CONCEPT: Accurate termination detection requires zero active consumers and empty buffer.
		if len(pendingJobs) == 0 && inFlight == 0 {
			break
		}

		// Dynamically enable/disable sending into jobsChan in the select statement.
		// If pendingJobs has items, sendChan is jobsChan.
		// If pendingJobs is empty, sendChan is nil (nil channels in Go select are never ready,
		// preventing busy-waiting while keeping resultsChan active).
		var sendChan chan Job
		var nextJob Job
		if len(pendingJobs) > 0 {
			sendChan = jobsChan
			nextJob = pendingJobs[0]
		}

		select {
		case <-ctx.Done():
			// Context canceled (timeout, interruption, or graceful stop)
			close(jobsChan)
			c.wg.Wait()

			// OS CONCEPT - GRACEFUL SHUTDOWN & PERSISTENT CHECKPOINT:
			// After all workers have exited and in-progress jobs have halted,
			// capture final consistent snapshot to disk before terminating.
			_ = c.saveCheckpoint("shutdown")
			return ctx.Err()

		case sendChan <- nextJob:
			// Dispatched job into worker channel
			pendingJobs = pendingJobs[1:]
			inFlight++

			// Mark URL as in_progress immediately upon dispatch into the channel.
			// Check error to ensure accounting consistency between inFlight and URL Manager.
			if err := c.urlManager.MarkInProgress(nextJob.URL); err != nil {
				log.Printf("[Controller WARN] failed transitioning %s to in_progress: %v", nextJob.URL, err)
				// Reconcile inFlight count if status update failed unexpectedly
				inFlight--
			}

		case res := <-resultsChan:
			// Worker returned a result for a completed or failed job
			inFlight--

			if res.Error != nil {
				if IsInterruptedError(res.Error) {
					// Interrupted job: Do NOT mark failed; preserve in_progress status for recovery
					log.Printf("[Controller INFO] URL processing interrupted for %s: %v", res.URL, res.Error)
				} else {
					if err := c.urlManager.MarkFailed(res.URL, res.StatusCode, res.Error); err != nil {
						log.Printf("[Controller WARN] failed transitioning %s to failed: %v", res.URL, err)
					}
				}
			} else {
				// Rule 1: Mark completed only when HTML was successfully saved to disk.
				// Record size, duration, and saved file path metadata.
				if err := c.urlManager.MarkCompletedWithMetadata(res.URL, res.StatusCode, res.Size, res.Duration, res.SavedPath); err != nil {
					log.Printf("[Controller WARN] failed transitioning %s to completed: %v", res.URL, err)
				}

				// Discover and schedule new links
				c.scheduleDiscoveredLinks(&pendingJobs, res.DiscoveredLinks, res.Depth+1)
			}

		case <-checkpointChan:
			// OS CONCEPT - CONSISTENT SNAPSHOT BOUNDARY:
			// Periodic checkpoint fires at the top of the event loop, ensuring no worker result
			// is half-processed (parent completed but children not yet scheduled).
			_ = c.saveCheckpoint("periodic")
		}
	}

	// 7. Graceful Termination Sequence:
	// Exactly one closer for jobsChan
	close(jobsChan)

	// Wait for all workers to notice EOF on jobsChan and terminate their goroutines
	c.wg.Wait()

	// Capture final checkpoint upon natural crawl completion
	_ = c.saveCheckpoint("completion")

	return nil
}

// saveCheckpoint captures a consistent snapshot of the URL Manager and commits it to disk.
// Checkpoint errors are logged but do not crash the crawler.
func (c *Controller) saveCheckpoint(reason string) error {
	snap := c.urlManager.Snapshot()

	checkpointCfg := state.CheckpointConfig{
		StartURL:       c.config.StartURL,
		WorkerCount:    c.config.WorkerCount,
		MaxPages:       c.config.MaxPages,
		MaxDepth:       c.config.MaxDepth,
		RequestTimeout: c.config.RequestTimeout.String(),
		SameDomainOnly: c.config.SameDomainOnly,
	}

	cp, err := c.checkpointManager.Save(c.sessionID, checkpointCfg, snap)
	if err != nil {
		log.Printf("[Checkpoint WARN] Failed saving %s checkpoint: %v", reason, err)
		return err
	}

	log.Printf("[Checkpoint] Successfully saved %s checkpoint #%d (Discovered: %d, Completed: %d, Pending: %d)",
		reason, cp.Sequence, snap.Stats.TotalDiscovered, snap.Stats.Completed, snap.Stats.Pending)
	return nil
}

// CreateCheckpoint manually triggers an immediate checkpoint save.
func (c *Controller) CreateCheckpoint() (*state.Checkpoint, error) {
	snap := c.urlManager.Snapshot()
	checkpointCfg := state.CheckpointConfig{
		StartURL:       c.config.StartURL,
		WorkerCount:    c.config.WorkerCount,
		MaxPages:       c.config.MaxPages,
		MaxDepth:       c.config.MaxDepth,
		RequestTimeout: c.config.RequestTimeout.String(),
		SameDomainOnly: c.config.SameDomainOnly,
	}
	return c.checkpointManager.Save(c.sessionID, checkpointCfg, snap)
}

// scheduleDiscoveredLinks filters and schedules newly extracted hyperlinks.
// Only the Controller modifies the scheduling state; workers only return discovered URLs.
func (c *Controller) scheduleDiscoveredLinks(pendingJobs *[]Job, links []string, nextDepth int) {
	// Respect max depth bound
	if c.config.MaxDepth > 0 && nextDepth > c.config.MaxDepth {
		return
	}

	for _, link := range links {
		// Respect max pages limit if configured (includes seed URL)
		if c.config.MaxPages > 0 {
			stats := c.urlManager.GetStats()
			if stats.TotalDiscovered >= c.config.MaxPages {
				return
			}
		}

		// Atomically attempt to schedule link
		newlyScheduled, normURL, err := c.urlManager.TrySchedule(link, nextDepth)
		if err == nil && newlyScheduled {
			*pendingJobs = append(*pendingJobs, Job{
				URL:   normURL,
				Depth: nextDepth,
			})
		}
	}
}

// SessionID returns the unique session identifier for this crawl session.
func (c *Controller) SessionID() string {
	return c.sessionID
}

// CheckpointManager returns the associated CheckpointManager.
func (c *Controller) CheckpointManager() *state.CheckpointManager {
	return c.checkpointManager
}

// GetWorkerStates returns the live worker monitor data.
func (c *Controller) GetWorkerStates() []WorkerInfo {
	return c.workerPool.GetWorkerStates()
}

// GetStats retrieves URL tracking statistics.
func (c *Controller) GetStats() urlmanager.Stats {
	return c.urlManager.GetStats()
}

// GetURLManager exposes the underlying URL Manager for inspection.
func (c *Controller) GetURLManager() *urlmanager.Manager {
	return c.urlManager
}

package controller

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"

	"crawlsync/internal/fetcher"
	"crawlsync/internal/state"
	"crawlsync/internal/urlmanager"
)

// ControllerState represents the operational status of the crawler.
type ControllerState string

const (
	StateReady   ControllerState = "READY"
	StateRunning ControllerState = "RUNNING"
	StatePaused  ControllerState = "PAUSED"
	StateStopped ControllerState = "STOPPED"
)

// Sentinel lifecycle errors
var (
	ErrAlreadyStarted = errors.New("controller has already been started")
	ErrAlreadyStopped = errors.New("controller has already stopped")
)

type controlCmd int

const (
	cmdPause controlCmd = iota
	cmdResume
	cmdStop
	cmdCheckpoint
)

type checkpointResult struct {
	cp  *state.Checkpoint
	err error
}

type controlRequest struct {
	cmd            controlCmd
	ackChan        chan struct{}
	checkpointChan chan checkpointResult
}

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
// manages the worker pool, triggers periodic checkpoints, handles runtime pause/resume,
// and detects crawl completion.
//
// OS CONCEPT - SINGLE OWNER SCHEDULER & DEADLOCK AVOIDANCE:
// In classical multithreaded systems, deadlock occurs when producer and consumer mutually
// block on each other's full buffers (Controller blocked sending to full Job channel while
// Workers are blocked sending to full Results channel).
//
// CrawlSync prevents this deadlock using an internal pending slice combined with a dynamic
// select multiplexer. If the jobs channel is full or the controller is paused, the controller
// does not block; it remains continuously ready to drain the results channel and handle control requests.
type Controller struct {
	config            Config
	sessionID         string
	urlManager        *urlmanager.Manager
	checkpointManager *state.CheckpointManager
	workerPool        *WorkerPool
	fetcher           *fetcher.Fetcher

	// Synchronization & Goroutine management
	wg sync.WaitGroup

	// Runtime control & state management (Phase 5)
	stateMu     sync.RWMutex
	state       ControllerState
	started     bool
	controlChan chan controlRequest
	doneChan    chan struct{}
	doneOnce    sync.Once
	stopCancel  context.CancelFunc
}

// NewController creates and initializes a Controller with its components.
func NewController(cfg Config, mgr *urlmanager.Manager) (*Controller, error) {
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

	sessID, err := state.GenerateSessionID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate session id: %w", err)
	}

	return &Controller{
		config:            cfg,
		sessionID:         sessID,
		urlManager:        mgr,
		checkpointManager: cm,
		workerPool:        wp,
		fetcher:           f,
		state:             StateReady,
		controlChan:       make(chan controlRequest, 10),
		doneChan:          make(chan struct{}),
	}, nil
}

// Run executes the complete crawl lifecycle from start URL until completion condition is met.
//
// OS CONCEPTS DEMONSTRATED:
// 1. Thread Pool Lifecycle: Worker pool spawned, jobs dispatched, channels drained and closed.
// 2. Preemption & Cancellation: Respects context.Context cancellation signals.
// 3. Runtime Scheduling Control: Dynamic select multiplexing enables pause/resume without busy loops.
// 4. Periodic Checkpointing & Durability: Checkpoint taken at clean event-loop boundaries.
// 5. Termination Detection: Crawl ends when: (pending in queue == 0) AND (in-flight jobs == 0).
func (c *Controller) Run(parentCtx context.Context) error {
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	c.stateMu.Lock()
	if c.started {
		c.stateMu.Unlock()
		return ErrAlreadyStarted
	}
	if c.state == StateStopped {
		c.stateMu.Unlock()
		return ErrAlreadyStopped
	}
	c.started = true
	isInitiallyPaused := (c.state == StatePaused)
	if !isInitiallyPaused {
		c.state = StateRunning
	}
	c.stopCancel = cancel
	c.stateMu.Unlock()

	defer func() {
		c.stateMu.Lock()
		c.state = StateStopped
		c.stateMu.Unlock()
		c.closeDone()
	}()

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
	paused := isInitiallyPaused

	// 6. Main Event Loop
	for {
		// Check completion condition:
		// Both the internal queue must be empty AND no jobs may be in-flight.
		// OS CONCEPT: A paused crawler with pending work does NOT terminate because len(pendingJobs) > 0.
		// Crawl terminates only when genuinely completed with zero pending and zero in-flight tasks.
		if len(pendingJobs) == 0 && inFlight == 0 {
			break
		}

		// Dynamically enable/disable sending into jobsChan in the select statement.
		// If paused, sendChan is nil (disabling dispatch).
		// If running and pendingJobs has items, sendChan is jobsChan.
		// If running and pendingJobs is empty, sendChan is nil.
		var sendChan chan Job
		var nextJob Job
		if !paused && len(pendingJobs) > 0 {
			sendChan = jobsChan
			nextJob = pendingJobs[0]
		}

		select {
		case <-ctx.Done():
			// Context canceled (timeout, interruption, or graceful stop)
			close(jobsChan)
			c.wg.Wait()

			// OS CONCEPT - SHUTDOWN RESULT DRAINING:
			// Workers may have finished downloading and saving pages, sending CrawlResult
			// into the buffered resultsChan right before terminating.
			// Drain all remaining results so successfully saved pages are marked completed,
			// their discovered URLs are recorded, and interrupted jobs remain in_progress.
			for {
				select {
				case res := <-resultsChan:
					inFlight--
					c.handleCrawlResult(res, &pendingJobs, false)
				default:
					goto Drained
				}
			}
		Drained:
			_ = c.saveCheckpoint("shutdown")
			return ctx.Err()

		case req := <-c.controlChan:
			// OS CONCEPT - RUNTIME CONTROL COMMAND MULTIPLEXING:
			// Handle Pause and Resume requests sequentially within the single-owner scheduler loop.
			switch req.cmd {
			case cmdPause:
				if !paused {
					paused = true
					c.stateMu.Lock()
					c.state = StatePaused
					c.stateMu.Unlock()
					log.Println("[Controller] Crawl session paused")
				}
				req.ackChan <- struct{}{}

			case cmdResume:
				if paused {
					paused = false
					c.stateMu.Lock()
					c.state = StateRunning
					c.stateMu.Unlock()
					log.Println("[Controller] Crawl session resumed")
				}
				req.ackChan <- struct{}{}

			case cmdStop:
				if req.ackChan != nil {
					req.ackChan <- struct{}{}
				}
				cancel()

			case cmdCheckpoint:
				cp, err := c.doSaveCheckpoint("manual")
				if req.checkpointChan != nil {
					req.checkpointChan <- checkpointResult{cp: cp, err: err}
				}
			}

		case sendChan <- nextJob:
			// Dispatched job into worker channel
			pendingJobs = pendingJobs[1:]
			inFlight++

			// Mark URL as in_progress immediately upon dispatch into the channel.
			// Check error to ensure accounting consistency between inFlight and URL Manager.
			if err := c.urlManager.MarkInProgress(nextJob.URL); err != nil {
				log.Printf("[Controller WARN] failed transitioning %s to in_progress: %v", nextJob.URL, err)
				inFlight--
			}

		case res := <-resultsChan:
			// Worker returned a result for a completed or failed job.
			// Note: Even while paused, results from already-dispatched workers continue to be processed.
			inFlight--
			c.handleCrawlResult(res, &pendingJobs, true)

		case <-checkpointChan:
			// OS CONCEPT - CONSISTENT SNAPSHOT BOUNDARY:
			// Periodic checkpoint fires at the top of the event loop.
			// Continues to function while paused, capturing any completed in-flight results.
			_ = c.saveCheckpoint("periodic")
		}
	}

	// 7. Graceful Termination Sequence:
	close(jobsChan)

	// Wait for all workers to notice EOF on jobsChan and terminate their goroutines
	c.wg.Wait()

	// Drain any remaining results in channel before saving completion checkpoint
	for {
		select {
		case res := <-resultsChan:
			inFlight--
			c.handleCrawlResult(res, &pendingJobs, false)
		default:
			goto Finished
		}
	}
Finished:
	// Capture final checkpoint upon natural crawl completion
	_ = c.saveCheckpoint("completion")

	return nil
}

// Pause pauses job dispatch without cancelling active workers or dropping state.
// Idempotent: safe to call repeatedly or when stopped.
func (c *Controller) Pause() error {
	c.stateMu.RLock()
	st := c.state
	c.stateMu.RUnlock()

	if st == StateStopped {
		return nil // Harmless no-op
	}
	if st == StatePaused {
		return nil // Already paused (idempotent)
	}
	if st == StateReady {
		c.stateMu.Lock()
		c.state = StatePaused
		c.stateMu.Unlock()
		return nil
	}

	ack := make(chan struct{}, 1)
	req := controlRequest{
		cmd:     cmdPause,
		ackChan: ack,
	}

	select {
	case c.controlChan <- req:
		select {
		case <-ack:
			return nil
		case <-c.doneChan:
			return nil
		}
	case <-c.doneChan:
		return nil
	}
}

// Resume unpauses job dispatch.
// Idempotent: safe to call repeatedly or when stopped.
func (c *Controller) Resume() error {
	c.stateMu.RLock()
	st := c.state
	c.stateMu.RUnlock()

	if st == StateStopped {
		return nil // Harmless no-op
	}
	if st == StateRunning {
		return nil // Already running (idempotent)
	}
	if st == StateReady {
		c.stateMu.Lock()
		c.state = StateRunning
		c.stateMu.Unlock()
		return nil
	}

	ack := make(chan struct{}, 1)
	req := controlRequest{
		cmd:     cmdResume,
		ackChan: ack,
	}

	select {
	case c.controlChan <- req:
		select {
		case <-ack:
			return nil
		case <-c.doneChan:
			return nil
		}
	case <-c.doneChan:
		return nil
	}
}

// Stop initiates a clean graceful shutdown of the controller.
// Idempotent: safe to call repeatedly or when already stopped.
func (c *Controller) Stop() error {
	c.stateMu.Lock()
	if c.state == StateStopped {
		c.stateMu.Unlock()
		return nil
	}
	cancel := c.stopCancel
	if cancel == nil {
		c.state = StateStopped
		c.stateMu.Unlock()
		c.closeDone()
		return nil
	}
	c.stateMu.Unlock()

	cancel()
	<-c.doneChan
	return nil
}

// State returns the current operational state of the controller.
func (c *Controller) State() ControllerState {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.state
}

// IsPaused reports whether the controller is currently paused.
func (c *Controller) IsPaused() bool {
	return c.State() == StatePaused
}

// IsRunning reports whether the controller is actively dispatching jobs.
func (c *Controller) IsRunning() bool {
	return c.State() == StateRunning
}

// IsStopped reports whether the controller has terminated.
func (c *Controller) IsStopped() bool {
	return c.State() == StateStopped
}

func (c *Controller) closeDone() {
	c.doneOnce.Do(func() {
		close(c.doneChan)
	})
}

// handleCrawlResult applies the status transition for a finished worker job.
// If allowNewPending is true, newly discovered links are queued into pendingJobs for worker dispatch.
// If allowNewPending is false (e.g. during shutdown), newly discovered links are scheduled in the
// URLManager as 'pending' so they appear in the checkpoint, but not queued for dispatch.
func (c *Controller) handleCrawlResult(res CrawlResult, pendingJobs *[]Job, allowNewPending bool) {
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
		c.scheduleDiscoveredLinks(pendingJobs, res.DiscoveredLinks, res.Depth+1, allowNewPending)
	}
}

// saveCheckpoint captures a consistent snapshot of the URL Manager and commits it to disk.
// Checkpoint errors are logged but do not crash the crawler.
func (c *Controller) saveCheckpoint(reason string) error {
	_, err := c.doSaveCheckpoint(reason)
	return err
}

func (c *Controller) doSaveCheckpoint(reason string) (*state.Checkpoint, error) {
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
		return nil, err
	}

	log.Printf("[Checkpoint] Successfully saved %s checkpoint #%d (Discovered: %d, Completed: %d, Pending: %d)",
		reason, cp.Sequence, snap.Stats.TotalDiscovered, snap.Stats.Completed, snap.Stats.Pending)
	return cp, nil
}

// CreateCheckpoint manually triggers an immediate checkpoint save.
// OS CONCEPT - ATOMIC SNAPSHOT CONSISTENCY:
// When the controller is running or paused, the request is multiplexed through the
// event loop to ensure it executes at a clean boundary where no partial result processing
// (e.g. parent marked completed but discovered children not yet scheduled) is underway.
func (c *Controller) CreateCheckpoint() (*state.Checkpoint, error) {
	c.stateMu.RLock()
	st := c.state
	c.stateMu.RUnlock()

	// If controller is ready or stopped, no worker or event-loop interleaving can occur.
	if st == StateReady || st == StateStopped {
		return c.doSaveCheckpoint("manual")
	}

	cpChan := make(chan checkpointResult, 1)
	req := controlRequest{
		cmd:            cmdCheckpoint,
		checkpointChan: cpChan,
	}

	select {
	case c.controlChan <- req:
		select {
		case res := <-cpChan:
			return res.cp, res.err
		case <-c.doneChan:
			return c.doSaveCheckpoint("manual")
		}
	case <-c.doneChan:
		return c.doSaveCheckpoint("manual")
	}
}

// scheduleDiscoveredLinks filters and schedules newly extracted hyperlinks.
// Only the Controller modifies the scheduling state; workers only return discovered URLs.
func (c *Controller) scheduleDiscoveredLinks(pendingJobs *[]Job, links []string, nextDepth int, allowNewPending bool) {
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
		if err == nil && newlyScheduled && allowNewPending && pendingJobs != nil {
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

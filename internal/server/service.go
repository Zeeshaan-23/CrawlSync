package server

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"crawlsync/internal/controller"
	"crawlsync/internal/state"
	"crawlsync/internal/urlmanager"
)

// LifecycleState represents the operational state of the CrawlerService.
type LifecycleState string

const (
	StateIdle      LifecycleState = "IDLE"
	StateRunning   LifecycleState = "RUNNING"
	StatePaused    LifecycleState = "PAUSED"
	StateStopping  LifecycleState = "STOPPING"
	StateStopped   LifecycleState = "STOPPED"
	StateCompleted LifecycleState = "COMPLETED"
)

// Service-level sentinel errors for mapping to appropriate HTTP statuses.
var (
	ErrCrawlAlreadyRunning        = errors.New("a crawl session is already running or paused")
	ErrCrawlStopping              = errors.New("crawl session is currently stopping")
	ErrPreviousSessionTerminating = errors.New("previous crawl session is still terminating")
	ErrNoActiveCrawl              = errors.New("no active crawl session exists")
	ErrInvalidStateTransition     = errors.New("invalid lifecycle state transition")
	ErrInvalidConfiguration       = errors.New("invalid crawl configuration")
	ErrURLNotFound                = errors.New("url record not found")
)

// ServiceOptions configures the defaults and operator-controlled permissions for the CrawlerService.
type ServiceOptions struct {
	DefaultStorageDir         string
	DefaultCheckpointDir      string
	DefaultWorkerCount        int
	DefaultCheckpointInterval time.Duration
	AllowLocal                bool // Operator-controlled: controls access to localhost/private networks (SSRF defense)
}

// StartCrawlRequest contains client parameters to start or resume a crawl.
// Note: AllowLocal is deliberately omitted to prevent client-side SSRF bypass.
type StartCrawlRequest struct {
	URL                string        `json:"url"`
	Workers            int           `json:"workers,omitempty"`
	MaxPages           int           `json:"max_pages,omitempty"`
	MaxDepth           int           `json:"max_depth,omitempty"`
	TimeoutSeconds     int           `json:"timeout_seconds,omitempty"`
	SameDomain         *bool         `json:"same_domain,omitempty"` // Defaults to true if nil
	StorageDir         string        `json:"storage_dir,omitempty"`
	CheckpointDir      string        `json:"checkpoint_dir,omitempty"`
	CheckpointInterval time.Duration `json:"checkpoint_interval,omitempty"`

	// Resumption options
	Resume       bool `json:"resume,omitempty"`
	RetryFailed  bool `json:"retry_failed,omitempty"`
	PauseOnStart bool `json:"pause_on_start,omitempty"`
}

// CrawlStatusInfo encapsulates point-in-time status metadata for the active or most recent session.
type CrawlStatusInfo struct {
	State             LifecycleState `json:"state"`
	SessionID         string         `json:"session_id,omitempty"`
	StartURL          string         `json:"start_url,omitempty"`
	WorkerCount       int            `json:"worker_count,omitempty"`
	MaxPages          int            `json:"max_pages,omitempty"`
	MaxDepth          int            `json:"max_depth,omitempty"`
	CumulativeElapsed time.Duration  `json:"cumulative_elapsed"`
	IsResumed         bool           `json:"is_resumed"`
	IsPaused          bool           `json:"is_paused"`
	LastRunError      string         `json:"last_run_error,omitempty"`
}

// CrawlSessionInfo holds summary details returned upon session initialization.
type CrawlSessionInfo struct {
	SessionID          string         `json:"session_id"`
	State              LifecycleState `json:"state"`
	StartURL           string         `json:"start_url"`
	WorkerCount        int            `json:"worker_count"`
	MaxPages           int            `json:"max_pages"`
	MaxDepth           int            `json:"max_depth"`
	SameDomain         bool           `json:"same_domain"`
	StorageDir         string         `json:"storage_dir"`
	CheckpointDir      string         `json:"checkpoint_dir"`
	CheckpointInterval time.Duration  `json:"checkpoint_interval"`
	IsResumed          bool           `json:"is_resumed"`
}

// CrawlerService orchestrates CrawlSync sessions, managing the active Controller and URL Manager
// behind a strictly synchronized, deadlock-free state machine.
//
// OS CONCEPT - CONCURRENCY ISOLATION & MONITOR PATTERN:
// The service acts as an application-level monitor. The service mutex protects the lifecycle
// state machine, active session pointers, and generation IDs. Mutexes are NEVER held during
// blocking Controller calls (e.g. Stop, CreateCheckpoint, or Run wait) to prevent lock inversion.
type CrawlerService struct {
	options ServiceOptions

	mu               sync.RWMutex
	state            LifecycleState
	activeSessionID  string
	activeConfig     controller.Config
	activeController *controller.Controller
	activeManager    *urlmanager.Manager
	sessionDoneChan  chan struct{}
	lastRunError     error
	sessionCancel    context.CancelFunc
}

// NewCrawlerService initializes a new service with the provided operator options.
func NewCrawlerService(opts ServiceOptions) *CrawlerService {
	if opts.DefaultStorageDir == "" {
		opts.DefaultStorageDir = filepath.Join("data", "pages")
	}
	if opts.DefaultCheckpointDir == "" {
		opts.DefaultCheckpointDir = filepath.Join("data", "checkpoints")
	}

	return &CrawlerService{
		options: opts,
		state:   StateIdle,
	}
}

// Options returns a copy of the service operator configuration.
func (s *CrawlerService) Options() ServiceOptions {
	return s.options
}

// StartCrawl initiates a new crawl session or resumes from a checkpoint.
//
// CONCURRENCY CONTRACT:
// Rejects concurrent starts with ErrCrawlAlreadyRunning or ErrCrawlStopping.
// Only transitions to StateRunning if the previous session has fully terminated.
func (s *CrawlerService) StartCrawl(req StartCrawlRequest) (*CrawlSessionInfo, error) {
	return s.startSessionInternal(req, req.PauseOnStart)
}

// RestoreSession restores a crawl session from a checkpoint into the service.
// When pauseOnStart is true, the controller is initialized in StatePaused, allowing
// the operator/dashboard to inspect restored URLs before issuing Resume.
func (s *CrawlerService) RestoreSession(req StartCrawlRequest, pauseOnStart bool) (*CrawlSessionInfo, error) {
	req.Resume = true
	return s.startSessionInternal(req, pauseOnStart)
}

func (s *CrawlerService) startSessionInternal(req StartCrawlRequest, pauseOnStart bool) (*CrawlSessionInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. Validate lifecycle state machine
	if s.state == StateStopping {
		return nil, ErrCrawlStopping
	}
	if s.state == StateRunning || s.state == StatePaused {
		return nil, ErrCrawlAlreadyRunning
	}

	// 2. Ensure previous session goroutine has fully exited
	if s.sessionDoneChan != nil {
		select {
		case <-s.sessionDoneChan:
			// Fully terminated
		default:
			return nil, ErrPreviousSessionTerminating
		}
	}

	// 3. Resolve storage & checkpoint directories
	storageDir := req.StorageDir
	if storageDir == "" {
		storageDir = s.options.DefaultStorageDir
	}
	checkpointDir := req.CheckpointDir
	if checkpointDir == "" {
		checkpointDir = s.options.DefaultCheckpointDir
	}

	var (
		mgr        *urlmanager.Manager
		ctrl       *controller.Controller
		sessID     string
		startURL   string
		isResumed  bool
		cumElapsed time.Duration
		initSeq    int64
		cfg        controller.Config
	)

	sameDomain := true
	if req.SameDomain != nil {
		sameDomain = *req.SameDomain
	}

	workerCount := req.Workers
	if workerCount < 1 {
		if s.options.DefaultWorkerCount > 0 {
			workerCount = s.options.DefaultWorkerCount
		} else {
			workerCount = 4
		}
	}
	if workerCount > 20 {
		workerCount = 20
	}

	maxPages := req.MaxPages
	if maxPages <= 0 {
		maxPages = 20
	}

	maxDepth := req.MaxDepth
	if maxDepth < 0 {
		maxDepth = 2
	}

	timeout := 10 * time.Second
	if req.TimeoutSeconds > 0 {
		timeout = time.Duration(req.TimeoutSeconds) * time.Second
	}

	cpInterval := req.CheckpointInterval
	if cpInterval <= 0 {
		if s.options.DefaultCheckpointInterval > 0 {
			cpInterval = s.options.DefaultCheckpointInterval
		} else {
			cpInterval = 30 * time.Second
		}
	}

	// 4. Branch: Resume session vs. New session
	if req.Resume {
		cp, err := state.LoadWithFallback(checkpointDir, state.RecoveryValidationOptions{
			AllowLocal: s.options.AllowLocal, // Operator controlled
			StorageDir: storageDir,
		})
		if err != nil {
			return nil, fmt.Errorf("recovery failed: %w", err)
		}

		sessID = cp.SessionID
		startURL = cp.Config.StartURL
		isResumed = true
		cumElapsed = cp.CumulativeElapsed
		initSeq = cp.Sequence

		if req.Workers <= 0 && cp.Config.WorkerCount > 0 {
			workerCount = cp.Config.WorkerCount
		}
		if req.MaxPages <= 0 && cp.Config.MaxPages > 0 {
			maxPages = cp.Config.MaxPages
		}
		if req.MaxDepth <= 0 && cp.Config.MaxDepth > 0 {
			maxDepth = cp.Config.MaxDepth
		}
		if req.SameDomain == nil {
			sameDomain = cp.Config.SameDomainOnly
		}

		mgr = urlmanager.NewManager()
		if err := mgr.Restore(cp.URLs, urlmanager.RestoreOptions{
			StorageDir:  storageDir,
			RetryFailed: req.RetryFailed,
		}); err != nil {
			return nil, fmt.Errorf("state restoration failed: %w", err)
		}
	} else {
		// New crawl validation
		if strings.TrimSpace(req.URL) == "" {
			return nil, fmt.Errorf("%w: url is required", ErrInvalidConfiguration)
		}
		parsedURL, err := url.Parse(req.URL)
		if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
			return nil, fmt.Errorf("%w: url must have valid http or https scheme", ErrInvalidConfiguration)
		}
		startURL = req.URL
		mgr = urlmanager.NewManager()
	}

	cfg = controller.Config{
		StartURL:           startURL,
		WorkerCount:        workerCount,
		MaxPages:           maxPages,
		MaxDepth:           maxDepth,
		RequestTimeout:     timeout,
		StorageDir:         storageDir,
		CheckpointDir:      checkpointDir,
		CheckpointInterval: cpInterval,
		SameDomainOnly:     sameDomain,
		AllowLocal:         s.options.AllowLocal, // Operator controlled
		SessionID:          sessID,
		CumulativeElapsed:  cumElapsed,
		InitialSequence:    initSeq,
		IsResumed:          isResumed,
	}

	var err error
	ctrl, err = controller.NewController(cfg, mgr)
	if err != nil {
		return nil, fmt.Errorf("failed creating controller: %w", err)
	}

	// 5. Update service state
	if pauseOnStart {
		_ = ctrl.Pause()
		s.state = StatePaused
	} else {
		s.state = StateRunning
	}

	s.activeController = ctrl
	s.activeManager = mgr
	s.activeConfig = cfg
	s.activeSessionID = ctrl.SessionID()
	s.lastRunError = nil

	runCtx, cancel := context.WithCancel(context.Background())
	s.sessionCancel = cancel

	doneChan := make(chan struct{})
	s.sessionDoneChan = doneChan

	// 6. Launch asynchronous session supervisor
	go s.superviseSession(ctrl, runCtx, ctrl.SessionID(), doneChan)

	infoState := s.state
	info := &CrawlSessionInfo{
		SessionID:          ctrl.SessionID(),
		State:              infoState,
		StartURL:           cfg.StartURL,
		WorkerCount:        cfg.WorkerCount,
		MaxPages:           cfg.MaxPages,
		MaxDepth:           cfg.MaxDepth,
		SameDomain:         cfg.SameDomainOnly,
		StorageDir:         cfg.StorageDir,
		CheckpointDir:      cfg.CheckpointDir,
		CheckpointInterval: cfg.CheckpointInterval,
		IsResumed:          cfg.IsResumed,
	}

	return info, nil
}

// superviseSession runs the Controller event loop in the background and safely records termination.
func (s *CrawlerService) superviseSession(ctrl *controller.Controller, ctx context.Context, sessID string, doneChan chan struct{}) {
	runErr := ctrl.Run(ctx)

	s.mu.Lock()
	defer s.mu.Unlock()
	defer close(doneChan)

	// Generation check: only update state if this session is still the active session
	if s.activeSessionID != sessID {
		return
	}

	s.lastRunError = runErr
	if s.state == StateStopping {
		s.state = StateStopped
	} else if s.state == StateRunning || s.state == StatePaused {
		s.state = StateCompleted
	}
}

// PauseCrawl pauses worker dispatch.
//
// DEADLOCK PREVENTION:
// Mutex is acquired to validate state, released before calling ctrl.Pause(), and reacquired.
func (s *CrawlerService) PauseCrawl() error {
	s.mu.Lock()
	if s.state == StatePaused {
		s.mu.Unlock()
		return nil // Idempotent
	}
	if s.state != StateRunning {
		st := s.state
		s.mu.Unlock()
		if st == StateIdle || st == StateStopped || st == StateCompleted {
			return ErrNoActiveCrawl
		}
		return fmt.Errorf("%w: cannot pause crawl in state %s", ErrInvalidStateTransition, st)
	}
	ctrl := s.activeController
	s.mu.Unlock()

	// Call blocking / synchronized controller method outside service mutex
	if err := ctrl.Pause(); err != nil {
		return err
	}

	s.mu.Lock()
	if s.state == StateRunning {
		s.state = StatePaused
	}
	s.mu.Unlock()
	return nil
}

// ResumeCrawl unpauses worker dispatch.
//
// DEADLOCK PREVENTION:
// Mutex is acquired to validate state, released before calling ctrl.Resume(), and reacquired.
func (s *CrawlerService) ResumeCrawl() error {
	s.mu.Lock()
	if s.state == StateRunning {
		s.mu.Unlock()
		return nil // Idempotent
	}
	if s.state != StatePaused {
		st := s.state
		s.mu.Unlock()
		if st == StateIdle || st == StateStopped || st == StateCompleted {
			return ErrNoActiveCrawl
		}
		return fmt.Errorf("%w: cannot resume crawl in state %s", ErrInvalidStateTransition, st)
	}
	ctrl := s.activeController
	s.mu.Unlock()

	// Call controller method outside service mutex
	if err := ctrl.Resume(); err != nil {
		return err
	}

	s.mu.Lock()
	if s.state == StatePaused {
		s.state = StateRunning
	}
	s.mu.Unlock()
	return nil
}

// StopCrawl initiates graceful termination of the active crawl session and blocks until completion.
//
// DEADLOCK PREVENTION:
// 1. Acquires lock, transitions state to StateStopping, captures doneChan and ctrl.
// 2. Releases lock BEFORE calling ctrl.Stop() and BEFORE waiting on doneChan.
// 3. Allows the completion supervisor goroutine to acquire the lock and mark StateStopped.
func (s *CrawlerService) StopCrawl() error {
	s.mu.Lock()
	switch s.state {
	case StateIdle, StateStopped, StateCompleted:
		s.mu.Unlock()
		return nil // Idempotent
	case StateStopping:
		doneChan := s.sessionDoneChan
		s.mu.Unlock()
		if doneChan != nil {
			<-doneChan
		}
		return nil
	case StateRunning, StatePaused:
		s.state = StateStopping
		ctrl := s.activeController
		doneChan := s.sessionDoneChan
		s.mu.Unlock()

		if ctrl != nil {
			_ = ctrl.Stop()
		}

		if doneChan != nil {
			<-doneChan
		}
		return nil
	default:
		st := s.state
		s.mu.Unlock()
		return fmt.Errorf("%w: cannot stop crawl in state %s", ErrInvalidStateTransition, st)
	}
}

// CreateCheckpoint manually triggers an immediate atomic checkpoint save.
//
// DEADLOCK PREVENTION:
// Releases service mutex before calling ctrl.CreateCheckpoint(), which waits on the controller's
// event loop channel.
func (s *CrawlerService) CreateCheckpoint() (*state.Checkpoint, error) {
	s.mu.RLock()
	ctrl := s.activeController
	st := s.state
	s.mu.RUnlock()

	if ctrl == nil || st == StateIdle {
		return nil, ErrNoActiveCrawl
	}
	if st == StateStopping {
		return nil, ErrCrawlStopping
	}

	return ctrl.CreateCheckpoint()
}

// GetStatus returns point-in-time lifecycle status metadata.
func (s *CrawlerService) GetStatus() CrawlStatusInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	info := CrawlStatusInfo{
		State:       s.state,
		SessionID:   s.activeSessionID,
		StartURL:    s.activeConfig.StartURL,
		WorkerCount: s.activeConfig.WorkerCount,
		MaxPages:    s.activeConfig.MaxPages,
		MaxDepth:    s.activeConfig.MaxDepth,
		IsPaused:    s.state == StatePaused,
		IsResumed:   s.activeConfig.IsResumed,
	}

	if s.lastRunError != nil {
		info.LastRunError = s.lastRunError.Error()
	}

	if s.activeController != nil {
		info.CumulativeElapsed = s.activeController.CumulativeElapsed()
	}

	return info
}

// GetStats returns the current URL tracking statistics.
func (s *CrawlerService) GetStats() (urlmanager.Stats, error) {
	s.mu.RLock()
	mgr := s.activeManager
	s.mu.RUnlock()

	if mgr == nil {
		return urlmanager.Stats{}, ErrNoActiveCrawl
	}
	return mgr.GetStats(), nil
}

// GetURLs returns a paginated and filtered slice of URL items.
func (s *CrawlerService) GetURLs(opts urlmanager.URLFilterOptions) ([]urlmanager.URLItem, int, int, error) {
	s.mu.RLock()
	mgr := s.activeManager
	s.mu.RUnlock()

	if mgr == nil {
		return nil, 0, 0, ErrNoActiveCrawl
	}
	items, total, filtered := mgr.GetFilteredURLs(opts)
	return items, total, filtered, nil
}

// GetURLBySequence retrieves a specific URL record by its unique sequence ID.
func (s *CrawlerService) GetURLBySequence(seq int64) (*urlmanager.URLItem, error) {
	s.mu.RLock()
	mgr := s.activeManager
	s.mu.RUnlock()

	if mgr == nil {
		return nil, ErrNoActiveCrawl
	}

	item, ok := mgr.GetBySequence(seq)
	if !ok || item == nil {
		return nil, ErrURLNotFound
	}
	return item, nil
}

// GetWorkers returns live monitoring states for worker goroutines.
func (s *CrawlerService) GetWorkers() ([]controller.WorkerInfo, error) {
	s.mu.RLock()
	ctrl := s.activeController
	s.mu.RUnlock()

	if ctrl == nil {
		return nil, ErrNoActiveCrawl
	}
	return ctrl.GetWorkerStates(), nil
}

// GetSessionInfo returns the summary parameters of the active or latest session.
func (s *CrawlerService) GetSessionInfo() (*CrawlSessionInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.activeController == nil {
		return nil, ErrNoActiveCrawl
	}

	return &CrawlSessionInfo{
		SessionID:          s.activeSessionID,
		State:              s.state,
		StartURL:           s.activeConfig.StartURL,
		WorkerCount:        s.activeConfig.WorkerCount,
		MaxPages:           s.activeConfig.MaxPages,
		MaxDepth:           s.activeConfig.MaxDepth,
		SameDomain:         s.activeConfig.SameDomainOnly,
		StorageDir:         s.activeConfig.StorageDir,
		CheckpointDir:      s.activeConfig.CheckpointDir,
		CheckpointInterval: s.activeConfig.CheckpointInterval,
		IsResumed:          s.activeConfig.IsResumed,
	}, nil
}

// Shutdown gracefully stops any active crawl session.
func (s *CrawlerService) Shutdown(ctx context.Context) error {
	stopDone := make(chan error, 1)
	go func() {
		stopDone <- s.StopCrawl()
	}()

	select {
	case err := <-stopDone:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

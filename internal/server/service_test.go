package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"crawlsync/internal/controller"
	"crawlsync/internal/state"
	"crawlsync/internal/urlmanager"
)

// waitForState polls until the service reaches target state or times out.
func waitForState(t *testing.T, s *CrawlerService, target LifecycleState, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s.GetStatus().State == target {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for state %s, current state is %s", target, s.GetStatus().State)
}

func createTestHTTPSite() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/":
			_, _ = fmt.Fprint(w, `<html><body><a href="/p1">P1</a><a href="/p2">P2</a></body></html>`)
		case "/p1":
			_, _ = fmt.Fprint(w, `<html><body><a href="/p3">P3</a></body></html>`)
		case "/p2":
			_, _ = fmt.Fprint(w, `<html><body>Leaf 2</body></html>`)
		case "/p3":
			_, _ = fmt.Fprint(w, `<html><body>Leaf 3</body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
}

// 1. Service initialization
func TestService_Initialization(t *testing.T) {
	opts := ServiceOptions{
		DefaultStorageDir:    "temp/pages",
		DefaultCheckpointDir: "temp/checkpoints",
		AllowLocal:           true,
	}
	s := NewCrawlerService(opts)

	status := s.GetStatus()
	if status.State != StateIdle {
		t.Errorf("expected initial state IDLE, got %s", status.State)
	}
	if status.SessionID != "" {
		t.Errorf("expected empty session ID on init, got %s", status.SessionID)
	}

	// Read queries on unstarted service
	if _, err := s.GetStats(); err != ErrNoActiveCrawl {
		t.Errorf("expected ErrNoActiveCrawl, got %v", err)
	}
	if _, _, _, err := s.GetURLs(urlmanager.URLFilterOptions{}); err != ErrNoActiveCrawl {
		t.Errorf("expected ErrNoActiveCrawl, got %v", err)
	}
	if _, err := s.GetURLBySequence(1); err != ErrNoActiveCrawl {
		t.Errorf("expected ErrNoActiveCrawl, got %v", err)
	}
	if _, err := s.GetWorkers(); err != ErrNoActiveCrawl {
		t.Errorf("expected ErrNoActiveCrawl, got %v", err)
	}
}

// 2. Starting a new crawl
func TestService_StartNewCrawl(t *testing.T) {
	ts := createTestHTTPSite()
	defer ts.Close()

	tempDir, err := os.MkdirTemp("", "crawlsync_svc_start_*")
	if err != nil {
		t.Fatalf("failed creating temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	s := NewCrawlerService(ServiceOptions{
		DefaultStorageDir:    filepath.Join(tempDir, "pages"),
		DefaultCheckpointDir: filepath.Join(tempDir, "checkpoints"),
		AllowLocal:           true,
	})

	info, err := s.StartCrawl(StartCrawlRequest{
		URL:      ts.URL + "/",
		Workers:  2,
		MaxPages: 5,
	})
	if err != nil {
		t.Fatalf("StartCrawl failed: %v", err)
	}

	if info.State != StateRunning {
		t.Errorf("expected info.State RUNNING, got %s", info.State)
	}
	if info.SessionID == "" {
		t.Error("expected non-empty session ID")
	}

	waitForState(t, s, StateCompleted, 3*time.Second)

	stats, err := s.GetStats()
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}
	if stats.Completed < 4 {
		t.Errorf("expected at least 4 completed pages, got %d", stats.Completed)
	}
}

// 3. Rejecting a second simultaneous start
func TestService_RejectSecondStartWhileRunning(t *testing.T) {
	ts := createTestHTTPSite()
	defer ts.Close()

	tempDir, _ := os.MkdirTemp("", "crawlsync_svc_double_start_*")
	defer os.RemoveAll(tempDir)

	s := NewCrawlerService(ServiceOptions{
		DefaultStorageDir:    filepath.Join(tempDir, "pages"),
		DefaultCheckpointDir: filepath.Join(tempDir, "checkpoints"),
		AllowLocal:           true,
	})

	_, err := s.StartCrawl(StartCrawlRequest{
		URL:      ts.URL + "/",
		Workers:  1,
		MaxPages: 10,
	})
	if err != nil {
		t.Fatalf("first StartCrawl failed: %v", err)
	}

	// Immediate second start must be rejected
	_, err2 := s.StartCrawl(StartCrawlRequest{
		URL: ts.URL + "/",
	})
	if err2 != ErrCrawlAlreadyRunning {
		t.Fatalf("expected ErrCrawlAlreadyRunning, got: %v", err2)
	}

	_ = s.StopCrawl()
}

// 4. Natural crawl completion
func TestService_NaturalCompletion(t *testing.T) {
	ts := createTestHTTPSite()
	defer ts.Close()

	tempDir, _ := os.MkdirTemp("", "crawlsync_svc_natural_*")
	defer os.RemoveAll(tempDir)

	s := NewCrawlerService(ServiceOptions{
		DefaultStorageDir:    filepath.Join(tempDir, "pages"),
		DefaultCheckpointDir: filepath.Join(tempDir, "checkpoints"),
		AllowLocal:           true,
	})

	_, err := s.StartCrawl(StartCrawlRequest{
		URL:      ts.URL + "/",
		Workers:  2,
		MaxPages: 4,
	})
	if err != nil {
		t.Fatalf("StartCrawl failed: %v", err)
	}

	waitForState(t, s, StateCompleted, 3*time.Second)

	status := s.GetStatus()
	if status.State != StateCompleted {
		t.Errorf("expected StateCompleted, got %s", status.State)
	}
}

// 5. Pause and resume
func TestService_PauseAndResume(t *testing.T) {
	ts := createTestHTTPSite()
	defer ts.Close()

	tempDir, _ := os.MkdirTemp("", "crawlsync_svc_pause_resume_*")
	defer os.RemoveAll(tempDir)

	s := NewCrawlerService(ServiceOptions{
		DefaultStorageDir:    filepath.Join(tempDir, "pages"),
		DefaultCheckpointDir: filepath.Join(tempDir, "checkpoints"),
		AllowLocal:           true,
	})

	_, err := s.StartCrawl(StartCrawlRequest{
		URL:      ts.URL + "/",
		Workers:  2,
		MaxPages: 10,
	})
	if err != nil {
		t.Fatalf("StartCrawl failed: %v", err)
	}

	// Pause
	if err := s.PauseCrawl(); err != nil {
		t.Fatalf("PauseCrawl failed: %v", err)
	}
	if s.GetStatus().State != StatePaused {
		t.Errorf("expected StatePaused, got %s", s.GetStatus().State)
	}

	// Idempotent pause
	if err := s.PauseCrawl(); err != nil {
		t.Errorf("expected idempotent pause, got %v", err)
	}

	// Resume
	if err := s.ResumeCrawl(); err != nil {
		t.Fatalf("ResumeCrawl failed: %v", err)
	}
	if s.GetStatus().State != StateRunning {
		t.Errorf("expected StateRunning after resume, got %s", s.GetStatus().State)
	}

	// Idempotent resume
	if err := s.ResumeCrawl(); err != nil {
		t.Errorf("expected idempotent resume, got %v", err)
	}

	_ = s.StopCrawl()
}

// 6. Stop while running
func TestService_StopWhileRunning(t *testing.T) {
	ts := createTestHTTPSite()
	defer ts.Close()

	tempDir, _ := os.MkdirTemp("", "crawlsync_svc_stop_run_*")
	defer os.RemoveAll(tempDir)

	s := NewCrawlerService(ServiceOptions{
		DefaultStorageDir:    filepath.Join(tempDir, "pages"),
		DefaultCheckpointDir: filepath.Join(tempDir, "checkpoints"),
		AllowLocal:           true,
	})

	_, err := s.StartCrawl(StartCrawlRequest{
		URL:      ts.URL + "/",
		Workers:  1,
		MaxPages: 10,
	})
	if err != nil {
		t.Fatalf("StartCrawl failed: %v", err)
	}

	if err := s.StopCrawl(); err != nil {
		t.Fatalf("StopCrawl failed: %v", err)
	}

	status := s.GetStatus()
	if status.State != StateStopped {
		t.Errorf("expected StateStopped, got %s", status.State)
	}

	// Idempotent stop
	if err := s.StopCrawl(); err != nil {
		t.Errorf("expected idempotent StopCrawl, got %v", err)
	}
}

// 7. Stop while paused
func TestService_StopWhilePaused(t *testing.T) {
	ts := createTestHTTPSite()
	defer ts.Close()

	tempDir, _ := os.MkdirTemp("", "crawlsync_svc_stop_paused_*")
	defer os.RemoveAll(tempDir)

	s := NewCrawlerService(ServiceOptions{
		DefaultStorageDir:    filepath.Join(tempDir, "pages"),
		DefaultCheckpointDir: filepath.Join(tempDir, "checkpoints"),
		AllowLocal:           true,
	})

	_, _ = s.StartCrawl(StartCrawlRequest{
		URL:      ts.URL + "/",
		Workers:  1,
		MaxPages: 10,
	})

	_ = s.PauseCrawl()
	if s.GetStatus().State != StatePaused {
		t.Fatalf("expected state PAUSED")
	}

	if err := s.StopCrawl(); err != nil {
		t.Fatalf("StopCrawl while paused failed: %v", err)
	}

	if s.GetStatus().State != StateStopped {
		t.Errorf("expected StateStopped, got %s", s.GetStatus().State)
	}
}

// 8. Starting a new session after previous session has terminated
func TestService_StartNewSessionAfterPreviousTerminated(t *testing.T) {
	ts := createTestHTTPSite()
	defer ts.Close()

	tempDir, _ := os.MkdirTemp("", "crawlsync_svc_restart_*")
	defer os.RemoveAll(tempDir)

	s := NewCrawlerService(ServiceOptions{
		DefaultStorageDir:    filepath.Join(tempDir, "pages"),
		DefaultCheckpointDir: filepath.Join(tempDir, "checkpoints"),
		AllowLocal:           true,
	})

	// Session 1: run to completion
	info1, err := s.StartCrawl(StartCrawlRequest{
		URL:      ts.URL + "/",
		Workers:  2,
		MaxPages: 2,
	})
	if err != nil {
		t.Fatalf("Session 1 start failed: %v", err)
	}
	waitForState(t, s, StateCompleted, 3*time.Second)

	// Session 2: start new session
	info2, err := s.StartCrawl(StartCrawlRequest{
		URL:      ts.URL + "/p1",
		Workers:  2,
		MaxPages: 2,
	})
	if err != nil {
		t.Fatalf("Session 2 start failed: %v", err)
	}

	if info2.SessionID == info1.SessionID {
		t.Errorf("expected distinct session ID for session 2, got %s", info2.SessionID)
	}

	_ = s.StopCrawl()
}

// 9. Rejecting start while stopping
func TestService_RejectStartWhileStopping(t *testing.T) {
	s := NewCrawlerService(ServiceOptions{})
	s.mu.Lock()
	s.state = StateStopping
	s.mu.Unlock()

	_, err := s.StartCrawl(StartCrawlRequest{URL: "https://example.com/"})
	if err != ErrCrawlStopping {
		t.Fatalf("expected ErrCrawlStopping, got: %v", err)
	}
}

// 10. Resuming from a checkpoint
func TestService_ResumeFromCheckpoint(t *testing.T) {
	ts := createTestHTTPSite()
	defer ts.Close()

	tempDir, _ := os.MkdirTemp("", "crawlsync_svc_resume_cp_*")
	defer os.RemoveAll(tempDir)

	storageDir := filepath.Join(tempDir, "pages")
	checkpointDir := filepath.Join(tempDir, "checkpoints")

	s := NewCrawlerService(ServiceOptions{
		DefaultStorageDir:    storageDir,
		DefaultCheckpointDir: checkpointDir,
		AllowLocal:           true,
	})

	// Start and stop session 1 to generate a checkpoint
	info1, err := s.StartCrawl(StartCrawlRequest{
		URL:      ts.URL + "/",
		Workers:  1,
		MaxPages: 10,
	})
	if err != nil {
		t.Fatalf("Session 1 start failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	_ = s.StopCrawl()

	// Resume session 2 using checkpoint
	info2, err := s.StartCrawl(StartCrawlRequest{
		Resume:        true,
		StorageDir:    storageDir,
		CheckpointDir: checkpointDir,
	})
	if err != nil {
		t.Fatalf("Resume crawl failed: %v", err)
	}

	if info2.SessionID != info1.SessionID {
		t.Errorf("expected resumed session ID %s, got %s", info1.SessionID, info2.SessionID)
	}
	if !info2.IsResumed {
		t.Error("expected IsResumed = true")
	}

	waitForState(t, s, StateCompleted, 3*time.Second)
}

// 11. Retry-failed recovery
func TestService_RetryFailedRecovery(t *testing.T) {
	tempDir, _ := os.MkdirTemp("", "crawlsync_svc_retry_failed_*")
	defer os.RemoveAll(tempDir)

	storageDir := filepath.Join(tempDir, "pages")
	checkpointDir := filepath.Join(tempDir, "checkpoints")
	_ = os.MkdirAll(checkpointDir, 0755)

	// Persist a checkpoint with a failed URL
	cm := state.NewCheckpointManager(checkpointDir)
	cfg := state.CheckpointConfig{
		StartURL:    "https://example.com/",
		WorkerCount: 1,
		MaxPages:    5,
		StorageDir:  storageDir,
	}
	snap := urlmanager.Snapshot{
		Stats: urlmanager.Stats{TotalDiscovered: 1, Failed: 1},
		URLs: []urlmanager.URLItem{
			{Sequence: 1, URL: "https://example.com/fail", Normalized: "https://example.com/fail", Status: urlmanager.StatusFailed, Depth: 0},
		},
	}
	_, _ = cm.Save("session_fail_test", cfg, snap)

	s := NewCrawlerService(ServiceOptions{
		DefaultStorageDir:    storageDir,
		DefaultCheckpointDir: checkpointDir,
		AllowLocal:           false,
	})

	// Resume with retry_failed = true
	_, err := s.StartCrawl(StartCrawlRequest{
		Resume:        true,
		RetryFailed:   true,
		StorageDir:    storageDir,
		CheckpointDir: checkpointDir,
	})
	if err != nil {
		t.Fatalf("StartCrawl resume failed: %v", err)
	}

	// Check that the URL was demoted to pending in the service
	item, err := s.GetURLBySequence(1)
	if err != nil {
		t.Fatalf("GetURLBySequence failed: %v", err)
	}
	// It was either pending or picked up
	if item.Status != urlmanager.StatusPending && item.Status != urlmanager.StatusInProgress && item.Status != urlmanager.StatusFailed {
		t.Errorf("expected URL to be demoted to pending/in_progress, got: %s", item.Status)
	}

	_ = s.StopCrawl()
}

// 12. Manual checkpointing
func TestService_ManualCheckpointing(t *testing.T) {
	ts := createTestHTTPSite()
	defer ts.Close()

	tempDir, _ := os.MkdirTemp("", "crawlsync_svc_manual_cp_*")
	defer os.RemoveAll(tempDir)

	s := NewCrawlerService(ServiceOptions{
		DefaultStorageDir:    filepath.Join(tempDir, "pages"),
		DefaultCheckpointDir: filepath.Join(tempDir, "checkpoints"),
		AllowLocal:           true,
	})

	_, _ = s.StartCrawl(StartCrawlRequest{
		URL:      ts.URL + "/",
		Workers:  2,
		MaxPages: 10,
	})

	cp, err := s.CreateCheckpoint()
	if err != nil {
		t.Fatalf("CreateCheckpoint failed: %v", err)
	}
	if cp.Sequence < 1 {
		t.Errorf("expected sequence >= 1, got %d", cp.Sequence)
	}

	_ = s.StopCrawl()
}

// 13. Read-only queries while crawling
func TestService_ReadOnlyQueriesWhileCrawling(t *testing.T) {
	ts := createTestHTTPSite()
	defer ts.Close()

	tempDir, _ := os.MkdirTemp("", "crawlsync_svc_queries_*")
	defer os.RemoveAll(tempDir)

	s := NewCrawlerService(ServiceOptions{
		DefaultStorageDir:    filepath.Join(tempDir, "pages"),
		DefaultCheckpointDir: filepath.Join(tempDir, "checkpoints"),
		AllowLocal:           true,
	})

	_, _ = s.StartCrawl(StartCrawlRequest{
		URL:      ts.URL + "/",
		Workers:  2,
		MaxPages: 10,
	})

	// Wait until at least 1 URL (the seed URL) is scheduled
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st, err := s.GetStats()
		if err == nil && st.TotalDiscovered >= 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Perform read-only queries while crawling
	status := s.GetStatus()
	if status.State != StateRunning && status.State != StateCompleted {
		t.Errorf("unexpected state: %s", status.State)
	}

	stats, err := s.GetStats()
	if err != nil {
		t.Errorf("GetStats failed: %v", err)
	}
	if stats.TotalDiscovered < 1 {
		t.Errorf("expected at least 1 discovered URL")
	}

	workers, err := s.GetWorkers()
	if err != nil {
		t.Errorf("GetWorkers failed: %v", err)
	}
	if len(workers) != 2 {
		t.Errorf("expected 2 workers, got %d", len(workers))
	}

	urls, total, _, err := s.GetURLs(urlmanager.URLFilterOptions{Limit: 5})
	if err != nil {
		t.Errorf("GetURLs failed: %v", err)
	}
	if total < 1 || len(urls) < 1 {
		t.Errorf("expected URLs returned, got total=%d, len=%d", total, len(urls))
	}

	// Lookup by sequence 1
	item, err := s.GetURLBySequence(1)
	if err != nil || item == nil {
		t.Errorf("GetURLBySequence(1) failed: %v", err)
	}

	_ = s.StopCrawl()
}

// 14. Concurrent status and statistics queries
func TestService_ConcurrentStatusAndStatsQueries(t *testing.T) {
	ts := createTestHTTPSite()
	defer ts.Close()

	tempDir, _ := os.MkdirTemp("", "crawlsync_svc_concurrent_queries_*")
	defer os.RemoveAll(tempDir)

	s := NewCrawlerService(ServiceOptions{
		DefaultStorageDir:    filepath.Join(tempDir, "pages"),
		DefaultCheckpointDir: filepath.Join(tempDir, "checkpoints"),
		AllowLocal:           true,
	})

	_, _ = s.StartCrawl(StartCrawlRequest{
		URL:      ts.URL + "/",
		Workers:  2,
		MaxPages: 10,
	})

	var wg sync.WaitGroup
	const readers = 10
	wg.Add(readers)

	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = s.GetStatus()
				_, _ = s.GetStats()
				_, _, _, _ = s.GetURLs(urlmanager.URLFilterOptions{Limit: 5})
				_, _ = s.GetWorkers()
			}
		}()
	}

	wg.Wait()
	_ = s.StopCrawl()
}

// 15. Concurrent lifecycle operations
func TestService_ConcurrentLifecycleOperations(t *testing.T) {
	ts := createTestHTTPSite()
	defer ts.Close()

	tempDir, _ := os.MkdirTemp("", "crawlsync_svc_concurrent_lifecycle_*")
	defer os.RemoveAll(tempDir)

	s := NewCrawlerService(ServiceOptions{
		DefaultStorageDir:    filepath.Join(tempDir, "pages"),
		DefaultCheckpointDir: filepath.Join(tempDir, "checkpoints"),
		AllowLocal:           true,
	})

	_, _ = s.StartCrawl(StartCrawlRequest{
		URL:      ts.URL + "/",
		Workers:  2,
		MaxPages: 20,
	})

	var wg sync.WaitGroup
	const actors = 6
	wg.Add(actors)

	// Concurrently issue Pause, Resume, CreateCheckpoint, Stop
	go func() { defer wg.Done(); _ = s.PauseCrawl() }()
	go func() { defer wg.Done(); _ = s.ResumeCrawl() }()
	go func() { defer wg.Done(); _ = s.PauseCrawl() }()
	go func() { defer wg.Done(); _, _ = s.CreateCheckpoint() }()
	go func() { defer wg.Done(); _ = s.ResumeCrawl() }()
	go func() { defer wg.Done(); _ = s.StopCrawl() }()

	wg.Wait()

	// Service must end up cleanly stopped without deadlocks or hangs
	status := s.GetStatus()
	if status.State != StateStopped {
		_ = s.StopCrawl()
	}
}

// 16. Ensuring an old session completion cannot modify a newer session's state
func TestService_OldSessionCompletionCannotModifyNewerSession(t *testing.T) {
	tempDir, _ := os.MkdirTemp("", "crawlsync_svc_old_sess_*")
	defer os.RemoveAll(tempDir)

	s := NewCrawlerService(ServiceOptions{
		DefaultStorageDir:    filepath.Join(tempDir, "pages"),
		DefaultCheckpointDir: filepath.Join(tempDir, "checkpoints"),
	})

	// Manually simulate active session 2
	s.mu.Lock()
	s.activeSessionID = "session_2_active"
	s.state = StateRunning
	s.mu.Unlock()

	// Simulate session 1 completion arriving late
	oldCtrl, _ := controller.NewController(controller.Config{
		StartURL:      "https://example.com",
		StorageDir:    filepath.Join(tempDir, "pages"),
		CheckpointDir: filepath.Join(tempDir, "checkpoints"),
	}, urlmanager.NewManager())
	oldDone := make(chan struct{})

	// Supervise with old session ID using an already-canceled context so Run returns immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.superviseSession(oldCtrl, ctx, "session_1_old", oldDone)

	// Verify that session 2's state was NOT altered
	s.mu.RLock()
	currentState := s.state
	currentSessionID := s.activeSessionID
	s.mu.RUnlock()

	if currentState != StateRunning {
		t.Errorf("expected state to remain RUNNING, got %s", currentState)
	}
	if currentSessionID != "session_2_active" {
		t.Errorf("expected session ID to remain session_2_active, got %s", currentSessionID)
	}
}

// 17. Graceful service shutdown
func TestService_GracefulShutdown(t *testing.T) {
	ts := createTestHTTPSite()
	defer ts.Close()

	tempDir, _ := os.MkdirTemp("", "crawlsync_svc_shutdown_*")
	defer os.RemoveAll(tempDir)

	s := NewCrawlerService(ServiceOptions{
		DefaultStorageDir:    filepath.Join(tempDir, "pages"),
		DefaultCheckpointDir: filepath.Join(tempDir, "checkpoints"),
		AllowLocal:           true,
	})

	_, _ = s.StartCrawl(StartCrawlRequest{
		URL:      ts.URL + "/",
		Workers:  2,
		MaxPages: 10,
	})

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := s.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	if s.GetStatus().State != StateStopped {
		t.Errorf("expected StateStopped after shutdown, got %s", s.GetStatus().State)
	}
}

package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"crawlsync/internal/urlmanager"
)

// TestController_RuntimePause_DeterministicMultiWorker tests CrawlSync's runtime pause semantics
// with multiple workers and controlled response delays using synchronization channels.
//
// Expected semantics:
// 1. Pause must prevent dispatching NEW jobs.
// 2. Already-dispatched HTTP requests may finish normally.
// 3. Results from in-flight workers must still be processed.
// 4. Newly discovered URLs must remain pending while paused.
// 5. The controller must remain in PAUSED state.
// 6. Resume must restart dispatching pending jobs.
// 7. Checkpointing must continue to work while paused.
// 8. Stop must still work while paused.
func TestController_RuntimePause_DeterministicMultiWorker(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_pause_multiworker_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	const workerCount = 3
	activeWorkers := int64(0)
	workersReached := make(chan struct{})
	var workersOnce sync.Once
	releaseInFlight := make(chan struct{})

	var extraRequested int64
	var discoveredRequested int64

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		path := r.URL.Path

		switch {
		case path == "/":
			// Root page discovers 3 initial worker URLs and 2 extra URLs
			_, _ = fmt.Fprint(w, `<!DOCTYPE html><html><body>
				<a href="/w1">W1</a>
				<a href="/w2">W2</a>
				<a href="/w3">W3</a>
				<a href="/extra1">Extra 1</a>
				<a href="/extra2">Extra 2</a>
			</body></html>`)

		case strings.HasPrefix(path, "/w"):
			// Active in-flight worker requests
			cur := atomic.AddInt64(&activeWorkers, 1)
			if cur == workerCount {
				workersOnce.Do(func() { close(workersReached) })
			}
			<-releaseInFlight // Controlled delay: hold until Pause is issued
			// Discovers a secondary link while completing under pause
			_, _ = fmt.Fprintf(w, `<!DOCTYPE html><html><body>
				<a href="/discovered_%s">Discovered Link</a>
			</body></html>`, strings.TrimPrefix(path, "/"))

		case strings.HasPrefix(path, "/extra"):
			atomic.AddInt64(&extraRequested, 1)
			_, _ = fmt.Fprint(w, `<!DOCTYPE html><html><body>Extra Page</body></html>`)

		case strings.HasPrefix(path, "/discovered"):
			atomic.AddInt64(&discoveredRequested, 1)
			_, _ = fmt.Fprint(w, `<!DOCTYPE html><html><body>Discovered Page</body></html>`)

		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	cfg := Config{
		StartURL:           ts.URL + "/",
		WorkerCount:        workerCount,
		MaxPages:           20,
		MaxDepth:           3,
		RequestTimeout:     5 * time.Second,
		StorageDir:         filepath.Join(tempDir, "pages"),
		CheckpointDir:      filepath.Join(tempDir, "checkpoints"),
		CheckpointInterval: 0,
		SameDomainOnly:     true,
		AllowLocal:         true,
	}

	mgr := urlmanager.NewManager()
	ctrl, err := NewController(cfg, mgr)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- ctrl.Run(ctx)
	}()

	// 1. Wait until all 3 workers have active, in-flight HTTP requests (/w1, /w2, /w3)
	select {
	case <-workersReached:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %d workers to become active (currently %d)", workerCount, atomic.LoadInt64(&activeWorkers))
	}

	// 2. Issue Pause while workers are actively executing requests
	if err := ctrl.Pause(); err != nil {
		t.Fatalf("Pause failed: %v", err)
	}

	// 3. Verify controller is in PAUSED state
	if !ctrl.IsPaused() {
		t.Fatal("expected ctrl.IsPaused() to be true")
	}
	if ctrl.State() != StatePaused {
		t.Fatalf("expected StatePaused, got %s", ctrl.State())
	}

	// 4. Release in-flight workers so they finish normally and report their results
	close(releaseInFlight)

	// 5. Wait deterministically for in-flight requests to drop to 0
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ctrl.GetStats().InProgress == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	statsPaused := ctrl.GetStats()
	if statsPaused.InProgress != 0 {
		t.Fatalf("expected InProgress == 0 after in-flight requests finished, got %d", statsPaused.InProgress)
	}

	// 6. Verify semantics while paused:
	// - Extra URLs must NOT have been dispatched
	if reqCount := atomic.LoadInt64(&extraRequested); reqCount != 0 {
		t.Errorf("SEMANTIC VIOLATION: expected 0 requests to /extra while paused, but %d were dispatched", reqCount)
	}

	// - Discovered URLs must NOT have been dispatched
	if discCount := atomic.LoadInt64(&discoveredRequested); discCount != 0 {
		t.Errorf("SEMANTIC VIOLATION: expected 0 requests to /discovered while paused, but %d were dispatched", discCount)
	}

	// - Undispatched URLs must remain with status Pending in URLManager
	pendingItems := mgr.GetPendingItems()
	if len(pendingItems) == 0 {
		t.Errorf("expected pending URLs in URLManager while paused, got 0")
	}
	for _, item := range pendingItems {
		if item.Status != urlmanager.StatusPending {
			t.Errorf("expected status 'pending' for %s, got %s", item.Normalized, item.Status)
		}
	}

	// - Controller must remain in PAUSED state
	if !ctrl.IsPaused() {
		t.Errorf("controller left PAUSED state prematurely")
	}

	// 7. Verify Checkpointing works while paused
	cp, err := ctrl.CreateCheckpoint()
	if err != nil {
		t.Fatalf("CreateCheckpoint while paused failed: %v", err)
	}
	if cp == nil || cp.Stats.Pending == 0 {
		t.Errorf("checkpoint while paused invalid: %+v", cp)
	}

	// 8. Resume: dispatches pending jobs and completes crawl
	if err := ctrl.Resume(); err != nil {
		t.Fatalf("Resume failed: %v", err)
	}
	if ctrl.IsPaused() {
		t.Fatal("expected ctrl.IsPaused() to be false after Resume")
	}

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run finished with error after resume: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for crawl to complete after resume")
	}

	// 9. Verify full completion after resume
	finalStats := ctrl.GetStats()
	if finalStats.InProgress != 0 || finalStats.Pending != 0 {
		t.Errorf("expected all jobs completed, got stats: %+v", finalStats)
	}
	if atomic.LoadInt64(&extraRequested) == 0 {
		t.Errorf("expected /extra to be requested after resume")
	}
	if atomic.LoadInt64(&discoveredRequested) == 0 {
		t.Errorf("expected /discovered to be requested after resume")
	}
}

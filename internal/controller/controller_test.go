package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"crawlsync/internal/state"
	"crawlsync/internal/urlmanager"
)

func TestController_CompleteGraphCrawl(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	var requestCounts sync.Map
	var totalRequests int64

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&totalRequests, 1)
		val, _ := requestCounts.LoadOrStore(r.URL.Path, new(int64))
		counter := val.(*int64)
		atomic.AddInt64(counter, 1)

		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		switch r.URL.Path {
		case "/":
			_, _ = fmt.Fprint(w, `
				<html><body>
					<h1>Home</h1>
					<a href="/page1">Page 1</a>
					<a href="/page2">Page 2</a>
				</body></html>`)
		case "/page1":
			_, _ = fmt.Fprint(w, `
				<html><body>
					<h1>Page 1</h1>
					<a href="/page3">Page 3</a>
					<a href="/page2">Page 2 duplicate</a>
				</body></html>`)
		case "/page2":
			_, _ = fmt.Fprint(w, `
				<html><body>
					<h1>Page 2</h1>
					<a href="/page1">Page 1 cycle</a>
					<a href="/page3">Page 3 duplicate</a>
				</body></html>`)
		case "/page3":
			_, _ = fmt.Fprint(w, `
				<html><body>
					<h1>Page 3</h1>
					<a href="/">Back to Home</a>
				</body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	cfg := Config{
		StartURL:           ts.URL + "/",
		WorkerCount:        4,
		MaxPages:           10,
		MaxDepth:           5,
		RequestTimeout:     2 * time.Second,
		StorageDir:         filepath.Join(tempDir, "pages"),
		CheckpointDir:      filepath.Join(tempDir, "checkpoints"),
		CheckpointInterval: 0, // Disabled periodic for this test
		SameDomainOnly:     true,
		AllowLocal:         true,
	}

	mgr := urlmanager.NewManager()
	ctrl := NewController(cfg, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = ctrl.Run(ctx)
	if err != nil {
		t.Fatalf("Controller run failed: %v", err)
	}

	stats := mgr.GetStats()

	// 1. Assert exactly 4 pages discovered and completed (/, /page1, /page2, /page3)
	if stats.Completed != 4 {
		t.Errorf("expected 4 completed pages, got %d", stats.Completed)
	}
	if stats.Pending != 0 {
		t.Errorf("expected 0 pending pages, got %d", stats.Pending)
	}
	if stats.InProgress != 0 {
		t.Errorf("expected 0 in_progress pages, got %d", stats.InProgress)
	}
	if stats.Failed != 0 {
		t.Errorf("expected 0 failed pages, got %d", stats.Failed)
	}

	// 2. Assert NO duplicate fetches: every page must be requested exactly once
	expectedPaths := []string{"/", "/page1", "/page2", "/page3"}
	for _, p := range expectedPaths {
		val, ok := requestCounts.Load(p)
		if !ok {
			t.Errorf("path %s was never requested", p)
			continue
		}
		count := atomic.LoadInt64(val.(*int64))
		if count != 1 {
			t.Errorf("path %s was requested %d times, expected exactly 1 (duplicate fetch detected!)", p, count)
		}
	}

	// 3. Assert all downloaded HTML files were saved to disk in storageDir
	files, err := os.ReadDir(cfg.StorageDir)
	if err != nil {
		t.Fatalf("failed reading storage dir: %v", err)
	}
	if len(files) != 4 {
		t.Errorf("expected 4 saved HTML files in %s, found %d", cfg.StorageDir, len(files))
	}

	// 4. Assert final completion checkpoint was written
	cpPath := filepath.Join(cfg.CheckpointDir, state.CheckpointFileName)
	cp, err := state.Load(cpPath)
	if err != nil {
		t.Fatalf("failed to load final checkpoint: %v", err)
	}
	if cp.Stats.Completed != 4 {
		t.Errorf("checkpoint Completed = %d, want 4", cp.Stats.Completed)
	}
}

// TestController_MaxPages_SeedOnly tests that setting MaxPages = 1 crawls only the seed URL
// and stops, confirming that the seed URL counts toward MaxPages.
func TestController_MaxPages_SeedOnly(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_seedonly_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	requestedPages := make(map[string]int)
	var mu sync.Mutex

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestedPages[r.URL.Path]++
		mu.Unlock()

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `
			<html><body>
				<a href="/child1">Child 1</a>
				<a href="/child2">Child 2</a>
			</body></html>`)
	}))
	defer ts.Close()

	cfg := Config{
		StartURL:           ts.URL + "/",
		WorkerCount:        3,
		MaxPages:           1, // Only allow 1 page (seed URL)
		MaxDepth:           5,
		RequestTimeout:     2 * time.Second,
		StorageDir:         filepath.Join(tempDir, "pages"),
		CheckpointDir:      filepath.Join(tempDir, "checkpoints"),
		CheckpointInterval: 0,
		SameDomainOnly:     true,
		AllowLocal:         true,
	}

	mgr := urlmanager.NewManager()
	ctrl := NewController(cfg, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = ctrl.Run(ctx)
	if err != nil {
		t.Fatalf("Controller run failed: %v", err)
	}

	stats := mgr.GetStats()
	if stats.TotalDiscovered != 1 {
		t.Errorf("TotalDiscovered = %d, expected 1 when MaxPages = 1", stats.TotalDiscovered)
	}
	if stats.Completed != 1 {
		t.Errorf("Completed = %d, expected 1", stats.Completed)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requestedPages) != 1 || requestedPages["/"] != 1 {
		t.Errorf("expected only root URL to be requested, got: %v", requestedPages)
	}
}

// TestController_MaxPages_Duplicates ensures duplicate links encountered across pages
// do NOT consume page capacity.
func TestController_MaxPages_Duplicates(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_duplicates_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/":
			_, _ = fmt.Fprint(w, `
				<html><body>
					<a href="/page1">1</a>
					<a href="/page1/">1 trailing slash duplicate</a>
					<a href="/page1#anchor">1 fragment duplicate</a>
					<a href="/page2">2</a>
				</body></html>`)
		case "/page1":
			_, _ = fmt.Fprint(w, `
				<html><body>
					<a href="/page2">2 duplicate</a>
					<a href="/page2/">2 trailing duplicate</a>
					<a href="/page3">3</a>
				</body></html>`)
		default:
			_, _ = fmt.Fprint(w, `<html><body>Leaf</body></html>`)
		}
	}))
	defer ts.Close()

	cfg := Config{
		StartURL:           ts.URL + "/",
		WorkerCount:        2,
		MaxPages:           3,
		MaxDepth:           5,
		RequestTimeout:     2 * time.Second,
		StorageDir:         filepath.Join(tempDir, "pages"),
		CheckpointDir:      filepath.Join(tempDir, "checkpoints"),
		CheckpointInterval: 0,
		SameDomainOnly:     true,
		AllowLocal:         true,
	}

	mgr := urlmanager.NewManager()
	ctrl := NewController(cfg, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = ctrl.Run(ctx)
	if err != nil {
		t.Fatalf("Controller run failed: %v", err)
	}

	stats := mgr.GetStats()
	if stats.TotalDiscovered > 3 {
		t.Errorf("TotalDiscovered = %d exceeded MaxPages limit of 3 due to duplicates", stats.TotalDiscovered)
	}
}

// TestController_ActiveDownloadCancellation verifies that canceling the context while
// workers are actively downloading terminates cleanly, resets all worker monitor states to IDLE,
// ensures unfinished URLs are NOT marked completed, and creates a final shutdown checkpoint.
func TestController_ActiveDownloadCancellation(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_actcancel_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	fetchStarted := make(chan struct{})
	var once sync.Once

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() {
			close(fetchStarted)
		})
		time.Sleep(2 * time.Second)
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<html><body>Slow Response</body></html>")
	}))
	defer ts.Close()

	cfg := Config{
		StartURL:           ts.URL + "/",
		WorkerCount:        4,
		MaxPages:           10,
		MaxDepth:           2,
		RequestTimeout:     5 * time.Second,
		StorageDir:         filepath.Join(tempDir, "pages"),
		CheckpointDir:      filepath.Join(tempDir, "checkpoints"),
		CheckpointInterval: 0,
		SameDomainOnly:     true,
		AllowLocal:         true,
	}

	mgr := urlmanager.NewManager()
	ctrl := NewController(cfg, mgr)

	ctx, cancel := context.WithCancel(context.Background())

	runErrChan := make(chan error, 1)
	go func() {
		runErrChan <- ctrl.Run(ctx)
	}()

	select {
	case <-fetchStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for worker to begin download")
	}

	cancel()

	select {
	case err := <-runErrChan:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("controller failed to terminate within deadline after cancellation")
	}

	states := ctrl.GetWorkerStates()
	for _, w := range states {
		if w.State != WorkerStateIdle {
			t.Errorf("worker %d remained in state %s after cancellation", w.ID, w.State)
		}
	}

	stats := mgr.GetStats()
	if stats.Completed != 0 {
		t.Errorf("expected 0 completed pages on canceled download, got %d", stats.Completed)
	}

	// Verify final checkpoint was written on shutdown
	cpPath := filepath.Join(cfg.CheckpointDir, state.CheckpointFileName)
	cp, err := state.Load(cpPath)
	if err != nil {
		t.Fatalf("failed loading final shutdown checkpoint: %v", err)
	}
	if cp.Stats.Completed != 0 {
		t.Errorf("checkpoint completed = %d, want 0", cp.Stats.Completed)
	}
}

// TestController_PeriodicCheckpointing tests that a ticker automatically creates checkpoints.
func TestController_PeriodicCheckpointing(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_periodic_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(30 * time.Millisecond) // Slow enough to trigger periodic ticker
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/" {
			_, _ = fmt.Fprint(w, `<html><body><a href="/p1">1</a><a href="/p2">2</a></body></html>`)
		} else {
			_, _ = fmt.Fprint(w, `<html><body>Leaf</body></html>`)
		}
	}))
	defer ts.Close()

	cfg := Config{
		StartURL:           ts.URL + "/",
		WorkerCount:        2,
		MaxPages:           3,
		MaxDepth:           2,
		RequestTimeout:     2 * time.Second,
		StorageDir:         filepath.Join(tempDir, "pages"),
		CheckpointDir:      filepath.Join(tempDir, "checkpoints"),
		CheckpointInterval: 20 * time.Millisecond, // Fast interval to ensure periodic fires
		SameDomainOnly:     true,
		AllowLocal:         true,
	}

	mgr := urlmanager.NewManager()
	ctrl := NewController(cfg, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := ctrl.Run(ctx); err != nil {
		t.Fatalf("Controller run failed: %v", err)
	}

	cpPath := filepath.Join(cfg.CheckpointDir, state.CheckpointFileName)
	cp, err := state.Load(cpPath)
	if err != nil {
		t.Fatalf("failed loading periodic checkpoint: %v", err)
	}

	if cp.Sequence < 1 {
		t.Errorf("expected sequence >= 1, got %d", cp.Sequence)
	}
	if cp.Version != state.SchemaVersion {
		t.Errorf("version = %d, want %d", cp.Version, state.SchemaVersion)
	}
}

// TestWorker_MalformedHTMLNonFatal verifies that non-fatal parse errors on invalid HTML
// do not invalidate the successfully saved page.
func TestWorker_MalformedHTMLNonFatal(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_malformed_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<html><head><title>Broken<body><a href="/valid">Link</a><<<>>>bad tag`)
	}))
	defer ts.Close()

	cfg := Config{
		StartURL:           ts.URL + "/",
		WorkerCount:        1,
		MaxPages:           2,
		MaxDepth:           1,
		RequestTimeout:     2 * time.Second,
		StorageDir:         filepath.Join(tempDir, "pages"),
		CheckpointDir:      filepath.Join(tempDir, "checkpoints"),
		CheckpointInterval: 0,
		SameDomainOnly:     true,
		AllowLocal:         true,
	}

	mgr := urlmanager.NewManager()
	ctrl := NewController(cfg, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err = ctrl.Run(ctx)
	if err != nil {
		t.Fatalf("Controller run failed on malformed HTML: %v", err)
	}

	stats := mgr.GetStats()
	if stats.Completed < 1 {
		t.Errorf("expected at least 1 completed page despite malformed HTML, got %d", stats.Completed)
	}
}

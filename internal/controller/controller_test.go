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
	ctrl, err := NewController(cfg, mgr)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

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
	ctrl, err := NewController(cfg, mgr)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

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
	ctrl, err := NewController(cfg, mgr)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

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
		<-r.Context().Done()
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
	ctrl, err := NewController(cfg, mgr)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

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
	ctrl, err := NewController(cfg, mgr)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

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
	ctrl, err := NewController(cfg, mgr)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

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

// TestWorker_PageReplacement_PreservesExistingOnFailure tests that atomic page file replacement
// preserves the existing destination file if committing the new file fails.
func TestWorker_PageReplacement_PreservesExistingOnFailure(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_atomic_replace_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	targetFile := filepath.Join(tempDir, "existing_page.html")
	originalContent := "<html><body>Original Valid Content</body></html>"
	if err := os.WriteFile(targetFile, []byte(originalContent), 0644); err != nil {
		t.Fatalf("failed writing original file: %v", err)
	}

	// Case 1: Failed replacement when tmpPath does not exist
	nonExistentTmp := filepath.Join(tempDir, "does_not_exist.tmp")
	err = replaceFileWithBackup(nonExistentTmp, targetFile, tempDir)
	if err == nil {
		t.Fatal("expected error when replacing with non-existent tmpPath, got nil")
	}

	// Verify original file survived intact with identical content
	data, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("failed reading targetFile after failed replacement: %v", err)
	}
	if string(data) != originalContent {
		t.Fatalf("original file altered on failure! Got %q, want %q", string(data), originalContent)
	}

	// Case 2: Successful replacement with valid tmpPath
	validTmp := filepath.Join(tempDir, "valid_new.tmp")
	newContent := "<html><body>New Updated Content</body></html>"
	if err := os.WriteFile(validTmp, []byte(newContent), 0644); err != nil {
		t.Fatalf("failed writing valid tmp: %v", err)
	}

	if err := replaceFileWithBackup(validTmp, targetFile, tempDir); err != nil {
		t.Fatalf("expected successful replacement, got: %v", err)
	}

	data, err = os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("failed reading targetFile after successful replacement: %v", err)
	}
	if string(data) != newContent {
		t.Fatalf("file content mismatch after replacement: got %q, want %q", string(data), newContent)
	}
}

// TestController_ShutdownResultDraining verifies that:
// 1. A successful worker result buffered in resultsChan immediately before cancellation
//    is drained and processed before the shutdown checkpoint.
// 2. The resulting URL is marked completed in the checkpoint.
// 3. An interrupted worker remains in_progress.
// 4. Newly discovered links are preserved in the checkpoint as pending.
func TestController_ShutdownResultDraining(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_drain_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	slowStarted := make(chan struct{})
	var slowOnce sync.Once
	fastDone := make(chan struct{})
	var fastOnce sync.Once

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/":
			// Seed page links to /fast and /slow
			_, _ = fmt.Fprint(w, `<html><body><a href="/fast">Fast</a><a href="/slow">Slow</a></body></html>`)
		case "/fast":
			// Fast page downloads quickly and saves to disk
			_, _ = fmt.Fprint(w, `<html><body><h1>Fast page</h1></body></html>`)
			fastOnce.Do(func() { close(fastDone) })
		case "/slow":
			// Slow page signals start and waits until interrupted by cancellation
			slowOnce.Do(func() { close(slowStarted) })
			<-r.Context().Done()
			_, _ = fmt.Fprint(w, `<html><body>Slow</body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	cfg := Config{
		StartURL:           ts.URL + "/",
		WorkerCount:        2,
		MaxPages:           10,
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

	ctx, cancel := context.WithCancel(context.Background())

	runErrChan := make(chan error, 1)
	go func() {
		runErrChan <- ctrl.Run(ctx)
	}()

	// Wait for /slow to start downloading
	select {
	case <-slowStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for /slow to start")
	}

	// Wait until /fast has completed its network download and disk save
	fastFileSaved := false
	for i := 0; i < 100; i++ {
		entries, _ := os.ReadDir(cfg.StorageDir)
		// StorageDir contains root (/) and /fast once /fast is saved to disk
		if len(entries) >= 2 {
			fastFileSaved = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !fastFileSaved {
		t.Fatal("timed out waiting for /fast to be saved to disk")
	}

	// Trigger cancellation while /fast result is buffered/processed and /slow is downloading
	cancel()

	select {
	case err := <-runErrChan:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("controller failed to terminate cleanly on cancellation")
	}

	// Load shutdown checkpoint and verify statuses
	cpPath := filepath.Join(cfg.CheckpointDir, state.CheckpointFileName)
	cp, err := state.Load(cpPath)
	if err != nil {
		t.Fatalf("failed loading shutdown checkpoint: %v", err)
	}

	urlStatuses := make(map[string]urlmanager.URLStatus)
	for _, u := range cp.URLs {
		urlStatuses[u.Normalized] = u.Status
	}

	fastNorm, _ := urlmanager.NormalizeURL(ts.URL + "/fast")
	slowNorm, _ := urlmanager.NormalizeURL(ts.URL + "/slow")

	// 1. /fast must be marked completed in the checkpoint
	if status, ok := urlStatuses[fastNorm]; !ok || status != urlmanager.StatusCompleted {
		t.Errorf("expected /fast to be 'completed' in checkpoint, got %s (exists=%t)", status, ok)
	}

	// 2. /slow must remain in_progress (interrupted worker)
	if status, ok := urlStatuses[slowNorm]; !ok || status != urlmanager.StatusInProgress {
		t.Errorf("expected /slow to be 'in_progress' in checkpoint, got %s (exists=%t)", status, ok)
	}
}

// TestController_HandleCrawlResult_DrainPolicy verifies that draining a successful CrawlResult:
// 1. Marks the URL completed with metadata.
// 2. Records discovered child links as 'pending' in URLManager.
// 3. Does not dispatch jobs to pendingJobs slice when allowNewPending is false.
// 4. Preserves in_progress status on interrupted errors.
func TestController_HandleCrawlResult_DrainPolicy(t *testing.T) {
	mgr := urlmanager.NewManager()
	ctrl, err := NewController(Config{
		StartURL: "https://example.com/",
		MaxPages: 10,
		MaxDepth: 3,
	}, mgr)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	// Schedule parent and interrupted URLs
	_, parentNorm, _ := mgr.TrySchedule("https://example.com/parent", 1)
	_ = mgr.MarkInProgress(parentNorm)
	_, interruptedNorm, _ := mgr.TrySchedule("https://example.com/interrupted", 1)
	_ = mgr.MarkInProgress(interruptedNorm)

	var pendingJobs []Job

	// 1. Process successful result in shutdown/drain mode (allowNewPending = false)
	successRes := CrawlResult{
		URL:             parentNorm,
		Depth:           1,
		StatusCode:      200,
		Size:            2048,
		Duration:        100 * time.Millisecond,
		SavedPath:       "data/pages/parent.html",
		DiscoveredLinks: []string{"https://example.com/child_from_drain"},
		Error:           nil,
	}
	ctrl.handleCrawlResult(successRes, &pendingJobs, false)

	// Parent must be completed with metadata
	item, ok := mgr.GetStatus(parentNorm)
	if !ok || item.Status != urlmanager.StatusCompleted {
		t.Errorf("expected parent to be completed, got %v", item)
	}
	if item.Size != 2048 || item.SavedPath != "data/pages/parent.html" {
		t.Errorf("parent metadata mismatch: %+v", item)
	}

	// Child must be scheduled as pending in URLManager
	childNorm, _ := urlmanager.NormalizeURL("https://example.com/child_from_drain")
	childItem, ok := mgr.GetStatus(childNorm)
	if !ok || childItem.Status != urlmanager.StatusPending {
		t.Errorf("expected child to be pending in URLManager, got %v", childItem)
	}

	// But pendingJobs slice must NOT have been appended to (no new jobs during shutdown)
	if len(pendingJobs) != 0 {
		t.Errorf("expected pendingJobs to be empty when allowNewPending is false, got %d", len(pendingJobs))
	}

	// 2. Process interrupted result
	interruptedRes := CrawlResult{
		URL:   interruptedNorm,
		Depth: 1,
		Error: context.Canceled,
	}
	ctrl.handleCrawlResult(interruptedRes, &pendingJobs, false)

	// Interrupted URL must remain in_progress
	itemInterrupted, ok := mgr.GetStatus(interruptedNorm)
	if !ok || itemInterrupted.Status != urlmanager.StatusInProgress {
		t.Errorf("expected interrupted URL to remain in_progress, got %v", itemInterrupted)
	}
}


// TestController_Pause_ActiveDownloads verifies Test A:
// Requesting pause while workers are active allows in-flight jobs to finish,
// but prevents any further jobs from being dispatched.
func TestController_Pause_ActiveDownloads(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_pause_active_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	slowStarted := make(chan struct{})
	var slowOnce sync.Once
	childRequested := int64(0)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/":
			_, _ = fmt.Fprint(w, `<html><body><a href="/slow">Slow</a></body></html>`)
		case "/slow":
			slowOnce.Do(func() { close(slowStarted) })
			time.Sleep(30 * time.Millisecond)
			_, _ = fmt.Fprint(w, `<html><body><a href="/child">Child</a></body></html>`)
		case "/child":
			atomic.AddInt64(&childRequested, 1)
			_, _ = fmt.Fprint(w, `<html><body>Child page</body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	cfg := Config{
		StartURL:           ts.URL + "/",
		WorkerCount:        2,
		MaxPages:           10,
		MaxDepth:           3,
		RequestTimeout:     2 * time.Second,
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- ctrl.Run(ctx)
	}()

	// Wait until /slow download begins
	select {
	case <-slowStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for /slow to start")
	}

	// Request pause while /slow is in-flight
	if err := ctrl.Pause(); err != nil {
		t.Fatalf("Pause failed: %v", err)
	}

	if !ctrl.IsPaused() {
		t.Error("expected ctrl.IsPaused() to be true")
	}

	// Wait deterministically for /slow to finish and report its result
	slowNorm, _ := urlmanager.NormalizeURL(ts.URL + "/slow")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if item, ok := mgr.GetStatus(slowNorm); ok && item.Status == urlmanager.StatusCompleted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Verify /slow was allowed to finish successfully
	itemSlow, ok := mgr.GetStatus(slowNorm)
	if !ok || itemSlow.Status != urlmanager.StatusCompleted {
		t.Errorf("expected /slow to be completed, got status=%v (ok=%t)", itemSlow, ok)
	}

	// Verify /child was NOT dispatched while paused
	if count := atomic.LoadInt64(&childRequested); count != 0 {
		t.Errorf("expected 0 requests to /child while paused, got %d", count)
	}

	// Verify /child was registered as pending in URLManager
	childNorm, _ := urlmanager.NormalizeURL(ts.URL + "/child")
	itemChild, ok := mgr.GetStatus(childNorm)
	if !ok || itemChild.Status != urlmanager.StatusPending {
		t.Errorf("expected /child to be pending in URLManager, got status=%v (ok=%t)", itemChild, ok)
	}

	// Clean stop
	_ = ctrl.Stop()
	<-runDone
}

// TestController_ResultsArrivingWhilePaused verifies Test B:
// Results from active workers arriving while paused are properly processed,
// updating statistics, and newly discovered links remain pending in queue.
func TestController_ResultsArrivingWhilePaused(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_pause_results_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	downloadStarted := make(chan struct{})
	var once sync.Once

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/inflight" {
			once.Do(func() { close(downloadStarted) })
			time.Sleep(30 * time.Millisecond)
			_, _ = fmt.Fprint(w, `<html><body><a href="/discovered">New Link</a></body></html>`)
		} else {
			_, _ = fmt.Fprint(w, `<html><body><a href="/inflight">Start</a></body></html>`)
		}
	}))
	defer ts.Close()

	cfg := Config{
		StartURL:           ts.URL + "/",
		WorkerCount:        2,
		MaxPages:           10,
		MaxDepth:           3,
		RequestTimeout:     2 * time.Second,
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- ctrl.Run(ctx)
	}()

	select {
	case <-downloadStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for /inflight to start")
	}

	// Pause while /inflight is active
	_ = ctrl.Pause()

	// Wait deterministically for /inflight to complete
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ctrl.GetStats().InProgress == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Verify stats: 1 in-flight finished and completed, 1 discovered pending
	stats := ctrl.GetStats()
	if stats.InProgress != 0 {
		t.Errorf("expected 0 in_progress after in-flight finished, got %d", stats.InProgress)
	}
	if stats.Pending < 1 {
		t.Errorf("expected at least 1 pending URL, got %d", stats.Pending)
	}

	_ = ctrl.Stop()
	<-runDone
}

// TestController_Resume verifies Test C:
// Resuming a paused crawler dispatches pending URLs and completes the crawl graph.
func TestController_Resume(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_resume_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	p1Done := make(chan struct{})
	var once sync.Once

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/":
			_, _ = fmt.Fprint(w, `<html><body><a href="/p1">P1</a></body></html>`)
		case "/p1":
			once.Do(func() { close(p1Done) })
			time.Sleep(50 * time.Millisecond)
			_, _ = fmt.Fprint(w, `<html><body><a href="/p2">P2</a></body></html>`)
		case "/p2":
			_, _ = fmt.Fprint(w, `<html><body><a href="/p3">P3</a></body></html>`)
		case "/p3":
			_, _ = fmt.Fprint(w, `<html><body>Done</body></html>`)
		}
	}))
	defer ts.Close()

	cfg := Config{
		StartURL:           ts.URL + "/",
		WorkerCount:        2,
		MaxPages:           5,
		MaxDepth:           4,
		RequestTimeout:     2 * time.Second,
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- ctrl.Run(ctx)
	}()

	select {
	case <-p1Done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for /p1")
	}

	// Pause
	if err := ctrl.Pause(); err != nil {
		t.Fatalf("Pause failed: %v", err)
	}
	if !ctrl.IsPaused() {
		t.Fatal("expected controller to be paused")
	}

	// Resume
	if err := ctrl.Resume(); err != nil {
		t.Fatalf("Resume failed: %v", err)
	}
	if ctrl.IsPaused() {
		t.Fatal("expected controller to no longer be paused")
	}

	// Crawl should now run to completion naturally
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run finished with error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("crawl did not complete after resume")
	}

	stats := ctrl.GetStats()
	if stats.Completed != 4 {
		t.Errorf("expected 4 completed pages (/, /p1, /p2, /p3), got %d", stats.Completed)
	}
	if stats.Pending != 0 {
		t.Errorf("expected 0 pending pages after completion, got %d", stats.Pending)
	}
}

// TestController_DoublePause verifies Test D:
// Calling Pause repeatedly is idempotent and does not deadlock.
func TestController_DoublePause(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_double_pause_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		time.Sleep(100 * time.Millisecond)
		_, _ = fmt.Fprint(w, `<html><body><a href="/p1">P1</a></body></html>`)
	}))
	defer ts.Close()

	cfg := Config{
		StartURL:           ts.URL + "/",
		WorkerCount:        2,
		MaxPages:           5,
		MaxDepth:           2,
		RequestTimeout:     2 * time.Second,
		StorageDir:         filepath.Join(tempDir, "pages"),
		CheckpointDir:      filepath.Join(tempDir, "checkpoints"),
		CheckpointInterval: 0,
		SameDomainOnly:     true,
		AllowLocal:         true,
	}

	ctrl, err := NewController(cfg, nil)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- ctrl.Run(ctx)
	}()

	time.Sleep(30 * time.Millisecond)

	// First pause
	if err := ctrl.Pause(); err != nil {
		t.Errorf("first Pause error: %v", err)
	}
	// Second pause (idempotent)
	if err := ctrl.Pause(); err != nil {
		t.Errorf("second Pause error: %v", err)
	}

	if !ctrl.IsPaused() {
		t.Error("expected controller to be paused")
	}

	_ = ctrl.Stop()
	<-runDone
}

// TestController_DoubleResume verifies Test E:
// Calling Resume repeatedly is idempotent and does not break normal operation.
func TestController_DoubleResume(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_double_resume_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<html><body>Hello</body></html>`)
	}))
	defer ts.Close()

	cfg := Config{
		StartURL:           ts.URL + "/",
		WorkerCount:        2,
		MaxPages:           1,
		MaxDepth:           1,
		RequestTimeout:     2 * time.Second,
		StorageDir:         filepath.Join(tempDir, "pages"),
		CheckpointDir:      filepath.Join(tempDir, "checkpoints"),
		CheckpointInterval: 0,
		SameDomainOnly:     true,
		AllowLocal:         true,
	}

	ctrl, err := NewController(cfg, nil)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- ctrl.Run(ctx)
	}()

	// Resume while already running (idempotent no-op)
	if err := ctrl.Resume(); err != nil {
		t.Errorf("Resume while running error: %v", err)
	}
	if err := ctrl.Resume(); err != nil {
		t.Errorf("Second Resume while running error: %v", err)
	}

	select {
	case err := <-runDone:
		if err != nil {
			t.Errorf("Run error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("crawl timed out")
	}
}

// TestController_StopWhilePaused verifies Test F:
// Stopping a paused crawler executes full graceful shutdown, drains results, and creates final checkpoint.
func TestController_StopWhilePaused(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_stop_paused_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	p1Started := make(chan struct{})
	var once sync.Once

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/p1" {
			once.Do(func() { close(p1Started) })
			time.Sleep(100 * time.Millisecond)
			_, _ = fmt.Fprint(w, `<html><body><a href="/child">Child</a></body></html>`)
		} else {
			_, _ = fmt.Fprint(w, `<html><body><a href="/p1">P1</a></body></html>`)
		}
	}))
	defer ts.Close()

	cfg := Config{
		StartURL:           ts.URL + "/",
		WorkerCount:        2,
		MaxPages:           5,
		MaxDepth:           3,
		RequestTimeout:     2 * time.Second,
		StorageDir:         filepath.Join(tempDir, "pages"),
		CheckpointDir:      filepath.Join(tempDir, "checkpoints"),
		CheckpointInterval: 0,
		SameDomainOnly:     true,
		AllowLocal:         true,
	}

	ctrl, err := NewController(cfg, nil)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- ctrl.Run(ctx)
	}()

	select {
	case <-p1Started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for /p1")
	}

	// Pause while /p1 is downloading
	_ = ctrl.Pause()
	if !ctrl.IsPaused() {
		t.Fatal("expected controller to be paused")
	}

	// Now stop while paused
	if err := ctrl.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	select {
	case err := <-runDone:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("controller did not terminate cleanly on Stop")
	}

	if !ctrl.IsStopped() {
		t.Error("expected ctrl.IsStopped() to be true")
	}

	// Verify final checkpoint was written
	cpPath := filepath.Join(cfg.CheckpointDir, state.CheckpointFileName)
	cp, err := state.Load(cpPath)
	if err != nil {
		t.Fatalf("failed loading shutdown checkpoint: %v", err)
	}

	// /p1 was interrupted mid-flight by Stop() cancellation and must remain in_progress in the shutdown checkpoint
	p1Norm, _ := urlmanager.NormalizeURL(ts.URL + "/p1")
	foundP1 := false
	for _, u := range cp.URLs {
		if u.Normalized == p1Norm {
			foundP1 = true
			if u.Status != urlmanager.StatusInProgress {
				t.Errorf("expected /p1 to be in_progress in shutdown checkpoint, got %s", u.Status)
			}
		}
	}
	if !foundP1 {
		t.Errorf("/p1 not found in checkpoint URLs: %+v", cp.URLs)
	}
}

// TestController_CheckpointWhilePaused verifies Test G:
// Checkpoint captures updated statistics and URL statuses after in-flight workers finish while paused.
func TestController_CheckpointWhilePaused(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_checkpoint_paused_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	fetchStarted := make(chan struct{})
	var once sync.Once

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/p1" {
			once.Do(func() { close(fetchStarted) })
			time.Sleep(30 * time.Millisecond)
			_, _ = fmt.Fprint(w, `<html><body>Done P1</body></html>`)
		} else {
			_, _ = fmt.Fprint(w, `<html><body><a href="/p1">P1</a></body></html>`)
		}
	}))
	defer ts.Close()

	cfg := Config{
		StartURL:           ts.URL + "/",
		WorkerCount:        2,
		MaxPages:           5,
		MaxDepth:           2,
		RequestTimeout:     2 * time.Second,
		StorageDir:         filepath.Join(tempDir, "pages"),
		CheckpointDir:      filepath.Join(tempDir, "checkpoints"),
		CheckpointInterval: 0,
		SameDomainOnly:     true,
		AllowLocal:         true,
	}

	ctrl, err := NewController(cfg, nil)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- ctrl.Run(ctx)
	}()

	select {
	case <-fetchStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for /p1")
	}

	_ = ctrl.Pause()

	// Wait deterministically for /p1 to complete while paused
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ctrl.GetStats().InProgress == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Trigger manual checkpoint while paused
	cp, err := ctrl.CreateCheckpoint()
	if err != nil {
		t.Fatalf("CreateCheckpoint while paused failed: %v", err)
	}

	if cp.Stats.Completed != 2 { // root and /p1
		t.Errorf("expected 2 completed in checkpoint while paused, got %d", cp.Stats.Completed)
	}
	if cp.Stats.InProgress != 0 {
		t.Errorf("expected 0 in_progress in checkpoint while paused, got %d", cp.Stats.InProgress)
	}

	_ = ctrl.Stop()
	<-runDone
}

// TestController_ControlRequestsAfterShutdown verifies Test H:
// Calling Pause, Resume, or Stop after the controller has terminated does not hang or deadlock.
func TestController_ControlRequestsAfterShutdown(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_after_shutdown_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<html><body>Hello</body></html>`)
	}))
	defer ts.Close()

	cfg := Config{
		StartURL:           ts.URL + "/",
		WorkerCount:        1,
		MaxPages:           1,
		MaxDepth:           1,
		RequestTimeout:     2 * time.Second,
		StorageDir:         filepath.Join(tempDir, "pages"),
		CheckpointDir:      filepath.Join(tempDir, "checkpoints"),
		CheckpointInterval: 0,
		SameDomainOnly:     true,
		AllowLocal:         true,
	}

	ctrl, err := NewController(cfg, nil)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := ctrl.Run(ctx); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if !ctrl.IsStopped() {
		t.Fatal("expected controller to be stopped after Run returns")
	}

	// All of these calls must return immediately without blocking
	done := make(chan struct{})
	go func() {
		_ = ctrl.Pause()
		_ = ctrl.Resume()
		_ = ctrl.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Succeeded immediately
	case <-time.After(1 * time.Second):
		t.Fatal("control calls hung after controller stopped")
	}
}

// TestController_ConcurrentRunRejected verifies that Run() can execute at most once per Controller instance,
// rejecting concurrent and subsequent calls with ErrAlreadyStarted.
func TestController_ConcurrentRunRejected(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_concurrent_run_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	fetchStarted := make(chan struct{})
	var once sync.Once

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(fetchStarted) })
		<-r.Context().Done()
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<html><body>Hello</body></html>`)
	}))
	defer ts.Close()

	cfg := Config{
		StartURL:           ts.URL + "/",
		WorkerCount:        2,
		MaxPages:           5,
		MaxDepth:           2,
		RequestTimeout:     2 * time.Second,
		StorageDir:         filepath.Join(tempDir, "pages"),
		CheckpointDir:      filepath.Join(tempDir, "checkpoints"),
		CheckpointInterval: 0,
		SameDomainOnly:     true,
		AllowLocal:         true,
	}

	ctrl, err := NewController(cfg, nil)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const numAttempts = 5
	errs := make(chan error, numAttempts)
	var wg sync.WaitGroup

	for i := 0; i < numAttempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- ctrl.Run(ctx)
		}()
	}

	// Wait until the single running controller begins fetching
	select {
	case <-fetchStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for running controller to begin fetch")
	}

	// Stop the running instance
	_ = ctrl.Stop()
	wg.Wait()
	close(errs)

	runningCount := 0
	rejectedCount := 0

	for err := range errs {
		if errors.Is(err, ErrAlreadyStarted) {
			rejectedCount++
		} else if err == nil || errors.Is(err, context.Canceled) {
			runningCount++
		} else {
			t.Errorf("unexpected error from Run: %v", err)
		}
	}

	if runningCount != 1 {
		t.Errorf("expected exactly 1 execution of Run() to start, got %d", runningCount)
	}
	if rejectedCount != numAttempts-1 {
		t.Errorf("expected %d calls to be rejected with ErrAlreadyStarted, got %d", numAttempts-1, rejectedCount)
	}

	// Verify sequential Run() call after termination is also rejected
	if err := ctrl.Run(ctx); !errors.Is(err, ErrAlreadyStarted) {
		t.Errorf("expected sequential Run() to return ErrAlreadyStarted, got: %v", err)
	}
}

// TestController_PausedWithPendingWorkAndZeroInFlight verifies Section 5 pause semantics:
// When paused with Pending URLs > 0 and InProgress == 0, the controller:
// 1. Remains alive without natural termination.
// 2. Does not busy-loop.
// 3. Preserves pending jobs.
// 4. Dispatches pending jobs on Resume and cleanly terminates on Stop.
func TestController_PausedWithPendingWorkAndZeroInFlight(t *testing.T) {
	t.Run("Resume completes remaining work", func(t *testing.T) {
		tempDir, err := os.MkdirTemp("", "crawlsync_paused_pending_resume_*")
		if err != nil {
			t.Fatalf("failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tempDir)

		p1Started := make(chan struct{})
		p1Release := make(chan struct{})
		var p1Once sync.Once

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			switch r.URL.Path {
			case "/":
				// Root discovers /p1 only
				_, _ = fmt.Fprint(w, `<html><body><a href="/p1">P1</a></body></html>`)
			case "/p1":
				p1Once.Do(func() { close(p1Started) })
				<-p1Release // Hold until paused
				// /p1 discovers /p2 while finishing under pause
				_, _ = fmt.Fprint(w, `<html><body><a href="/p2">P2</a></body></html>`)
			case "/p2":
				_, _ = fmt.Fprint(w, `<html><body>Page 2</body></html>`)
			default:
				http.NotFound(w, r)
			}
		}))
		defer ts.Close()

		cfg := Config{
			StartURL:           ts.URL + "/",
			WorkerCount:        1, // Single worker ensures sequential dispatch
			MaxPages:           5,
			MaxDepth:           2,
			RequestTimeout:     5 * time.Second,
			StorageDir:         filepath.Join(tempDir, "pages"),
			CheckpointDir:      filepath.Join(tempDir, "checkpoints"),
			CheckpointInterval: 0,
			SameDomainOnly:     true,
			AllowLocal:         true,
		}

		ctrl, err := NewController(cfg, nil)
		if err != nil {
			t.Fatalf("NewController failed: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		runDone := make(chan error, 1)
		go func() {
			runDone <- ctrl.Run(ctx)
		}()

		// Wait until /p1 is being fetched
		select {
		case <-p1Started:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for /p1 to start")
		}

		// Pause while /p1 is downloading
		if err := ctrl.Pause(); err != nil {
			t.Fatalf("Pause failed: %v", err)
		}
		if !ctrl.IsPaused() {
			t.Fatal("expected controller to be paused")
		}

		// Release /p1 to finish downloading while paused
		close(p1Release)

		// Wait until in_progress drops to 0 (worker finished /p1 and reported result)
		deadline := time.Now().Add(3 * time.Second)
		for {
			stats := ctrl.GetStats()
			if stats.InProgress == 0 && stats.Completed == 2 { // root and /p1 completed
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for /p1 to finish; stats: %+v", ctrl.GetStats())
			}
			time.Sleep(10 * time.Millisecond)
		}

		// State verification: Paused, InProgress == 0, Pending == 1 (/p2)
		stats := ctrl.GetStats()
		if !ctrl.IsPaused() {
			t.Errorf("expected controller to be paused")
		}
		if stats.InProgress != 0 {
			t.Errorf("expected InProgress == 0, got %d", stats.InProgress)
		}
		if stats.Pending != 1 {
			t.Errorf("expected Pending == 1, got %d", stats.Pending)
		}

		// Verify controller remains alive and does NOT terminate naturally
		select {
		case err := <-runDone:
			t.Fatalf("controller terminated prematurely while paused with pending work: %v", err)
		case <-time.After(150 * time.Millisecond):
			// Expected: controller remains alive, blocked waiting on select, no busy loop
		}

		// Verify pending jobs remain intact in URL manager
		statsAfterWait := ctrl.GetStats()
		if statsAfterWait.Pending != 1 || statsAfterWait.Completed != 2 {
			t.Errorf("state corrupted while waiting: %+v", statsAfterWait)
		}

		// Resume: dispatches /p2 and crawl completes naturally
		if err := ctrl.Resume(); err != nil {
			t.Fatalf("Resume failed: %v", err)
		}

		select {
		case err := <-runDone:
			if err != nil {
				t.Errorf("controller returned error after resume: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for crawl to complete after resume")
		}

		finalStats := ctrl.GetStats()
		if finalStats.Completed != 3 { // root, /p1, /p2
			t.Errorf("expected 3 completed pages, got %d", finalStats.Completed)
		}
		if finalStats.Pending != 0 || finalStats.InProgress != 0 {
			t.Errorf("expected 0 pending and 0 in_progress at end, got pending=%d, in_progress=%d",
				finalStats.Pending, finalStats.InProgress)
		}
	})

	t.Run("Stop terminates cleanly and preserves pending in checkpoint", func(t *testing.T) {
		tempDir, err := os.MkdirTemp("", "crawlsync_paused_pending_stop_*")
		if err != nil {
			t.Fatalf("failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tempDir)

		p1Started := make(chan struct{})
		p1Release := make(chan struct{})
		var p1Once sync.Once

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			switch r.URL.Path {
			case "/":
				_, _ = fmt.Fprint(w, `<html><body><a href="/p1">P1</a></body></html>`)
			case "/p1":
				p1Once.Do(func() { close(p1Started) })
				<-p1Release
				_, _ = fmt.Fprint(w, `<html><body><a href="/p2">P2</a></body></html>`)
			case "/p2":
				_, _ = fmt.Fprint(w, `<html><body>Page 2</body></html>`)
			default:
				http.NotFound(w, r)
			}
		}))
		defer ts.Close()

		cfg := Config{
			StartURL:           ts.URL + "/",
			WorkerCount:        1,
			MaxPages:           5,
			MaxDepth:           2,
			RequestTimeout:     5 * time.Second,
			StorageDir:         filepath.Join(tempDir, "pages"),
			CheckpointDir:      filepath.Join(tempDir, "checkpoints"),
			CheckpointInterval: 0,
			SameDomainOnly:     true,
			AllowLocal:         true,
		}

		ctrl, err := NewController(cfg, nil)
		if err != nil {
			t.Fatalf("NewController failed: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		runDone := make(chan error, 1)
		go func() {
			runDone <- ctrl.Run(ctx)
		}()

		select {
		case <-p1Started:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for /p1")
		}

		_ = ctrl.Pause()
		close(p1Release)

		// Wait for /p1 to finish: InProgress == 0, Pending == 1 (/p2)
		deadline := time.Now().Add(3 * time.Second)
		for {
			stats := ctrl.GetStats()
			if stats.InProgress == 0 && stats.Completed == 2 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for /p1 to finish; stats: %+v", ctrl.GetStats())
			}
			time.Sleep(10 * time.Millisecond)
		}

		// Stop from this state
		if err := ctrl.Stop(); err != nil {
			t.Fatalf("Stop failed: %v", err)
		}

		select {
		case err := <-runDone:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("expected context.Canceled on Stop, got: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("controller did not stop cleanly")
		}

		// Verify shutdown checkpoint preserved /p2 as pending
		cpPath := filepath.Join(cfg.CheckpointDir, state.CheckpointFileName)
		cp, err := state.Load(cpPath)
		if err != nil {
			t.Fatalf("failed loading checkpoint: %v", err)
		}
		if cp.Stats.Pending < 1 {
			t.Errorf("expected pending >= 1 in shutdown checkpoint, got %d", cp.Stats.Pending)
		}
		if cp.Stats.Completed != 2 {
			t.Errorf("expected 2 completed in shutdown checkpoint, got %d", cp.Stats.Completed)
		}
	})
}

// TestController_ConcurrentControlRequests verifies thread-safety, race freedom, and
// absence of deadlocks under chaotic concurrent Pause, Resume, and inspection calls.
func TestController_ConcurrentControlRequests(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_concurrent_ctrl_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		time.Sleep(5 * time.Millisecond)
		switch r.URL.Path {
		case "/":
			_, _ = fmt.Fprint(w, `<html><body><a href="/p1">1</a><a href="/p2">2</a><a href="/p3">3</a></body></html>`)
		default:
			_, _ = fmt.Fprint(w, `<html><body>Page</body></html>`)
		}
	}))
	defer ts.Close()

	cfg := Config{
		StartURL:           ts.URL + "/",
		WorkerCount:        3,
		MaxPages:           10,
		MaxDepth:           2,
		RequestTimeout:     2 * time.Second,
		StorageDir:         filepath.Join(tempDir, "pages"),
		CheckpointDir:      filepath.Join(tempDir, "checkpoints"),
		CheckpointInterval: 0,
		SameDomainOnly:     true,
		AllowLocal:         true,
	}

	ctrl, err := NewController(cfg, nil)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- ctrl.Run(ctx)
	}()

	const numGoroutines = 8
	const opsPerGoroutine = 12
	var wg sync.WaitGroup

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		goroutineID := g
		go func() {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				switch (goroutineID + i) % 4 {
				case 0:
					_ = ctrl.Pause()
				case 1:
					_ = ctrl.Resume()
				case 2:
					_ = ctrl.State()
					_ = ctrl.IsPaused()
					_ = ctrl.IsRunning()
				case 3:
					_ = ctrl.GetStats()
					_ = ctrl.GetWorkerStates()
				}
				time.Sleep(2 * time.Millisecond)
			}
		}()
	}

	wg.Wait()

	// Ensure crawler is resumed so it can conclude or stop cleanly
	_ = ctrl.Resume()

	select {
	case err := <-runDone:
		if err != nil {
			t.Errorf("crawl finished with error: %v", err)
		}
	case <-time.After(3 * time.Second):
		_ = ctrl.Stop()
		<-runDone
	}

	// Verify consistent state
	st := ctrl.State()
	if st != StateStopped {
		t.Errorf("expected controller to be stopped, got: %s", st)
	}

	// Verify no URL was duplicate-scheduled in URL manager
	urls := ctrl.GetURLManager().Snapshot().URLs
	seen := make(map[string]bool)
	for _, u := range urls {
		if seen[u.Normalized] {
			t.Errorf("duplicate URL found in URL manager: %s", u.Normalized)
		}
		seen[u.Normalized] = true
	}
}

// TestController_PeriodicCheckpointWhilePaused verifies that periodic checkpointing
// continues operating while the controller is paused:
// 1. Controller remains paused.
// 2. Ticker continues firing and sequence advances.
// 3. Worker results that finish while paused are reflected in snapshots.
// 4. Pending URLs remain pending.
func TestController_PeriodicCheckpointWhilePaused(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_periodic_paused_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	p1Started := make(chan struct{})
	var p1Once sync.Once

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/":
			// Seed page links to /p1 only
			_, _ = fmt.Fprint(w, `<html><body><a href="/p1">P1</a></body></html>`)
		case "/p1":
			p1Once.Do(func() { close(p1Started) })
			// Brief delay to ensure pause takes effect while /p1 is downloading
			time.Sleep(30 * time.Millisecond)
			// /p1 discovers /p2 while finishing under pause
			_, _ = fmt.Fprint(w, `<html><body><a href="/p2">P2</a></body></html>`)
		case "/p2":
			_, _ = fmt.Fprint(w, `<html><body>Page 2</body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	cfg := Config{
		StartURL:           ts.URL + "/",
		WorkerCount:        1,
		MaxPages:           10,
		MaxDepth:           2,
		RequestTimeout:     2 * time.Second,
		StorageDir:         filepath.Join(tempDir, "pages"),
		CheckpointDir:      filepath.Join(tempDir, "checkpoints"),
		CheckpointInterval: 25 * time.Millisecond, // Fast periodic ticker
		SameDomainOnly:     true,
		AllowLocal:         true,
	}

	ctrl, err := NewController(cfg, nil)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- ctrl.Run(ctx)
	}()

	// Wait until /p1 fetch starts
	select {
	case <-p1Started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for /p1 to start")
	}

	// Pause immediately while /p1 is downloading
	if err := ctrl.Pause(); err != nil {
		t.Fatalf("Pause failed: %v", err)
	}

	if !ctrl.IsPaused() {
		t.Fatal("expected controller to be paused")
	}

	// Wait until periodic ticker fires and advances checkpoint sequence while paused
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ctrl.CheckpointManager().SaveCount() >= 2 && ctrl.GetStats().Completed >= 2 {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}

	if ctrl.CheckpointManager().SaveCount() < 2 {
		t.Fatalf("failed to observe periodic checkpoint saveCount >= 2 while paused; got %d",
			ctrl.CheckpointManager().SaveCount())
	}

	// Verifications while still paused:
	// 1. Controller remained paused throughout
	if !ctrl.IsPaused() {
		t.Errorf("expected controller to remain paused")
	}

	// 2. Stop controller cleanly
	_ = ctrl.Stop()
	<-runDone

	// 3. Inspect final saved checkpoint from disk
	cpPath := filepath.Join(cfg.CheckpointDir, state.CheckpointFileName)
	cp, err := state.Load(cpPath)
	if err != nil {
		t.Fatalf("failed loading checkpoint from disk: %v", err)
	}

	// 4. Verify sequence advanced
	if cp.Sequence < 2 {
		t.Errorf("expected sequence >= 2, got %d", cp.Sequence)
	}

	// 5. Worker result for /p1 was processed and reflected in checkpoint
	if cp.Stats.Completed != 2 { // root and /p1
		t.Errorf("expected 2 completed in periodic checkpoint, got %d", cp.Stats.Completed)
	}

	// 6. /p2 remains pending
	if cp.Stats.Pending < 1 {
		t.Errorf("expected at least 1 pending URL in checkpoint, got %d", cp.Stats.Pending)
	}
}

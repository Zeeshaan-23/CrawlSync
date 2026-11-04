package controller

import (
	"context"
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

// TestRecovery_CrashSimulationAndFullGraphCompletion implements the primary end-to-end recovery test:
// 1. Starts Controller 1 against a controlled link graph.
// 2. Allows a subset of pages to download and saves a checkpoint.
// 3. Simulates process crash/interruption and terminates Controller 1.
// 4. Instantiates a completely NEW URLManager and NEW Controller from the checkpoint.
// 5. Verifies already-completed pages are NOT downloaded again.
// 6. Completes the remaining link graph to 100% resolution.
func TestRecovery_CrashSimulationAndFullGraphCompletion(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_rec_e2e_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	storageDir := filepath.Join(tempDir, "pages")
	checkpointDir := filepath.Join(tempDir, "checkpoints")

	var requests sync.Map

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		val, _ := requests.LoadOrStore(r.URL.Path, new(int64))
		counter := val.(*int64)
		atomic.AddInt64(counter, 1)

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/":
			_, _ = fmt.Fprint(w, `<html><body><a href="/p1">P1</a><a href="/p2">P2</a></body></html>`)
		case "/p1":
			_, _ = fmt.Fprint(w, `<html><body><a href="/p3">P3</a></body></html>`)
		case "/p2":
			_, _ = fmt.Fprint(w, `<html><body><a href="/p4">P4</a></body></html>`)
		case "/p3":
			_, _ = fmt.Fprint(w, `<html><body>Leaf 3</body></html>`)
		case "/p4":
			_, _ = fmt.Fprint(w, `<html><body>Leaf 4</body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	// --- PHASE A: Run Controller 1 until root and /p1 complete ---
	cfg1 := Config{
		StartURL:           ts.URL + "/",
		WorkerCount:        2,
		MaxPages:           10,
		MaxDepth:           3,
		RequestTimeout:     2 * time.Second,
		StorageDir:         storageDir,
		CheckpointDir:      checkpointDir,
		CheckpointInterval: 0,
		SameDomainOnly:     true,
		AllowLocal:         true,
	}

	mgr1 := urlmanager.NewManager()
	ctrl1, err := NewController(cfg1, mgr1)
	if err != nil {
		t.Fatalf("NewController 1 failed: %v", err)
	}

	ctx1, cancel1 := context.WithCancel(context.Background())
	runDone1 := make(chan error, 1)
	go func() {
		runDone1 <- ctrl1.Run(ctx1)
	}()

	// Wait until at least 2 pages (root and /p1 or /p2) are completed
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ctrl1.GetStats().Completed >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if ctrl1.GetStats().Completed < 2 {
		t.Fatalf("controller 1 failed to reach 2 completed pages; stats: %+v", ctrl1.GetStats())
	}

	// Stop Controller 1 (saving final shutdown checkpoint)
	cancel1()
	_ = ctrl1.Stop()
	<-runDone1

	snap1 := ctrl1.GetStats()
	sessionID := ctrl1.SessionID()

	// Record request counts for completed pages after Controller 1 exits
	snapshotCounts := make(map[string]int64)
	requests.Range(func(key, value any) bool {
		snapshotCounts[key.(string)] = atomic.LoadInt64(value.(*int64))
		return true
	})

	// --- PHASE B: Recover using NEW URLManager and NEW Controller ---
	recoveredCP, err := state.LoadWithFallback(checkpointDir, state.RecoveryValidationOptions{
		AllowLocal: true,
		StorageDir: storageDir,
	})
	if err != nil {
		t.Fatalf("LoadWithFallback failed: %v", err)
	}

	if recoveredCP.SessionID != sessionID {
		t.Errorf("expected recovered session %s, got %s", sessionID, recoveredCP.SessionID)
	}

	mgr2 := urlmanager.NewManager()
	if err := mgr2.Restore(recoveredCP.URLs, urlmanager.RestoreOptions{
		StorageDir:  storageDir,
		RetryFailed: false,
	}); err != nil {
		t.Fatalf("mgr2.Restore failed: %v", err)
	}

	cfg2 := Config{
		StartURL:           recoveredCP.Config.StartURL,
		WorkerCount:        2,
		MaxPages:           recoveredCP.Config.MaxPages,
		MaxDepth:           recoveredCP.Config.MaxDepth,
		RequestTimeout:     2 * time.Second,
		StorageDir:         storageDir,
		CheckpointDir:      checkpointDir,
		CheckpointInterval: 0,
		SameDomainOnly:     recoveredCP.Config.SameDomainOnly,
		AllowLocal:         true,
		SessionID:          recoveredCP.SessionID,
		CumulativeElapsed: recoveredCP.CumulativeElapsed,
		InitialSequence:    recoveredCP.Sequence,
		IsResumed:          true,
	}

	ctrl2, err := NewController(cfg2, mgr2)
	if err != nil {
		t.Fatalf("NewController 2 failed: %v", err)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()

	if err := ctrl2.Run(ctx2); err != nil {
		t.Fatalf("Controller 2 run failed: %v", err)
	}

	// --- PHASE C: Verifications ---
	stats2 := mgr2.GetStats()
	if stats2.Completed != 5 { // /, /p1, /p2, /p3, /p4
		t.Errorf("expected 5 completed pages in recovered run, got %d (stats: %+v)", stats2.Completed, stats2.TotalDiscovered)
	}
	if stats2.Pending != 0 || stats2.InProgress != 0 {
		t.Errorf("expected 0 pending and 0 in_progress at completion, got pending=%d, in_progress=%d",
			stats2.Pending, stats2.InProgress)
	}

	// Assert that pages completed in Run 1 were NOT requested again in Run 2
	for path, countBefore := range snapshotCounts {
		val, ok := requests.Load(path)
		if !ok {
			continue
		}
		countAfter := atomic.LoadInt64(val.(*int64))

		// If this path was completed in Run 1, its count must NOT have increased in Run 2
		normURL, _ := urlmanager.NormalizeURL(ts.URL + path)
		if item, ok := mgr1.GetStatus(normURL); ok && item.Status == urlmanager.StatusCompleted {
			if countAfter != countBefore {
				t.Errorf("page %s was already completed in Run 1 but was re-fetched in Run 2 (before=%d, after=%d)",
					path, countBefore, countAfter)
			}
		}
	}

	_ = snap1
}

// TestRecovery_InProgressRestoredToPending verifies that URLs left in_progress during a crash
// are restored to pending and retried upon recovery.
func TestRecovery_InProgressRestoredToPending(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_rec_inprog_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	storageDir := filepath.Join(tempDir, "pages")
	_ = os.MkdirAll(storageDir, 0755)

	records := []urlmanager.URLItem{
		{
			Sequence:   1,
			URL:        "https://example.com/",
			Normalized: "https://example.com/",
			Status:     urlmanager.StatusCompleted,
			Depth:      0,
			SavedPath:  filepath.Join(storageDir, "page_1.html"),
		},
		{
			Sequence:   2,
			URL:        "https://example.com/interrupted",
			Normalized: "https://example.com/interrupted",
			Status:     urlmanager.StatusInProgress, // interrupted
			Depth:      1,
		},
	}

	// Create dummy page for completed URL
	_ = os.WriteFile(records[0].SavedPath, []byte("<html>hello</html>"), 0644)

	mgr := urlmanager.NewManager()
	if err := mgr.Restore(records, urlmanager.RestoreOptions{
		StorageDir:  storageDir,
		RetryFailed: false,
	}); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	item, ok := mgr.GetStatus("https://example.com/interrupted")
	if !ok {
		t.Fatal("expected /interrupted to be present in restored manager")
	}
	if item.Status != urlmanager.StatusPending {
		t.Errorf("expected in_progress URL to be demoted to pending, got: %s", item.Status)
	}

	pending := mgr.GetPendingItems()
	if len(pending) != 1 || pending[0].Normalized != "https://example.com/interrupted" {
		t.Errorf("expected pending items to contain /interrupted, got %+v", pending)
	}
}

// TestRecovery_PendingOrderPreservesDiscoverySequence verifies that reconstructed pending jobs
// follow discovery sequence ascending.
func TestRecovery_PendingOrderPreservesDiscoverySequence(t *testing.T) {
	records := []urlmanager.URLItem{
		{Sequence: 4, URL: "https://example.com/p4", Normalized: "https://example.com/p4", Status: urlmanager.StatusPending, Depth: 2},
		{Sequence: 2, URL: "https://example.com/p2", Normalized: "https://example.com/p2", Status: urlmanager.StatusPending, Depth: 1},
		{Sequence: 1, URL: "https://example.com/p1", Normalized: "https://example.com/p1", Status: urlmanager.StatusCompleted, Depth: 0},
		{Sequence: 3, URL: "https://example.com/p3", Normalized: "https://example.com/p3", Status: urlmanager.StatusPending, Depth: 1},
	}

	mgr := urlmanager.NewManager()
	_ = mgr.Restore(records, urlmanager.RestoreOptions{})

	pending := mgr.GetPendingItems()
	if len(pending) != 3 {
		t.Fatalf("expected 3 pending items, got %d", len(pending))
	}

	expectedOrder := []string{"https://example.com/p2", "https://example.com/p3", "https://example.com/p4"}
	for i, exp := range expectedOrder {
		if pending[i].Normalized != exp {
			t.Errorf("position %d: expected %s, got %s (seq %d)", i, exp, pending[i].Normalized, pending[i].Sequence)
		}
	}
}

// TestRecovery_MissingSavedFileDemotedToPending verifies that completed URLs whose saved HTML files
// are missing from disk are demoted to pending.
func TestRecovery_MissingSavedFileDemotedToPending(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_rec_missing_file_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	storageDir := filepath.Join(tempDir, "pages")
	_ = os.MkdirAll(storageDir, 0755)

	existingPath := filepath.Join(storageDir, "existing.html")
	_ = os.WriteFile(existingPath, []byte("<html>OK</html>"), 0644)

	missingPath := filepath.Join(storageDir, "missing.html") // not created

	records := []urlmanager.URLItem{
		{
			Sequence:   1,
			URL:        "https://example.com/existing",
			Normalized: "https://example.com/existing",
			Status:     urlmanager.StatusCompleted,
			SavedPath:  existingPath,
		},
		{
			Sequence:   2,
			URL:        "https://example.com/missing",
			Normalized: "https://example.com/missing",
			Status:     urlmanager.StatusCompleted,
			SavedPath:  missingPath, // file does not exist
		},
	}

	mgr := urlmanager.NewManager()
	_ = mgr.Restore(records, urlmanager.RestoreOptions{StorageDir: storageDir})

	item1, _ := mgr.GetStatus("https://example.com/existing")
	if item1.Status != urlmanager.StatusCompleted {
		t.Errorf("expected existing file to remain completed, got %s", item1.Status)
	}

	item2, _ := mgr.GetStatus("https://example.com/missing")
	if item2.Status != urlmanager.StatusPending {
		t.Errorf("expected missing file to be demoted to pending, got %s", item2.Status)
	}
	if item2.SavedPath != "" {
		t.Errorf("expected demoted URL SavedPath to be cleared, got: %s", item2.SavedPath)
	}
}

// TestRecovery_NextSequenceExceedsEveryExistingSequence verifies sequence monotonicity.
func TestRecovery_NextSequenceExceedsEveryExistingSequence(t *testing.T) {
	records := []urlmanager.URLItem{
		{Sequence: 1, URL: "https://example.com/1", Normalized: "https://example.com/1", Status: urlmanager.StatusCompleted},
		{Sequence: 7, URL: "https://example.com/7", Normalized: "https://example.com/7", Status: urlmanager.StatusPending},
		{Sequence: 3, URL: "https://example.com/3", Normalized: "https://example.com/3", Status: urlmanager.StatusCompleted},
	}

	mgr := urlmanager.NewManager()
	_ = mgr.Restore(records, urlmanager.RestoreOptions{})

	// Next scheduled URL should receive sequence 8
	sched, norm, err := mgr.TrySchedule("https://example.com/new", 1)
	if err != nil || !sched {
		t.Fatalf("TrySchedule failed: %v", err)
	}

	item, _ := mgr.GetStatus(norm)
	if item.Sequence != 8 {
		t.Errorf("expected next sequence to be 8, got %d", item.Sequence)
	}
}

// TestRecovery_FailedURLs_PreservedByDefault verifies that failed URLs remain failed unless retry is enabled.
func TestRecovery_FailedURLs_PreservedByDefault(t *testing.T) {
	records := []urlmanager.URLItem{
		{
			Sequence:     1,
			URL:          "https://example.com/fail",
			Normalized:   "https://example.com/fail",
			Status:       urlmanager.StatusFailed,
			ErrorMessage: "404 Not Found",
			StatusCode:   404,
		},
	}

	mgr := urlmanager.NewManager()
	_ = mgr.Restore(records, urlmanager.RestoreOptions{RetryFailed: false})

	item, _ := mgr.GetStatus("https://example.com/fail")
	if item.Status != urlmanager.StatusFailed {
		t.Errorf("expected failed URL to remain failed, got %s", item.Status)
	}
	if item.ErrorMessage != "404 Not Found" {
		t.Errorf("expected error message to be preserved, got %s", item.ErrorMessage)
	}

	pending := mgr.GetPendingItems()
	if len(pending) != 0 {
		t.Errorf("expected 0 pending items, got %d", len(pending))
	}
}

// TestRecovery_FailedURLs_RetriedWithFlag verifies that failed URLs are demoted to pending when RetryFailed is true.
func TestRecovery_FailedURLs_RetriedWithFlag(t *testing.T) {
	records := []urlmanager.URLItem{
		{
			Sequence:     1,
			URL:          "https://example.com/fail",
			Normalized:   "https://example.com/fail",
			Status:       urlmanager.StatusFailed,
			ErrorMessage: "500 Server Error",
			StatusCode:   500,
		},
	}

	mgr := urlmanager.NewManager()
	_ = mgr.Restore(records, urlmanager.RestoreOptions{RetryFailed: true})

	item, _ := mgr.GetStatus("https://example.com/fail")
	if item.Status != urlmanager.StatusPending {
		t.Errorf("expected failed URL to be demoted to pending with RetryFailed, got %s", item.Status)
	}
	if item.ErrorMessage != "" {
		t.Errorf("expected error message to be cleared, got %s", item.ErrorMessage)
	}
	if item.Sequence != 1 {
		t.Errorf("expected original sequence 1 to be preserved, got %d", item.Sequence)
	}

	pending := mgr.GetPendingItems()
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending item, got %d", len(pending))
	}
}

// TestRecovery_CumulativeElapsedAcrossMultipleCycles verifies runtime accumulator across recovery cycles.
func TestRecovery_CumulativeElapsedAcrossMultipleCycles(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_rec_elapsed_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(30 * time.Millisecond)
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<html><body>Done</body></html>`)
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
		CumulativeElapsed: 10 * time.Second, // Simulating 10s from previous session
		SessionID:          "session_elapsed_test",
		IsResumed:          false,
	}

	ctrl, err := NewController(cfg, nil)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_ = ctrl.Run(ctx)

	totalElapsed := ctrl.CumulativeElapsed()
	if totalElapsed < 10*time.Second {
		t.Errorf("expected total elapsed >= 10s, got %v", totalElapsed)
	}

	// Verify checkpoint persisted cumulative elapsed
	cpPath := filepath.Join(cfg.CheckpointDir, state.CheckpointFileName)
	cp, err := state.Load(cpPath)
	if err != nil {
		t.Fatalf("failed loading checkpoint: %v", err)
	}
	if cp.CumulativeElapsed < 10*time.Second {
		t.Errorf("expected checkpoint cumulativeElapsed >= 10s, got %v", cp.CumulativeElapsed)
	}
}

// TestRecovery_AlreadyCompleteSession_DoesNotStartWorkers verifies clean exit when no pending URLs remain.
func TestRecovery_AlreadyCompleteSession_DoesNotStartWorkers(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_rec_already_comp_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	storageDir := filepath.Join(tempDir, "pages")
	_ = os.MkdirAll(storageDir, 0755)

	savedPath := filepath.Join(storageDir, "done.html")
	_ = os.WriteFile(savedPath, []byte("<html>Done</html>"), 0644)

	records := []urlmanager.URLItem{
		{
			Sequence:   1,
			URL:        "https://example.com/",
			Normalized: "https://example.com/",
			Status:     urlmanager.StatusCompleted,
			SavedPath:  savedPath,
		},
	}

	mgr := urlmanager.NewManager()
	_ = mgr.Restore(records, urlmanager.RestoreOptions{StorageDir: storageDir})

	cfg := Config{
		StartURL:           "https://example.com/",
		WorkerCount:        2,
		MaxPages:           5,
		MaxDepth:           2,
		RequestTimeout:     2 * time.Second,
		StorageDir:         storageDir,
		CheckpointDir:      filepath.Join(tempDir, "checkpoints"),
		CheckpointInterval: 0,
		SameDomainOnly:     true,
		AllowLocal:         true,
		IsResumed:          true,
	}

	ctrl, err := NewController(cfg, mgr)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	ctx := context.Background()
	// Must return nil immediately without hanging or running workers
	err = ctrl.Run(ctx)
	if err != nil {
		t.Errorf("expected clean nil return on already complete session, got: %v", err)
	}
}

// TestRecovery_CompatibleWithPauseResumeStop verifies pause, resume, and stop work on a recovered controller.
func TestRecovery_CompatibleWithPauseResumeStop(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_rec_pause_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(30 * time.Millisecond)
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<html><body><a href="/leaf">Leaf</a></body></html>`)
	}))
	defer ts.Close()

	records := []urlmanager.URLItem{
		{
			Sequence:   1,
			URL:        ts.URL + "/",
			Normalized: ts.URL + "/",
			Status:     urlmanager.StatusPending,
		},
	}

	mgr := urlmanager.NewManager()
	_ = mgr.Restore(records, urlmanager.RestoreOptions{})

	cfg := Config{
		StartURL:           ts.URL + "/",
		WorkerCount:        1,
		MaxPages:           5,
		MaxDepth:           2,
		RequestTimeout:     2 * time.Second,
		StorageDir:         filepath.Join(tempDir, "pages"),
		CheckpointDir:      filepath.Join(tempDir, "checkpoints"),
		CheckpointInterval: 0,
		SameDomainOnly:     true,
		AllowLocal:         true,
		IsResumed:          true,
	}

	ctrl, err := NewController(cfg, mgr)
	if err != nil {
		t.Fatalf("NewController failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- ctrl.Run(ctx)
	}()

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

	// Stop
	if err := ctrl.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	select {
	case err := <-runDone:
		if err != nil && err != context.Canceled {
			t.Errorf("unexpected error on Stop: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("controller did not terminate on Stop")
	}
}


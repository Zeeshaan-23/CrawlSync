package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"crawlsync/internal/state"
	"crawlsync/internal/urlmanager"
)

// TestServer_RecoveryLifecycle verifies:
// 1. Restoring a checkpoint containing completed and pending URLs into the server.
// 2. Initializing the service in StatePaused awaiting operator resume.
// 3. Retrieving restored session metadata, stats, and URL items through the REST API.
// 4. Resuming the recovered crawl session via POST /api/crawl/resume.
// 5. Verifying that pending URLs are processed while previously completed URLs are NOT redownloaded.
func TestServer_RecoveryLifecycle(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_server_recovery_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	storageDir := filepath.Join(tempDir, "pages")
	checkpointDir := filepath.Join(tempDir, "checkpoints")
	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		t.Fatalf("failed creating storage dir: %v", err)
	}
	if err := os.MkdirAll(checkpointDir, 0o755); err != nil {
		t.Fatalf("failed creating checkpoint dir: %v", err)
	}

	var completed1Requests int64
	var completed2Requests int64
	var pending1Requests int64
	var pending2Requests int64

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/completed1":
			atomic.AddInt64(&completed1Requests, 1)
			_, _ = fmt.Fprint(w, `<html><body>Completed 1</body></html>`)
		case "/completed2":
			atomic.AddInt64(&completed2Requests, 1)
			_, _ = fmt.Fprint(w, `<html><body>Completed 2</body></html>`)
		case "/pending1":
			atomic.AddInt64(&pending1Requests, 1)
			_, _ = fmt.Fprint(w, `<html><body>Pending Page 1</body></html>`)
		case "/pending2":
			atomic.AddInt64(&pending2Requests, 1)
			_, _ = fmt.Fprint(w, `<html><body>Pending Page 2</body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	normRoot, _ := urlmanager.NormalizeURL(ts.URL + "/")
	normC1, _ := urlmanager.NormalizeURL(ts.URL + "/completed1")
	normC2, _ := urlmanager.NormalizeURL(ts.URL + "/completed2")
	normP1, _ := urlmanager.NormalizeURL(ts.URL + "/pending1")
	normP2, _ := urlmanager.NormalizeURL(ts.URL + "/pending2")

	// Pre-create saved HTML files for completed URLs so disk validation passes
	savedPath1 := filepath.Join(storageDir, "completed1.html")
	savedPath2 := filepath.Join(storageDir, "completed2.html")
	if err := os.WriteFile(savedPath1, []byte("<html><body>Completed 1</body></html>"), 0o644); err != nil {
		t.Fatalf("failed writing saved file: %v", err)
	}
	if err := os.WriteFile(savedPath2, []byte("<html><body>Completed 2</body></html>"), 0o644); err != nil {
		t.Fatalf("failed writing saved file: %v", err)
	}

	urls := []urlmanager.URLItem{
		{
			URL:        ts.URL + "/",
			Normalized: normRoot,
			Status:     urlmanager.StatusCompleted,
			Depth:      0,
			Sequence:   1,
			StatusCode: 200,
			Size:       40,
			SavedPath:  savedPath1,
		},
		{
			URL:        ts.URL + "/completed1",
			Normalized: normC1,
			Status:     urlmanager.StatusCompleted,
			Depth:      1,
			Sequence:   2,
			StatusCode: 200,
			Size:       40,
			SavedPath:  savedPath1,
		},
		{
			URL:        ts.URL + "/completed2",
			Normalized: normC2,
			Status:     urlmanager.StatusCompleted,
			Depth:      1,
			Sequence:   3,
			StatusCode: 200,
			Size:       40,
			SavedPath:  savedPath2,
		},
		{
			URL:        ts.URL + "/pending1",
			Normalized: normP1,
			Status:     urlmanager.StatusPending,
			Depth:      1,
			Sequence:   4,
		},
		{
			URL:        ts.URL + "/pending2",
			Normalized: normP2,
			Status:     urlmanager.StatusPending,
			Depth:      1,
			Sequence:   5,
		},
	}

	cpConfig := state.CheckpointConfig{
		StartURL:       ts.URL + "/",
		WorkerCount:    2,
		MaxPages:       10,
		MaxDepth:       2,
		RequestTimeout: "5s",
		SameDomainOnly: true,
		StorageDir:     storageDir,
	}

	cm := state.NewCheckpointManager(checkpointDir)
	_, err = cm.Save("session_test_recovery_999", cpConfig, urlmanager.Snapshot{
		URLs: urls,
		Stats: urlmanager.Stats{
			TotalDiscovered: 5,
			Completed:       3, // Root + 2 completed
			Pending:         2,
		},
	}, 4*time.Second)
	if err != nil {
		t.Fatalf("failed saving initial test checkpoint: %v", err)
	}

	// 1. Initialize CrawlerService and recover session with pauseOnStart = true
	svc := NewCrawlerService(ServiceOptions{
		DefaultStorageDir:    storageDir,
		DefaultCheckpointDir: checkpointDir,
		AllowLocal:           true,
	})

	info, err := svc.RestoreSession(StartCrawlRequest{
		StorageDir:    storageDir,
		CheckpointDir: checkpointDir,
		Resume:        true,
	}, true)
	if err != nil {
		t.Fatalf("svc.RestoreSession failed: %v", err)
	}

	if info.State != StatePaused {
		t.Fatalf("expected recovered session state StatePaused, got %s", info.State)
	}
	if info.SessionID != "session_test_recovery_999" {
		t.Fatalf("expected session ID session_test_recovery_999, got %s", info.SessionID)
	}

	// 2. Start HTTP Server
	srv, err := NewServer(ServerConfig{
		Host:          "127.0.0.1",
		EphemeralPort: true,
		Service:       svc,
	})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	if err := srv.Start(); err != nil {
		t.Fatalf("srv.Start failed: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	client := &http.Client{Timeout: 5 * time.Second}
	apiBase := "http://" + srv.Addr()

	// 3. Verify GET /api/crawl/status returns recovered session information
	respStatus, err := client.Get(apiBase + "/api/crawl/status")
	if err != nil || respStatus.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/crawl/status failed: code=%v, err=%v", respStatus.StatusCode, err)
	}
	var statusDTO APIResponse
	if err := json.NewDecoder(respStatus.Body).Decode(&statusDTO); err != nil {
		t.Fatalf("decoding status JSON failed: %v", err)
	}
	_ = respStatus.Body.Close()

	statusMap := statusDTO.Data.(map[string]any)
	if statusMap["state"] != "PAUSED" {
		t.Errorf("expected status state PAUSED, got %v", statusMap["state"])
	}
	if statusMap["is_resumed"] != true {
		t.Errorf("expected is_resumed true, got %v", statusMap["is_resumed"])
	}
	if statusMap["is_paused"] != true {
		t.Errorf("expected is_paused true, got %v", statusMap["is_paused"])
	}
	if statusMap["session_id"] != "session_test_recovery_999" {
		t.Errorf("expected restored session_id, got %v", statusMap["session_id"])
	}

	// 4. Verify GET /api/crawl/stats returns recovered statistics
	respStats, err := client.Get(apiBase + "/api/crawl/stats")
	if err != nil || respStats.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/crawl/stats failed: code=%v, err=%v", respStats.StatusCode, err)
	}
	var statsDTO APIResponse
	if err := json.NewDecoder(respStats.Body).Decode(&statsDTO); err != nil {
		t.Fatalf("decoding stats JSON failed: %v", err)
	}
	_ = respStats.Body.Close()

	statsMap := statsDTO.Data.(map[string]any)
	if int(statsMap["completed"].(float64)) != 3 { // root + 2 completed
		t.Errorf("expected completed=3, got %v", statsMap["completed"])
	}
	if int(statsMap["pending"].(float64)) != 2 {
		t.Errorf("expected pending=2, got %v", statsMap["pending"])
	}
	if int(statsMap["in_progress"].(float64)) != 0 {
		t.Errorf("expected in_progress=0, got %v", statsMap["in_progress"])
	}

	// 5. Verify GET /api/urls returns all 5 recovered URLs
	respURLs, err := client.Get(apiBase + "/api/urls?limit=10")
	if err != nil || respURLs.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/urls failed: code=%v, err=%v", respURLs.StatusCode, err)
	}
	var urlsDTO APIResponse
	if err := json.NewDecoder(respURLs.Body).Decode(&urlsDTO); err != nil {
		t.Fatalf("decoding urls JSON failed: %v", err)
	}
	_ = respURLs.Body.Close()

	urlsMap := urlsDTO.Data.(map[string]any)
	items := urlsMap["items"].([]any)
	if len(items) != 5 {
		t.Fatalf("expected 5 recovered URL items, got %d", len(items))
	}

	// 6. Verify NO requests were dispatched while paused
	if c1 := atomic.LoadInt64(&completed1Requests); c1 != 0 {
		t.Errorf("expected 0 requests to /completed1, got %d", c1)
	}
	if p1 := atomic.LoadInt64(&pending1Requests); p1 != 0 {
		t.Errorf("expected 0 requests to /pending1 while paused, got %d", p1)
	}

	// 7. Resume the crawl via POST /api/crawl/resume
	respResume, err := client.Post(apiBase+"/api/crawl/resume", "application/json", nil)
	if err != nil || respResume.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/crawl/resume failed: code=%v, err=%v", respResume.StatusCode, err)
	}
	_ = respResume.Body.Close()

	// 8. Wait for crawl to complete
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if svc.GetStatus().State == StateCompleted {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if svc.GetStatus().State != StateCompleted {
		t.Fatalf("expected crawl to complete after resume, got %s", svc.GetStatus().State)
	}

	// 9. CRITICAL CONCURRENCY & RECOVERY ASSERTION:
	// Verify that pending URLs were fetched, but previously completed URLs were NEVER re-downloaded
	if p1 := atomic.LoadInt64(&pending1Requests); p1 != 1 {
		t.Errorf("expected /pending1 to be fetched exactly once, got %d", p1)
	}
	if p2 := atomic.LoadInt64(&pending2Requests); p2 != 1 {
		t.Errorf("expected /pending2 to be fetched exactly once, got %d", p2)
	}
	if c1 := atomic.LoadInt64(&completed1Requests); c1 != 0 {
		t.Errorf("REGRESSION DETECTED: previously completed URL /completed1 was redownloaded %d times", c1)
	}
	if c2 := atomic.LoadInt64(&completed2Requests); c2 != 0 {
		t.Errorf("REGRESSION DETECTED: previously completed URL /completed2 was redownloaded %d times", c2)
	}

	// 10. Verify final stats report all 5 completed
	finalStats, err := svc.GetStats()
	if err != nil {
		t.Fatalf("final GetStats failed: %v", err)
	}
	if finalStats.Completed != 5 {
		t.Errorf("expected 5 completed, got %d", finalStats.Completed)
	}
	if finalStats.Pending != 0 || finalStats.InProgress != 0 {
		t.Errorf("expected 0 pending and 0 in_progress, got pending=%d, in_progress=%d",
			finalStats.Pending, finalStats.InProgress)
	}
}

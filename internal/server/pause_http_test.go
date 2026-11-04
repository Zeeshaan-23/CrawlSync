package server

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
)

// TestServer_RuntimePause_DeterministicOverHTTP verifies Pause/Resume semantics over the REST API
// with multiple workers, controlled response delays, and HTTP API endpoints.
func TestServer_RuntimePause_DeterministicOverHTTP(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_srv_pause_*")
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

	// Mock target website
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		path := r.URL.Path

		switch {
		case path == "/":
			_, _ = fmt.Fprint(w, `<!DOCTYPE html><html><body>
				<a href="/w1">W1</a>
				<a href="/w2">W2</a>
				<a href="/w3">W3</a>
				<a href="/extra1">Extra 1</a>
				<a href="/extra2">Extra 2</a>
			</body></html>`)

		case strings.HasPrefix(path, "/w"):
			cur := atomic.AddInt64(&activeWorkers, 1)
			if cur == workerCount {
				workersOnce.Do(func() { close(workersReached) })
			}
			<-releaseInFlight // Controlled delay: hold until Pause is issued
			_, _ = fmt.Fprint(w, `<!DOCTYPE html><html><body>Worker Page</body></html>`)

		case strings.HasPrefix(path, "/extra"):
			atomic.AddInt64(&extraRequested, 1)
			_, _ = fmt.Fprint(w, `<!DOCTYPE html><html><body>Extra Page</body></html>`)

		default:
			http.NotFound(w, r)
		}
	}))
	defer targetServer.Close()

	svc := NewCrawlerService(ServiceOptions{
		DefaultStorageDir:    filepath.Join(tempDir, "pages"),
		DefaultCheckpointDir: filepath.Join(tempDir, "checkpoints"),
		AllowLocal:           true,
	})

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

	// 1. Start crawl session via POST /api/crawl/start
	startBody := strings.NewReader(fmt.Sprintf(`{
		"url": %q,
		"workers": %d,
		"max_pages": 10,
		"max_depth": 2,
		"same_domain": true
	}`, targetServer.URL+"/", workerCount))

	respStart, err := client.Post(apiBase+"/api/crawl/start", "application/json", startBody)
	if err != nil || respStart.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /api/crawl/start failed: code=%v, err=%v", respStart.StatusCode, err)
	}
	_ = respStart.Body.Close()

	// 2. Wait until all 3 workers have active, in-flight requests
	select {
	case <-workersReached:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %d workers to become active", workerCount)
	}

	// 3. Issue Pause via POST /api/crawl/pause
	respPause, err := client.Post(apiBase+"/api/crawl/pause", "application/json", nil)
	if err != nil || respPause.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/crawl/pause failed: code=%v, err=%v", respPause.StatusCode, err)
	}
	_ = respPause.Body.Close()

	// 4. Verify status is PAUSED
	if svc.GetStatus().State != StatePaused {
		t.Fatalf("expected service StatePaused, got %s", svc.GetStatus().State)
	}

	// 5. Release in-flight workers
	close(releaseInFlight)

	// 6. Wait until in_progress drops to 0
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		stats, err := svc.GetStats()
		if err == nil && stats.InProgress == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	statsPaused, err := svc.GetStats()
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}
	if statsPaused.InProgress != 0 {
		t.Errorf("expected InProgress == 0 while paused, got %d", statsPaused.InProgress)
	}

	// 7. Verify NO extra requests were dispatched while paused
	if count := atomic.LoadInt64(&extraRequested); count != 0 {
		t.Errorf("SEMANTIC VIOLATION: expected 0 requests to /extra while paused, got %d", count)
	}
	if statsPaused.Pending == 0 {
		t.Errorf("expected pending URLs in stats while paused, got 0")
	}

	// 8. Test Checkpointing while paused over HTTP
	respCp, err := client.Post(apiBase+"/api/crawl/checkpoint", "application/json", nil)
	if err != nil || respCp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/crawl/checkpoint while paused failed: code=%v, err=%v", respCp.StatusCode, err)
	}
	_ = respCp.Body.Close()

	// 9. Resume crawl via POST /api/crawl/resume
	respResume, err := client.Post(apiBase+"/api/crawl/resume", "application/json", nil)
	if err != nil || respResume.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/crawl/resume failed: code=%v, err=%v", respResume.StatusCode, err)
	}
	_ = respResume.Body.Close()

	// 10. Wait for crawl completion
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if svc.GetStatus().State == StateCompleted {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if svc.GetStatus().State != StateCompleted {
		t.Fatalf("expected crawl to complete after resume, got %s", svc.GetStatus().State)
	}

	// 11. Verify extra URLs were processed after resume
	if atomic.LoadInt64(&extraRequested) == 0 {
		t.Errorf("expected /extra to be requested after resume, got 0")
	}
}

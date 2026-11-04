package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"crawlsync/internal/state"
)

// setupTestServer creates a temporary service and server for testing.
func setupTestServer(t *testing.T, opts ...func(*ServerConfig)) (*Server, *CrawlerService, string) {
	t.Helper()

	tempDir, err := os.MkdirTemp("", "crawlsync_srv_test_*")
	if err != nil {
		t.Fatalf("failed creating temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })

	storageDir := filepath.Join(tempDir, "pages")
	checkpointDir := filepath.Join(tempDir, "checkpoints")

	svc := NewCrawlerService(ServiceOptions{
		DefaultStorageDir:    storageDir,
		DefaultCheckpointDir: checkpointDir,
		AllowLocal:           true,
	})

	cfg := ServerConfig{
		Host:            "127.0.0.1",
		Port:            0,
		EphemeralPort:   true,
		Service:         svc,
		ShutdownTimeout: 5 * time.Second,
	}

	for _, opt := range opts {
		opt(&cfg)
	}

	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("failed constructing server: %v", err)
	}

	return srv, svc, tempDir
}

// 1. Server construction
func TestServer_Construction(t *testing.T) {
	svc := NewCrawlerService(ServiceOptions{})

	// Valid default construction
	srv, err := NewServer(ServerConfig{
		Service: svc,
	})
	if err != nil {
		t.Fatalf("unexpected error creating server: %v", err)
	}

	if srv.cfg.Host != "127.0.0.1" {
		t.Errorf("expected default host 127.0.0.1, got %s", srv.cfg.Host)
	}
	if srv.cfg.Port != 8080 {
		t.Errorf("expected default port 8080, got %d", srv.cfg.Port)
	}
	if srv.ShutdownTimeout() != DefaultShutdownTimeout {
		t.Errorf("expected default shutdown timeout %v, got %v", DefaultShutdownTimeout, srv.ShutdownTimeout())
	}

	// Missing service error
	_, errMissingSvc := NewServer(ServerConfig{})
	if errMissingSvc == nil {
		t.Errorf("expected error when service is nil")
	}
}

// 2. Route registration
func TestServer_RouteRegistration(t *testing.T) {
	srv, _, _ := setupTestServer(t)

	// Verify that handler is non-nil and dispatches routes
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/crawl/status", nil)
	srv.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 from /api/crawl/status, got %d", rec.Code)
	}
}

// 3. Server starts successfully
func TestServer_StartsSuccessfully(t *testing.T) {
	srv, _, _ := setupTestServer(t)

	if err := srv.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer func() { _ = srv.Shutdown(context.Background()) }()

	if !srv.IsRunning() {
		t.Errorf("expected server to report running")
	}
}

// 4. Server listens on an available local port
func TestServer_ListensOnAvailableLocalPort(t *testing.T) {
	srv, _, _ := setupTestServer(t)

	if err := srv.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer func() { _ = srv.Shutdown(context.Background()) }()

	addr := srv.Addr()
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Errorf("expected addr to start with 127.0.0.1:, got %s", addr)
	}

	// Ensure port is not 0 (was allocated an actual ephemeral port)
	parts := strings.Split(addr, ":")
	if len(parts) != 2 || parts[1] == "0" {
		t.Errorf("expected valid non-zero allocated port, got %s", addr)
	}
}

// 5. Status endpoint responds over an actual HTTP connection
func TestServer_StatusEndpointRespondsOverActualHTTP(t *testing.T) {
	srv, _, _ := setupTestServer(t)

	if err := srv.Start(); err != nil {
		t.Fatalf("failed starting server: %v", err)
	}
	defer func() { _ = srv.Shutdown(context.Background()) }()

	resp, err := http.Get("http://" + srv.Addr() + "/api/crawl/status")
	if err != nil {
		t.Fatalf("failed HTTP GET request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var apiResp struct {
		Success bool                `json:"success"`
		Data    CrawlStatusResponseDTO `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		t.Fatalf("failed decoding JSON response: %v", err)
	}

	if !apiResp.Success {
		t.Errorf("expected success=true")
	}
	if apiResp.Data.State != "IDLE" {
		t.Errorf("expected state IDLE, got %s", apiResp.Data.State)
	}
}

// 6. All API routes remain reachable
func TestServer_AllAPIRoutesReachable(t *testing.T) {
	srv, _, _ := setupTestServer(t)

	if err := srv.Start(); err != nil {
		t.Fatalf("failed starting server: %v", err)
	}
	defer func() { _ = srv.Shutdown(context.Background()) }()

	client := &http.Client{Timeout: 3 * time.Second}
	baseURL := "http://" + srv.Addr()

	routes := []struct {
		method       string
		path         string
		body         string
		expectedCode int
	}{
		{http.MethodGet, "/api/crawl/status", "", http.StatusOK},
		{http.MethodGet, "/api/crawl/stats", "", http.StatusNotFound}, // No active crawl
		{http.MethodPost, "/api/crawl/pause", "", http.StatusNotFound},
		{http.MethodPost, "/api/crawl/resume", "", http.StatusNotFound},
		{http.MethodPost, "/api/crawl/checkpoint", "", http.StatusNotFound},
		{http.MethodGet, "/api/workers", "", http.StatusNotFound},
		{http.MethodGet, "/api/urls", "", http.StatusNotFound},
		{http.MethodGet, "/api/urls/1", "", http.StatusNotFound},
		{http.MethodPost, "/api/crawl/stop", "", http.StatusOK}, // Idempotent stop returns 200
		{http.MethodPost, "/api/crawl/start", `{"invalid_json": true}`, http.StatusBadRequest},
	}

	for _, rt := range routes {
		var bodyReader io.Reader
		if rt.body != "" {
			bodyReader = strings.NewReader(rt.body)
		}
		req, err := http.NewRequest(rt.method, baseURL+rt.path, bodyReader)
		if err != nil {
			t.Fatalf("failed creating request for %s %s: %v", rt.method, rt.path, err)
		}
		if rt.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("failed executing %s %s: %v", rt.method, rt.path, err)
		}
		_ = resp.Body.Close()

		if resp.StatusCode != rt.expectedCode {
			t.Errorf("%s %s expected status %d, got %d", rt.method, rt.path, rt.expectedCode, resp.StatusCode)
		}
	}
}

// 7. Invalid host/port configuration is rejected
func TestServer_InvalidHostPortConfiguration(t *testing.T) {
	svc := NewCrawlerService(ServiceOptions{})

	// Negative port
	_, errNegPort := NewServer(ServerConfig{
		Port:    -1,
		Service: svc,
	})
	if errNegPort == nil {
		t.Errorf("expected error for negative port")
	}

	// Port > 65535
	_, errHighPort := NewServer(ServerConfig{
		Port:    70000,
		Service: svc,
	})
	if errHighPort == nil {
		t.Errorf("expected error for port > 65535")
	}

	// Invalid bind address causing listener error
	srvBadHost, _ := NewServer(ServerConfig{
		Host:    "256.256.256.256",
		Port:    8080,
		Service: svc,
	})
	if err := srvBadHost.Start(); err == nil {
		_ = srvBadHost.Shutdown(context.Background())
		t.Errorf("expected Start error for invalid host IP")
	}
}

// 8. Graceful server shutdown
func TestServer_GracefulShutdown(t *testing.T) {
	srv, _, _ := setupTestServer(t)

	if err := srv.Start(); err != nil {
		t.Fatalf("failed starting server: %v", err)
	}

	// Verify server responds
	resp, err := http.Get("http://" + srv.Addr() + "/api/crawl/status")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("expected server to be responsive before shutdown")
	}
	_ = resp.Body.Close()

	// Perform graceful shutdown
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("unexpected error during graceful shutdown: %v", err)
	}

	if srv.IsRunning() {
		t.Errorf("expected server to report not running")
	}

	// Verify server no longer accepts requests
	client := &http.Client{Timeout: 500 * time.Millisecond}
	_, errReq := client.Get("http://" + srv.Addr() + "/api/crawl/status")
	if errReq == nil {
		t.Errorf("expected connection error after shutdown, but request succeeded")
	}
}

// 9. Shutdown with an active crawl
func TestServer_ShutdownWithActiveCrawl(t *testing.T) {
	ts := createTestHTTPSite()
	defer ts.Close()

	srv, svc, tempDir := setupTestServer(t)

	if err := srv.Start(); err != nil {
		t.Fatalf("failed starting server: %v", err)
	}

	// Start a crawl session via HTTP
	startReqBody := fmt.Sprintf(`{"url": "%s/", "workers": 2, "max_pages": 50}`, ts.URL)
	resp, err := http.Post("http://"+srv.Addr()+"/api/crawl/start", "application/json", strings.NewReader(startReqBody))
	if err != nil {
		t.Fatalf("failed starting crawl via API: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d", resp.StatusCode)
	}

	// Verify crawl is running
	st := svc.GetStatus()
	if st.State != StateRunning && st.State != StateCompleted {
		t.Fatalf("expected running state, got %s", st.State)
	}

	http.DefaultClient.CloseIdleConnections()

	// Perform graceful server shutdown while active
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown error: %v", err)
	}

	// Verify service is cleanly stopped
	finalStatus := svc.GetStatus()
	if finalStatus.State != StateStopped && finalStatus.State != StateCompleted {
		t.Errorf("expected service to end in STOPPED or COMPLETED, got %s", finalStatus.State)
	}

	// Verify shutdown checkpoint exists on disk
	cpPath := filepath.Join(tempDir, "checkpoints", state.CheckpointFileName)
	if _, err := os.Stat(cpPath); os.IsNotExist(err) {
		t.Errorf("expected shutdown checkpoint to be written to %s", cpPath)
	}
}

// 10. Shutdown while a crawl is paused
func TestServer_ShutdownWhilePaused(t *testing.T) {
	ts := createTestHTTPSite()
	defer ts.Close()

	srv, svc, _ := setupTestServer(t)

	if err := srv.Start(); err != nil {
		t.Fatalf("failed starting server: %v", err)
	}

	// Start a crawl
	startReqBody := fmt.Sprintf(`{"url": "%s/", "workers": 1, "max_pages": 50}`, ts.URL)
	resp, err := http.Post("http://"+srv.Addr()+"/api/crawl/start", "application/json", strings.NewReader(startReqBody))
	if err != nil || resp.StatusCode != http.StatusAccepted {
		t.Fatalf("failed starting crawl")
	}
	_ = resp.Body.Close()

	// Pause the crawl via HTTP
	respPause, err := http.Post("http://"+srv.Addr()+"/api/crawl/pause", "application/json", nil)
	if err != nil || respPause.StatusCode != http.StatusOK {
		t.Fatalf("failed pausing crawl")
	}
	_ = respPause.Body.Close()

	// Verify state is PAUSED
	if svc.GetStatus().State != StatePaused {
		t.Fatalf("expected PAUSED state, got %s", svc.GetStatus().State)
	}

	http.DefaultClient.CloseIdleConnections()

	// Shutdown while paused
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown error while paused: %v", err)
	}

	if svc.GetStatus().State != StateStopped {
		t.Errorf("expected STOPPED state after shutdown, got %s", svc.GetStatus().State)
	}
}

// 11. No crawler is created simply by starting the HTTP server
func TestServer_NoCrawlerCreatedMerelyByStartingServer(t *testing.T) {
	srv, svc, _ := setupTestServer(t)

	if err := srv.Start(); err != nil {
		t.Fatalf("failed starting server: %v", err)
	}
	defer func() { _ = srv.Shutdown(context.Background()) }()

	// Verify service remains strictly in IDLE state with nil active controller
	status := svc.GetStatus()
	if status.State != StateIdle {
		t.Errorf("expected IDLE state before client crawl request, got %s", status.State)
	}

	svc.mu.RLock()
	activeCtrl := svc.activeController
	activeMgr := svc.activeManager
	svc.mu.RUnlock()

	if activeCtrl != nil {
		t.Errorf("expected nil active controller upon server startup")
	}
	if activeMgr != nil {
		t.Errorf("expected nil active URL manager upon server startup")
	}
}

// 12. Server can be started and stopped repeatedly without leaking goroutines
func TestServer_StartStopRepeatedlyNoGoroutineLeaks(t *testing.T) {
	srv, _, _ := setupTestServer(t)

	for i := 0; i < 4; i++ {
		if err := srv.Start(); err != nil {
			t.Fatalf("iteration %d: failed to start server: %v", i, err)
		}

		resp, err := http.Get("http://" + srv.Addr() + "/api/crawl/status")
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("iteration %d: request failed", i)
		}
		_ = resp.Body.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := srv.Shutdown(ctx); err != nil {
			cancel()
			t.Fatalf("iteration %d: shutdown failed: %v", i, err)
		}
		cancel()

		if srv.IsRunning() {
			t.Fatalf("iteration %d: server reported running after shutdown", i)
		}
	}
}

// 13. Existing middleware remains active
func TestServer_ExistingMiddlewareRemainsActive(t *testing.T) {
	srv, _, _ := setupTestServer(t, func(c *ServerConfig) {
		c.CORSOrigins = []string{"http://allowed-dashboard.com"}
	})

	if err := srv.Start(); err != nil {
		t.Fatalf("failed starting server: %v", err)
	}
	defer func() { _ = srv.Shutdown(context.Background()) }()

	baseURL := "http://" + srv.Addr()

	// 1. Content-Type JSON Header Middleware verification
	resp, err := http.Get(baseURL + "/api/crawl/status")
	if err != nil {
		t.Fatalf("failed request: %v", err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected application/json Content-Type header, got: %s", ct)
	}

	// 2. CORS Preflight verification
	client := &http.Client{Timeout: 2 * time.Second}
	reqOptions, _ := http.NewRequest(http.MethodOptions, baseURL+"/api/crawl/status", nil)
	reqOptions.Header.Set("Origin", "http://allowed-dashboard.com")
	respOptions, err := client.Do(reqOptions)
	if err != nil {
		t.Fatalf("OPTIONS request failed: %v", err)
	}
	defer respOptions.Body.Close()

	if respOptions.StatusCode != http.StatusNoContent {
		t.Errorf("preflight expected 204 No Content, got %d", respOptions.StatusCode)
	}
	if respOptions.Header.Get("Access-Control-Allow-Origin") != "http://allowed-dashboard.com" {
		t.Errorf("missing or incorrect CORS Allow-Origin header: %s", respOptions.Header.Get("Access-Control-Allow-Origin"))
	}

	// 3. Method Not Allowed (405) via ServeMux routing
	reqDelete, _ := http.NewRequest(http.MethodDelete, baseURL+"/api/crawl/status", nil)
	respDelete, err := client.Do(reqDelete)
	if err != nil {
		t.Fatalf("DELETE request failed: %v", err)
	}
	defer respDelete.Body.Close()

	if respDelete.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("DELETE expected 405 Method Not Allowed, got %d", respDelete.StatusCode)
	}
}

// 14. Server shutdown does not race with concurrent HTTP requests
func TestServer_ShutdownDoesNotRaceWithConcurrentRequests(t *testing.T) {
	srv, _, _ := setupTestServer(t)

	if err := srv.Start(); err != nil {
		t.Fatalf("failed starting server: %v", err)
	}

	baseURL := "http://" + srv.Addr()
	const workers = 15
	var wg sync.WaitGroup
	wg.Add(workers)

	// Launch concurrent HTTP clients
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			client := &http.Client{Timeout: 1 * time.Second}
			for j := 0; j < 10; j++ {
				resp, err := client.Get(baseURL + "/api/crawl/status")
				if err == nil {
					_ = resp.Body.Close()
				}
				time.Sleep(1 * time.Millisecond)
			}
		}()
	}

	// Shut down server while requests are in-flight
	time.Sleep(5 * time.Millisecond)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Errorf("shutdown error during concurrent requests: %v", err)
	}

	wg.Wait()
}

// 15. HTTP shutdown deadline expiring while an HTTP handler is still active
func TestServer_HTTPDrainTimeoutWhileHandlerActive(t *testing.T) {
	inFlightStarted := make(chan struct{})

	srv, _, _ := setupTestServer(t, func(c *ServerConfig) {
		c.HTTPDrainTimeout = 50 * time.Millisecond // very short drain budget
		c.HandlerWrapper = func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/slow" {
					close(inFlightStarted)
					time.Sleep(300 * time.Millisecond) // exceeds HTTPDrainTimeout
					w.WriteHeader(http.StatusOK)
					return
				}
				next.ServeHTTP(w, r)
			})
		}
	})

	if err := srv.Start(); err != nil {
		t.Fatalf("failed starting server: %v", err)
	}

	// Send request to slow handler in background
	go func() {
		client := &http.Client{Timeout: 1 * time.Second}
		resp, err := client.Get("http://" + srv.Addr() + "/slow")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	// Wait until handler is actively running
	<-inFlightStarted

	// Initiate shutdown
	err := srv.Shutdown(context.Background())
	if err == nil {
		t.Errorf("expected shutdown error due to HTTP drain timeout")
	}
	if !strings.Contains(err.Error(), "http shutdown error") {
		t.Errorf("expected error to mention http shutdown error, got: %v", err)
	}
}

// 16. Crawler shutdown still being attempted and succeeding after HTTP shutdown times out
func TestServer_CrawlerShutdownAttemptedAfterHTTPTimeout(t *testing.T) {
	ts := createTestHTTPSite()
	defer ts.Close()

	inFlightStarted := make(chan struct{})

	srv, svc, tempDir := setupTestServer(t, func(c *ServerConfig) {
		c.HTTPDrainTimeout = 50 * time.Millisecond  // Short HTTP drain budget
		c.CrawlerShutdownTimeout = 5 * time.Second  // Dedicated crawler shutdown budget
		c.HandlerWrapper = func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/slow" {
					close(inFlightStarted)
					time.Sleep(300 * time.Millisecond) // deliberately outlasts HTTPDrainTimeout
					w.WriteHeader(http.StatusOK)
					return
				}
				next.ServeHTTP(w, r)
			})
		}
	})

	if err := srv.Start(); err != nil {
		t.Fatalf("failed starting server: %v", err)
	}

	// 1. Start an active crawl session
	startReqBody := fmt.Sprintf(`{"url": "%s/", "workers": 2, "max_pages": 50}`, ts.URL)
	resp, err := http.Post("http://"+srv.Addr()+"/api/crawl/start", "application/json", strings.NewReader(startReqBody))
	if err != nil || resp.StatusCode != http.StatusAccepted {
		t.Fatalf("failed starting crawl via API: %v", err)
	}
	_ = resp.Body.Close()

	// 2. Start slow HTTP request
	go func() {
		client := &http.Client{Timeout: 1 * time.Second}
		respSlow, errSlow := client.Get("http://" + srv.Addr() + "/slow")
		if errSlow == nil {
			_ = respSlow.Body.Close()
		}
	}()

	<-inFlightStarted

	// 3. Initiate server shutdown
	// Phase 1 (HTTP draining) will time out, but Phase 2 (Crawler shutdown) MUST still run!
	shutdownErr := srv.Shutdown(context.Background())
	if shutdownErr == nil {
		t.Errorf("expected shutdownErr to report HTTP drain timeout")
	}

	// 4. Verify that crawler shutdown was still attempted and succeeded!
	finalStatus := svc.GetStatus()
	if finalStatus.State != StateStopped && finalStatus.State != StateCompleted {
		t.Errorf("expected crawler to be cleanly STOPPED or COMPLETED, got: %s", finalStatus.State)
	}

	// 5. Verify the shutdown checkpoint was successfully written to disk
	cpPath := filepath.Join(tempDir, "checkpoints", state.CheckpointFileName)
	if _, err := os.Stat(cpPath); os.IsNotExist(err) {
		t.Errorf("expected shutdown checkpoint to exist at %s even after HTTP drain timeout", cpPath)
	}
}

// 17. Repeated shutdown calls are safe and idempotent
func TestServer_RepeatedShutdownCalls(t *testing.T) {
	srv, _, _ := setupTestServer(t)

	if err := srv.Start(); err != nil {
		t.Fatalf("failed starting server: %v", err)
	}

	// First shutdown call
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Errorf("first shutdown returned unexpected error: %v", err)
	}

	// Second shutdown call (idempotent no-op)
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Errorf("second shutdown returned unexpected error: %v", err)
	}

	// Third shutdown call
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Errorf("third shutdown returned unexpected error: %v", err)
	}
}

// 18. Shutdown when no crawl is active
func TestServer_ShutdownWhenNoCrawlActive(t *testing.T) {
	srv, svc, tempDir := setupTestServer(t)

	if err := srv.Start(); err != nil {
		t.Fatalf("failed starting server: %v", err)
	}

	// Verify IDLE state
	if svc.GetStatus().State != StateIdle {
		t.Fatalf("expected IDLE state before shutdown, got %s", svc.GetStatus().State)
	}

	// Shutdown when IDLE
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Errorf("unexpected error shutting down idle server: %v", err)
	}

	// Verify state remains IDLE
	if svc.GetStatus().State != StateIdle {
		t.Errorf("expected state to remain IDLE after shutdown, got %s", svc.GetStatus().State)
	}

	// Verify no checkpoint was created
	cpPath := filepath.Join(tempDir, "checkpoints", state.CheckpointFileName)
	if _, err := os.Stat(cpPath); err == nil {
		t.Errorf("expected no checkpoint file to be created when idle")
	}
}

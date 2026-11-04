package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// setupTestHandler initializes a CrawlerService and returns the configured http.Handler.
func setupTestHandler(t *testing.T) (*CrawlerService, http.Handler, string) {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "crawlsync_hdlr_test_*")
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

	handler := NewHandler(HandlerConfig{
		Service:     svc,
		CORSOrigins: []string{"http://localhost:3000"},
	})

	return svc, handler, tempDir
}

// 1. Successful responses across endpoints
func TestHandlers_SuccessfulResponses(t *testing.T) {
	ts := createTestHTTPSite()
	defer ts.Close()

	svc, handler, _ := setupTestHandler(t)

	// A. POST /api/crawl/start
	startBody := `{"url":"` + ts.URL + `/","workers":2,"max_pages":5,"max_depth":2}`
	req := httptest.NewRequest(http.MethodPost, "/api/crawl/start", strings.NewReader(startBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /api/crawl/start expected 202, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	var startResp APIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &startResp); err != nil || !startResp.Success {
		t.Fatalf("expected valid success envelope: %v", err)
	}

	// B. GET /api/crawl/status
	reqStatus := httptest.NewRequest(http.MethodGet, "/api/crawl/status", nil)
	recStatus := httptest.NewRecorder()
	handler.ServeHTTP(recStatus, reqStatus)
	if recStatus.Code != http.StatusOK {
		t.Errorf("GET /api/crawl/status expected 200, got %d", recStatus.Code)
	}

	// Wait until at least 1 URL is discovered
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st, err := svc.GetStats()
		if err == nil && st.TotalDiscovered >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// C. GET /api/crawl/stats
	reqStats := httptest.NewRequest(http.MethodGet, "/api/crawl/stats", nil)
	recStats := httptest.NewRecorder()
	handler.ServeHTTP(recStats, reqStats)
	if recStats.Code != http.StatusOK {
		t.Errorf("GET /api/crawl/stats expected 200, got %d", recStats.Code)
	}

	// D. GET /api/workers
	reqWorkers := httptest.NewRequest(http.MethodGet, "/api/workers", nil)
	recWorkers := httptest.NewRecorder()
	handler.ServeHTTP(recWorkers, reqWorkers)
	if recWorkers.Code != http.StatusOK {
		t.Errorf("GET /api/workers expected 200, got %d", recWorkers.Code)
	}

	// E. POST /api/crawl/pause
	reqPause := httptest.NewRequest(http.MethodPost, "/api/crawl/pause", nil)
	recPause := httptest.NewRecorder()
	handler.ServeHTTP(recPause, reqPause)
	if recPause.Code != http.StatusOK {
		t.Errorf("POST /api/crawl/pause expected 200, got %d", recPause.Code)
	}

	// F. POST /api/crawl/checkpoint
	reqCP := httptest.NewRequest(http.MethodPost, "/api/crawl/checkpoint", nil)
	recCP := httptest.NewRecorder()
	handler.ServeHTTP(recCP, reqCP)
	if recCP.Code != http.StatusOK {
		t.Errorf("POST /api/crawl/checkpoint expected 200, got %d", recCP.Code)
	}

	// G. POST /api/crawl/resume
	reqResume := httptest.NewRequest(http.MethodPost, "/api/crawl/resume", nil)
	recResume := httptest.NewRecorder()
	handler.ServeHTTP(recResume, reqResume)
	if recResume.Code != http.StatusOK {
		t.Errorf("POST /api/crawl/resume expected 200, got %d", recResume.Code)
	}

	// H. GET /api/urls
	reqURLs := httptest.NewRequest(http.MethodGet, "/api/urls?limit=10&offset=0", nil)
	recURLs := httptest.NewRecorder()
	handler.ServeHTTP(recURLs, reqURLs)
	if recURLs.Code != http.StatusOK {
		t.Errorf("GET /api/urls expected 200, got %d", recURLs.Code)
	}

	// I. GET /api/urls/1
	reqURL1 := httptest.NewRequest(http.MethodGet, "/api/urls/1", nil)
	recURL1 := httptest.NewRecorder()
	handler.ServeHTTP(recURL1, reqURL1)
	if recURL1.Code != http.StatusOK {
		t.Errorf("GET /api/urls/1 expected 200, got %d", recURL1.Code)
	}

	// J. POST /api/crawl/stop
	reqStop := httptest.NewRequest(http.MethodPost, "/api/crawl/stop", nil)
	recStop := httptest.NewRecorder()
	handler.ServeHTTP(recStop, reqStop)
	if recStop.Code != http.StatusOK {
		t.Errorf("POST /api/crawl/stop expected 200, got %d", recStop.Code)
	}
}

// 2. Invalid JSON syntax
func TestHandlers_InvalidJSON(t *testing.T) {
	_, handler, _ := setupTestHandler(t)

	malformedJSON := `{"url": "https://example.com", "workers": `
	req := httptest.NewRequest(http.MethodPost, "/api/crawl/start", strings.NewReader(malformedJSON))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for malformed JSON, got %d", rec.Code)
	}

	var errResp APIErrorResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &errResp)
	if errResp.Error.Code != "INVALID_JSON" {
		t.Errorf("expected code INVALID_JSON, got %s", errResp.Error.Code)
	}
}

// 3. Missing required fields
func TestHandlers_MissingRequiredFields(t *testing.T) {
	_, handler, _ := setupTestHandler(t)

	emptyBody := `{}`
	req := httptest.NewRequest(http.MethodPost, "/api/crawl/start", strings.NewReader(emptyBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing url field, got %d", rec.Code)
	}

	var errResp APIErrorResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &errResp)
	if errResp.Error.Code != "INVALID_CONFIGURATION" {
		t.Errorf("expected code INVALID_CONFIGURATION, got %s", errResp.Error.Code)
	}
}

// 4. Unknown JSON fields rejected
func TestHandlers_UnknownJSONFields(t *testing.T) {
	_, handler, _ := setupTestHandler(t)

	unknownBody := `{"url": "https://example.com", "malicious_injected_field": true}`
	req := httptest.NewRequest(http.MethodPost, "/api/crawl/start", strings.NewReader(unknownBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown field, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "unknown field") {
		t.Errorf("expected error message to mention unknown field, got: %s", rec.Body.String())
	}
}

// 5. Oversized request body rejected (> 1MB)
func TestHandlers_OversizedRequestBody(t *testing.T) {
	_, handler, _ := setupTestHandler(t)

	// Create oversized payload > 1MB
	oversized := bytes.Repeat([]byte("A"), 1024*1024+100)
	req := httptest.NewRequest(http.MethodPost, "/api/crawl/start", bytes.NewReader(oversized))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge && rec.Code != http.StatusBadRequest {
		t.Errorf("expected 413 or 400 for oversized payload, got %d", rec.Code)
	}
}

// 6. Invalid URL schemes rejected
func TestHandlers_InvalidURLSchemes(t *testing.T) {
	_, handler, _ := setupTestHandler(t)

	invalidSchemes := []string{
		`{"url": "ftp://example.com/file"}`,
		`{"url": "javascript:alert(1)"}`,
		`{"url": "example.com/no-scheme"}`,
		`{"url": "http://"}`,
	}

	for _, body := range invalidSchemes {
		req := httptest.NewRequest(http.MethodPost, "/api/crawl/start", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("payload %s expected 400, got %d", body, rec.Code)
		}
	}
}

// 7. Invalid numeric configuration bounds rejected
func TestHandlers_InvalidNumericConfiguration(t *testing.T) {
	_, handler, _ := setupTestHandler(t)

	badCases := []string{
		`{"url":"https://example.com","workers":0}`,
		`{"url":"https://example.com","workers":25}`,
		`{"url":"https://example.com","max_pages":0}`,
		`{"url":"https://example.com","max_depth":-1}`,
		`{"url":"https://example.com","timeout_seconds":0}`,
	}

	for _, body := range badCases {
		req := httptest.NewRequest(http.MethodPost, "/api/crawl/start", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("payload %s expected 400, got %d", body, rec.Code)
		}
	}
}

// 8. Invalid pagination parameters rejected
func TestHandlers_InvalidPaginationParameters(t *testing.T) {
	ts := createTestHTTPSite()
	defer ts.Close()
	svc, handler, _ := setupTestHandler(t)

	_, _ = svc.StartCrawl(StartCrawlRequest{URL: ts.URL + "/", Workers: 1, MaxPages: 5})

	invalidQueries := []string{
		"/api/urls?limit=0",
		"/api/urls?limit=101",
		"/api/urls?limit=abc",
		"/api/urls?offset=-1",
		"/api/urls?offset=xyz",
	}

	for _, path := range invalidQueries {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("query %s expected 400, got %d (body: %s)", path, rec.Code, rec.Body.String())
		}
	}

	_ = svc.StopCrawl()
}

// 8b. Pagination bounds, defaults, and offset edge cases
func TestHandlers_PaginationBoundsAndDefaults(t *testing.T) {
	ts := createTestHTTPSite()
	defer ts.Close()
	svc, handler, _ := setupTestHandler(t)

	_, _ = svc.StartCrawl(StartCrawlRequest{URL: ts.URL + "/", Workers: 1, MaxPages: 5})

	// Wait for at least 1 URL to be discovered
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st := svc.GetStatus()
		if st.State == StateRunning || st.State == StateCompleted {
			stats, err := svc.GetStats()
			if err == nil && stats.TotalDiscovered >= 1 {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
	}

	t.Run("NoLimitParameter_AppliesDefault20", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/urls", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}

		var resp struct {
			Success bool               `json:"success"`
			Data    URLListResponseDTO `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed unmarshaling response: %v", err)
		}

		if resp.Data.Limit != 20 {
			t.Errorf("expected default limit 20, got %d", resp.Data.Limit)
		}
		if resp.Data.Offset != 0 {
			t.Errorf("expected default offset 0, got %d", resp.Data.Offset)
		}
	})

	t.Run("Limit0_RejectedWith400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/urls?limit=0", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", rec.Code)
		}
	})

	t.Run("Limit100_Accepted", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/urls?limit=100", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}

		var resp struct {
			Success bool               `json:"success"`
			Data    URLListResponseDTO `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed unmarshaling response: %v", err)
		}

		if resp.Data.Limit != 100 {
			t.Errorf("expected limit 100, got %d", resp.Data.Limit)
		}
	})

	t.Run("Limit101_RejectedWith400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/urls?limit=101", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", rec.Code)
		}
	})

	t.Run("NegativeOffset_RejectedWith400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/urls?offset=-5", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", rec.Code)
		}
	})

	t.Run("OffsetBeyondFilteredCount_ReturnsEmptyItemsSlice", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/urls?offset=9999", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}

		var resp struct {
			Success bool               `json:"success"`
			Data    URLListResponseDTO `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed unmarshaling response: %v", err)
		}

		if len(resp.Data.Items) != 0 {
			t.Errorf("expected empty items slice, got %d items", len(resp.Data.Items))
		}
		if resp.Data.Offset != 9999 {
			t.Errorf("expected offset 9999 in response, got %d", resp.Data.Offset)
		}
		if resp.Data.Total < 1 {
			t.Errorf("expected total >= 1, got %d", resp.Data.Total)
		}
	})

	_ = svc.StopCrawl()
}

// 9. Invalid URL status filters rejected
func TestHandlers_InvalidURLStatusFilters(t *testing.T) {
	ts := createTestHTTPSite()
	defer ts.Close()
	svc, handler, _ := setupTestHandler(t)

	_, _ = svc.StartCrawl(StartCrawlRequest{URL: ts.URL + "/", Workers: 1, MaxPages: 5})

	req := httptest.NewRequest(http.MethodGet, "/api/urls?status=invalid_status", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid status filter, got %d", rec.Code)
	}

	_ = svc.StopCrawl()
}

// 10. Invalid sorting values rejected
func TestHandlers_InvalidSortingValues(t *testing.T) {
	ts := createTestHTTPSite()
	defer ts.Close()
	svc, handler, _ := setupTestHandler(t)

	_, _ = svc.StartCrawl(StartCrawlRequest{URL: ts.URL + "/", Workers: 1, MaxPages: 5})

	req := httptest.NewRequest(http.MethodGet, "/api/urls?sort=random_sort", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid sort parameter, got %d", rec.Code)
	}

	_ = svc.StopCrawl()
}

// 11. Missing URL sequence ID
func TestHandlers_MissingURLSequence(t *testing.T) {
	_, handler, _ := setupTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/urls/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// In Go 1.22+, /api/urls/ does not match /api/urls/{id} and returns 404
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusBadRequest {
		t.Errorf("expected 404 or 400, got %d", rec.Code)
	}
}

// 12. Invalid URL sequence ID rejected
func TestHandlers_InvalidURLSequence(t *testing.T) {
	ts := createTestHTTPSite()
	defer ts.Close()
	svc, handler, _ := setupTestHandler(t)

	_, _ = svc.StartCrawl(StartCrawlRequest{URL: ts.URL + "/", Workers: 1, MaxPages: 5})

	invalidIDs := []string{
		"/api/urls/abc",
		"/api/urls/0",
		"/api/urls/-5",
	}

	for _, path := range invalidIDs {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("query %s expected 400, got %d", path, rec.Code)
		}
	}

	_ = svc.StopCrawl()
}

// 13. No active crawl returns 404
func TestHandlers_NoActiveCrawl(t *testing.T) {
	_, handler, _ := setupTestHandler(t)

	endpoints := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/crawl/stats"},
		{http.MethodGet, "/api/urls"},
		{http.MethodGet, "/api/urls/1"},
		{http.MethodGet, "/api/workers"},
		{http.MethodPost, "/api/crawl/pause"},
		{http.MethodPost, "/api/crawl/resume"},
		{http.MethodPost, "/api/crawl/checkpoint"},
	}

	for _, ep := range endpoints {
		req := httptest.NewRequest(ep.method, ep.path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s expected 404, got %d (body: %s)", ep.method, ep.path, rec.Code, rec.Body.String())
		}
	}
}

// 14. Conflicting lifecycle operations return 409
func TestHandlers_ConflictingLifecycleOperations(t *testing.T) {
	ts := createTestHTTPSite()
	defer ts.Close()
	svc, handler, _ := setupTestHandler(t)

	// Start crawl
	_, _ = svc.StartCrawl(StartCrawlRequest{URL: ts.URL + "/", Workers: 1, MaxPages: 10})

	// Starting again while running returns 409 Conflict
	reqStartAgain := httptest.NewRequest(http.MethodPost, "/api/crawl/start", strings.NewReader(`{"url":"`+ts.URL+`/"}`))
	reqStartAgain.Header.Set("Content-Type", "application/json")
	recStartAgain := httptest.NewRecorder()
	handler.ServeHTTP(recStartAgain, reqStartAgain)

	if recStartAgain.Code != http.StatusConflict {
		t.Errorf("expected 409 Conflict for double start, got %d", recStartAgain.Code)
	}

	_ = svc.StopCrawl()
}

// 15. Unsupported HTTP methods return 405 Method Not Allowed
func TestHandlers_UnsupportedHTTPMethods(t *testing.T) {
	_, handler, _ := setupTestHandler(t)

	// GET on a POST endpoint
	req := httptest.NewRequest(http.MethodGet, "/api/crawl/start", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/crawl/start expected 405, got %d", rec.Code)
	}

	// POST on a GET endpoint
	reqPost := httptest.NewRequest(http.MethodPost, "/api/crawl/status", nil)
	recPost := httptest.NewRecorder()
	handler.ServeHTTP(recPost, reqPost)

	if recPost.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/crawl/status expected 405, got %d", recPost.Code)
	}
}

// 16. Consistent JSON response envelopes
func TestHandlers_ConsistentResponseEnvelopes(t *testing.T) {
	_, handler, _ := setupTestHandler(t)

	// Verify error envelope structure
	reqErr := httptest.NewRequest(http.MethodPost, "/api/crawl/start", strings.NewReader(`{"url":""}`))
	reqErr.Header.Set("Content-Type", "application/json")
	recErr := httptest.NewRecorder()
	handler.ServeHTTP(recErr, reqErr)

	var errResp APIErrorResponse
	if err := json.Unmarshal(recErr.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed unmarshaling error response: %v", err)
	}
	if errResp.Success {
		t.Errorf("expected success=false in error envelope")
	}
	if errResp.Error.Code == "" || errResp.Error.Message == "" {
		t.Errorf("error envelope missing code or message: %+v", errResp.Error)
	}

	// Verify success envelope structure
	reqOk := httptest.NewRequest(http.MethodGet, "/api/crawl/status", nil)
	recOk := httptest.NewRecorder()
	handler.ServeHTTP(recOk, reqOk)

	var okResp APIResponse
	if err := json.Unmarshal(recOk.Body.Bytes(), &okResp); err != nil {
		t.Fatalf("failed unmarshaling success response: %v", err)
	}
	if !okResp.Success {
		t.Errorf("expected success=true in success envelope")
	}
	if okResp.Data == nil {
		t.Errorf("expected non-nil data in success envelope")
	}
}

// 16b. Panic recovery middleware intercepts unhandled panics and outputs 500 error envelope
func TestHandlers_PanicRecoveryMiddleware(t *testing.T) {
	panickingHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("simulated unhandled runtime panic in endpoint handler")
	})

	recoveredHandler := panicRecoveryMiddleware(panickingHandler)

	req := httptest.NewRequest(http.MethodGet, "/test/panic", nil)
	rec := httptest.NewRecorder()

	// Ensure the test execution recovers and does not terminate the test process
	recoveredHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", rec.Code)
	}

	var errResp APIErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed unmarshaling 500 response envelope: %v", err)
	}

	if errResp.Success {
		t.Errorf("expected success=false in error envelope")
	}
	if errResp.Error.Code != "INTERNAL_ERROR" {
		t.Errorf("expected error code INTERNAL_ERROR, got %s", errResp.Error.Code)
	}
	if !strings.Contains(errResp.Error.Message, "internal server error") {
		t.Errorf("expected internal server error message, got %s", errResp.Error.Message)
	}
}

// 17. Concurrent read-only requests
func TestHandlers_ConcurrentReadOnlyRequests(t *testing.T) {
	ts := createTestHTTPSite()
	defer ts.Close()
	svc, handler, _ := setupTestHandler(t)

	_, _ = svc.StartCrawl(StartCrawlRequest{URL: ts.URL + "/", Workers: 2, MaxPages: 10})

	const clients = 8
	var wg sync.WaitGroup
	wg.Add(clients)

	for i := 0; i < clients; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				req := httptest.NewRequest(http.MethodGet, "/api/crawl/status", nil)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Errorf("status query failed with %d", rec.Code)
				}

				reqStats := httptest.NewRequest(http.MethodGet, "/api/crawl/stats", nil)
				recStats := httptest.NewRecorder()
				handler.ServeHTTP(recStats, reqStats)
			}
		}()
	}

	wg.Wait()
	_ = svc.StopCrawl()
}

// 18. Concurrent lifecycle requests
func TestHandlers_ConcurrentLifecycleRequests(t *testing.T) {
	ts := createTestHTTPSite()
	defer ts.Close()
	svc, handler, _ := setupTestHandler(t)

	_, _ = svc.StartCrawl(StartCrawlRequest{URL: ts.URL + "/", Workers: 2, MaxPages: 20})

	var wg sync.WaitGroup
	const actors = 6
	wg.Add(actors)

	endpoints := []string{
		"/api/crawl/pause",
		"/api/crawl/resume",
		"/api/crawl/checkpoint",
		"/api/crawl/pause",
		"/api/crawl/resume",
		"/api/crawl/stop",
	}

	for _, ep := range endpoints {
		path := ep
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			// Valid responses depending on race scheduling:
			// 200 OK: Action succeeded
			// 409 Conflict: Action conflicted with state transition (e.g., stopping)
			// 404 Not Found: Crawl already stopped by Stop goroutine before this request arrived
			if rec.Code != http.StatusOK && rec.Code != http.StatusConflict && rec.Code != http.StatusNotFound {
				t.Errorf("%s returned unexpected code %d: %s", path, rec.Code, rec.Body.String())
			}

			// Verify that every response produces a valid JSON envelope
			var jsonMap map[string]interface{}
			if err := json.Unmarshal(rec.Body.Bytes(), &jsonMap); err != nil {
				t.Errorf("%s returned malformed JSON: %v", path, err)
			}
		}()
	}

	wg.Wait()
}

// 19. Manual checkpoint endpoint
func TestHandlers_ManualCheckpointEndpoint(t *testing.T) {
	ts := createTestHTTPSite()
	defer ts.Close()
	svc, handler, _ := setupTestHandler(t)

	_, _ = svc.StartCrawl(StartCrawlRequest{URL: ts.URL + "/", Workers: 2, MaxPages: 10})

	req := httptest.NewRequest(http.MethodPost, "/api/crawl/checkpoint", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from manual checkpoint, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	var resp APIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed parsing checkpoint response: %v", err)
	}

	_ = svc.StopCrawl()
}

// 20. Worker state endpoint
func TestHandlers_WorkerStateEndpoint(t *testing.T) {
	ts := createTestHTTPSite()
	defer ts.Close()
	svc, handler, _ := setupTestHandler(t)

	_, _ = svc.StartCrawl(StartCrawlRequest{URL: ts.URL + "/", Workers: 3, MaxPages: 10})

	req := httptest.NewRequest(http.MethodGet, "/api/workers", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from /api/workers, got %d", rec.Code)
	}

	var resp struct {
		Success bool                  `json:"success"`
		Data    WorkerListResponseDTO `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed parsing workers response: %v", err)
	}

	if resp.Data.WorkerCount != 3 || len(resp.Data.Workers) != 3 {
		t.Errorf("expected 3 workers in pool, got count=%d, len=%d", resp.Data.WorkerCount, len(resp.Data.Workers))
	}

	_ = svc.StopCrawl()
}

// Test strict JSON decoding: single object accepted, multiple values and trailing text rejected
func TestHandlers_MultipleJSONValuesRejected(t *testing.T) {
	t.Run("SingleValidJSONObject_Accepted", func(t *testing.T) {
		ts := createTestHTTPSite()
		defer ts.Close()
		svc, handler, _ := setupTestHandler(t)

		singleJSON := `{"url":"` + ts.URL + `/", "workers": 1, "max_pages": 5}`
		req := httptest.NewRequest(http.MethodPost, "/api/crawl/start", strings.NewReader(singleJSON))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusAccepted {
			t.Fatalf("expected 202 Accepted for single valid JSON, got %d (body: %s)", rec.Code, rec.Body.String())
		}
		_ = svc.StopCrawl()
	})

	t.Run("TwoConsecutiveJSONObjects_Rejected", func(t *testing.T) {
		_, handler, _ := setupTestHandler(t)

		multiJSON := `{"url":"https://example.com"}{"url":"https://evil.com"}`
		req := httptest.NewRequest(http.MethodPost, "/api/crawl/start", strings.NewReader(multiJSON))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for two consecutive JSON objects, got %d", rec.Code)
		}
	})

	t.Run("ValidJSONObjectFollowedByArbitraryText_Rejected", func(t *testing.T) {
		_, handler, _ := setupTestHandler(t)

		trailingText := `{"url":"https://example.com"} random trailing text here`
		req := httptest.NewRequest(http.MethodPost, "/api/crawl/start", strings.NewReader(trailingText))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for JSON followed by arbitrary text, got %d", rec.Code)
		}
	})

	t.Run("ValidJSONObjectFollowedByAnotherJSONValue_Rejected", func(t *testing.T) {
		_, handler, _ := setupTestHandler(t)

		cases := []struct {
			name string
			body string
		}{
			{"FollowedByNumber", `{"url":"https://example.com"} 12345`},
			{"FollowedByString", `{"url":"https://example.com"} "unwanted_string"`},
			{"FollowedByBoolean", `{"url":"https://example.com"} true`},
			{"FollowedByArray", `{"url":"https://example.com"} [1, 2, 3]`},
			{"FollowedByNull", `{"url":"https://example.com"} null`},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodPost, "/api/crawl/start", strings.NewReader(tc.body))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)

				if rec.Code != http.StatusBadRequest {
					t.Errorf("case %s expected 400, got %d (body: %s)", tc.name, rec.Code, rec.Body.String())
				}
			})
		}
	})
}

// Test CORS preflight and origin verification
func TestHandlers_CORSVerification(t *testing.T) {
	_, handler, _ := setupTestHandler(t)

	// Valid origin
	req := httptest.NewRequest(http.MethodOptions, "/api/crawl/status", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight expected 204, got %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Errorf("expected CORS header for allowed origin, got: %s", rec.Header().Get("Access-Control-Allow-Origin"))
	}

	// Disallowed origin (no wildcard default)
	reqDisallowed := httptest.NewRequest(http.MethodOptions, "/api/crawl/status", nil)
	reqDisallowed.Header.Set("Origin", "http://malicious-site.com")
	recDisallowed := httptest.NewRecorder()
	handler.ServeHTTP(recDisallowed, reqDisallowed)

	if recDisallowed.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("expected no CORS header for disallowed origin, got: %s", recDisallowed.Header().Get("Access-Control-Allow-Origin"))
	}
}

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"crawlsync/web"
)

// HandlerConfig configures the HTTP router and API endpoints.
type HandlerConfig struct {
	Service     *CrawlerService
	CORSOrigins []string
	StaticFS    fs.FS  // Optional custom filesystem for dashboard assets (defaults to embedded web.Files())
	StaticDir   string // Optional on-disk directory for live frontend development
}

// apiHandler encapsulates endpoint handlers bound to the CrawlerService.
type apiHandler struct {
	service *CrawlerService
}

// NewHandler creates and registers the HTTP ServeMux with all routes, static file serving, and middleware.
func NewHandler(cfg HandlerConfig) http.Handler {
	mux := http.NewServeMux()
	h := &apiHandler{service: cfg.Service}

	// 1. Register API routes using Go 1.22+ method-aware routing patterns.
	// In Go 1.22 ServeMux, these exact /api/... routes take strict precedence over the root "GET /".
	mux.HandleFunc("POST /api/crawl/start", h.handleCrawlStart)
	mux.HandleFunc("GET /api/crawl/status", h.handleCrawlStatus)
	mux.HandleFunc("GET /api/crawl/stats", h.handleCrawlStats)
	mux.HandleFunc("POST /api/crawl/pause", h.handleCrawlPause)
	mux.HandleFunc("POST /api/crawl/resume", h.handleCrawlResume)
	mux.HandleFunc("POST /api/crawl/stop", h.handleCrawlStop)
	mux.HandleFunc("POST /api/crawl/checkpoint", h.handleCrawlCheckpoint)
	mux.HandleFunc("GET /api/workers", h.handleWorkers)
	mux.HandleFunc("GET /api/urls", h.handleURLList)
	mux.HandleFunc("GET /api/urls/{id}", h.handleURLDetail)

	// 2. Register static dashboard handler at "GET /"
	staticHandler := createStaticHandler(cfg)
	mux.Handle("GET /", staticHandler)

	return applyMiddleware(mux, cfg)
}

var postOnlyAPIRoutes = map[string]bool{
	"/api/crawl/start":      true,
	"/api/crawl/pause":      true,
	"/api/crawl/resume":     true,
	"/api/crawl/stop":       true,
	"/api/crawl/checkpoint": true,
}

// createStaticHandler builds a safe file server serving the CrawlSync dashboard.
// It prevents directory traversal, suppresses directory browsing, and returns JSON 404 for unknown /api/* requests.
func createStaticHandler(cfg HandlerConfig) http.Handler {
	var targetFS fs.FS

	// 1. If explicit StaticDir is configured and exists on disk, use it
	if cfg.StaticDir != "" {
		if fi, err := os.Stat(cfg.StaticDir); err == nil && fi.IsDir() {
			targetFS = os.DirFS(cfg.StaticDir)
		}
	}

	// 2. If running from repository root where "./web/index.html" exists, use local web dir for development
	if targetFS == nil {
		if fi, err := os.Stat("web/index.html"); err == nil && !fi.IsDir() {
			targetFS = os.DirFS("web")
		}
	}

	// 3. If explicit StaticFS is provided
	if targetFS == nil && cfg.StaticFS != nil {
		targetFS = cfg.StaticFS
	}

	// 4. Default to embedded web.Files()
	if targetFS == nil {
		targetFS = web.Files()
	}

	fileServer := http.FileServer(http.FS(targetFS))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cleanPath := path.Clean(r.URL.Path)

		// 1. If an exact POST-only API route was hit via GET, return 405 Method Not Allowed
		if postOnlyAPIRoutes[cleanPath] {
			w.Header().Set("Allow", "POST")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		// 2. Prevent path traversal attacks
		if strings.Contains(cleanPath, "..") || strings.Contains(r.URL.RawQuery, "..") {
			writeError(w, http.StatusBadRequest, "INVALID_PATH", "Invalid path traversal sequence")
			return
		}

		// 3. Unknown /api/* requests falling back to root handler return structured JSON 404
		if strings.HasPrefix(cleanPath, "/api/") {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "API endpoint not found")
			return
		}

		// 4. Disable directory browsing: if path ends with "/" and is not root "/", return 404
		if cleanPath != "/" && strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}

		// 5. If a directory without trailing slash is requested (e.g. /css or /js), deny directory listing
		trimmed := strings.TrimPrefix(cleanPath, "/")
		if trimmed != "" {
			if f, err := targetFS.Open(trimmed); err == nil {
				defer f.Close()
				if fi, err := f.Stat(); err == nil && fi.IsDir() {
					http.NotFound(w, r)
					return
				}
			}
		}

		fileServer.ServeHTTP(w, r)
	})
}

// Helper to write JSON responses
func writeJSON(w http.ResponseWriter, statusCode int, data any) {
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(APIResponse{
		Success: true,
		Data:    data,
	})
}

// Helper to write standardized error responses
func writeError(w http.ResponseWriter, statusCode int, code, message string) {
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(APIErrorResponse{
		Success: false,
		Error: APIErrorDetail{
			Code:    code,
			Message: message,
		},
	})
}

// httpError defines an error carrying HTTP status code and API error code.
type httpError struct {
	statusCode int
	code       string
	message    string
}

func (e *httpError) Error() string {
	return e.message
}

const maxRequestBodyBytes = 1024 * 1024 // 1 MB limit

// decodeJSONBody decodes JSON strictly: enforces 1MB cap, disallows unknown fields, and rejects multiple JSON values.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) error {
	ct := r.Header.Get("Content-Type")
	if ct != "" && !strings.HasPrefix(strings.ToLower(ct), "application/json") {
		return &httpError{
			statusCode: http.StatusUnsupportedMediaType,
			code:       "UNSUPPORTED_MEDIA_TYPE",
			message:    "Content-Type must be application/json",
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return &httpError{
				statusCode: http.StatusRequestEntityTooLarge,
				code:       "PAYLOAD_TOO_LARGE",
				message:    "Request body exceeds 1MB limit",
			}
		}
		var unmarshalErr *json.UnmarshalTypeError
		if errors.As(err, &unmarshalErr) {
			return &httpError{
				statusCode: http.StatusBadRequest,
				code:       "INVALID_JSON",
				message:    fmt.Sprintf("Invalid type for field %q (expected %s)", unmarshalErr.Field, unmarshalErr.Type),
			}
		}
		if strings.HasPrefix(err.Error(), "json: unknown field") {
			return &httpError{
				statusCode: http.StatusBadRequest,
				code:       "INVALID_JSON",
				message:    err.Error(),
			}
		}
		return &httpError{
			statusCode: http.StatusBadRequest,
			code:       "INVALID_JSON",
			message:    fmt.Sprintf("Malformed JSON request: %v", err),
		}
	}

	// Call dec.Decode() a second time; it MUST return io.EOF.
	// If it returns nil (another JSON value was decoded) or a syntax error (trailing arbitrary text),
	// the request is strictly rejected.
	var extra struct{}
	if err := dec.Decode(&extra); err != io.EOF {
		return &httpError{
			statusCode: http.StatusBadRequest,
			code:       "INVALID_JSON",
			message:    "Request body must contain only a single JSON object",
		}
	}

	return nil
}

// POST /api/crawl/start
func (h *apiHandler) handleCrawlStart(w http.ResponseWriter, r *http.Request) {
	var dto StartCrawlRequestDTO
	if err := decodeJSONBody(w, r, &dto); err != nil {
		if he, ok := err.(*httpError); ok {
			writeError(w, he.statusCode, he.code, he.message)
			return
		}
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}

	// Validate configuration boundaries
	isResume := dto.Resume != nil && *dto.Resume

	if !isResume {
		if strings.TrimSpace(dto.URL) == "" {
			writeError(w, http.StatusBadRequest, "INVALID_CONFIGURATION", "A valid HTTP or HTTPS URL is required")
			return
		}
		parsed, err := url.Parse(dto.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			writeError(w, http.StatusBadRequest, "INVALID_CONFIGURATION", "A valid HTTP or HTTPS URL is required")
			return
		}
	}

	req := StartCrawlRequest{
		URL: dto.URL,
	}

	if dto.Workers != nil {
		if *dto.Workers < 1 || *dto.Workers > 20 {
			writeError(w, http.StatusBadRequest, "INVALID_CONFIGURATION", "workers must be an integer between 1 and 20")
			return
		}
		req.Workers = *dto.Workers
	}

	if dto.MaxPages != nil {
		if *dto.MaxPages < 1 {
			writeError(w, http.StatusBadRequest, "INVALID_CONFIGURATION", "max_pages must be at least 1")
			return
		}
		req.MaxPages = *dto.MaxPages
	}

	if dto.MaxDepth != nil {
		if *dto.MaxDepth < 0 {
			writeError(w, http.StatusBadRequest, "INVALID_CONFIGURATION", "max_depth must be a non-negative integer")
			return
		}
		req.MaxDepth = *dto.MaxDepth
	}

	if dto.TimeoutSeconds != nil {
		if *dto.TimeoutSeconds < 1 {
			writeError(w, http.StatusBadRequest, "INVALID_CONFIGURATION", "timeout_seconds must be at least 1")
			return
		}
		req.TimeoutSeconds = *dto.TimeoutSeconds
	}

	req.SameDomain = dto.SameDomain
	if dto.Resume != nil {
		req.Resume = *dto.Resume
	}
	if dto.RetryFailed != nil {
		req.RetryFailed = *dto.RetryFailed
	}
	if dto.PauseOnStart != nil {
		req.PauseOnStart = *dto.PauseOnStart
	}

	sessionInfo, err := h.service.StartCrawl(req)
	if err != nil {
		h.mapServiceError(w, err)
		return
	}

	resp := CrawlSessionResponseDTO{
		SessionID:          sessionInfo.SessionID,
		State:              string(sessionInfo.State),
		StartURL:           sessionInfo.StartURL,
		WorkerCount:        sessionInfo.WorkerCount,
		MaxPages:           sessionInfo.MaxPages,
		MaxDepth:           sessionInfo.MaxDepth,
		SameDomain:         sessionInfo.SameDomain,
		StorageDir:         sessionInfo.StorageDir,
		CheckpointDir:      sessionInfo.CheckpointDir,
		CheckpointInterval: sessionInfo.CheckpointInterval.String(),
		IsResumed:          sessionInfo.IsResumed,
	}

	writeJSON(w, http.StatusAccepted, resp)
}

// GET /api/crawl/status
func (h *apiHandler) handleCrawlStatus(w http.ResponseWriter, r *http.Request) {
	st := h.service.GetStatus()

	resp := CrawlStatusResponseDTO{
		State:             string(st.State),
		SessionID:         st.SessionID,
		StartURL:          st.StartURL,
		WorkerCount:       st.WorkerCount,
		MaxPages:          st.MaxPages,
		MaxDepth:          st.MaxDepth,
		CumulativeElapsed: st.CumulativeElapsed.Milliseconds(),
		IsResumed:         st.IsResumed,
		IsPaused:          st.IsPaused,
		LastRunError:      st.LastRunError,
	}

	writeJSON(w, http.StatusOK, resp)
}

// GET /api/crawl/stats
func (h *apiHandler) handleCrawlStats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.service.GetStats()
	if err != nil {
		h.mapServiceError(w, err)
		return
	}

	resp := CrawlStatsResponseDTO{
		TotalDiscovered: stats.TotalDiscovered,
		Pending:         stats.Pending,
		InProgress:      stats.InProgress,
		Completed:       stats.Completed,
		Failed:          stats.Failed,
	}

	writeJSON(w, http.StatusOK, resp)
}

// POST /api/crawl/pause
func (h *apiHandler) handleCrawlPause(w http.ResponseWriter, r *http.Request) {
	if err := h.service.PauseCrawl(); err != nil {
		h.mapServiceError(w, err)
		return
	}

	st := h.service.GetStatus()
	resp := ControlActionResponseDTO{
		State:     string(st.State),
		SessionID: st.SessionID,
		Message:   "Crawl session paused",
	}

	writeJSON(w, http.StatusOK, resp)
}

// POST /api/crawl/resume
func (h *apiHandler) handleCrawlResume(w http.ResponseWriter, r *http.Request) {
	if err := h.service.ResumeCrawl(); err != nil {
		h.mapServiceError(w, err)
		return
	}

	st := h.service.GetStatus()
	resp := ControlActionResponseDTO{
		State:     string(st.State),
		SessionID: st.SessionID,
		Message:   "Crawl session resumed",
	}

	writeJSON(w, http.StatusOK, resp)
}

// POST /api/crawl/stop
func (h *apiHandler) handleCrawlStop(w http.ResponseWriter, r *http.Request) {
	if err := h.service.StopCrawl(); err != nil {
		h.mapServiceError(w, err)
		return
	}

	st := h.service.GetStatus()
	resp := ControlActionResponseDTO{
		State:     string(st.State),
		SessionID: st.SessionID,
		Message:   "Crawl session stopped",
	}

	writeJSON(w, http.StatusOK, resp)
}

// POST /api/crawl/checkpoint
func (h *apiHandler) handleCrawlCheckpoint(w http.ResponseWriter, r *http.Request) {
	cp, err := h.service.CreateCheckpoint()
	if err != nil {
		h.mapServiceError(w, err)
		return
	}

	resp := CheckpointResponseDTO{
		SessionID: cp.SessionID,
		Sequence:  cp.Sequence,
		Timestamp: cp.CreatedAt.Format(time.RFC3339Nano),
		Stats:     cp.Stats,
	}

	writeJSON(w, http.StatusOK, resp)
}

// GET /api/workers
func (h *apiHandler) handleWorkers(w http.ResponseWriter, r *http.Request) {
	workers, err := h.service.GetWorkers()
	if err != nil {
		h.mapServiceError(w, err)
		return
	}

	var dtoList []WorkerInfoResponseDTO
	for _, w := range workers {
		dtoList = append(dtoList, WorkerInfoResponseDTO{
			ID:         w.ID,
			State:      string(w.State),
			CurrentURL: w.CurrentURL,
			UpdatedAt:  w.UpdatedAt,
		})
	}

	resp := WorkerListResponseDTO{
		WorkerCount: len(dtoList),
		Workers:     dtoList,
	}

	writeJSON(w, http.StatusOK, resp)
}

// mapServiceError translates internal service errors to standard HTTP status codes and JSON envelopes.
func (h *apiHandler) mapServiceError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrCrawlAlreadyRunning) {
		writeError(w, http.StatusConflict, "CRAWL_ALREADY_RUNNING", "A crawl session is already running or paused")
		return
	}
	if errors.Is(err, ErrCrawlStopping) {
		writeError(w, http.StatusConflict, "CRAWL_STOPPING", "Crawl session is currently stopping")
		return
	}
	if errors.Is(err, ErrPreviousSessionTerminating) {
		writeError(w, http.StatusConflict, "PREVIOUS_SESSION_TERMINATING", "Previous crawl session is still terminating")
		return
	}
	if errors.Is(err, ErrNoActiveCrawl) {
		writeError(w, http.StatusNotFound, "NO_ACTIVE_CRAWL", "No active crawl session exists")
		return
	}
	if errors.Is(err, ErrInvalidStateTransition) {
		writeError(w, http.StatusConflict, "INVALID_STATE_TRANSITION", err.Error())
		return
	}
	if errors.Is(err, ErrInvalidConfiguration) {
		writeError(w, http.StatusBadRequest, "INVALID_CONFIGURATION", err.Error())
		return
	}
	if errors.Is(err, ErrURLNotFound) {
		writeError(w, http.StatusNotFound, "URL_NOT_FOUND", "URL record not found")
		return
	}

	// Default fallback: internal error without exposing sensitive system paths
	writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "An unexpected internal error occurred")
}

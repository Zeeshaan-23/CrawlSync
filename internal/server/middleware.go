package server

import (
	"encoding/json"
	"log"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
	"time"
)

// responseWriterInterceptor wraps http.ResponseWriter to capture status code for logging.
type responseWriterInterceptor struct {
	http.ResponseWriter
	statusCode int
}

func (w *responseWriterInterceptor) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

// applyMiddleware wraps the root HTTP handler with the standard middleware pipeline.
func applyMiddleware(next http.Handler, cfg HandlerConfig) http.Handler {
	handler := next

	// 1. JSON Header Enforcement
	handler = jsonHeaderMiddleware(handler)

	// 2. Restrictive CORS (if configured)
	if len(cfg.CORSOrigins) > 0 {
		handler = corsMiddleware(handler, cfg.CORSOrigins)
	}

	// 3. Request Logging
	handler = requestLoggingMiddleware(handler)

	// 4. Panic Recovery (outermost to capture any runtime panics)
	handler = panicRecoveryMiddleware(handler)

	return handler
}

// jsonHeaderMiddleware ensures Content-Type is always application/json for API responses.
func jsonHeaderMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
		}
		next.ServeHTTP(w, r)
	})
}

// panicRecoveryMiddleware catches unhandled panics and returns a structured 500 error.
func panicRecoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[HTTP PANIC] %s %s: %v\nStack:\n%s", r.Method, r.URL.Path, rec, debug.Stack())
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(APIErrorResponse{
					Success: false,
					Error: APIErrorDetail{
						Code:    "INTERNAL_ERROR",
						Message: "An internal server error occurred",
					},
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// requestLoggingMiddleware logs request method, path, status, and duration.
func requestLoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriterInterceptor{
			ResponseWriter: w,
			statusCode:     http.StatusOK, // Default if WriteHeader is not explicitly called
		}

		next.ServeHTTP(rw, r)

		duration := time.Since(start)
		log.Printf("[HTTP] %s %s %d %v", r.Method, r.URL.Path, rw.statusCode, duration)
	})
}

// corsMiddleware implements restrictive CORS checking against an explicit origin whitelist.
// Wildcard CORS (*) is intentionally disabled.
func corsMiddleware(next http.Handler, allowedOrigins []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && slices.Contains(allowedOrigins, origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Authorization")
			w.Header().Set("Access-Control-Max-Age", "86400")
		}

		// Handle preflight requests
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

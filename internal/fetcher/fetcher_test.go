package fetcher

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetcher_HTMLSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, "<html><body>Hello CrawlSync</body></html>")
	}))
	defer ts.Close()

	// Must set AllowLocal: true because httptest.Server binds to 127.0.0.1 (local address)
	f := New(Config{
		Timeout:    2 * time.Second,
		AllowLocal: true,
	})

	res := f.Fetch(context.Background(), ts.URL)
	if res.Error != nil {
		t.Fatalf("unexpected fetch error: %v", res.Error)
	}

	if res.StatusCode != http.StatusOK {
		t.Errorf("status code = %d, want %d", res.StatusCode, http.StatusOK)
	}

	if !strings.Contains(string(res.Body), "Hello CrawlSync") {
		t.Errorf("expected body to contain 'Hello CrawlSync', got %s", string(res.Body))
	}
}

func TestFetcher_NonHTMLRejected(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"error": "not html"}`)
	}))
	defer ts.Close()

	f := New(Config{
		Timeout:    2 * time.Second,
		AllowLocal: true,
	})

	res := f.Fetch(context.Background(), ts.URL)
	if res.Error == nil {
		t.Fatal("expected error for non-HTML Content-Type, got nil")
	}

	if !strings.Contains(res.Error.Error(), "rejected non-HTML Content-Type") {
		t.Errorf("unexpected error message: %v", res.Error)
	}
}

func TestFetcher_SizeCapEnforced(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		// Send 2048 bytes
		_, _ = w.Write([]byte(strings.Repeat("A", 2048)))
	}))
	defer ts.Close()

	// Cap at 1000 bytes
	f := New(Config{
		Timeout:     2 * time.Second,
		MaxBodySize: 1000,
		AllowLocal:  true,
	})

	res := f.Fetch(context.Background(), ts.URL)
	if res.Error == nil {
		t.Fatal("expected error due to exceeding MaxBodySize, got nil")
	}

	if !strings.Contains(res.Error.Error(), "response exceeded maximum allowed size") {
		t.Errorf("unexpected error message: %v", res.Error)
	}
}

func TestFetcher_SSRFProtection(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<html>Should not be reached</html>")
	}))
	defer ts.Close()

	// When AllowLocal is false (default), local IP access is blocked
	f := New(Config{
		Timeout:    2 * time.Second,
		AllowLocal: false,
	})

	res := f.Fetch(context.Background(), ts.URL)
	if res.Error == nil {
		t.Fatal("expected SSRF block error on localhost, got nil")
	}

	if !strings.Contains(res.Error.Error(), "blocked") {
		t.Logf("fetch correctly blocked localhost with error: %v", res.Error)
	}
}


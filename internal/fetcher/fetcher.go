package fetcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultUserAgent identifies CrawlSync requests.
	DefaultUserAgent = "CrawlSync/1.0 (academic project)"

	// DefaultTimeout is the HTTP client timeout per request.
	DefaultTimeout = 10 * time.Second

	// DefaultMaxBodySize limits HTML content to 5MB to avoid memory exhaustion (OS resource bounds).
	DefaultMaxBodySize = 5 * 1024 * 1024
)

// FetchResult holds the outcome of an HTTP fetch operation.
type FetchResult struct {
	URL        string
	StatusCode int
	Body       []byte
	Size       int64
	Duration   time.Duration
	Error      error
}

// Config specifies options for HTTP Fetcher.
type Config struct {
	Timeout     time.Duration
	UserAgent   string
	MaxBodySize int64
	AllowLocal  bool // If false, disallows localhost and private RFC-1918/link-local IPs (SSRF safety)
}

// Fetcher handles HTTP downloads with timeouts, size limits, and safety restrictions.
type Fetcher struct {
	config Config
	client *http.Client
}

// New creates a new Fetcher with the given configuration.
func New(cfg Config) *Fetcher {
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = DefaultUserAgent
	}
	if cfg.MaxBodySize <= 0 {
		cfg.MaxBodySize = DefaultMaxBodySize
	}

	f := &Fetcher{
		config: cfg,
	}

	// Custom transport to enforce private IP / SSRF limits at network dial time.
	// OS Concept: Network dial operates across socket boundaries via the OS network stack.
	// We intercept DNS resolution and socket connection to enforce security boundaries.
	dialer := &net.Dialer{
		Timeout:   cfg.Timeout,
		KeepAlive: 30 * time.Second,
	}

	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if !cfg.AllowLocal {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, fmt.Errorf("invalid address: %w", err)
				}

				// Resolve IP addresses to inspect whether any point to private/local networks
				ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
				if err != nil {
					return nil, fmt.Errorf("dns resolution failed for %s: %w", host, err)
				}

				for _, ip := range ips {
					if isDisallowedIP(ip) {
						return nil, fmt.Errorf("connection to private/local IP blocked: %s (%s)", host, ip.String())
					}
				}

				// Dial using first safe IP to prevent DNS rebinding race conditions
				if len(ips) > 0 {
					addr = net.JoinHostPort(ips[0].String(), port)
				}
			}
			return dialer.DialContext(ctx, network, addr)
		},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	f.client = &http.Client{
		Transport: transport,
		Timeout:   cfg.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			// Enforce IP checks on redirects if local access is disallowed
			if !cfg.AllowLocal {
				host := req.URL.Hostname()
				if host == "localhost" {
					return errors.New("redirect to localhost blocked")
				}
				ip := net.ParseIP(host)
				if ip != nil && isDisallowedIP(ip) {
					return fmt.Errorf("redirect to private IP blocked: %s", ip.String())
				}
			}
			return nil
		},
	}

	return f
}

// isDisallowedIP verifies if an IP belongs to private/internal/loopback ranges.
func isDisallowedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// Check loopback (127.0.0.0/8, ::1)
	if ip.IsLoopback() {
		return true
	}
	// Check link-local (169.254.0.0/16, fe80::/10)
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	// Check unspecified (0.0.0.0, ::)
	if ip.IsUnspecified() {
		return true
	}
	// Check RFC 1918 private IPv4 & RFC 4193 private IPv6
	if ip.IsPrivate() {
		return true
	}
	return false
}

// Fetch executes an HTTP GET request on the given URL, enforces timeouts, checks Content-Type,
// and bounds memory allocation by capping the response read.
// OS CONCURRENCY NOTE: Never hold locks during this call as it performs blocking network I/O.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) FetchResult {
	start := time.Now()
	res := FetchResult{
		URL: rawURL,
	}

	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		res.Duration = time.Since(start)
		res.Error = fmt.Errorf("invalid URL: %w", err)
		return res
	}

	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		res.Duration = time.Since(start)
		res.Error = fmt.Errorf("unsupported scheme: %s (only http and https supported)", parsedURL.Scheme)
		return res
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		res.Duration = time.Since(start)
		res.Error = fmt.Errorf("failed to create request: %w", err)
		return res
	}

	req.Header.Set("User-Agent", f.config.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	// Execute HTTP request
	resp, err := f.client.Do(req)
	if err != nil {
		res.Duration = time.Since(start)
		res.Error = err
		return res
	}
	defer resp.Body.Close()

	res.StatusCode = resp.StatusCode

	// Verify Content-Type is HTML
	contentType := resp.Header.Get("Content-Type")
	if !isHTMLContentType(contentType) {
		res.Duration = time.Since(start)
		res.Error = fmt.Errorf("rejected non-HTML Content-Type: %s", contentType)
		return res
	}

	// Limit reader to prevent memory exhaustion (DoS / resource starvation in OS)
	// We read at most MaxBodySize + 1 bytes to detect truncation.
	limitReader := io.LimitReader(resp.Body, f.config.MaxBodySize+1)
	body, err := io.ReadAll(limitReader)
	if err != nil {
		res.Duration = time.Since(start)
		res.Error = fmt.Errorf("failed reading response body: %w", err)
		return res
	}

	if int64(len(body)) > f.config.MaxBodySize {
		res.Duration = time.Since(start)
		res.Error = fmt.Errorf("response exceeded maximum allowed size of %d bytes", f.config.MaxBodySize)
		return res
	}

	res.Body = body
	res.Size = int64(len(body))
	res.Duration = time.Since(start)
	return res
}

func isHTMLContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	// Often Content-Type is "text/html; charset=utf-8"
	return strings.HasPrefix(ct, "text/html") || strings.HasPrefix(ct, "application/xhtml+xml")
}


package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Default server configuration parameters
const (
	DefaultHost            = "127.0.0.1"
	DefaultPort            = 8080
	DefaultShutdownTimeout = 15 * time.Second

	DefaultReadHeaderTimeout = 5 * time.Second
	DefaultReadTimeout       = 15 * time.Second
	DefaultWriteTimeout      = 30 * time.Second
	DefaultIdleTimeout       = 60 * time.Second
	DefaultMaxHeaderBytes    = 1 << 20 // 1 MB
)

// ServerConfig configures the HTTP server lifecycle and underlying services.
type ServerConfig struct {
	Host              string
	Port              int
	EphemeralPort     bool // If true, allows binding to port 0 (OS allocates an available local port for testing)
	Service           *CrawlerService
	CORSOrigins       []string
	ShutdownTimeout   time.Duration
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int
}

// Server encapsulates the HTTP server, listener, and service coordination.
//
// OS CONCEPT - PROCESS & RESOURCE COORDINATION:
// The Server manages the HTTP socket listener and coordinates process lifecycle with the
// application monitor (CrawlerService). During shutdown, it ensures all active socket connections
// are drained before stopping worker pools and persisting state snapshots.
type Server struct {
	cfg        ServerConfig
	service    *CrawlerService
	handler    http.Handler
	httpServer *http.Server
	listener   net.Listener
	addr       string // Configured address (host:port)
	actualAddr string // Actual listening address after bind

	mu      sync.Mutex
	running bool
}

// NewServer constructs and validates a new Server instance.
func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.Service == nil {
		return nil, errors.New("crawler service is required")
	}

	host := cfg.Host
	if host == "" {
		host = DefaultHost
	}

	port := cfg.Port
	if port == 0 && !cfg.EphemeralPort {
		port = DefaultPort
	}
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("invalid port %d: must be between 1 and 65535", port)
	}

	shutdownTimeout := cfg.ShutdownTimeout
	if shutdownTimeout <= 0 {
		shutdownTimeout = DefaultShutdownTimeout
	}

	readHeaderTimeout := cfg.ReadHeaderTimeout
	if readHeaderTimeout <= 0 {
		readHeaderTimeout = DefaultReadHeaderTimeout
	}

	readTimeout := cfg.ReadTimeout
	if readTimeout <= 0 {
		readTimeout = DefaultReadTimeout
	}

	writeTimeout := cfg.WriteTimeout
	if writeTimeout <= 0 {
		writeTimeout = DefaultWriteTimeout
	}

	idleTimeout := cfg.IdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = DefaultIdleTimeout
	}

	maxHeaderBytes := cfg.MaxHeaderBytes
	if maxHeaderBytes <= 0 {
		maxHeaderBytes = DefaultMaxHeaderBytes
	}

	// Security check: Warn operator if binding beyond loopback addresses
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		log.Printf("[HTTP Server SECURITY NOTICE] Server configured to bind on %s. "+
			"Exposing the API beyond localhost without access control or reverse proxy authentication "+
			"is insecure.", host)
	}

	handler := NewHandler(HandlerConfig{
		Service:     cfg.Service,
		CORSOrigins: cfg.CORSOrigins,
	})

	bindAddr := net.JoinHostPort(host, strconv.Itoa(port))

	cfg.Host = host
	cfg.Port = port
	cfg.ShutdownTimeout = shutdownTimeout
	cfg.ReadHeaderTimeout = readHeaderTimeout
	cfg.ReadTimeout = readTimeout
	cfg.WriteTimeout = writeTimeout
	cfg.IdleTimeout = idleTimeout
	cfg.MaxHeaderBytes = maxHeaderBytes

	return &Server{
		cfg:     cfg,
		service: cfg.Service,
		handler: handler,
		addr:    bindAddr,
	}, nil
}

// Addr returns the listening address of the server.
// If the server is running, returns the actual bound host:port.
// If not yet running, returns the configured bind address.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.actualAddr != "" {
		return s.actualAddr
	}
	return s.addr
}

// ShutdownTimeout returns the configured graceful shutdown deadline.
func (s *Server) ShutdownTimeout() time.Duration {
	return s.cfg.ShutdownTimeout
}

// Service returns the underlying CrawlerService.
func (s *Server) Service() *CrawlerService {
	return s.service
}

// IsRunning reports whether the server is actively listening.
func (s *Server) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Start opens the network listener and begins serving HTTP requests on a background goroutine.
// Returns an error immediately if the socket cannot be bound (e.g. port already in use).
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return errors.New("server is already running")
	}

	l, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("failed listening on %s: %w", s.addr, err)
	}

	s.listener = l
	s.actualAddr = l.Addr().String()
	s.running = true

	// Re-instantiate http.Server for each start cycle to allow repeated Start/Shutdown cycles
	s.httpServer = &http.Server{
		Addr:              s.actualAddr,
		Handler:           s.handler,
		ReadHeaderTimeout: s.cfg.ReadHeaderTimeout,
		ReadTimeout:       s.cfg.ReadTimeout,
		WriteTimeout:      s.cfg.WriteTimeout,
		IdleTimeout:       s.cfg.IdleTimeout,
		MaxHeaderBytes:    s.cfg.MaxHeaderBytes,
	}

	log.Printf("[HTTP Server] Listening on http://%s", s.actualAddr)

	go func() {
		if err := s.httpServer.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[HTTP Server ERROR] Serve error: %v", err)
		}
	}()

	return nil
}

// Shutdown initiates graceful termination of the HTTP server and crawler service.
//
// DETERMINISTIC SHUTDOWN SEQUENCE:
// 1. Closes the socket listener and refuses new incoming connections.
// 2. Waits for all active in-flight HTTP request handlers to finish within the ctx deadline.
// 3. Gracefully stops any active crawl session (signals controller, waits for workers to drain,
//    and saves the final shutdown checkpoint).
// 4. Returns combined errors if shutdown fails or times out.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = false
	httpSrv := s.httpServer
	s.mu.Unlock()

	log.Printf("[HTTP Server] Initiating graceful shutdown...")

	var shutdownErr error

	// 1. Stop accepting new HTTP requests and drain active handlers
	if httpSrv != nil {
		httpSrv.SetKeepAlivesEnabled(false)
		if err := httpSrv.Shutdown(ctx); err != nil {
			log.Printf("[HTTP Server WARN] HTTP server shutdown error: %v", err)
			shutdownErr = errors.Join(shutdownErr, fmt.Errorf("http shutdown error: %w", err))
		}
	}

	// 2. Stop active crawl session, drain worker pool, and save final checkpoint
	if err := s.service.Shutdown(ctx); err != nil {
		log.Printf("[HTTP Server WARN] Crawler service shutdown error: %v", err)
		shutdownErr = errors.Join(shutdownErr, fmt.Errorf("crawler service shutdown error: %w", err))
	}

	log.Printf("[HTTP Server] Graceful shutdown completed")
	return shutdownErr
}

// ListenAndServe starts the server and blocks until the server terminates or an error occurs.
func (s *Server) ListenAndServe() error {
	if err := s.Start(); err != nil {
		return err
	}

	// Wait until running is set to false by Shutdown()
	for s.IsRunning() {
		time.Sleep(50 * time.Millisecond)
	}

	return nil
}

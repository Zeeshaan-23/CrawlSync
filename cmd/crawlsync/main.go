package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"crawlsync/internal/controller"
	"crawlsync/internal/urlmanager"
)

func main() {
	targetURL := flag.String("url", "https://example.com", "Target start URL to crawl")
	workers := flag.Int("workers", 5, "Number of concurrent worker goroutines (1-20)")
	maxPages := flag.Int("max-pages", 10, "Maximum number of pages to crawl")
	maxDepth := flag.Int("max-depth", 2, "Maximum link discovery depth")
	timeoutSec := flag.Int("timeout", 10, "HTTP request timeout in seconds")
	checkpointInterval := flag.Duration("checkpoint-interval", 30*time.Second, "Interval between periodic checkpoints (e.g. 10s, 30s)")
	allowLocal := flag.Bool("allow-local", false, "Allow crawling localhost/private IP addresses")
	flag.Parse()

	fmt.Println("==================================================")
	fmt.Println(" CrawlSync - Phase 4 Crawler & Checkpointing")
	fmt.Println("==================================================")
	fmt.Printf("Seed URL:            %s\n", *targetURL)
	fmt.Printf("Worker Count:        %d\n", *workers)
	fmt.Printf("Max Pages:           %d\n", *maxPages)
	fmt.Printf("Max Depth:           %d\n", *maxDepth)
	fmt.Printf("Request Timeout:     %d seconds\n", *timeoutSec)
	fmt.Printf("Checkpoint Interval: %v\n", *checkpointInterval)
	fmt.Printf("Allow Local IPs:     %t\n", *allowLocal)
	fmt.Println("--------------------------------------------------")

	// Context with interrupt signal handling (SIGINT, SIGTERM)
	// OS CONCEPT: Signal handling and graceful termination with final state persistence
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := controller.Config{
		StartURL:           *targetURL,
		WorkerCount:        *workers,
		MaxPages:           *maxPages,
		MaxDepth:           *maxDepth,
		RequestTimeout:     time.Duration(*timeoutSec) * time.Second,
		StorageDir:         "data/pages",
		CheckpointDir:      "data/checkpoints",
		CheckpointInterval: *checkpointInterval,
		SameDomainOnly:     true,
		AllowLocal:         *allowLocal,
	}

	mgr := urlmanager.NewManager()
	ctrl := controller.NewController(cfg, mgr)

	fmt.Println("[Controller] Starting crawl session...")
	startTime := time.Now()

	err := ctrl.Run(ctx)
	duration := time.Since(startTime)

	fmt.Println("--------------------------------------------------")
	if err != nil && err != context.Canceled {
		fmt.Printf("[Controller] Session ended with notice: %v\n", err)
	} else {
		fmt.Println("[Controller] Crawl session completed successfully.")
	}

	stats := mgr.GetStats()
	fmt.Printf("Total Elapsed:   %v\n", duration)
	fmt.Printf("Discovered:      %d\n", stats.TotalDiscovered)
	fmt.Printf("Completed:       %d\n", stats.Completed)
	fmt.Printf("Pending:         %d\n", stats.Pending)
	fmt.Printf("Failed:          %d\n", stats.Failed)
	fmt.Println("==================================================")
}

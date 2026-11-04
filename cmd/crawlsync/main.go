package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"crawlsync/internal/controller"
	"crawlsync/internal/state"
	"crawlsync/internal/urlmanager"
)

func main() {
	targetURL := flag.String("url", "https://example.com", "Target start URL to crawl (new session)")
	workers := flag.Int("workers", 5, "Number of concurrent worker goroutines (1-20)")
	maxPages := flag.Int("max-pages", 10, "Maximum number of pages to crawl")
	maxDepth := flag.Int("max-depth", 2, "Maximum link discovery depth")
	timeoutSec := flag.Int("timeout", 10, "HTTP request timeout in seconds")
	checkpointInterval := flag.Duration("checkpoint-interval", 30*time.Second, "Interval between periodic checkpoints (e.g. 10s, 30s)")
	allowLocal := flag.Bool("allow-local", false, "Allow crawling localhost/private IP addresses")
	resume := flag.Bool("resume", false, "Resume crawl session from checkpoint")
	retryFailed := flag.Bool("retry-failed", false, "Retry previously failed URLs when resuming")
	storageDirFlag := flag.String("dir", "data/pages", "Storage directory for downloaded HTML pages")
	checkpointDirFlag := flag.String("checkpoint-dir", "", "Directory containing checkpoints (defaults to data/checkpoints or relative to storage dir)")
	flag.Parse()

	explicitFlags := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) {
		explicitFlags[f.Name] = true
	})

	fmt.Println("==================================================")
	if *resume {
		fmt.Println(" CrawlSync - Phase 6 Session Recovery & Resume")
	} else {
		fmt.Println(" CrawlSync - Web Crawler Session")
	}
	fmt.Println("==================================================")

	var cfg controller.Config
	mgr := urlmanager.NewManager()

	if *resume {
		checkpointDir := resolveCheckpointDir(*checkpointDirFlag, *storageDirFlag, explicitFlags["checkpoint-dir"])

		// If storage dir was explicitly specified on CLI, validate with it; otherwise let ValidateForRecovery check checkpoint's StorageDir
		var validationStorageDir string
		if explicitFlags["dir"] {
			validationStorageDir = *storageDirFlag
		}

		fmt.Println("[Recovery] Inspecting checkpoint directory:", checkpointDir)
		cp, err := state.LoadWithFallback(checkpointDir, state.RecoveryValidationOptions{
			AllowLocal: *allowLocal,
			StorageDir: validationStorageDir,
		})
		if err != nil {
			fmt.Printf("[Recovery ERROR] Checkpoint recovery failed: %v\n", err)
			os.Exit(1)
		}

		storageDir := *storageDirFlag
		if !explicitFlags["dir"] && cp.Config.StorageDir != "" {
			storageDir = cp.Config.StorageDir
		}

		// Request timeout from checkpoint unless explicitly supplied
		reqTimeout := 10 * time.Second
		if cp.Config.RequestTimeout != "" {
			if parsedT, err := time.ParseDuration(cp.Config.RequestTimeout); err == nil && parsedT > 0 {
				reqTimeout = parsedT
			}
		}
		if explicitFlags["timeout"] {
			reqTimeout = time.Duration(*timeoutSec) * time.Second
		}

		// Workers: use checkpoint unless explicitly supplied
		workerCount := cp.Config.WorkerCount
		if explicitFlags["workers"] {
			workerCount = *workers
		}

		// Checkpoint interval: use default/flag if specified
		cpInterval := 30 * time.Second
		if explicitFlags["checkpoint-interval"] {
			cpInterval = *checkpointInterval
		}

		cfg = controller.Config{
			StartURL:           cp.Config.StartURL,
			WorkerCount:        workerCount,
			MaxPages:           cp.Config.MaxPages,
			MaxDepth:           cp.Config.MaxDepth,
			RequestTimeout:     reqTimeout,
			StorageDir:         storageDir,
			CheckpointDir:      checkpointDir,
			CheckpointInterval: cpInterval,
			SameDomainOnly:     cp.Config.SameDomainOnly,
			AllowLocal:         *allowLocal, // Safety permissions come strictly from CLI
			SessionID:          cp.SessionID,
			CumulativeElapsed: cp.CumulativeElapsed,
			InitialSequence:    cp.Sequence,
			IsResumed:          true,
		}

		// Restore URL manager
		if err := mgr.Restore(cp.URLs, urlmanager.RestoreOptions{
			StorageDir:  storageDir,
			RetryFailed: *retryFailed,
		}); err != nil {
			fmt.Printf("[Recovery ERROR] URLManager state restoration failed: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Recovered Session:   %s\n", cp.SessionID)
		fmt.Printf("Restored Checkpoint: #%d\n", cp.Sequence)
		fmt.Printf("Prior Runtime:       %v\n", cp.CumulativeElapsed)
		fmt.Printf("Start URL:           %s\n", cfg.StartURL)
		fmt.Printf("Worker Count:        %d\n", cfg.WorkerCount)
		fmt.Printf("Max Pages:           %d\n", cfg.MaxPages)
		fmt.Printf("Max Depth:           %d\n", cfg.MaxDepth)
		fmt.Printf("Allow Local IPs:     %t\n", cfg.AllowLocal)
		fmt.Printf("Retry Failed:        %t\n", *retryFailed)
	} else {
		storageDir := *storageDirFlag
		checkpointDir := *checkpointDirFlag
		if checkpointDir == "" {
			checkpointDir = filepath.Join(filepath.Dir(storageDir), "checkpoints")
		}

		cfg = controller.Config{
			StartURL:           *targetURL,
			WorkerCount:        *workers,
			MaxPages:           *maxPages,
			MaxDepth:           *maxDepth,
			RequestTimeout:     time.Duration(*timeoutSec) * time.Second,
			StorageDir:         storageDir,
			CheckpointDir:      checkpointDir,
			CheckpointInterval: *checkpointInterval,
			SameDomainOnly:     true,
			AllowLocal:         *allowLocal,
		}

		fmt.Printf("Seed URL:            %s\n", *targetURL)
		fmt.Printf("Worker Count:        %d\n", *workers)
		fmt.Printf("Max Pages:           %d\n", *maxPages)
		fmt.Printf("Max Depth:           %d\n", *maxDepth)
		fmt.Printf("Request Timeout:     %d seconds\n", *timeoutSec)
		fmt.Printf("Checkpoint Interval: %v\n", *checkpointInterval)
		fmt.Printf("Allow Local IPs:     %t\n", *allowLocal)
	}
	fmt.Println("--------------------------------------------------")

	// Context with interrupt signal handling (SIGINT, SIGTERM)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ctrl, err := controller.NewController(cfg, mgr)
	if err != nil {
		fmt.Printf("[Controller ERROR] Initialization failed: %v\n", err)
		os.Exit(1)
	}

	if *resume {
		fmt.Println("[Controller] Resuming crawl session...")
	} else {
		fmt.Println("[Controller] Starting crawl session...")
	}

	err = ctrl.Run(ctx)

	fmt.Println("--------------------------------------------------")
	if err != nil && err != context.Canceled {
		fmt.Printf("[Controller] Session ended with notice: %v\n", err)
	} else {
		fmt.Println("[Controller] Crawl session completed successfully.")
	}

	stats := mgr.GetStats()
	fmt.Printf("Total Elapsed:   %v\n", ctrl.CumulativeElapsed())
	fmt.Printf("Discovered:      %d\n", stats.TotalDiscovered)
	fmt.Printf("Completed:       %d\n", stats.Completed)
	fmt.Printf("Pending:         %d\n", stats.Pending)
	fmt.Printf("Failed:          %d\n", stats.Failed)
	fmt.Println("==================================================")
}

// resolveCheckpointDir determines the directory containing checkpoints.
// Priority:
// 1. Explicitly provided -checkpoint-dir flag.
// 2. Candidate directory containing checkpoint.json:
//    - <storageDir>/../checkpoints
//    - <storageDir>/checkpoints
//    - <storageDir>
//    - data/checkpoints
// 3. Fallback default: <storageDir>/../checkpoints
func resolveCheckpointDir(cliCheckpointDir, cliStorageDir string, explicitCheckpointDir bool) string {
	if explicitCheckpointDir && cliCheckpointDir != "" {
		return cliCheckpointDir
	}

	candidates := []string{
		filepath.Join(filepath.Dir(cliStorageDir), "checkpoints"),
		filepath.Join(cliStorageDir, "checkpoints"),
		cliStorageDir,
		filepath.Join("data", "checkpoints"),
	}

	for _, candidate := range candidates {
		primaryPath := filepath.Join(candidate, state.CheckpointFileName)
		if _, err := os.Stat(primaryPath); err == nil {
			return candidate
		}
	}

	if cliCheckpointDir != "" {
		return cliCheckpointDir
	}
	return filepath.Join(filepath.Dir(cliStorageDir), "checkpoints")
}

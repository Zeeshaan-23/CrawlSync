# CrawlSync Implementation Log

## Phase 1 & 2: Project Initialization, Fetcher, Link Parser, and URL Manager

- **Date:** 2026-11-03
- **Status:** Verified (All unit tests passed with `-race`, `go vet` clean, single-page crawl verified)
- **Files Created:**
  - `go.mod`, `go.sum`: Initialized Go module (`crawlsync`) and dependency `golang.org/x/net v0.62.0`.
  - `internal/fetcher/fetcher.go`: HTTP fetcher with custom User-Agent, configurable timeout, 5MB response cap via `io.LimitReader`, Content-Type validation (`text/html`), and SSRF/private IP protection with `-allow-local` flag.
  - `internal/fetcher/fetcher_test.go`: Unit tests for HTML success, non-HTML Content-Type rejection, body size cap enforcement, and SSRF loopback blocking.
  - `internal/urlmanager/normalize.go`: URL normalization canonicalizing scheme, host, stripping default ports (80/443), fragment removal, resolving `.` and `..`, stripping non-root trailing slash, and sorting query parameters deterministically.
  - `internal/urlmanager/normalize_test.go`: 12 test cases covering normalization edge cases.
  - `internal/urlmanager/manager.go`: Thread-safe URL Manager with atomic `TrySchedule(url, depth)` protected by `sync.RWMutex`, tracking statuses (`pending`, `in_progress`, `completed`, `failed`), and stats calculation.
  - `internal/urlmanager/manager_test.go`: State transition test and heavy concurrent test (50 goroutines, 10 unique URLs, 2000 total attempts) verifying mutual exclusion with zero race conditions.
  - `internal/parser/links.go`: Link extractor using `golang.org/x/net/html` resolving relative URLs against base, filtering fragments, mailto, javascript, tel, data, and applying same-domain policy.
  - `internal/parser/links_test.go`: Link parsing unit tests with same-domain and cross-domain filtering.
  - `cmd/crawlsync/main.go`: CLI harness demonstrating single-page fetch, link extraction, and duplicate detection into the URL Manager.
  - `.gitignore`: Ignoring binaries, checkpoints, downloaded pages, and temporary files.

- **Verification Output:**
  - `go vet ./...`: PASSED (0 warnings)
  - `go test -v -race ./...`: ALL PASSED
    - `crawlsync/internal/fetcher`: PASS
    - `crawlsync/internal/parser`: PASS
    - `crawlsync/internal/urlmanager`: PASS

---

## Phase 3: Worker Pool + Controller

- **Date:** 2026-11-03
- **Status:** Verified (All unit tests passed with `-race`, `go vet` clean, multi-page crawl verified)
- **Files Created/Modified:**
  - `internal/controller/worker.go`: Worker pool and worker lifecycle.
  - `internal/controller/controller.go`: Controller scheduler and non-blocking event loop.
  - `internal/controller/controller_test.go`: Graph crawl, MaxPages cutoff, and active download cancellation tests.
  - `cmd/crawlsync/main.go`: CLI entry point.

---

## Phase 4: Checkpoint Creation and Persistence

- **Date:** 2026-11-04
- **Status:** Verified (All unit tests passed with `-race`, `go vet` clean, live checkpointing verified)
- **Files Created/Modified:**
  - `internal/urlmanager/manager.go`:
    - Added monotonically increasing `Sequence` counter to `URLItem` reflecting exact discovery order.
    - Extended `URLItem` with download metadata (`Size`, `DurationMs`, `SavedPath`).
    - Added `Snapshot()` method providing consistent, deep-copied snapshots under a single `RLock()`, with deterministic sorting by sequence number.
    - Added `MarkCompletedWithMetadata()` to record HTTP status, byte size, duration, and on-disk saved path.
  - `internal/urlmanager/manager_test.go`:
    - Tested monotonic sequence assignment and sequence preservation on duplicates.
    - Tested snapshot consistency, statistics matching, and complete copy independence (mutations to snapshot do not leak to manager and vice versa).
    - Tested heavy concurrent scheduling sequence integrity (50 goroutines, 10 unique URLs).
  - `internal/state/checkpoint.go`:
    - Created dedicated Checkpoint Manager and schema version 1 data structures.
    - Generates unique session IDs (`session_<timestamp>_<hex>`).
    - Atomic file persistence: writes to `checkpoint-<timestamp>-*.tmp`, calls `f.Sync()` to ensure OS durability, closes, and atomically swaps via `os.Rename`.
    - Backup rotation: moves existing `checkpoint.json` to `checkpoint.prev.json` before commit.
    - Windows file-replacement compatibility and rollback on failed commit.
    - Checkpoint schema validator (`Validate()`) and loader (`Load()`).
  - `internal/state/checkpoint_test.go`:
    - Tested serialization, round-trip JSON deserialization, and schema validation.
    - Tested atomic replacement and backup rotation (`checkpoint.prev.json`).
    - Tested validation errors on unsupported versions or missing identifiers.
    - Tested concurrent save serialization (`sync.Mutex`).
  - `internal/controller/worker.go`:
    - Upgraded `savePageToDisk()` to use atomic file writing (temporary file -> `f.Sync()` -> `f.Close()` -> `os.Rename`).
  - `internal/controller/controller.go`:
    - Added `CheckpointDir` and `CheckpointInterval` to `Config`.
    - Integrated periodic checkpointing via `time.Ticker` firing at safe event-loop boundaries.
    - Integrated final shutdown checkpointing upon context cancellation (`SIGINT`/`SIGTERM`) and natural completion.
    - Added `MarkCompletedWithMetadata()` invocation when worker reports success.
    - Exposed `CreateCheckpoint()` for clean manual snapshot triggers.
  - `internal/controller/controller_test.go`:
    - Added `TestController_PeriodicCheckpointing`: verified periodic ticker saves checkpoints during crawling.
    - Updated `TestController_ActiveDownloadCancellation` and `TestController_CompleteGraphCrawl` to verify final checkpoint generation.
  - `cmd/crawlsync/main.go`:
    - Added `--checkpoint-interval` flag (default 30s).
  - Documentation:
    - Created `docs/ARCHITECTURE.md`: comprehensive system architecture and checkpointing flow.
    - Created `docs/OS_CONCEPTS.md`: mapped 10 core OS concepts to real Go code.
    - Updated `PROJECT_CONTEXT.md`: updated status tracker and checkpointing documentation.

- **Verification Output:**
  - `go vet ./...`: PASSED (0 warnings)
  - `go test -v ./...`: ALL PASSED
    - `crawlsync/internal/controller`: PASS (all 6 tests)
    - `crawlsync/internal/fetcher`: PASS (all 4 tests)
    - `crawlsync/internal/parser`: PASS (all tests)
    - `crawlsync/internal/state`: PASS (all 4 tests)
    - `crawlsync/internal/urlmanager`: PASS (all 4 tests)
  - `go test -race ./...`: ALL PASSED (0 race conditions across all packages)
  - Live execution on `go.dev`:
    - Periodic and final checkpoint successfully written to `data/checkpoints/checkpoint.json`.
    - Checkpoint contains schema version 1, unique session ID, crawler config, consistent stats, and 4 URL records with sequence numbers and download metadata.

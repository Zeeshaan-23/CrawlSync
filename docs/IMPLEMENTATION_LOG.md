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

- **Targeted Improvements & Hardening (Phase 4 Review):**
  1. *Shutdown Result-Channel Draining:* Refactored result processing into `handleCrawlResult(res, pendingJobs, allowNewPending)`. When `ctx.Done()` fires, `jobsChan` is closed and `c.wg.Wait()` blocks until all workers terminate. Then all remaining results buffered in `resultsChan` are completely drained and processed. Successfully downloaded pages are marked `completed`, newly discovered links are recorded in `URLManager` as `pending` for the final checkpoint without dispatching new jobs (`allowNewPending = false`), and interrupted tasks remain `in_progress`.
  2. *Atomic Page-File Replacement with Rollback:* Refactored `savePageToDisk()` to use `replaceFileWithBackup()`. When replacing an existing HTML file, the previous file is backed up before moving the temporary file into place. If the replacement fails, the backup is rolled back to restore the original page content, guaranteeing existing pages are never destroyed by failed replacements on Windows or POSIX.
  3. *Cryptographic Session ID Error Handling:* Updated `GenerateSessionID()` to return `(string, error)`, checking `rand.Read()`. `NewController()` handles this error cleanly, ensuring the crawler never silently runs with insufficient entropy.
  4. *Test Verification Suite:*
     - `TestController_ShutdownResultDraining`: Verified buffered results before cancellation are drained and marked `completed` in the shutdown checkpoint, while interrupted workers remain `in_progress`.
     - `TestController_HandleCrawlResult_DrainPolicy`: Unit test verifying `allowNewPending: false` schedules discovered URLs into `URLManager` without queuing to `pendingJobs`.
     - `TestWorker_PageReplacement_PreservesExistingOnFailure`: Verified existing page content survives failed replacements.
     - `TestGenerateSessionID`: Verified unique IDs and entropy error checking.


---

## Phase 5: Runtime Pause and Resume (Verified)
- **Files Modified:**
  - `internal/controller/controller.go`
  - `internal/controller/controller_test.go`
  - `PROJECT_CONTEXT.md`
  - `docs/IMPLEMENTATION_LOG.md`

- **Key Architectural Implementation:**
  - **Single Owner Controller:** The Controller remains the sole owner of `pendingJobs`, `inFlight`, `paused` state, and job dispatch. Workers remain decoupled from scheduling.
  - **Dynamic Select Multiplexing:** Disables job dispatch when paused by setting the dispatch channel `sendChan` to `nil` (`var sendChan chan Job; if !paused && len(pendingJobs) > 0 { sendChan = jobsChan }`). In Go, sending to a `nil` channel blocks that specific select case, allowing results, control commands, checkpoint triggers, and cancellation signals to be processed immediately without busy-waiting.
  - **Non-blocking Control Channel:** Implemented `controlChan chan controlRequest` with command types `cmdPause`, `cmdResume`, and `cmdStop`. Control methods (`Pause()`, `Resume()`) use buffered 1-slot acknowledgment channels (`make(chan struct{}, 1)`) and select on `<-c.doneChan` to ensure callers never deadlock if called during shutdown or after termination.
  - **Idempotence & Lifecycle Management:**
    - `Pause()` when already paused returns `nil` safely.
    - `Resume()` when already running returns `nil` safely.
    - `Stop()` cancels the run context (`c.stopCancel()`) and waits on `<-c.doneChan`, returning cleanly if already stopped.
    - Control requests sent after the Controller has terminated return immediately without blocking.
  - **Pause Semantics:**
    - Workers with active downloads continue uninterrupted.
    - Results from active workers are drained and processed normally.
    - Discovered child URLs are registered and queued in `URLManager` as pending.
    - Periodic and manual checkpointing remains fully functional during pause, capturing updated statistics and page statuses.
  - **Pause vs. Termination Condition:**
    - Prevented premature exit: The termination condition checks `!paused && len(pendingJobs) == 0 && inFlight == 0`. A paused crawl with pending jobs remains alive awaiting resume or stop.
  - **Stop While Paused:**
    - Stopping while paused halts job dispatch, cancels active worker contexts, waits on `sync.WaitGroup`, completely drains remaining results from `resultsChan`, preserves interrupted downloads as `in_progress`, and commits the final shutdown checkpoint.

- **Automated Test Suite (Tests A through I):**
  - `TestController_Pause_ActiveDownloads` (Test A): Pauses during active downloads, verifies active jobs complete, and ensures no subsequent jobs are dispatched.
  - `TestController_ResultsArrivingWhilePaused` (Test B): Active workers finish while paused; verifies results are processed and discovered links remain pending.
  - `TestController_Resume` (Test C): Resumes paused crawler, verifies pending jobs are dispatched in order, and crawling reaches completion.
  - `TestController_DoublePause` (Test D): Verifies double pause is idempotent with no deadlock or panic.
  - `TestController_DoubleResume` (Test E): Verifies double resume is idempotent with no deadlock or panic.
  - `TestController_StopWhilePaused` (Test F): Stops while paused, verifies full shutdown sequence, clean worker exit, and shutdown checkpoint persistence.
  - `TestController_CheckpointWhilePaused` (Test G): Verifies manual and periodic checkpoint creation while paused reflects updated URL states and statistics.
  - `TestController_ControlRequestsAfterShutdown` (Test H): Verifies Pause, Resume, and Stop return without hanging when invoked after crawler termination.
  - `TestController_HandleCrawlResult_DrainPolicy`: Verifies result draining policy with `allowNewPending: false`.

- **Verification Output:**
  - `go vet ./...`: PASSED (0 warnings)
  - `go test -v ./...`: ALL PASSED across all packages (16 controller tests, 4 fetcher tests, all parser subtests, 5 state tests, all urlmanager subtests)
  - `go test -count=1 -race ./...`: ALL PASSED (0 data races detected)


### Targeted Hardening Pass (Phase 5 Review)
- **1. Enforce Single Execution of `Run()`:**
  - Added `started bool` guarded by `stateMu sync.RWMutex`.
  - Defined sentinel errors `ErrAlreadyStarted` and `ErrAlreadyStopped`.
  - Guaranteed `Run()` can execute at most once per Controller instance; concurrent calls immediately return `ErrAlreadyStarted` without spawning duplicate worker pools or channels.
  - Added `TestController_ConcurrentRunRejected`: verified concurrent and sequential `Run()` rejections.
- **2. Paused Controller with Pending Work and Zero In-Flight Jobs:**
  - Added `TestController_PausedWithPendingWorkAndZeroInFlight`:
    - Reaches `StatePaused`, `Pending URLs > 0`, and `InProgress == 0` deterministically using a controlled 2-tier link graph (`/` -> `/p1` -> `/p2`).
    - Verified controller remains alive without terminating prematurely and without busy-looping.
    - Verified pending jobs remain intact in `URLManager`.
    - Subtest 1: `Resume()` dispatches pending work and completes the crawl naturally.
    - Subtest 2: `Stop()` cleanly terminates and persists a shutdown checkpoint preserving pending items.
- **3. Concurrent Control Requests:**
  - Added `TestController_ConcurrentControlRequests`:
    - 8 concurrent goroutines executing random interleavings of `Pause()`, `Resume()`, `State()`, `GetStats()`, and `GetWorkerStates()`.
    - Verified zero deadlocks, zero goroutine leaks, zero data races, and consistent URL deduplication.
- **4. Periodic Checkpoint While Paused:**
  - Added `TestController_PeriodicCheckpointWhilePaused`:
    - Configured short periodic interval (`25ms`).
    - Paused during active crawl; verified ticker continues firing and advancing checkpoint sequence while paused.
    - Verified worker completions while paused are captured in subsequent snapshots while pending URLs remain pending.
- **5. Improved Timing-Sensitive Tests:**
  - Replaced arbitrary multi-second `time.Sleep` calls in `TestController_ActiveDownloadCancellation`, `TestController_ShutdownResultDraining`, `TestController_Pause_ActiveDownloads`, `TestController_ResultsArrivingWhilePaused`, `TestController_Resume`, and `TestController_CheckpointWhilePaused` with synchronization channels (`<-r.Context().Done()`) and condition polling (`ctrl.GetStats().InProgress == 0`).
  - Reduced controller test execution time from ~11s down to ~3.5s.
- **6. Manual Checkpoint Consistency:**
  - Reviewed `CreateCheckpoint()`: routed manual checkpoint requests (`cmdCheckpoint`) through the Controller's event loop via `controlChan` when running or paused.
  - Guarantees manual checkpoints are captured at clean event-loop boundaries where no partial URL status transition or child discovery is mid-flight.
- **7. Validation Output:**
  - `go vet ./...`: PASSED (0 warnings)
  - `go test -v ./...`: ALL PASSED across all packages (20 controller tests/subtests, 4 fetcher tests, all parser subtests, 5 state tests, all urlmanager subtests)
  - `go test -count=1 -race ./...`: ALL PASSED (0 data races detected)

---

## Phase 6: Recovery After Interruption

### Implementation Summary
- **Untrusted Input Validation & Filesystem Safety (`internal/state/recovery.go`):**
  - Implemented `ValidateForRecovery(cp *Checkpoint, opts RecoveryValidationOptions) error`:
    - Strict schema version checking (`Version == CurrentVersion`).
    - Validates strictly positive, unique URL sequence numbers.
    - Validates URL syntax (scheme must be `http` or `https`), canonical normalization matches stored representation, and domain restriction (`IsSameDomain` against checkpoint seed URL).
    - Prevents SSRF / private IP access on restored URLs unless explicitly permitted via `AllowLocal`.
    - Prevents directory traversal attacks: validates that all completed URLs have `SavedPath` residing strictly within `StorageDir` and within the expected page directory (`filepath.Dir(absSaved) == absStorage`).
    - Enforces depth limits (`0 <= Depth <= MaxDepth`).
  - Implemented `LoadWithFallback(dir string, opts RecoveryValidationOptions) (*Checkpoint, bool, error)`:
    - If `checkpoint.json` does not exist, returns clear `ErrNoCheckpoint` without falling back.
    - If `checkpoint.json` exists but is corrupt or fails validation, renames it to `checkpoint.corrupt.<timestamp>`, logs a warning, and falls back to `checkpoint.prev.json`.
    - If both files are missing or corrupt, recovery is strictly refused.
- **Checkpoint Sequence Restoration (`internal/state/checkpoint.go`):**
  - Added `SetSaveCount(seq int64)` to `CheckpointManager` so that recovered sessions resume checkpoint numbering at `cp.Sequence`.
  - Ensures the first checkpoint written after recovery has `Sequence = cp.Sequence + 1`, maintaining strict monotonic sequence ordering across crashes and restarts.
  - Added `CumulativeElapsed time.Duration` (JSON `cumulativeElapsed,omitempty`) to `Checkpoint` struct to track total active runtime across multiple process interruptions.
- **URL Manager Restoration (`internal/urlmanager/manager.go`):**
  - Added `RestoreOptions{StorageDir, RetryFailed}` and `Restore(records []URLItem, opts RestoreOptions) error`.
  - Reconstructs internal deduplication map and metadata.
  - Sets `nextSequence = max(existing sequence) + 1` ensuring future discovered URLs receive strictly higher sequence numbers.
  - Demotes `in_progress` URLs to `pending` so interrupted downloads are safely retried.
  - Verifies completed URLs have valid non-empty files on disk; demotes missing/zero-length files to `pending`.
  - Preserves `failed` URLs unless `RetryFailed: true` is passed, in which case failed URLs are demoted to `pending`.
  - Implemented `GetPendingItems() []URLItem` returning pending records sorted deterministically by `Sequence` ascending.
- **Controller Recovery Lifecycle (`internal/controller/controller.go`):**
  - Added `SessionID`, `CumulativeElapsed`, `InitialSequence`, and `IsResumed` fields to `Config` and `Controller`.
  - In `Run()`:
    - Reconstructs `pendingJobs` from `c.urlManager.GetPendingItems()`.
    - Detects completed crawls before starting workers: if `len(pendingJobs) == 0 && inFlight == 0`, logs `"Session is already complete; no pending tasks remaining."` and exits cleanly without spawning worker pools.
    - Tracks active session elapsed time and accumulates into `CumulativeElapsed` during periodic, shutdown, and completion checkpoints.
    - Fully compatible with `Pause()`, `Resume()`, and `Stop()`.
- **CLI Flag Integration (`cmd/crawlsync/main.go`):**
  - Added `--resume` (`bool`) and `--retry-failed` (`bool`) CLI flags.
  - Uses `flag.Visit` to dynamically detect which CLI flags were explicitly provided by the user.
  - Restores configuration from checkpoint (seed URL, same-domain, max pages, max depth), overriding only flags explicitly supplied on CLI (`-workers`, `-interval`, `--allow-local`, etc.).
  - Emits clear startup banners indicating recovery status, restored session ID, completed/pending/failed counts, and cumulative runtime.

### Automated Test Suite
- **Recovery & Validation Tests (`internal/state/recovery_test.go`):**
  - `TestRecovery_MissingPrimary_FailsWithoutFallback`: verifies missing primary does not fall back to `.prev`.
  - `TestRecovery_CorruptPrimary_FallbackToBackup`: verifies primary corruption quarantines file to `.corrupt.<timestamp>` and loads `.prev`.
  - `TestRecovery_BothCorrupt_FailsSafely`: verifies recovery refusal when both files fail.
  - `TestRecovery_Validation_OutOfDomainRejected`: rejects URLs outside allowed seed domain.
  - `TestRecovery_Validation_NonHTTPRejected`: rejects `ftp://` or other schemes.
  - `TestRecovery_Validation_PrivateIPBlockedUnlessAllowed`: blocks SSRF unless `--allow-local` is set.
  - `TestRecovery_Validation_PathTraversalRejected`: blocks Unix-style directory traversal attempts in `SavedPath`.
  - `TestRecovery_Validation_DuplicateSequenceRejected`: rejects duplicate sequences.
  - `TestRecovery_Validation_NonPositiveSequenceRejected`: rejects sequence `<= 0`.
  - `TestRecovery_Validation_DepthExceededRejected`: rejects URL depth exceeding `MaxDepth`.
  - `TestRecovery_LoadWithFallback_EmptyPrimaryFallsBackToBackup`: verifies empty 0-byte primary file quarantines and falls back to backup.
  - `TestRecovery_LoadWithFallback_TruncatedJSONFallsBackToBackup`: verifies truncated JSON primary quarantines and falls back to backup.
  - `TestRecovery_Validation_UnsupportedVersionRejected`: rejects unsupported schema versions (`Version != 1`).
  - `TestRecovery_Validation_SymlinkEscapeRejected`: rejects symlinks in `SavedPath` that resolve outside the storage directory.
  - `TestRecovery_Validation_WindowsPathTraversalRejected`: rejects Windows-style backslash traversal attempts (`..\..\`).
- **Controller Recovery Tests (`internal/controller/recovery_test.go`):**
  - `TestRecovery_CrashSimulationAndFullGraphCompletion`: end-to-end multi-cycle crash simulation, state recovery, and full graph completion.
  - `TestRecovery_InProgressRestoredToPending`: verifies in-progress tasks are reset to pending.
  - `TestRecovery_PendingOrderPreservesDiscoverySequence`: verifies restored queue is sorted by sequence ascending.
  - `TestRecovery_MissingSavedFileDemotedToPending`: verifies missing files on disk trigger demotion to pending.
  - `TestRecovery_NextSequenceExceedsEveryExistingSequence`: verifies monotonic sequence numbers after recovery.
  - `TestRecovery_FailedURLs_PreservedByDefault`: verifies failed URLs stay failed.
  - `TestRecovery_FailedURLs_RetriedWithFlag`: verifies `--retry-failed` demotes failed URLs to pending.
  - `TestRecovery_CumulativeElapsedAcrossMultipleCycles`: verifies runtime duration accumulates across recovery cycles.
  - `TestRecovery_AlreadyCompleteSession_DoesNotStartWorkers`: verifies clean exit without starting worker pools.
  - `TestRecovery_CompatibleWithPauseResumeStop`: verifies runtime pause/resume/stop work seamlessly after recovery.

### Verification Output
- `go vet ./...`: PASSED (0 warnings)
- `go test -v ./...`: ALL PASSED across all packages (31 controller tests with 33 PASS lines, 4 fetcher tests, all parser subtests, 19 state tests, all urlmanager subtests)
- `go test -count=1 -race ./...`: ALL PASSED (0 data races detected)

---

## Phase 7: REST API

### Stage 1: URL Manager Query Methods & Unit Tests
- **Implementation Summary (`internal/urlmanager/manager.go`):**
  - Added `GetBySequence(seq int64) (*URLItem, bool)`:
    - Scans URL table under `m.mu.RLock()` to find the URL matching sequence `seq`.
    - Returns a deep struct value copy, preserving memory isolation and preventing external mutation of internal URL map records.
    - Rejects sequence `<= 0` immediately.
  - Added `URLFilterOptions` struct:
    - `Status *URLStatus`: optional status filter (`pending`, `in_progress`, `completed`, `failed`).
    - `Depth *int`: optional discovery depth filter.
    - `Offset int`: zero-based pagination offset.
    - `Limit int`: page limit bounded between 1 and 100 (default 20).
    - `SortBy string`: sort criterion (`"seq_asc"` [default], `"seq_desc"`, `"depth_asc"`, `"depth_desc"`, `"time_desc"`).
  - Added `GetFilteredURLs(opts URLFilterOptions) (items []URLItem, total int, filtered int)`:
    - **Bounded Lock Retention:** Holds `m.mu.RLock()` strictly long enough to filter and copy matching items into a value slice.
    - **Lock-Free Processing:** Releases read lock before performing sorting, offset window calculation, and response slicing.
    - Safely handles out-of-bounds offsets and partial last pages.
- **Automated Unit Tests (`internal/urlmanager/query_test.go`):**
  - `TestURLManager_GetBySequence`: verifies valid sequence lookup, non-existent sequence handling, non-positive sequence rejection, and data mutation isolation.
  - `TestURLManager_GetFilteredURLs_Pagination`: verifies offset/limit windows, page 1/2/3, partial pages, beyond-filtered offsets, and default limits.
  - `TestURLManager_GetFilteredURLs_Filtering`: verifies status filtering, depth filtering, combined filters, and empty filter result sets.
  - `TestURLManager_GetFilteredURLs_Sorting`: verifies `seq_desc`, `depth_asc`, `depth_desc`, and `time_desc` sorting order.
  - `TestURLManager_GetFilteredURLs_ConcurrentSafe`: verifies 8 concurrent writer goroutines and 8 concurrent reader goroutines under race detector.
- **Verification Output:**
  - `go vet ./internal/urlmanager/...`: PASSED (0 warnings)
  - `go test -v ./internal/urlmanager/...`: ALL PASSED (9 top-level tests, 21 subtests)
  - `go test -count=1 -race ./internal/urlmanager/...`: ALL PASSED (0 data races)

### Stage 2: Server Core & CrawlerService
- **Implementation Summary (`internal/server/service.go`):**
  - **Lifecycle State Machine:**
    - States: `IDLE`, `RUNNING`, `PAUSED`, `STOPPING`, `STOPPED`, `COMPLETED`.
    - Enforced transitions:
      `IDLE -> RUNNING`, `RUNNING -> PAUSED`, `PAUSED -> RUNNING`, `RUNNING -> STOPPING`, `PAUSED -> STOPPING`, `STOPPING -> STOPPED`, `RUNNING -> COMPLETED`.
    - Start requests while `STOPPING` return `ErrCrawlStopping`.
    - Start requests while `RUNNING` or `PAUSED` return `ErrCrawlAlreadyRunning`.
  - **Concurrency & Deadlock Prevention Strategy:**
    - Never holds the `CrawlerService` mutex while calling potentially blocking Controller methods (`ctrl.Stop()`, `ctrl.CreateCheckpoint()`, or `doneChan` wait).
    - General execution pattern:
      1. Acquire service mutex (`s.mu.Lock()`).
      2. Validate lifecycle state.
      3. Capture active session/controller pointers.
      4. Set intermediate state (e.g. `StateStopping`).
      5. Release service mutex (`s.mu.Unlock()`).
      6. Invoke Controller method (`ctrl.Stop()` or `ctrl.CreateCheckpoint()`).
      7. Reacquire mutex / wait on `sessionDoneChan`.
  - **Asynchronous Execution & Generation Guard:**
    - `StartCrawl()` starts a new `Controller` on a background goroutine and monitors completion via `superviseSession()`.
    - Generation check: checks `s.activeSessionID == sessID` under mutex lock to ensure a terminated older session can never overwrite a newer session's state.
  - **SSRF Operator Control:**
    - `AllowLocal` is strictly managed via operator `ServiceOptions.AllowLocal`. Client-facing `StartCrawlRequest` cannot enable private IP crawling.
  - **Thread-Safe Read Queries:**
    - `GetStatus()`, `GetStats()`, `GetURLs()`, `GetURLBySequence()`, `GetWorkers()`, `GetSessionInfo()` safe for concurrent execution during crawls.
- **Automated Unit Tests (`internal/server/service_test.go`):**
  - 17 test functions covering: service initialization, starting new crawls, rejecting duplicate starts, natural completion, pause/resume, stop while running/paused, session restarts, rejecting start while stopping, checkpoint resumption, retry-failed recovery, manual checkpointing, read queries during crawl, concurrent queries, concurrent lifecycle operations, generation isolation, and graceful shutdown.
- **Verification Output:**
  - `go vet ./...`: PASSED (0 warnings)
  - `go test -v -count=1 ./internal/server/...`: ALL PASSED (17 tests)
  - `go test -count=1 -race ./internal/server/...`: ALL PASSED (0 data races)
  - `go test -count=1 -race ./...`: ALL PASSED across all packages

### Stage 3: HTTP Handlers, Routing, Models, and Middleware
- **Implementation Summary:**
  - **API Models (`internal/server/models.go`):**
    - Standard response envelopes: `APIResponse{Success, Data}` and `APIErrorResponse{Success: false, Error: {Code, Message, Details}}`.
    - DTOs: `StartCrawlRequestDTO` (safely excludes `allow_local` to prevent client SSRF override), `StartCrawlResponseDTO`, `CrawlStatusResponseDTO`, `CrawlStatsResponseDTO`, `ControlActionResponseDTO`, `CheckpointResponseDTO`, `WorkerInfoResponseDTO`, `WorkerListResponseDTO`, `URLItemResponseDTO`, `URLListResponseDTO`, and `PaginationResponseDTO`.
  - **Middleware (`internal/server/middleware.go`):**
    - `RecoveryMiddleware`: catches unhandled panics, logs stack trace, returns HTTP 500 JSON envelope without terminating process.
    - `JSONContentTypeMiddleware`: enforces `Content-Type: application/json; charset=utf-8` on all outgoing API responses.
    - `LoggingMiddleware`: logs method, URI path, status code, and execution duration.
    - `CORSMiddleware`: configurable CORS policy without wildcard `*` by default; handles `OPTIONS` preflight with `204 No Content` and caches headers (`Max-Age: 86400`).
  - **Routing & Handlers (`internal/server/handlers.go` and `internal/server/handlers_url.go`):**
    - Leverages Go 1.22+ `http.ServeMux` pattern-based routing with explicit HTTP methods and path variables (`GET /api/urls/{id}`).
    - Strict JSON decoding:
      - `http.MaxBytesReader` caps request bodies at 1 MB.
      - `decoder.DisallowUnknownFields()` detects and rejects injected fields.
      - Trailing content enforcement: calls `decoder.Decode(&extra)` a second time and strictly requires `io.EOF`, rejecting multiple consecutive JSON objects, arbitrary trailing text, or trailing JSON values (numbers, strings, booleans, arrays).
    - Comprehensive input validation:
      - Validates URL scheme (`http` / `https`), non-empty host, and valid URL syntax.
      - Range checks for worker counts, page limits, depth bounds, and timeout values.
      - Aligned URL query parameter pagination bounds sharing `urlmanager` constants (`DefaultPaginationLimit = 20`, `MaxPaginationLimit = 100`, `DefaultPaginationOffset = 0`, `MinPaginationOffset = 0`). Rejects `limit < 1`, `limit > 100`, `offset < 0`.
      - URL query filters: whitelist validation for status (`pending`, `in_progress`, `completed`, `failed`), depth, and sort modes (`seq_asc`, `seq_desc`, `depth_asc`, `depth_desc`, `time_desc`).
      - URL sequence parsing: enforces positive `int64` sequence IDs with proper 400 Bad Request error envelopes.
    - Standard error translation:
      - Maps service errors (`ErrCrawlAlreadyRunning`, `ErrCrawlStopping`, `ErrPreviousSessionTerminating`) to HTTP 409 Conflict.
      - Maps `ErrNoActiveCrawl` and sequence lookups not found to HTTP 404 Not Found.
      - Maps invalid parameters to HTTP 400 Bad Request.
      - Unregistered HTTP methods on registered paths return HTTP 405 Method Not Allowed via Go 1.22 ServeMux routing.
- **Automated Tests (`internal/server/handlers_test.go` and `internal/server/service_test.go`):**
  - **Handler Tests (24 top-level test functions + 15 subtests):**
    1. `TestHandlers_SuccessfulResponses`: all endpoints return expected codes and envelopes.
    2. `TestHandlers_InvalidJSON`: malformed syntax rejected.
    3. `TestHandlers_MissingRequiredFields`: missing seed URL rejected.
    4. `TestHandlers_UnknownJSONFields`: unrecognized fields rejected by `DisallowUnknownFields`.
    5. `TestHandlers_OversizedRequestBody`: payloads > 1 MB rejected.
    6. `TestHandlers_InvalidURLSchemes`: non-http(s) schemes rejected.
    7. `TestHandlers_InvalidNumericConfiguration`: invalid workers, pages, depth, timeouts rejected.
    8. `TestHandlers_InvalidPaginationParameters`: invalid strings/negatives rejected.
    9. `TestHandlers_PaginationBoundsAndDefaults`: subtests for no limit (default 20), limit 0, limit 100, limit 101, negative offset, offset beyond filtered count.
    10. `TestHandlers_InvalidURLStatusFilters`: invalid status string rejected.
    11. `TestHandlers_InvalidSortingValues`: invalid sort option rejected.
    12. `TestHandlers_MissingURLSequence`: empty ID in path returns 404.
    13. `TestHandlers_InvalidURLSequence`: non-positive or non-numeric IDs return 400.
    14. `TestHandlers_NoActiveCrawl`: queries when idle/stopped return 404.
    15. `TestHandlers_ConflictingLifecycleOperations`: duplicate starts return 409.
    16. `TestHandlers_UnsupportedHTTPMethods`: wrong methods return 405.
    17. `TestHandlers_ConsistentResponseEnvelopes`: verifies envelope format and success flag.
    18. `TestHandlers_PanicRecoveryMiddleware`: verifies unhandled panics produce 500 error envelope without crashing process.
    19. `TestHandlers_ConcurrentReadOnlyRequests`: concurrent GET requests under race detector.
    20. `TestHandlers_ConcurrentLifecycleRequests`: concurrent pause, resume, checkpoint, stop under race detector.
    21. `TestHandlers_ManualCheckpointEndpoint`: manual checkpointing via HTTP.
    22. `TestHandlers_WorkerStateEndpoint`: worker pool monitor inspection.
    23. `TestHandlers_MultipleJSONValuesRejected`: subtests for single JSON (accepted), consecutive objects (rejected), trailing arbitrary text (rejected), trailing values (numbers, strings, booleans, arrays, null rejected).
    24. `TestHandlers_CORSVerification`: CORS preflight, allowed and disallowed origin handling.
  - **Service Tests (17 top-level test functions):**
    1-17 covering lifecycle transitions, deadlock avoidance, generation tracking, and recovery.
- **Verification Output:**
  - `go vet ./...`: PASSED (0 warnings)
  - `go test -v -count=1 ./internal/server/...`: ALL PASSED (41 top-level test functions, 15 subtests, 56 total test executions)
  - `go test -count=1 -race ./internal/server/...`: ALL PASSED (0 data races)
  - `go test -count=1 -race ./...`: ALL PASSED across all packages (124 tests)

### Stage 4: HTTP Server Lifecycle and CLI Integration
- **Implementation Summary:**
  - **HTTP Server Abstraction (`internal/server/server.go`):**
    - `ServerConfig` encapsulates host, port, ephemeral port support, service reference, CORS origins, and bounded timeouts.
    - Standard library bounded server settings:
      - `ReadHeaderTimeout`: 5 seconds
      - `ReadTimeout`: 15 seconds
      - `WriteTimeout`: 30 seconds (bounded buffer for checkpoint persistence)
      - `IdleTimeout`: 60 seconds
      - `MaxHeaderBytes`: 1 MB (`1 << 20`)
      - `ShutdownTimeout`: 15 seconds
    - `Server.Start()`: synchronous TCP socket bind via `net.Listen`, captures dynamic port if ephemeral, starts background `httpServer.Serve(l)` goroutine with clean `http.ErrServerClosed` handling.
    - `Server.Shutdown(ctx)`:
      1. Disables keep-alives via `httpServer.SetKeepAlivesEnabled(false)` to close idle connections.
      2. Closes listener and drains in-flight HTTP requests via `httpServer.Shutdown(ctx)`.
      3. Coordinates graceful crawler stop via `s.service.Shutdown(ctx)`, which stops the active Controller, drains worker pool results, and commits the `"shutdown"` checkpoint.
      4. Aggregates and logs any timeout or cleanup errors using `errors.Join`.
    - `Server.ListenAndServe()`: blocks until shutdown completes.
    - Repeated lifecycle safety: reinstantiates internal `http.Server` on each `Start()` cycle to allow servers to be started and stopped repeatedly without error.
  - **Server Binding & Security:**
    - Binds strictly to `127.0.0.1:8080` by default.
    - Operator-controlled SSRF enforcement (`AllowLocal`) is preserved from server startup options; client request payloads cannot override it.
    - Emits a security notice if an operator binds beyond loopback interfaces (`127.0.0.1`, `localhost`, `::1`), warning that authentication or reverse proxy protection is required.
  - **CLI Mode Coexistence (`cmd/crawlsync/main.go`):**
    - Added CLI flags: `--serve`, `--port` (default 8080), `--host` (default 127.0.0.1).
    - When `--serve` is omitted: runs existing terminal crawler mode with 100% backward compatibility (seeds, workers, depth, `--resume`, `--retry-failed`, stats).
    - When `--serve` is specified: initializes `CrawlerService` without starting a Controller or worker pool upfront; starts HTTP server and blocks until SIGINT/SIGTERM triggers graceful shutdown.
- **Automated Tests (`internal/server/server_test.go`):**
  - 18 top-level test functions:
    1. `TestServer_Construction`: verifies valid construction, defaults, and error on nil service.
    2. `TestServer_RouteRegistration`: verifies routes are registered and respond properly.
    3. `TestServer_StartsSuccessfully`: verifies server starts and sets running flag.
    4. `TestServer_ListensOnAvailableLocalPort`: verifies ephemeral port allocation on localhost.
    5. `TestServer_StatusEndpointRespondsOverActualHTTP`: makes live HTTP GET request to `/api/crawl/status`.
    6. `TestServer_AllAPIRoutesReachable`: tests all 10 API routes over live HTTP connections.
    7. `TestServer_InvalidHostPortConfiguration`: verifies rejection of invalid ports and host IPs.
    8. `TestServer_GracefulShutdown`: verifies clean termination and socket closure.
    9. `TestServer_ShutdownWithActiveCrawl`: verifies server shutdown stops active crawl and commits checkpoint.
    10. `TestServer_ShutdownWhilePaused`: verifies server shutdown cleanly stops paused crawls.
    11. `TestServer_NoCrawlerCreatedMerelyByStartingServer`: verifies service remains `IDLE` with nil controller on startup.
    12. `TestServer_StartStopRepeatedlyNoGoroutineLeaks`: verifies server can start and stop across multiple cycles.
    13. `TestServer_ExistingMiddlewareRemainsActive`: verifies JSON headers, CORS preflight, and 405 Method Not Allowed.
    14. `TestServer_ShutdownDoesNotRaceWithConcurrentRequests`: runs 15 concurrent HTTP workers during server shutdown under race detector.
    15. `TestServer_HTTPDrainTimeoutWhileHandlerActive`: verifies that slow/hanging HTTP handlers trigger a logged timeout error without hanging indefinitely.
    16. `TestServer_CrawlerShutdownAttemptedAfterHTTPTimeout`: verifies that even if HTTP draining times out, Phase 2 still executes with its dedicated budget to stop the crawler and write the shutdown checkpoint.
    17. `TestServer_RepeatedShutdownCalls`: verifies that repeated calls to `Server.Shutdown()` are safe and idempotent.
    18. `TestServer_ShutdownWhenNoCrawlActive`: verifies that shutting down an idle server cleanly returns nil without creating empty checkpoints.
- **Verification Output:**
  - `go vet ./...`: PASSED (0 warnings)
  - `go test -v -count=1 ./internal/server/...`: ALL PASSED (59 top-level test functions, 15 subtests, 74 total test executions)
  - `go test -count=1 -race ./internal/server/...`: ALL PASSED (0 data races)
  - `go test -count=1 -race ./...`: ALL PASSED across all packages (155 tests)
  - CLI binary build: `go build -o crawlsync.exe ./cmd/crawlsync` succeeded; verified `--help`, CLI mode, and `--serve` mode over live HTTP.

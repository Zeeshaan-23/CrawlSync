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

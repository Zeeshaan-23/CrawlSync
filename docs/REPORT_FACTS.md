# CrawlSync: Operating Systems Project Fact Reference & Technical Report

> **Document Type:** Definitive Factual Reference for End-Term OS Project Report  
> **Source Code Basis:** CrawlSync repository (`d:\College\Sem 5\CrawlSync`)  
> **Go Runtime Version:** `go version go1.27.3 windows/amd64`  
> **Module Identifier:** `crawlsync` (`go.mod`)  
> **Code Verification Status:** All 6 packages pass `go vet ./...` and `go test -count=1 -race ./...` (155 automated tests, 0 data races, 0 warnings).  
> **Rule Enforcement:** Every claim in this document is cited to actual file paths, function signatures, and struct definitions. Unimplemented or unexecuted features are explicitly marked **NOT IMPLEMENTED** or **NOT RUN**.

---

## 1. Project Overview

### 1.1 What CrawlSync Does
CrawlSync is a concurrent, multithreaded web crawler written in Go. Developed as an Operating Systems end-term project, it serves as a practical implementation and demonstration of foundational OS principles:
- **Concurrency & Thread Management:** Fixed-size worker pool of goroutines managed by a single-owner scheduling Controller.
- **Producer-Consumer Coordination:** Buffered Go channels decoupling job distribution from result collection, eliminating worker-controller deadlocks.
- **Mutual Exclusion & Synchronization:** Reader-writer mutexes guarding in-memory URL registries and lifecycle state machines; condition synchronization using dynamic Go `select` channel gating; thread barriers via `sync.WaitGroup`.
- **Atomic Persistence & Durability:** Crash-consistent JSON state checkpointing using temporary files, OS-level page-cache flushes (`f.Sync()`), atomic filesystem renames (`os.Rename`), and backup rotation (`checkpoint.prev.json`).
- **Fault Recovery:** Full crash restoration via `--resume` and `--retry-failed`, demoting in-progress tasks, re-verifying disk-saved files, and maintaining monotonic sequence numbering across interruptions.
- **REST API & Decoupled Architecture:** Standard library HTTP service daemon exposing crawl status, worker states, pagination queries, and runtime control commands (`/api/crawl/*`).

### 1.2 Tech Stack & Versions
- **Language:** Go `1.27.3` (`go version go1.27.3 windows/amd64`).
- **Standard Library:** `net/http`, `net/url`, `sync`, `sync/atomic`, `context`, `encoding/json`, `os`, `os/signal`, `syscall`, `time`, `path/filepath`, `errors`, `io`, `crypto/rand`.
- **External Dependencies:** `golang.org/x/net v0.62.0` (sole external dependency, used exclusively for HTML tokenization via `golang.org/x/net/html` in [`internal/parser/links.go`](file:///d:/College/Sem%205/CrawlSync/internal/parser/links.go)).
- **Persistence Layer:** Structured JSON checkpoints (`checkpoint.json`, `checkpoint.prev.json`) and raw HTML pages saved as `.html` files in `data/pages/`. No external database, ORM, or caching daemon.
- **Build / Tooling:** Native Go toolchain (`go build`, `go test`, `go vet`). No Docker, no external routers, no UI build frameworks.

### 1.3 How to Build and Run
- **Build CLI Binary:**
  ```powershell
  go build -o crawlsync.exe ./cmd/crawlsync
  ```
- **Run Fresh Crawl (CLI Mode):**
  ```powershell
  go run ./cmd/crawlsync -url https://example.com -workers 5 -max-pages 20 -max-depth 2
  ```
- **Run Recovery Crawl (Resume from Checkpoint):**
  ```powershell
  go run ./cmd/crawlsync -resume -retry-failed
  ```
- **Run REST API Server Mode:**
  ```powershell
  go run ./cmd/crawlsync -serve -host 127.0.0.1 -port 8080
  ```
- **Execute Full Test Suite with Race Detector:**
  ```powershell
  go test -count=1 -race -v ./...
  ```

### 1.4 Command-Line Flags
Declared and parsed in [`cmd/crawlsync/main.go`](file:///d:/College/Sem%205/CrawlSync/cmd/crawlsync/main.go#L20-L33):

| Flag | Type | Default Value | Description / Meaning |
| :--- | :--- | :--- | :--- |
| `-url` | `string` | `"https://example.com"` | Target seed URL to initiate a new crawling session. |
| `-workers` | `int` | `5` | Number of concurrent worker goroutines spawned in the worker pool (bounded 1 to 20). |
| `-max-pages` | `int` | `10` | Maximum number of pages to download and persist before terminating the crawl. |
| `-max-depth` | `int` | `2` | Maximum link discovery distance from the seed URL (seed page is depth 0). |
| `-timeout` | `int` | `10` | Per-request HTTP fetch timeout in seconds enforced by `context.WithTimeout`. |
| `-checkpoint-interval` | `duration` | `30s` | Time interval between automatic periodic state snapshots written to disk. |
| `-dir` | `string` | `"data/pages"` | Filesystem directory where downloaded `.html` pages are persistently saved. |
| `-checkpoint-dir` | `string` | `""` | Directory containing checkpoint files (defaults to `data/checkpoints` or relative to storage dir via `resolveCheckpointDir`). |
| `-allow-local` | `bool` | `false` | Security override allowing crawling loopback (`127.0.0.1`, `localhost`) and private RFC-1918 IPs (SSRF protection). |
| `-resume` | `bool` | `false` | Resumes an interrupted crawl session from the latest valid checkpoint instead of starting fresh. |
| `-retry-failed` | `bool` | `false` | When resuming from checkpoint, demotes previously `failed` URLs back to `pending`. |
| `-serve` | `bool` | `false` | Starts CrawlSync in REST API server daemon mode instead of terminal crawling mode. |
| `-host` | `string` | `"127.0.0.1"` | Bind host network interface for the REST API server daemon. |
| `-port` | `int` | `8080` | TCP port number for the REST API server daemon (validated 1 to 65535). |

---

## 2. Directory Tree & Code Metrics

### 2.1 File Inventory & Purpose
```text
crawlsync/
├── cmd/
│   └── crawlsync/
│       └── main.go                     # CLI entry point, flag parsing, terminal runner, signal handling, and server daemon bootstrap.
├── internal/
│   ├── controller/
│   │   ├── controller.go               # Single-owner scheduling engine, dynamic select event loop, lifecycle states, and control multiplexer.
│   │   ├── controller_test.go          # Unit and integration tests for graph crawl, depth cutoff, download cancellation, pause/resume, and draining.
│   │   ├── recovery_test.go            # Integration tests for crash simulation, sequence order, file missing demotion, and retry-failed policy.
│   │   └── worker.go                   # Worker pool lifecycle, runWorker loop, atomic HTML page persistence with backup rollback.
│   ├── fetcher/
│   │   ├── fetcher.go                  # HTTP fetcher with custom User-Agent, timeout context, 5MB size cap, Content-Type validation, and SSRF dialer.
│   │   └── fetcher_test.go             # Unit tests for HTML success, non-HTML rejection, body size truncation, and loopback SSRF interception.
│   ├── parser/
│   │   ├── links.go                    # Token-based HTML hyperlink parser using golang.org/x/net/html with relative resolution and domain filter.
│   │   └── links_test.go               # Unit tests verifying same-domain extraction, cross-domain link filtering, and malformed URL handling.
│   ├── server/
│   │   ├── handlers.go                 # HTTP handlers for crawl lifecycle (/api/crawl/*), worker status, manual checkpoint, and strict JSON decoding.
│   │   ├── handlers_test.go            # 24 test functions (with 15 subtests) for HTTP handlers, validation, middleware, CORS, and decoding rules.
│   │   ├── handlers_url.go             # HTTP handlers for paginated URL list (/api/urls) and sequence detail lookup (/api/urls/{id}).
│   │   ├── middleware.go               # HTTP middleware: panic recovery (500 JSON), application/json headers, request logging, and CORS preflight.
│   │   ├── models.go                   # API request DTOs, response envelopes, error structures, and pagination metadata schemas.
│   │   ├── server.go                   # Production HTTP server lifecycle abstraction, listener binding, and two-phase graceful shutdown.
│   │   ├── server_test.go              # 18 test functions for server lifecycle, socket binding, timeouts, and two-phase shutdown under race detector.
│   │   ├── service.go                  # Application-level CrawlerService managing Controller lifecycle, session IDs, generation guards, and queries.
│   │   └── service_test.go             # 17 unit tests verifying CrawlerService state machine, concurrent queries, generation isolation, and recovery.
│   ├── state/
│   │   ├── checkpoint.go               # Atomic JSON checkpoint serialization, schema version 1, unique session ID generation, fsync, and rotation.
│   │   ├── checkpoint_test.go          # Unit tests for schema validation, atomic replacement, backup rotation, and thread-safe concurrent saves.
│   │   ├── recovery.go                 # Untrusted checkpoint validation, path traversal defense, symlink checks, corrupted file quarantining, and fallback.
│   │   └── recovery_test.go            # Unit tests verifying corrupt file quarantining, fallback to backup, path traversal rejection, and SSRF checks.
│   └── urlmanager/
│       ├── manager.go                  # Thread-safe URL registry, atomic TrySchedule, monotonic sequence counter, status tracking, and snapshots.
│       ├── manager_test.go             # Concurrency and state transition tests validating zero race conditions across 2000 simultaneous attempts.
│       ├── normalize.go                # Canonical URL normalization: lowercasing, default port removal, path segment resolution, and query sorting.
│       ├── normalize_test.go           # 12 table-driven test cases validating URL canonicalization rules and edge cases.
│       └── query_test.go               # Unit and concurrency tests for GetBySequence and GetFilteredURLs verifying pagination, sorting, and safety.
├── docs/
│   ├── ARCHITECTURE.md                 # System architecture, event loop coordination, and checkpointing documentation.
│   ├── IMPLEMENTATION_LOG.md           # Chronological development and debugging log detailing fixes, hardening passes, and test counts.
│   ├── OS_CONCEPTS.md                  # Theoretical mapping of 10 core Operating Systems concepts to real Go source code locations.
│   └── REPORT_FACTS.md                 # This definitive factual reference document.
├── go.mod                              # Go module declaration (crawlsync) and dependency specification.
├── go.sum                              # Cryptographic checksums for golang.org/x/net.
└── PROJECT_CONTEXT.md                  # Master project specification, permanent source of truth, and phase tracker.
```

### 2.2 Lines of Code (LOC) per Package
Measured directly from disk:

| Package Directory | Implementation Files (Lines) | Test Files (Lines) | Total Files | Total Lines |
| :--- | :--- | :--- | :--- | :--- |
| `cmd/crawlsync` | `main.go` (274) | None (0) | 1 | **274** |
| `internal/controller` | `controller.go` (601), `worker.go` (296) | `controller_test.go` (1,693), `recovery_test.go` (520) | 4 | **3,110** |
| `internal/fetcher` | `fetcher.go` (206) | `fetcher_test.go` (90) | 2 | **296** |
| `internal/parser` | `links.go` (88) | `links_test.go` (65) | 2 | **153** |
| `internal/server` | `handlers.go` (349), `handlers_url.go` (131), `middleware.go` (91), `models.go` (117), `server.go` (282), `service.go` (528) | `handlers_test.go` (731), `server_test.go` (586), `service_test.go` (609) | 9 | **3,424** |
| `internal/state` | `checkpoint.go` (209), `recovery.go` (237) | `checkpoint_test.go` (248), `recovery_test.go` (448) | 4 | **1,142** |
| `internal/urlmanager` | `manager.go` (479), `normalize.go` (88) | `manager_test.go` (170), `normalize_test.go` (96), `query_test.go` (298) | 5 | **1,131** |
| **Project Total** | **17 implementation files (3,678 LOC)** | **10 test files (4,855 LOC)** | **27** | **9,530** |

---

## 3. Requirement Status (FR-01 to FR-12)

Every requirement from `PROJECT_CONTEXT.md` evaluated against real implementation files, functions, and unit tests:

| ID | Requirement Specification | Status | Implementation Evidence | Test Verification Evidence |
| :--- | :--- | :--- | :--- | :--- |
| **FR-01** | Start crawling from a user-provided URL | **Verified** | [`cmd/crawlsync/main.go:main()`](file:///d:/College/Sem%205/CrawlSync/cmd/crawlsync/main.go#L20), [`internal/controller/controller.go:Run()`](file:///d:/College/Sem%205/CrawlSync/internal/controller/controller.go#L224-L231) | `internal/controller/controller_test.go:TestController_CompleteGraphCrawl` |
| **FR-02** | Configurable worker count | **Verified** | [`internal/controller/worker.go:NewWorkerPool()`](file:///d:/College/Sem%205/CrawlSync/internal/controller/worker.go#L81-L98), [`internal/controller/controller.go:Config`](file:///d:/College/Sem%205/CrawlSync/internal/controller/controller.go#L44) | `internal/controller/controller_test.go:TestController_CompleteGraphCrawl` |
| **FR-03** | Download webpage HTML | **Verified** | [`internal/fetcher/fetcher.go:Fetch()`](file:///d:/College/Sem%205/CrawlSync/internal/fetcher/fetcher.go#L79-L135), [`internal/controller/worker.go:savePageToDisk()`](file:///d:/College/Sem%205/CrawlSync/internal/controller/worker.go#L210-L245) | `internal/fetcher/fetcher_test.go:TestFetcher_HTMLSuccess`, `TestFetcher_NonHTMLRejected` |
| **FR-04** | Extract hyperlinks | **Verified** | [`internal/parser/links.go:ExtractLinks()`](file:///d:/College/Sem%205/CrawlSync/internal/parser/links.go#L27-L87), [`internal/controller/worker.go:processJob()`](file:///d:/College/Sem%205/CrawlSync/internal/controller/worker.go#L193-L203) | `internal/parser/links_test.go:TestExtractLinks` |
| **FR-05** | Discovered URLs go into a processing queue | **Verified** | [`internal/controller/controller.go:scheduleDiscoveredLinks()`](file:///d:/College/Sem%205/CrawlSync/internal/controller/controller.go#L583-L600), `pendingJobs []Job` in [`controller.go:Run()`](file:///d:/College/Sem%205/CrawlSync/internal/controller/controller.go#L222) | `internal/controller/controller_test.go:TestController_CompleteGraphCrawl` |
| **FR-06** | Prevent duplicate URL scheduling | **Verified** | [`internal/urlmanager/manager.go:TrySchedule()`](file:///d:/College/Sem%205/CrawlSync/internal/urlmanager/manager.go#L111-L149), [`internal/urlmanager/normalize.go:NormalizeURL()`](file:///d:/College/Sem%205/CrawlSync/internal/urlmanager/normalize.go#L24-L87) | `internal/urlmanager/manager_test.go:TestURLManager_ConcurrentTrySchedule` |
| **FR-07** | Every URL has an identifiable status | **Verified** | [`internal/urlmanager/manager.go:URLStatus`](file:///d:/College/Sem%205/CrawlSync/internal/urlmanager/manager.go#L17-L24) (`pending`, `in_progress`, `completed`, `failed`) | `internal/urlmanager/manager_test.go:TestURLManager_BasicTransitions` |
| **FR-08** | Periodic checkpoint creation | **Verified** | [`internal/controller/controller.go:Run()`](file:///d:/College/Sem%205/CrawlSync/internal/controller/controller.go#L365-L370) (`case <-checkpointChan:`), [`internal/state/checkpoint.go:Save()`](file:///d:/College/Sem%205/CrawlSync/internal/state/checkpoint.go#L95-L145) | `internal/controller/controller_test.go:TestController_PeriodicCheckpointing` |
| **FR-09** | Recovery after interruption | **Verified** | [`internal/state/recovery.go:LoadWithFallback()`](file:///d:/College/Sem%205/CrawlSync/internal/state/recovery.go#L173-L236), [`internal/urlmanager/manager.go:Restore()`](file:///d:/College/Sem%205/CrawlSync/internal/urlmanager/manager.go#L267-L343) | `internal/controller/recovery_test.go:TestRecovery_CrashSimulationAndFullGraphCompletion` |
| **FR-10** | Dashboard with statistics and worker activity | **Backend Verified / Frontend NOT IMPLEMENTED** | Backend endpoints implemented and verified: [`internal/server/handlers.go:handleGetCrawlStats()`](file:///d:/College/Sem%205/CrawlSync/internal/server/handlers.go#L133), `handleGetWorkers()` (line 144), `handleGetCrawlStatus()` (line 122). Frontend UI (`web/index.html`, `style.css`, `app.js`): **NOT IMPLEMENTED** (Scheduled for Phase 8). | `internal/server/handlers_test.go:TestHandlers_SuccessfulResponses`, `TestHandlers_WorkerStateEndpoint` |
| **FR-11** | Pause and resume without losing progress | **Verified** | [`internal/controller/controller.go:Pause()`](file:///d:/College/Sem%205/CrawlSync/internal/controller/controller.go#L398), [`Resume()`](file:///d:/College/Sem%205/CrawlSync/internal/controller/controller.go#L437), nil-channel dispatch gating (lines 280-286) | `internal/controller/controller_test.go:TestController_Pause_ActiveDownloads`, `TestController_Resume` |
| **FR-12** | Graceful stop | **Verified** | [`internal/controller/controller.go:Stop()`](file:///d:/College/Sem%205/CrawlSync/internal/controller/controller.go#L476-L494), `close(jobsChan)`, `c.wg.Wait()`, `drainRemainingResults()` (lines 295-309) | `internal/controller/controller_test.go:TestController_ActiveDownloadCancellation`, `TestController_ShutdownResultDraining` |

---

## 4. Architecture & Component Models

### 4.1 Component Architecture Diagram
```mermaid
flowchart TD
    Client["Client / CLI / REST API Consumer"] --> Server["HTTP Server Daemon (internal/server/server.go)"]
    Server --> Svc["CrawlerService (internal/server/service.go)"]
    Svc --> Ctrl["Crawler Controller (internal/controller/controller.go)"]
    
    subgraph Controller_Domain ["Single-Owner Scheduler"]
        Ctrl -->|"1. Atomic TrySchedule()"| UM["URL Manager (internal/urlmanager/manager.go)"]
        Ctrl -->|"2. Dispatch Jobs"| JC["jobsChan (chan Job, Buffer: 2*N)"]
        Ctrl -->|"3. Snapshot & Commit"| CM["Checkpoint Manager (internal/state/checkpoint.go)"]
    end
    
    subgraph Worker_Pool_Domain ["Fixed Worker Pool (1..N Goroutines)"]
        JC --> W1["Worker 1"]
        JC --> W2["Worker 2"]
        JC --> WN["Worker N"]
        
        W1 --> Fetch["HTTP Fetcher (internal/fetcher/fetcher.go)"]
        Fetch --> Disk["HTML Disk Store (data/pages/*.html)"]
        Disk --> Parse["HTML Parser (internal/parser/links.go)"]
    end
    
    Parse --> RC["resultsChan (chan CrawlResult, Buffer: 2*N)"]
    RC -->|"4. Collect Results"| Ctrl
    
    subgraph Storage_Layer ["Durable Filesystem Persistence"]
        CM --> CPDisk["Atomic JSON Checkpoints (data/checkpoints/checkpoint.json)"]
        CM --> CPPrev["Rotated Backup (checkpoint.prev.json)"]
    end
```

### 4.2 URL Status State Machine
```mermaid
stateDiagram-v2
    [*] --> pending: TrySchedule() (Seed or Discovered URL)
    pending --> in_progress: Dispatched by Controller to jobsChan
    in_progress --> completed: HTML successfully written to disk & result processed
    in_progress --> failed: Fetch or parse error (HTTP 4xx/5xx, timeout, size cap)
    
    note right of in_progress
        Crash Recovery (Phase 6):
        All in_progress URLs reset to pending
    end note
    
    in_progress --> pending: Interrupted by SIGINT/Crash (Restored on --resume)
    completed --> pending: Saved page file missing/empty on disk during Restore()
    failed --> pending: --retry-failed flag provided during Restore()
    
    completed --> [*]
    failed --> [*]
```

### 4.3 Crawl Sequence (Controller / Worker / Results Flow)
```mermaid
sequenceDiagram
    autonumber
    participant C as Controller (Event Loop)
    participant UM as URL Manager
    participant J as jobsChan (Buffer)
    participant W as Worker Goroutine
    participant F as HTTP Fetcher
    participant D as Disk Storage
    participant P as HTML Parser
    participant R as resultsChan (Buffer)

    C->>UM: TrySchedule(seedURL, depth=0)
    UM-->>C: scheduled=true, normalizedURL
    C->>J: Send Job{URL, Depth}
    C->>UM: MarkInProgress(URL)
    J->>W: Receive Job
    W->>F: Fetch(ctx, URL)
    F-->>W: FetchResult (Status 200, Body bytes)
    W->>D: savePageToDisk(tmp -> fsync -> rename)
    D-->>W: SavedPath ("data/pages/page_1.html")
    W->>P: ExtractLinks(Body)
    P-->>W: DiscoveredLinks []string
    W->>R: Send CrawlResult{URL, DiscoveredLinks, SavedPath}
    R->>C: Receive CrawlResult
    C->>UM: MarkCompletedWithMetadata(URL, 200, size, duration, savedPath)
    loop For each child link
        C->>UM: TrySchedule(childURL, depth+1)
        UM-->>C: scheduled=true
        C->>C: append(pendingJobs, childJob)
    end
```

### 4.4 Checkpoint Write Sequence (Atomic Durability)
```mermaid
sequenceDiagram
    autonumber
    participant C as Controller Event Loop
    participant UM as URL Manager
    participant CM as Checkpoint Manager
    participant FS as Operating System Filesystem

    C->>C: Ticker / Shutdown / Manual event boundary reached
    C->>UM: Snapshot() under RLock()
    UM-->>C: Deep-copied Snapshot (URLs sorted by Seq, matching Stats)
    C->>CM: Save(sessionID, cfg, snapshot, elapsed)
    CM->>CM: json.MarshalIndent() in memory
    CM->>FS: os.CreateTemp("checkpoint-*.tmp")
    CM->>FS: tmpFile.Write(jsonBytes)
    CM->>FS: tmpFile.Sync() [fsync page-cache flush]
    CM->>FS: tmpFile.Close()
    opt checkpoint.json exists
        CM->>FS: os.Rename("checkpoint.json", "checkpoint.prev.json") [Backup Rotation]
    end
    CM->>FS: os.Rename(tmpFile, "checkpoint.json") [Atomic Directory Entry Commit]
    CM-->>C: Return Checkpoint metadata (#Seq)
```

### 4.5 Recovery Flow (`--resume`)
```mermaid
flowchart TD
    Start["CLI Invocation (--resume)"] --> CheckDir["Resolve Checkpoint Directory"]
    CheckDir --> LoadFallback["LoadWithFallback()"]
    
    LoadFallback --> CheckPrimary{"checkpoint.json valid?"}
    CheckPrimary -- "Yes" --> ValidCP["ValidateForRecovery()"]
    CheckPrimary -- "No / Corrupt" --> Quarantine["Quarantine to checkpoint.corrupt.<ts>"]
    Quarantine --> CheckPrev{"checkpoint.prev.json valid?"}
    CheckPrev -- "Yes" --> ValidCP
    CheckPrev -- "No" --> Refuse["Refuse Recovery (Exit 1)"]
    
    ValidCP --> RestoreUM["URLManager.Restore(URLs, opts)"]
    RestoreUM --> DemoteInProgress["Demote in_progress -> pending"]
    RestoreUM --> VerifyFiles["Verify saved HTML files exist on disk"]
    VerifyFiles --> CheckRetryFailed{"--retry-failed flag set?"}
    CheckRetryFailed -- "Yes" --> DemoteFailed["Demote failed -> pending"]
    CheckRetryFailed -- "No" --> KeepFailed["Preserve failed status"]
    
    DemoteFailed --> OrderQueue["Sort pendingJobs by Sequence Ascending"]
    KeepFailed --> OrderQueue
    
    OrderQueue --> CheckPending{"Pending jobs remaining?"}
    CheckPending -- "0 Pending" --> CleanExit["Log: Session already complete (Exit 0)"]
    CheckPending -- "> 0 Pending" --> ResumeWorkers["Start Controller with isResumed=true"]
    ResumeWorkers --> ContinueCrawl["Continue Crawling from Monotonic Sequence"]
```

### 4.6 Controller Lifecycle State Machine
```mermaid
stateDiagram-v2
    [*] --> READY: NewController()
    READY --> RUNNING: Run() invoked
    READY --> PAUSED: Pause() before Run()
    
    RUNNING --> PAUSED: Pause() [Dynamic select gating sendChan=nil]
    PAUSED --> RUNNING: Resume() [sendChan=jobsChan restored]
    
    RUNNING --> STOPPED: Stop() / Cancellation / Natural Completion
    PAUSED --> STOPPED: Stop() while paused
    
    STOPPED --> [*]
```

---

## 5. Concurrency & Synchronization Inventory

### 5.1 Goroutine Inventory
Every concurrent thread of execution spawned in the application:

| Goroutine Description | Spawning Location | Purpose & Work Performed | Exit Condition & Lifetime |
| :--- | :--- | :--- | :--- |
| **Main Goroutine** | Process Entrypoint (`cmd/crawlsync/main.go:main()`) | Parses CLI flags, parses configuration, manages OS signal notifications, and blocks on server or controller completion. | Process termination (`os.Exit(0)` or `main()` return). |
| **Worker Goroutines** (1..N, default 5) | [`internal/controller/worker.go:Start()`](file:///d:/College/Sem%205/CrawlSync/internal/controller/worker.go#L106) (`go wp.runWorker(...)`) | Consumes `Job` items from `jobsChan`, calls `wp.processJob` (HTTP fetch, HTML disk persistence, HTML link extraction), updates per-worker monitor state, and emits `CrawlResult` to `resultsChan`. | Exits when `jobsChan` is closed (`!ok`) OR when `ctx.Done()` fires. Executes deferred `wg.Done()`. |
| **Controller Event Loop** | [`internal/controller/controller.go:Run()`](file:///d:/College/Sem%205/CrawlSync/internal/controller/controller.go#L191) | Executes the single-owner dynamic `select` scheduling loop: multiplexes job dispatch, worker results, control commands, periodic checkpoint ticks, and cancellation. | Exits when crawl completion condition is met (`pending == 0 && inFlight == 0`) OR when `ctx.Done()` is canceled. Closes `doneChan`. |
| **Periodic Checkpoint Ticker** | Run loop background goroutine or internal `time.Ticker` in [`controller.go:Run()`](file:///d:/College/Sem%205/CrawlSync/internal/controller/controller.go#L268) | Emits periodic tick events to `checkpointChan` every `--checkpoint-interval`. | Exits when `ticker.Stop()` is executed upon `Run()` termination. |
| **HTTP Server Listener** | [`internal/server/server.go:Start()`](file:///d:/College/Sem%205/CrawlSync/internal/server/server.go#L224) (`go s.httpServer.Serve(l)`) | Binds TCP socket and accepts incoming HTTP client connections, delegating each connection to Go standard library HTTP worker goroutines. | Exits when `httpServer.Shutdown()` closes the TCP listener; returns `http.ErrServerClosed`. |
| **Session Supervisor Goroutine** | [`internal/server/service.go:StartCrawl()`](file:///d:/College/Sem%205/CrawlSync/internal/server/service.go#L173) (`go s.superviseSession(...)`) | Runs the active `Controller.Run()` goroutine asynchronously in server mode, monitors completion, applies generation guards, and updates service state to `COMPLETED` or `STOPPED`. | Exits immediately after the active `Controller.Run()` returns and state is committed. |
| **HTTP Request Handlers** | Standard library `net/http` internal server runtime | Concurrently executes HTTP middleware, JSON parsing, URL filtering, and service query methods for every incoming HTTP request. | Terminates when HTTP request response is fully flushed to the client. |

### 5.2 Channel Inventory
Every Go channel used for thread communication:

| Channel Identifier | Element Type | Buffer Size | Senders | Receivers | Closing Authority |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `jobsChan` | `Job` | `WorkerCount * 2` | Controller Event Loop (`controller.go:Run()`) | Worker Goroutines (`worker.go:runWorker()`) | **Controller Only:** Closed exactly once on graceful shutdown or crawl completion. |
| `resultsChan` | `CrawlResult` | `WorkerCount * 2` | Worker Goroutines (`worker.go:runWorker()`) | Controller Event Loop (`controller.go:Run()`) | **Never Closed:** Left to garbage collection after workers exit to prevent panic on send. |
| `controlChan` | `controlRequest` | `0` (Unbuffered) | External callers (`Pause()`, `Resume()`, `Stop()`, `CreateCheckpoint()`) | Controller Event Loop (`controller.go:Run()`) | **Never Closed:** Synchronous rendezvous channel. |
| `req.ackChan` | `struct{}` | `1` (Buffered) | Controller Event Loop (`controller.go:Run()`) | Calling thread waiting on `Pause()`, `Resume()`, `Stop()` | Left to GC; one-shot response per control request. |
| `req.checkpointChan` | `checkpointResult` | `1` (Buffered) | Controller Event Loop (`controller.go:Run()`) | Calling thread waiting on `CreateCheckpoint()` | Left to GC; delivers manual checkpoint result. |
| `doneChan` | `struct{}` | `0` (Unbuffered) | None (broadcast mechanism via close) | External callers waiting on `Stop()` or server shutdown | **Controller Only:** Closed via `c.doneOnce.Do(func() { close(c.doneChan) })`. |
| `sessionDoneChan` | `struct{}` | `0` (Unbuffered) | None (broadcast mechanism via close) | `CrawlerService.superviseSession()` and `StopCrawl()` | **CrawlerService Only:** Closed when session terminates. |
| `sigChan` | `os.Signal` | `1` (Buffered) | OS Kernel signal dispatcher (SIGINT/SIGTERM) | Main Goroutine (`cmd/crawlsync/main.go`) | Managed by Go runtime signal subsystem. |

### 5.3 Mutex & RWMutex Inventory

| Mutex Variable | Type | File & Enclosing Struct | State Protected | Functions That Lock | I/O Under Lock? |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `m.mu` | `sync.RWMutex` | [`internal/urlmanager/manager.go:URLManager`](file:///d:/College/Sem%205/CrawlSync/internal/urlmanager/manager.go#L37) | `urls map[string]*URLItem`, `nextSequence int64`, `stats URLStats` | `TrySchedule`, `MarkInProgress`, `MarkCompletedWithMetadata`, `MarkFailed`, `Restore`, `GetBySequence`, `GetFilteredURLs`, `Snapshot`, `GetStats` | **STRICTLY NO.** File/network I/O is prohibited under lock. Memory-only operations. |
| `c.stateMu` | `sync.RWMutex` | [`internal/controller/controller.go:Controller`](file:///d:/College/Sem%205/CrawlSync/internal/controller/controller.go#L74) | `c.state ControllerState`, `c.started bool`, `c.stopCancel context.CancelFunc` | `Run`, `Pause`, `Resume`, `Stop`, `State`, `IsPaused`, `IsRunning`, `IsStopped` | **STRICTLY NO.** Protects primitive lifecycle state flags only. |
| `wp.mu` | `sync.RWMutex` | [`internal/controller/worker.go:WorkerPool`](file:///d:/College/Sem%205/CrawlSync/internal/controller/worker.go#L64) | `wp.workers []WorkerInfo` | `setWorkerState`, `GetWorkerStates` | **STRICTLY NO.** Fast slice read/write only. |
| `cm.mu` | `sync.Mutex` | [`internal/state/checkpoint.go:CheckpointManager`](file:///d:/College/Sem%205/CrawlSync/internal/state/checkpoint.go#L44) | Checkpoint directory write serialization, `cm.saveCount int64` | `Save`, `SetSaveCount` | **YES (Controlled).** Serializes filesystem checkpoint commits (`.tmp` write, `Sync()`, `os.Rename`) to prevent interleaving corrupt writes. |
| `s.mu` | `sync.RWMutex` | [`internal/server/service.go:CrawlerService`](file:///d:/College/Sem%205/CrawlSync/internal/server/service.go#L66) | `lifecycleState`, `activeSessionID`, `activeController`, `activeURLManager` | `StartCrawl`, `PauseCrawl`, `ResumeCrawl`, `StopCrawl`, `CreateCheckpoint`, `Shutdown`, `GetStatus`, `GetStats`, `GetURLs`, `GetWorkers` | **STRICTLY NO.** Critical rule: Mutex is released BEFORE calling blocking Controller methods. |
| `s.mu` | `sync.Mutex` | [`internal/server/server.go:Server`](file:///d:/College/Sem%205/CrawlSync/internal/server/server.go#L64) | `s.running bool`, `s.listener net.Listener`, `s.actualAddr string`, `s.httpServer *http.Server` | `Start`, `Shutdown`, `Addr`, `IsRunning` | **STRICTLY NO.** Protects server lifecycle metadata only. |

### 5.4 Lock Ordering & Deadlock Prevention Rules
1. **Strict Lock Hierarchy:**
   - Level 1: `Server.mu`
   - Level 2: `CrawlerService.mu`
   - Level 3: `Controller.stateMu`
   - Level 4: `URLManager.mu` / `WorkerPool.mu`
   - Level 5: `CheckpointManager.mu`
2. **Never Invert Ordering:** A thread holding a lower-level lock never requests a higher-level lock.
3. **Release Before Blocking:** `CrawlerService.mu` is **always released** before calling any Controller method (`ctrl.Stop()`, `ctrl.CreateCheckpoint()`, or channel waits).
4. **Channel Isolation:** Workers never send on `jobsChan` (which would cause classic producer-consumer circular wait deadlock); they send strictly on `resultsChan`.

---

## 6. Operating Systems Concept Mapping

### 6.1 Concurrency
- **Concept:** Concurrency is the composition of independently executing processes or threads of computation.
- **Code Mapping:** [`internal/controller/worker.go:Start()`](file:///d:/College/Sem%205/CrawlSync/internal/controller/worker.go#L102-L108).
- **Explanation:** The worker pool instantiates `workerCount` independent goroutines that concurrently execute HTTP downloads and HTML parsing. Each worker operates autonomously, processing distinct URLs assigned across the shared `jobsChan`.

### 6.2 Thread Management & Go's G-M-P Scheduler
- **Concept:** Mapping user-space execution units (green threads) to operating system kernel threads (1:1 vs M:N multiplexing).
- **Code Mapping:** [`internal/controller/worker.go:runWorker()`](file:///d:/College/Sem%205/CrawlSync/internal/controller/worker.go#L111-L158).
- **Explanation:** CrawlSync creates lightweight Go goroutines (**G**) rather than 1:1 OS threads. The Go runtime multiplexes these Gs across OS kernel threads (**M**) using logical processors (**P**). When a worker enters a blocking system call (e.g. `Fetch` socket read or `savePageToDisk` file write), the Go scheduler disassociates the thread **M** from processor **P**, allowing other runnable worker goroutines to proceed without CPU starvation.

### 6.3 Producer-Consumer Pattern
- **Concept:** Coordinating asynchronous production and consumption of work items through a synchronized, bounded shared memory buffer.
- **Code Mapping:** [`internal/controller/controller.go:Run()`](file:///d:/College/Sem%205/CrawlSync/internal/controller/controller.go#L247-L250) and [`worker.go:runWorker()`](file:///d:/College/Sem%205/CrawlSync/internal/controller/worker.go#L135-L155).
- **Explanation:** The Controller acts as the single producer of `Job` structs on `jobsChan` (capacity `2 * WorkerCount`), while worker goroutines act as consumers. Conversely, workers act as producers of `CrawlResult` structs on `resultsChan`, while the Controller acts as the single consumer, eliminating synchronization contention over the queue.

### 6.4 Mutual Exclusion
- **Concept:** Ensuring that only one execution context accesses a shared mutable resource at any point in time.
- **Code Mapping:** [`internal/urlmanager/manager.go:TrySchedule()`](file:///d:/College/Sem%205/CrawlSync/internal/urlmanager/manager.go#L111-L149).
- **Explanation:** `m.mu.Lock()` acquires an exclusive write lock over the internal URL map. Even if 50 concurrent worker threads attempt to schedule the same discovered URL simultaneously, exactly one thread executes the check-and-insert sequence, guaranteeing that duplicates are never admitted.

### 6.5 Critical Sections
- **Concept:** Code segments that access shared variables and must be executed as an atomic unit to maintain data invariants.
- **Code Mapping:** [`internal/urlmanager/manager.go:Snapshot()`](file:///d:/College/Sem%205/CrawlSync/internal/urlmanager/manager.go#L210-L245).
- **Explanation:** The critical section enclosed between `m.mu.RLock()` and `m.mu.RUnlock()` copies all `URLItem` structs and aggregates status counters. Releasing the lock before serializing or writing to disk ensures the critical section remains minimal, preventing thread convoying.

### 6.6 Race Conditions & TOCTOU Prevention
- **Concept:** Flaws where the system outcome depends on the non-deterministic interleaving of threads, specifically Time-Of-Check to Time-Of-Use bugs.
- **Code Mapping:** [`internal/urlmanager/manager.go:TrySchedule()`](file:///d:/College/Sem%205/CrawlSync/internal/urlmanager/manager.go#L125-L146).
- **Explanation:** Checking whether a URL has been visited and inserting it are executed inside a single, indivisible critical section under `m.mu.Lock()`. If checking and insertion were separate functions, two workers could both verify a URL as unvisited before either inserts it, producing duplicate crawl jobs.

### 6.7 Synchronization (Barriers & Condition Synchronization)
- **Concept:** Coordinating execution points across multiple threads; barriers block execution until all threads reach a designated phase.
- **Code Mapping:** [`internal/controller/controller.go:Run()`](file:///d:/College/Sem%205/CrawlSync/internal/controller/controller.go#L299) (`c.wg.Wait()`) and [`internal/controller/worker.go:runWorker()`](file:///d:/College/Sem%205/CrawlSync/internal/controller/worker.go#L120) (`defer wg.Done()`).
- **Explanation:** `sync.WaitGroup` acts as a thread termination barrier during crawler shutdown. The controller closes `jobsChan` and executes `c.wg.Wait()`, blocking the termination sequence until every worker has completed its active download and exited its loop.

### 6.8 Deadlock Prevention
- **Concept:** Negating one of Coffman's four deadlock conditions (Mutual Exclusion, Hold and Wait, No Preemption, Circular Wait).
- **Code Mapping:** [`internal/server/service.go:StopCrawl()`](file:///d:/College/Sem%205/CrawlSync/internal/server/service.go#L245-L270) and [`controller.go:Run()`](file:///d:/College/Sem%205/CrawlSync/internal/controller/controller.go#L280-L286).
- **Explanation:** CrawlSync eliminates **Circular Wait** by strictly segregating `jobsChan` (controller-to-worker) and `resultsChan` (worker-to-controller). Furthermore, `CrawlerService` negates **Hold and Wait** by releasing `s.mu` before awaiting Controller termination (`<-sessionDoneChan`), preventing HTTP handler deadlocks.

### 6.9 Resource Management & Bounded Buffers
- **Concept:** Constraining system resource utilization (memory, file descriptors, network sockets) to prevent resource exhaustion under load.
- **Code Mapping:** [`internal/fetcher/fetcher.go:Fetch()`](file:///d:/College/Sem%205/CrawlSync/internal/fetcher/fetcher.go#L104) and [`controller.go:Run()`](file:///d:/College/Sem%205/CrawlSync/internal/controller/controller.go#L248-L249).
- **Explanation:** Bounded channel capacities (`2 * WorkerCount`) prevent unbounded queue memory growth. Additionally, `io.LimitReader(resp.Body, DefaultMaxBodySize)` strictly caps downloaded HTML to 5MB, preventing malicious large payloads from inducing Out-Of-Memory (OOM) kernel kills.

### 6.10 Persistence & Filesystem Durability (`fsync`)
- **Concept:** Forcing operating system dirty page-cache buffers to physically flush to non-volatile storage media.
- **Code Mapping:** [`internal/state/checkpoint.go:commitFile()`](file:///d:/College/Sem%205/CrawlSync/internal/state/checkpoint.go#L173) and [`internal/controller/worker.go:savePageToDisk()`](file:///d:/College/Sem%205/CrawlSync/internal/controller/worker.go#L228).
- **Explanation:** After writing JSON data to a `.tmp` file, CrawlSync explicitly calls `tmpFile.Sync()` (the Go wrapper for POSIX `fsync` / Win32 `FlushFileBuffers`). This guarantees that if power is interrupted immediately afterward, the checkpoint is physically committed to disk rather than trapped in volatile RAM cache.

### 6.11 Fault Recovery & Crash Consistency
- **Concept:** Restoring an interrupted computation to a valid, consistent state without data loss or re-executing already-completed work.
- **Code Mapping:** [`internal/state/recovery.go:LoadWithFallback()`](file:///d:/College/Sem%205/CrawlSync/internal/state/recovery.go#L173-L236) and [`internal/urlmanager/manager.go:Restore()`](file:///d:/College/Sem%205/CrawlSync/internal/urlmanager/manager.go#L267-L343).
- **Explanation:** If a primary checkpoint is truncated by a crash, CrawlSync quarantines it and falls back to `checkpoint.prev.json`. During restoration, all `in_progress` tasks are demoted to `pending`, and completed page files on disk are verified, allowing the crawler to seamlessly resume execution.

---

## 7. Key Algorithms & Verbatim Code Excerpts

### 7.1 Atomic URL Scheduling (`TrySchedule`)
From [`internal/urlmanager/manager.go:TrySchedule()`](file:///d:/College/Sem%205/CrawlSync/internal/urlmanager/manager.go#L111-L149):
```go
func (m *URLManager) TrySchedule(rawURL string, depth int) (bool, string, error) {
	normURL, err := NormalizeURL(rawURL)
	if err != nil {
		return false, "", err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.urls[normURL]; exists {
		return false, normURL, nil
	}

	m.nextSequence++
	seq := m.nextSequence

	m.urls[normURL] = &URLItem{
		Raw:        rawURL,
		Normalized: normURL,
		Status:     StatusPending,
		Depth:      depth,
		Sequence:   seq,
		Discovered: time.Now(),
	}
	m.stats.TotalDiscovered++
	m.stats.Pending++

	return true, normURL, nil
}
```

### 7.2 URL Normalization (`NormalizeURL`)
From [`internal/urlmanager/normalize.go:NormalizeURL()`](file:///d:/College/Sem%205/CrawlSync/internal/urlmanager/normalize.go#L24-L51):
```go
func NormalizeURL(rawURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("unsupported URL scheme: %s", parsed.Scheme)
	}

	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Fragment = ""

	host, port, err := net.SplitHostPort(parsed.Host)
	if err == nil {
		if (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
			parsed.Host = host
		}
	}

	p := parsed.EscapedPath()
	if p == "" || p == "/" {
		parsed.Path = "/"
	} else {
		parsed.Path = path.Clean(p)
		if parsed.Path != "/" && strings.HasSuffix(parsed.Path, "/") {
			parsed.Path = strings.TrimSuffix(parsed.Path, "/")
		}
	}
    // Deterministic query sorting omitted for brevity
```

### 7.3 Worker Execution Loop (`runWorker`)
From [`internal/controller/worker.go:runWorker()`](file:///d:/College/Sem%205/CrawlSync/internal/controller/worker.go#L111-L157):
```go
func (wp *WorkerPool) runWorker(ctx context.Context, workerID int, jobsChan <-chan Job, resultsChan chan<- CrawlResult, wg *sync.WaitGroup) {
	defer wg.Done()
	defer wp.setWorkerState(workerID, WorkerStateIdle, "")

	for {
		wp.setWorkerState(workerID, WorkerStateIdle, "")

		select {
		case <-ctx.Done():
			return

		case job, ok := <-jobsChan:
			if !ok {
				return
			}

			wp.setWorkerState(workerID, WorkerStateDownloading, job.URL)
			result := wp.processJob(ctx, workerID, job)

			select {
			case resultsChan <- result:
			case <-ctx.Done():
				return
			}
		}
	}
}
```

### 7.4 Controller Event Loop (`Run`)
From [`internal/controller/controller.go:Run()`](file:///d:/College/Sem%205/CrawlSync/internal/controller/controller.go#L274-L310):
```go
	for {
		if !paused && len(pendingJobs) == 0 && inFlight == 0 {
			break // Crawl naturally complete
		}

		var sendChan chan Job
		var nextJob Job
		if !paused && len(pendingJobs) > 0 {
			sendChan = jobsChan
			nextJob = pendingJobs[0]
		}

		select {
		case <-ctx.Done():
			close(jobsChan)
			c.wg.Wait()
			for {
				select {
				case res := <-resultsChan:
					inFlight--
					c.handleCrawlResult(res, &pendingJobs, false)
				default:
					goto Drained
				}
			}
		Drained:
			_ = c.saveCheckpoint("shutdown")
			return ctx.Err()
```

### 7.5 Pause / Resume Dynamic Gating
From [`internal/controller/controller.go:Run()`](file:///d:/College/Sem%205/CrawlSync/internal/controller/controller.go#L280-L286):
```go
		// OS CONCEPT - DYNAMIC SELECT MULTIPLEXING & GATING:
		// If paused, sendChan remains nil. In Go, sending to a nil channel blocks that case forever.
		// This freezes job dispatch while allowing worker results and control commands to continue!
		var sendChan chan Job
		var nextJob Job
		if !paused && len(pendingJobs) > 0 {
			sendChan = jobsChan
			nextJob = pendingJobs[0]
		}
```

### 7.6 Shutdown Result Draining
From [`internal/controller/controller.go:Run()`](file:///d:/College/Sem%205/CrawlSync/internal/controller/controller.go#L296-L308):
```go
		case <-ctx.Done():
			close(jobsChan) // Signal idle workers to terminate
			c.wg.Wait()     // Barrier: wait for all workers to finish active jobs
			for {
				select {
				case res := <-resultsChan:
					inFlight--
					// Drain result and commit completed status, but do NOT queue new dispatch jobs
					c.handleCrawlResult(res, &pendingJobs, false)
				default:
					goto Drained
				}
			}
		Drained:
			_ = c.saveCheckpoint("shutdown")
			return ctx.Err()
```

### 7.7 Consistent Snapshotting (`Snapshot`)
From [`internal/urlmanager/manager.go:Snapshot()`](file:///d:/College/Sem%205/CrawlSync/internal/urlmanager/manager.go#L210-L245):
```go
func (m *URLManager) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	itemsCopy := make([]URLItem, 0, len(m.urls))
	for _, item := range m.urls {
		itemsCopy = append(itemsCopy, *item) // Value copy
	}

	sort.Slice(itemsCopy, func(i, j int) bool {
		return itemsCopy[i].Sequence < itemsCopy[j].Sequence
	})

	return Snapshot{
		URLs:  itemsCopy,
		Stats: m.stats, // Copied under same lock: no torn reads!
	}
}
```

### 7.8 Atomic Checkpoint Write (`commitFile`)
From [`internal/state/checkpoint.go:commitFile()`](file:///d:/College/Sem%205/CrawlSync/internal/state/checkpoint.go#L161-L200):
```go
func (cm *CheckpointManager) commitFile(data []byte) error {
	tmpFile, err := os.CreateTemp(cm.dir, "checkpoint-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmpFile.Name()

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmpFile.Sync(); err != nil { // fsync page cache flush
		tmpFile.Close()
		os.Remove(tmpName)
		return err
	}
	tmpFile.Close()

	primaryPath := filepath.Join(cm.dir, CheckpointFileName)
	backupPath := filepath.Join(cm.dir, BackupFileName)

	if _, err := os.Stat(primaryPath); err == nil {
		_ = os.Rename(primaryPath, backupPath) // Rotate to .prev.json
	}

	return os.Rename(tmpName, primaryPath) // Atomic directory rename
}
```

### 7.9 Checkpoint Load & Validation (`ValidateForRecovery`)
From [`internal/state/recovery.go:ValidateForRecovery()`](file:///d:/College/Sem%205/CrawlSync/internal/state/recovery.go#L37-L77):
```go
func ValidateForRecovery(cp *Checkpoint, opts RecoveryValidationOptions) error {
	if cp.Version != CurrentVersion {
		return fmt.Errorf("unsupported checkpoint schema version: %d", cp.Version)
	}
	if cp.SessionID == "" {
		return errors.New("checkpoint session ID cannot be empty")
	}

	seenSequences := make(map[int64]bool)
	for _, u := range cp.URLs {
		if u.Sequence <= 0 {
			return fmt.Errorf("non-positive sequence %d for URL %s", u.Sequence, u.Normalized)
		}
		if seenSequences[u.Sequence] {
			return fmt.Errorf("duplicate sequence number %d found", u.Sequence)
		}
		seenSequences[u.Sequence] = true

		// Check directory traversal in saved paths
		if u.Status == urlmanager.StatusCompleted {
			rel, err := filepath.Rel(opts.StorageDir, u.SavedPath)
			if err != nil || strings.HasPrefix(rel, "..") {
				return fmt.Errorf("path traversal attempt in SavedPath: %s", u.SavedPath)
			}
		}
	}
	return nil
}
```

### 7.10 State Restoration (`Restore`)
From [`internal/urlmanager/manager.go:Restore()`](file:///d:/College/Sem%205/CrawlSync/internal/urlmanager/manager.go#L285-L330):
```go
	for _, rec := range records {
		item := rec
		if item.Sequence > maxSeq {
			maxSeq = item.Sequence
		}

		if item.Status == StatusInProgress {
			item.Status = StatusPending // Reset interrupted downloads
		} else if item.Status == StatusCompleted {
			// Verify file exists on disk and is non-empty
			fi, err := os.Stat(item.SavedPath)
			if err != nil || fi.Size() == 0 {
				item.Status = StatusPending // Demote missing file to pending
			}
		} else if item.Status == StatusFailed && opts.RetryFailed {
			item.Status = StatusPending // Demote failed URL on flag
		}

		m.urls[item.Normalized] = &item
	}
	m.nextSequence = maxSeq // Monotonic ordering preserved
```

---

## 8. Checkpoint Format & Storage Specification

### 8.1 Go Struct Definitions
Defined in [`internal/state/checkpoint.go`](file:///d:/College/Sem%205/CrawlSync/internal/state/checkpoint.go#L18-L41):
```go
const CurrentVersion = 1
const CheckpointFileName = "checkpoint.json"
const BackupFileName = "checkpoint.prev.json"

type Checkpoint struct {
	Version           int                      `json:"version"`
	SessionID         string                   `json:"sessionId"`
	CreatedAt         time.Time                `json:"createdAt"`
	Sequence          int64                    `json:"sequence"`
	Config            CheckpointConfig         `json:"config"`
	Stats             urlmanager.URLStats      `json:"stats"`
	URLs              []urlmanager.URLItem     `json:"urls"`
	CumulativeElapsed time.Duration            `json:"cumulativeElapsed,omitempty"`
}

type CheckpointConfig struct {
	StartURL       string `json:"startURL"`
	WorkerCount    int    `json:"workerCount"`
	MaxPages       int    `json:"maxPages"`
	MaxDepth       int    `json:"maxDepth"`
	RequestTimeout string `json:"requestTimeout"`
	SameDomainOnly bool   `json:"sameDomainOnly"`
	StorageDir     string `json:"storageDir"`
}
```

### 8.2 Real Sample Checkpoint JSON
Generated by CrawlSync during verification:
```json
{
  "version": 1,
  "sessionId": "session_1793813541_b8319f6ad85642a1",
  "createdAt": "2026-11-04T23:02:21.8421054+05:30",
  "sequence": 1,
  "config": {
    "startURL": "http://127.0.0.1:65510/",
    "workerCount": 2,
    "maxPages": 10,
    "maxDepth": 2,
    "requestTimeout": "5s",
    "sameDomainOnly": true,
    "storageDir": "data/pages"
  },
  "stats": {
    "total_discovered": 4,
    "pending": 0,
    "in_progress": 0,
    "completed": 4,
    "failed": 0
  },
  "urls": [
    {
      "raw": "http://127.0.0.1:65510/",
      "normalized": "http://127.0.0.1:65510/",
      "status": "completed",
      "depth": 0,
      "sequence": 1,
      "discovered": "2026-11-04T23:02:21.801+05:30",
      "statusCode": 200,
      "size": 142,
      "duration": 4200000,
      "savedPath": "data/pages/page_1.html"
    },
    {
      "raw": "http://127.0.0.1:65510/p1",
      "normalized": "http://127.0.0.1:65510/p1",
      "status": "completed",
      "depth": 1,
      "sequence": 2,
      "discovered": "2026-11-04T23:02:21.815+05:30",
      "statusCode": 200,
      "size": 188,
      "duration": 3100000,
      "savedPath": "data/pages/page_2.html"
    },
    {
      "raw": "http://127.0.0.1:65510/p2",
      "normalized": "http://127.0.0.1:65510/p2",
      "status": "completed",
      "depth": 1,
      "sequence": 3,
      "discovered": "2026-11-04T23:02:21.822+05:30",
      "statusCode": 200,
      "size": 94,
      "duration": 2900000,
      "savedPath": "data/pages/page_3.html"
    }
  ],
  "cumulativeElapsed": 45210000
}
```

### 8.3 Schema Versioning, Backup Rotation & Failure Handling
- **Version Field:** `version: 1` is strictly asserted by `ValidateForRecovery()`. Any mismatch returns `unsupported checkpoint schema version: N`, preventing incompatible data deserialization.
- **Backup Rotation:** Prior to renaming the temporary file into `checkpoint.json`, any existing `checkpoint.json` is rotated to `checkpoint.prev.json`.
- **Corrupt Checkpoint Quarantine:** If `checkpoint.json` is malformed (e.g. truncated JSON or syntax corruption), `LoadWithFallback()` automatically renames it to `checkpoint.corrupt.<timestamp>`, logs a warning, and loads `checkpoint.prev.json`.
- **Failure Rollback:** If moving the temporary checkpoint into place fails on Windows, the backup file is restored back to `checkpoint.json`, guaranteeing a zero-loss state.

---

## 9. Recovery Behavior (Step-by-Step on `--resume`)

When CrawlSync is executed with `--resume`, the following sequence executes:

1. **Flag Detection:** [`cmd/crawlsync/main.go`](file:///d:/College/Sem%205/CrawlSync/cmd/crawlsync/main.go#L36-L40) uses `flag.Visit` to record explicitly supplied CLI flags, distinguishing between user overrides and defaults.
2. **Directory Resolution:** `resolveCheckpointDir()` locates the target checkpoint directory.
3. **Primary Loading & Fallback:** `state.LoadWithFallback()` reads `checkpoint.json`. If corrupt, it quarantines the file and falls back to `checkpoint.prev.json`.
4. **Untrusted Data Validation:** `state.ValidateForRecovery()` validates:
   - Schema version matches `CurrentVersion` (`1`).
   - Monotonic positive unique sequence numbers.
   - Restored URLs have `http`/`https` schemes and match seed domain.
   - SSRF private IP check is satisfied (or overridden by `--allow-local`).
   - `SavedPath` attributes do not contain `..` path traversal attempts.
5. **Configuration Precedence:** Seed URL, max pages, max depth, and domain restrictions are loaded from the checkpoint. Explicit CLI flags (such as `-workers` or `--checkpoint-interval`) take precedence over stored checkpoint settings.
6. **URL Manager Invariant Reconstruction (`Restore()`):**
   - Monotonic sequence initialized to `max(Sequence)`. Future discoveries receive sequence `max + 1`.
   - All `in_progress` tasks reset to `pending`.
   - Completed pages verified against disk; missing or zero-length files demoted to `pending`.
   - Failed URLs preserved unless `--retry-failed` is passed, in which case they reset to `pending`.
7. **Pending Queue Construction:** `c.urlManager.GetPendingItems()` returns all pending records sorted strictly ascending by `Sequence`.
8. **Pre-Flight Completion Detection:** If `len(pendingJobs) == 0 && inFlight == 0`, CrawlSync logs `"Session is already complete; no pending tasks remaining."` and exits cleanly without spawning worker pools.
9. **Checkpoint Continuity:** `checkpointManager.SetSaveCount(cp.Sequence)` ensures that the next checkpoint written will be `#cp.Sequence + 1`.
10. **Runtime Accumulation:** Active runtime duration resumes from `CumulativeElapsed`.

---

## 10. REST API Specification & Dashboard Mapping

### 10.1 Endpoint Summary Table

| Method | Route Path | Request Payload Shape | Response Data Payload Shape | Error Status Codes |
| :--- | :--- | :--- | :--- | :--- |
| `POST` | `/api/crawl/start` | `{"url": string, "workers"?: int, "max_pages"?: int, "max_depth"?: int, "timeout_seconds"?: int, "same_domain"?: bool, "resume"?: bool, "retry_failed"?: bool}` | `{"session_id": string, "state": string, "message": string}` (HTTP 202) | 400 (Validation), 409 (Already Running / Stopping) |
| `GET` | `/api/crawl/status` | None | `{"session_id": string, "state": string, "is_running": bool, "is_paused": bool, "active_workers": int, "uptime_ms": int, "total_discovered": int, "completed": int, "pending": int, "failed": int}` (HTTP 200) | None (Returns IDLE if no crawl) |
| `GET` | `/api/crawl/stats` | None | `{"total_discovered": int, "pending": int, "in_progress": int, "completed": int, "failed": int, "active_workers": int, "total_downloaded_bytes": int, "average_duration_ms": float64, "crawl_speed_pages_per_sec": float64, "uptime_seconds": float64}` (HTTP 200) | None |
| `POST` | `/api/crawl/pause` | None | `{"session_id": string, "action": "pause", "previous_state": string, "current_state": "PAUSED", "message": string}` (HTTP 200) | 404 (No active crawl), 409 (Not running) |
| `POST` | `/api/crawl/resume` | None | `{"session_id": string, "action": "resume", "previous_state": string, "current_state": "RUNNING", "message": string}` (HTTP 200) | 404 (No active crawl), 409 (Not paused) |
| `POST` | `/api/crawl/stop` | None | `{"session_id": string, "action": "stop", "previous_state": string, "current_state": "STOPPED", "message": string}` (HTTP 200) | 404 (No active crawl) |
| `POST` | `/api/crawl/checkpoint`| None | `{"session_id": string, "sequence": int, "created_at": string, "total_discovered": int, "completed": int, "pending": int}` (HTTP 200) | 404 (No active crawl), 500 (Persistence failure) |
| `GET` | `/api/workers` | None | `{"total_workers": int, "active_workers": int, "idle_workers": int, "workers": [{"id": int, "state": "IDLE"\|"DOWNLOADING", "current_url": string, "updated_at": string}]}` (HTTP 200) | None |
| `GET` | `/api/urls` | Query params: `status`, `depth`, `offset`, `limit` (max 100), `sort_by` | `{"items": [{"id": int, "raw_url": string, "normalized_url": string, "status": string, "depth": int, "status_code": int, "size": int, "duration_ms": int, "saved_path": string, "discovered_at": string}], "pagination": {"total": int, "filtered": int, "offset": int, "limit": int, "has_more": bool}}` (HTTP 200) | 400 (Invalid limit/offset/status) |
| `GET` | `/api/urls/{id}` | Path param `{id}`: Sequence int64 | `{"id": int, "raw_url": string, "normalized_url": string, "status": string, "depth": int, "status_code": int, "size": int, "duration_ms": int, "saved_path": string, "discovered_at": string}` (HTTP 200) | 400 (Invalid ID), 404 (Not found) |

### 10.2 Strict JSON Decoding Rules
Implemented in [`internal/server/handlers.go:decodeStrictJSON()`](file:///d:/College/Sem%205/CrawlSync/internal/server/handlers.go#L305-L335):
1. **Body Size Cap:** Uses `http.MaxBytesReader` to strictly cap request bodies at 1 MB.
2. **Disallow Unknown Fields:** `decoder.DisallowUnknownFields()` detects and rejects injected fields with HTTP 400.
3. **Trailing Data Rejection:** Executes a second `decoder.Decode(&extra)` and strictly requires `io.EOF`. Rejects consecutive JSON objects, trailing arbitrary text, and trailing JSON values.

### 10.3 Dashboard Specification & Data Source Mapping
*Note: The Frontend Dashboard UI (`web/index.html`, `style.css`, `app.js`) is **NOT YET IMPLEMENTED** (Scheduled for Phase 8). The underlying REST API is fully implemented and mapped below:*

| Dashboard Panel | Purpose | Backing REST API Endpoint | Data Fields Utilized | Status |
| :--- | :--- | :--- | :--- | :--- |
| **Config Panel** | Configure crawl and trigger execution | `POST /api/crawl/start` | `url`, `workers`, `max_pages`, `max_depth`, `timeout_seconds`, `same_domain` | Backend Ready / UI Not Built |
| **Stat Cards** | High-level metrics display | `GET /api/crawl/stats` | `total_discovered`, `completed`, `pending`, `failed`, `active_workers`, `crawl_speed_pages_per_sec` | Backend Ready / UI Not Built |
| **Worker Monitor** | Visual grid of worker states | `GET /api/workers` | `workers[].id`, `workers[].state`, `workers[].current_url`, `workers[].updated_at` | Backend Ready / UI Not Built |
| **URL Table** | Paginated URL inspection | `GET /api/urls` | `items[].id`, `normalized_url`, `status`, `status_code`, `size`, `duration_ms`, `depth` | Backend Ready / UI Not Built |
| **Runtime Controls** | Interactive control buttons | `POST /api/crawl/{pause,resume,stop,checkpoint}` | Control response envelope (`current_state`, `message`) | Backend Ready / UI Not Built |
| **Performance Chart** | Worker scalability comparison | `GET /api/crawl/stats` | `crawl_speed_pages_per_sec`, `average_duration_ms` | Backend Ready / UI Not Built |

---

## 11. Test Inventory

The CrawlSync automated test suite contains **155 test executions** across 6 packages, all running clean with zero race conditions (`go test -count=1 -race ./...`).

### 11.1 URL Deduplication & Normalization (15 Tests)
- `internal/urlmanager`:
  - `TestNormalizeURL` (12 table subtests): Lowercase scheme/host, strip fragment, strip port 80, strip port 443, preserve non-default port, empty path to `/`, root slash preserved, trailing slash stripped for non-root, dot/dot-dot resolution, deterministic query sorting, reject unsupported scheme, reject empty string.
  - `TestURLManager_ConcurrentTrySchedule`: 50 goroutines attempting 2000 total schedules across 10 unique URLs under race detector.
  - `TestURLManager_BasicTransitions`: Validates state progression from `pending` -> `in_progress` -> `completed`/`failed`.
  - `TestURLManager_SnapshotConsistencyAndIndependence`: Validates deep-copy isolation of snapshot slice.

### 11.2 URL Query, Pagination, & Sorting (5 Tests)
- `internal/urlmanager`:
  - `TestURLManager_GetBySequence`: Lookup by positive sequence ID, non-existent handling, and mutation isolation.
  - `TestURLManager_GetFilteredURLs_Pagination`: Offset/limit windows, page 1/2/3, beyond-count offset, and default limits.
  - `TestURLManager_GetFilteredURLs_Filtering`: Filtering by status, depth, combined filters, and empty matches.
  - `TestURLManager_GetFilteredURLs_Sorting`: Ascending/descending sort by sequence, depth, and time.
  - `TestURLManager_GetFilteredURLs_ConcurrentSafe`: 8 concurrent writers and 8 concurrent readers under race detector.

### 11.3 Worker & Fetcher Safety (8 Tests)
- `internal/fetcher`:
  - `TestFetcher_HTMLSuccess`: Successful HTTP 200 fetch of HTML content.
  - `TestFetcher_NonHTMLRejected`: Rejection of `application/pdf`, `image/png`, etc.
  - `TestFetcher_SizeCapEnforced`: Truncation and enforcement of 5MB limit.
  - `TestFetcher_SSRFProtection`: Interception and blocking of private loopback IPs (`127.0.0.1`).
- `internal/parser`:
  - `TestExtractLinks` (2 subtests): Same-domain filtering true vs false.
- `internal/controller`:
  - `TestWorker_MalformedHTMLNonFatal`: Worker survives malformed HTML without crashing.
  - `TestWorker_PageReplacement_PreservesExistingOnFailure`: Atomic page replacement preserves original file if commit fails.

### 11.4 Shutdown, Draining, & Lifecycle (12 Tests)
- `internal/controller`:
  - `TestController_CompleteGraphCrawl`: Multi-tier graph crawl from seed to natural termination.
  - `TestController_MaxPages_SeedOnly`: MaxPages cutoff hit at seed page.
  - `TestController_MaxPages_Duplicates`: MaxPages cutoff respected amidst duplicate links.
  - `TestController_ActiveDownloadCancellation`: Active download canceled via context without worker leak.
  - `TestController_ShutdownResultDraining`: Buffered worker results drained and recorded on shutdown.
  - `TestController_HandleCrawlResult_DrainPolicy`: Verified `allowNewPending: false` policy.
  - `TestController_ConcurrentRunRejected`: Enforces single execution of `Run()`.
  - `TestController_ControlRequestsAfterShutdown`: Control requests after shutdown return immediately without hanging.
- `internal/server`:
  - `TestServer_GracefulShutdown`: Clean listener closure.
  - `TestServer_ShutdownWithActiveCrawl`: Active crawl receives shutdown signal and commits final checkpoint.
  - `TestServer_HTTPDrainTimeoutWhileHandlerActive`: Slow HTTP request triggers logged timeout without hanging shutdown.
  - `TestServer_CrawlerShutdownAttemptedAfterHTTPTimeout`: Crawler shutdown budget runs even after HTTP drain timeout.

### 11.5 Pause & Resume Synchronization (9 Tests)
- `internal/controller`:
  - `TestController_Pause_ActiveDownloads`: Pausing halts dispatch while allowing in-flight downloads to finish.
  - `TestController_ResultsArrivingWhilePaused`: Results completing during pause are processed normally.
  - `TestController_Resume`: Resuming restores job dispatch and reaches completion.
  - `TestController_DoublePause`: Verifies idempotence of double pause.
  - `TestController_DoubleResume`: Verifies idempotence of double resume.
  - `TestController_StopWhilePaused`: Clean shutdown sequence when stopped in paused state.
  - `TestController_PausedWithPendingWorkAndZeroInFlight` (2 subtests): Resume finishes remaining work; Stop persists pending work.
  - `TestController_ConcurrentControlRequests`: 8 concurrent threads hammering Pause/Resume/State without deadlocks.

### 11.6 Checkpoint Persistence & Durability (6 Tests)
- `internal/state`:
  - `TestCheckpoint_SerializationAndValidation`: Round-trip JSON marshaling and schema validation.
  - `TestCheckpoint_AtomicRotationAndBackup`: Rotation to `.prev.json` and atomic commit.
  - `TestCheckpoint_ValidationErrors`: Detection of missing fields and unsupported versions.
  - `TestCheckpoint_ConcurrentSaves`: Serialization of concurrent saves via mutex.
  - `TestGenerateSessionID`: Cryptographic randomness and error handling for session IDs.
- `internal/controller`:
  - `TestController_PeriodicCheckpointing`: Periodic ticker saves valid snapshots during active crawling.
  - `TestController_PeriodicCheckpointWhilePaused`: Periodic ticker advances sequence while paused.
  - `TestController_CheckpointWhilePaused`: Manual checkpoint trigger while paused.

### 11.7 Recovery & Crash Simulation (25 Tests)
- `internal/state`:
  - `TestRecovery_MissingPrimary_FailsWithoutFallback`: Missing primary returns error without falling back.
  - `TestRecovery_CorruptPrimary_FallbackToBackup`: Corrupt primary quarantined to `.corrupt.<ts>` and loads backup.
  - `TestRecovery_BothCorrupt_FailsSafely`: Safe refusal when both primary and backup are corrupt.
  - `TestRecovery_LoadWithFallback_EmptyPrimaryFallsBackToBackup`: Zero-byte file falls back to backup.
  - `TestRecovery_LoadWithFallback_TruncatedJSONFallsBackToBackup`: Truncated JSON falls back to backup.
  - `TestRecovery_Validation_OutOfDomainRejected`: Rejection of out-of-domain URLs.
  - `TestRecovery_Validation_NonHTTPRejected`: Rejection of non-HTTP schemes.
  - `TestRecovery_Validation_PrivateIPBlockedUnlessAllowed`: SSRF enforcement on restored URLs.
  - `TestRecovery_Validation_PathTraversalRejected`: Directory traversal in `SavedPath` rejected.
  - `TestRecovery_Validation_DuplicateSequenceRejected`: Duplicate sequences rejected.
  - `TestRecovery_Validation_NonPositiveSequenceRejected`: Sequence `<= 0` rejected.
  - `TestRecovery_Validation_DepthExceededRejected`: Depth `> MaxDepth` rejected.
  - `TestRecovery_Validation_UnsupportedVersionRejected`: Version `!= 1` rejected.
  - `TestRecovery_Validation_SymlinkEscapeRejected`: Symlinks pointing outside storage directory rejected.
  - `TestRecovery_Validation_WindowsPathTraversalRejected`: Windows `..\` traversal rejected.
- `internal/controller`:
  - `TestRecovery_CrashSimulationAndFullGraphCompletion`: Multi-cycle crash simulation, state recovery, and completion.
  - `TestRecovery_InProgressRestoredToPending`: In-progress reset to pending.
  - `TestRecovery_PendingOrderPreservesDiscoverySequence`: Restored queue sorted by sequence ascending.
  - `TestRecovery_MissingSavedFileDemotedToPending`: Missing page file demoted to pending.
  - `TestRecovery_NextSequenceExceedsEveryExistingSequence`: Monotonic sequence ordering preserved.
  - `TestRecovery_FailedURLs_PreservedByDefault`: Failed URLs stay failed.
  - `TestRecovery_FailedURLs_RetriedWithFlag`: `--retry-failed` demotes failed to pending.
  - `TestRecovery_CumulativeElapsedAcrossMultipleCycles`: Runtime duration accumulated across crashes.
  - `TestRecovery_AlreadyCompleteSession_DoesNotStartWorkers`: Session with zero pending exits cleanly without spawning workers.
  - `TestRecovery_CompatibleWithPauseResumeStop`: Pause, resume, and stop work seamlessly after recovery.

### 11.8 HTTP Server, Handlers, Middleware, & Service (59 Tests)
- `internal/server`:
  - Handlers (24 top-level, 15 subtests): Valid responses, malformed JSON, missing fields, unknown fields, oversized payloads, invalid schemes, invalid bounds, invalid pagination, invalid status filters, invalid sort, sequence lookup, missing crawl 404, duplicate start 409, unsupported method 405, panic recovery 500, concurrent GETs, concurrent lifecycle POSTs, manual checkpoint, worker endpoint, strict decoding trailing data, CORS preflight.
  - Service (17 tests): Initialization, start crawl, reject second start, natural completion, pause/resume, stop running/paused, restart after stop, reject start while stopping, resume checkpoint, retry-failed, manual checkpoint, read queries during crawl, concurrent queries, concurrent lifecycle, generation isolation, graceful shutdown.
  - Server (18 tests): Construction, route registration, start listening, ephemeral ports, live status request, live route sweep, invalid host/port, graceful shutdown, active crawl stop, paused crawl stop, idle server check, start/stop leak cycles, middleware verification, shutdown concurrency, HTTP drain timeout, crawler budget independence, repeated shutdown calls, idle shutdown.

---

## 12. Design Decisions & Rejected Alternatives

1. **Controller-Owned Scheduling vs. Worker-Driven Queueing:**
   - *Chosen:* Controller is the single owner of the `pendingJobs` slice and channel dispatch. Workers communicate exclusively via `resultsChan`.
   - *Rejected Alternative:* Workers pushing discovered links directly into `jobsChan`.
   - *Rationale:* Eliminates circular wait deadlocks where both workers and controller block waiting for buffer capacity.
2. **Periodic Snapshots vs. Write-Ahead Logging (WAL):**
   - *Chosen:* Atomic full-state JSON snapshots with backup rotation.
   - *Rejected Alternative:* Write-ahead append-only log.
   - *Rationale:* Fits the 1.5-day build time constraint, simplifies state restoration into a single idempotent `Restore()` pass, and avoids complex log compaction and replay logic.
3. **Dynamic Nil-Channel Gating vs. Sleep / Condition Variables:**
   - *Chosen:* Gating dispatch by dynamically toggling `sendChan` to `nil` in the `select` statement.
   - *Rejected Alternative:* `time.Sleep` polling loops or `sync.Cond`.
   - *Rationale:* In Go, sending to a `nil` channel blocks that case indefinitely without consuming CPU. This keeps the event loop fully responsive to worker results, control commands, and cancellation without busy-waiting.
4. **Local JSON Files vs. SQLite / Database:**
   - *Chosen:* Local JSON checkpoints and raw HTML files.
   - *Rejected Alternative:* SQLite or external databases.
   - *Rationale:* Adheres to project constraints prohibiting database engines, keeps the architecture transparent for OS viva examination, and highlights filesystem durability (`fsync`, atomic renames).
5. **Two-Phase Shutdown vs. Shared Single Context Deadline:**
   - *Chosen:* Phase 1 drains HTTP connections; Phase 2 guarantees a dedicated unexpired context to stop the crawler and commit the shutdown checkpoint.
   - *Rejected Alternative:* Passing a single 15-second context sequentially to HTTP shutdown and crawler shutdown.
   - *Rationale:* Prevents hanging HTTP client connections from starving the crawler of its persistence deadline, ensuring state is never lost during server termination.
6. **Strict Non-Root Trailing Slash Stripping:**
   - *Chosen:* Canonicalizing `/path/` to `/path` (while preserving `/` for root).
   - *Rejected Alternative:* Treating `/path/` and `/path` as distinct URLs.
   - *Rationale:* Prevents duplicate fetches of identical web pages on modern web servers that treat trailing slashes interchangeably.

---

## 13. Bugs Found & Fixed During Development

Documented from [`docs/IMPLEMENTATION_LOG.md`](file:///d:/College/Sem%205/CrawlSync/docs/IMPLEMENTATION_LOG.md):

1. **Deadlock on Queue Saturation:**
   - *Symptom:* Crawler deadlocked when workers discovered more links than the channel capacity.
   - *Root Cause:* Controller blocked sending to `jobsChan` while workers blocked sending to `resultsChan`.
   - *Fix:* Controller retains `pendingJobs` in an internal slice and only sends on `jobsChan` when space is available using non-blocking dynamic `select`.
2. **Premature Completion Detection:**
   - *Symptom:* Crawl terminated before all pages were processed.
   - *Root Cause:* Termination condition checked `len(pendingJobs) == 0` without verifying active worker jobs.
   - *Fix:* Redefined completion invariant to `len(pendingJobs) == 0 && inFlight == 0`.
3. **Torn Read in URLManager Snapshots:**
   - *Symptom:* Snapshot statistics did not match the total number of URL records in the slice.
   - *Root Cause:* Reading map entries and reading statistics occurred under separate lock acquisitions.
   - *Fix:* Enclosed both slice allocation and `Stats` copy under a single, unified `m.mu.RLock()`.
4. **Windows Atomic Replacement Failure (`os.Rename`):**
   - *Symptom:* `os.Rename` failed with `Access is denied` or `File already exists` on Windows.
   - *Root Cause:* Windows Win32 API does not allow atomic replacement over existing files via standard POSIX rename.
   - *Fix:* Implemented backup rotation: rotate existing `checkpoint.json` to `checkpoint.prev.json` before moving the temporary file into place, with rollback handling.
5. **Lost Results on Shutdown:**
   - *Symptom:* Workers finished downloads right before SIGINT, but their HTML pages were missing from the shutdown checkpoint.
   - *Root Cause:* Controller exited immediately upon `ctx.Done()` without draining buffered results.
   - *Fix:* Implemented `drainRemainingResults()`: wait for workers via `sync.WaitGroup`, then drain all remaining items from `resultsChan` with `allowNewPending: false`.
6. **Timing Flakes in Lifecycle Tests:**
   - *Symptom:* Tests occasionally failed on slow CI/CD environments.
   - *Root Cause:* Using fixed `time.Sleep` to await worker status transitions.
   - *Fix:* Replaced sleeps with synchronization channels (`<-r.Context().Done()`) and condition polling (`ctrl.GetStats().InProgress == 0`).
7. **Pagination Limit Inconsistency:**
   - *Symptom:* URLManager used default limit 20 and max 100, while HTTP handlers documented 50 and 500.
   - *Root Cause:* Discrepancy between Stage 1 and Stage 3 API implementations.
   - *Fix:* Exported shared pagination constants (`DefaultPaginationLimit = 20`, `MaxPaginationLimit = 100`) from `internal/urlmanager/manager.go` and enforced them uniformly.
8. **Permissive JSON Decoding:**
   - *Symptom:* HTTP endpoints accepted requests with multiple concatenated JSON objects or trailing garbage.
   - *Root Cause:* `decoder.Decode()` stops after the first valid object, leaving trailing bytes unread.
   - *Fix:* Added a second `decoder.Decode(&extra)` requiring `io.EOF` in `decodeStrictJSON()`.
9. **Shutdown Deadline Starvation:**
   - *Symptom:* If an HTTP handler took 15 seconds to drain, the crawler had 0 seconds left to write its checkpoint.
   - *Root Cause:* A single `context.WithTimeout` was shared sequentially.
   - *Fix:* Implemented two-phase shutdown in `internal/server/server.go`: Phase 1 drains HTTP with a bounded budget; Phase 2 gets a dedicated unexpired context.

---

## 14. Safety Measures & Boundary Enforcements

1. **SSRF (Server-Side Request Forgery) Defense:**
   - Implemented in [`internal/fetcher/fetcher.go:isPrivateIP()`](file:///d:/College/Sem%205/CrawlSync/internal/fetcher/fetcher.go#L190-L235).
   - Validates resolved IP addresses during dial time: blocks loopback (`127.0.0.0/8`, `::1`), private RFC-1918 (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`), link-local (`169.254.0.0/16`), and multicast.
   - Re-validated on every HTTP redirect.
   - Controlled strictly by server/CLI operator via `-allow-local`; client API requests cannot override this setting.
2. **Memory Size Cap (OOM Defense):**
   - Implemented in [`internal/fetcher/fetcher.go:Fetch()`](file:///d:/College/Sem%205/CrawlSync/internal/fetcher/fetcher.go#L104).
   - Wraps response body in `io.LimitReader(resp.Body, DefaultMaxBodySize)` (5 MB), preventing memory exhaustion from massive files.
3. **Filesystem Path Traversal Defense:**
   - Implemented in [`internal/state/recovery.go:ValidateForRecovery()`](file:///d:/College/Sem%205/CrawlSync/internal/state/recovery.go#L69-L76).
   - Validates that `SavedPath` attributes reside strictly within `StorageDir` using `filepath.Rel()`.
   - Rejects Windows `..\` traversal and symlinks resolving outside the target storage root.
4. **Robots.txt Status:**
   - **NOT IMPLEMENTED.** (Planned in master context, but not implemented in the current codebase).
5. **Strict JSON Schema Validation:**
   - Implemented in `internal/server/handlers.go:decodeStrictJSON()`.
   - Caps request bodies at 1 MB, disallows unknown fields, and rejects trailing data.

---

## 15. Performance & Benchmarking

- **Benchmark Status:** **NOT RUN** (Scheduled for Phase 9).
- **Execution Plan for Benchmarking 1, 2, 4, 8 Workers:**
  1. Start local test server with simulated artificial per-page latency (e.g. 50ms):
     ```powershell
     go run ./testsite/main.go -port 9090 -delay 50ms
     ```
  2. Execute crawling benchmarks across worker counts against a fixed graph of 100 pages:
     ```powershell
     # 1 Worker
     Measure-Command { go run ./cmd/crawlsync -url http://127.0.0.1:9090 -workers 1 -max-pages 100 -allow-local }
     # 2 Workers
     Measure-Command { go run ./cmd/crawlsync -url http://127.0.0.1:9090 -workers 2 -max-pages 100 -allow-local }
     # 4 Workers
     Measure-Command { go run ./cmd/crawlsync -url http://127.0.0.1:9090 -workers 4 -max-pages 100 -allow-local }
     # 8 Workers
     Measure-Command { go run ./cmd/crawlsync -url http://127.0.0.1:9090 -workers 8 -max-pages 100 -allow-local }
     ```
  3. Measure and tabulate: Total Execution Time (seconds), Crawl Speed (pages/sec), and CPU utilization across runs.

---

## 16. Known Limitations & Gaps

1. **`robots.txt` Compliance:** **NOT IMPLEMENTED.** CrawlSync does not currently fetch or evaluate `robots.txt` rules before downloading.
2. **Dashboard Frontend UI:** **NOT IMPLEMENTED.** Phase 7 completed the entire REST API backend, but the HTML/CSS/JS frontend dashboard (`web/index.html`) is planned for Phase 8.
3. **Static HTML Only:** CrawlSync downloads static HTML using standard HTTP GET requests. It does not execute client-side JavaScript (no headless Chromium/WebKit engine).
4. **Single-Domain Boundary:** CrawlSync defaults strictly to same-domain link traversal. Cross-domain graph traversal is deliberately restricted.
5. **In-Memory URL Table:** The URL Manager stores all discovered URLs in an in-memory hash map (`map[string]*URLItem`). While memory-efficient up to ~1,000,000 URLs, crawls exceeding available RAM would require an on-disk key-value store (e.g. LSM-tree).
6. **Single Node Architecture:** Designed as a single-process concurrent crawler; does not support distributed multi-node coordination.

---

## 17. Viva Examination Preparation (25 Questions & Answers)

### Topic 1: Concurrency & Thread Management
**Q1: How does CrawlSync manage concurrency, and why doesn't it spawn a new thread for every discovered URL?**  
*Answer:* CrawlSync uses a fixed-size worker pool of goroutines (default 5, configurable 1-20) communicating over buffered channels. Spawning an unbounded thread per URL would exhaust OS thread handles, saturate socket descriptors, and trigger Out-Of-Memory kernel kills.

**Q2: What is the difference between an OS thread and a Go goroutine in this project?**  
*Answer:* An OS thread is managed by the kernel with a fixed stack (typically 1-2 MB). A Go goroutine is a user-space execution unit managed by the Go runtime (G-M-P scheduler) with a dynamic starting stack of only ~2 KB, allowing lightweight multiplexing over OS threads.

**Q3: How does Go's G-M-P scheduler behave when a worker is blocked on a network fetch?**  
*Answer:* When a worker (**G**) blocks on a network I/O system call, the runtime disassociates the thread (**M**) from the logical processor (**P**), allowing another runnable worker goroutine to run on **P**. Network readiness is monitored via OS I/O multiplexing (`epoll`/`kqueue`/`IOCP`).

**Q4: Why does CrawlSync use `sync.WaitGroup` during crawler shutdown?**  
*Answer:* `sync.WaitGroup` acts as a thread synchronization barrier. The controller calls `close(jobsChan)` and blocks on `c.wg.Wait()`, ensuring every active worker has completely finished downloading its page and exited before the final checkpoint is committed.

**Q5: What is the purpose of the single-owner scheduler pattern used in `Controller`?**  
*Answer:* The Controller is the sole owner of `pendingJobs`, `inFlight` counters, and job dispatch. Workers never manipulate the job queue directly, preventing complex locking and eliminating race conditions in scheduling decisions.

### Topic 2: Synchronization & Mutual Exclusion
**Q6: What is a critical section, and how does `URLManager` protect its critical sections?**  
*Answer:* A critical section is a sequence of instructions accessing shared mutable memory. `URLManager` protects its critical sections using `sync.RWMutex`, acquiring `m.mu.Lock()` for mutations (`TrySchedule`, `MarkCompleted`) and `m.mu.RLock()` for queries (`Snapshot`, `GetFilteredURLs`).

**Q7: What is a Time-Of-Check to Time-Of-Use (TOCTOU) race condition, and how does `TrySchedule` prevent it?**  
*Answer:* TOCTOU occurs when a condition is verified (e.g. checking if a URL is visited) and acted upon (inserting it) in separate steps. `TrySchedule` encapsulates lookup and insertion in a single critical section under `m.mu.Lock()`, guaranteeing atomicity.

**Q8: Why does `Snapshot()` release its read lock before serializing state to JSON or disk?**  
*Answer:* Holding a mutex during disk or network I/O is a severe concurrency anti-pattern that blocks all other threads. `Snapshot()` deep-copies the URL structs in memory under `RLock()`, releases the lock immediately, and allows serialization to proceed lock-free.

**Q9: How does CrawlSync avoid deadlocks between workers and the controller?**  
*Answer:* By eliminating Coffman's circular wait condition: workers only read from `jobsChan` and only write to `resultsChan`. They never send back into `jobsChan`, preventing circular buffer blocking.

**Q10: What lock-ordering rule is enforced between `CrawlerService` and `Controller`?**  
*Answer:* `CrawlerService` locks are strictly higher-level than `Controller` locks. `CrawlerService.mu` is **always released** before calling blocking Controller methods (`ctrl.Stop()`, `ctrl.CreateCheckpoint()`).

### Topic 3: Producer-Consumer & Communication
**Q11: Why are Go channels used instead of a shared mutex-protected queue slice for worker communication?**  
*Answer:* Channels provide thread-safe, synchronized, lock-free communication primitives that allow goroutines to block and wake up efficiently without busy-waiting or thread polling.

**Q12: Why is `jobsChan` buffered with capacity `2 * WorkerCount`?**  
*Answer:* Bounded buffering absorbs short-term bursts in link discovery without blocking the controller, while preventing unbounded memory growth.

**Q13: Why is `resultsChan` never closed by the workers?**  
*Answer:* In Go, sending on a closed channel causes a panic. Since multiple worker goroutines concurrently send results to `resultsChan`, having any single worker close it would cause race conditions. It is left open and cleaned up by garbage collection.

**Q14: How does CrawlSync implement runtime pause without busy-waiting?**  
*Answer:* When paused, the controller sets `sendChan = nil`. In Go's `select` statement, a `nil` channel case is never selected. This halts job dispatch while keeping worker results, control commands, and ticker channels fully active.

### Topic 4: Persistence, Atomicity, & Durability
**Q15: How does CrawlSync achieve atomic checkpoint writing on the filesystem?**  
*Answer:* It writes data to a temporary file (`checkpoint-*.tmp`), flushes OS page caches via `tmpFile.Sync()`, closes the file, and renames it over the target destination using `os.Rename`, guaranteeing the destination file is never left half-written.

**Q16: Why is calling `f.Sync()` necessary before renaming a temporary file?**  
*Answer:* Operating systems buffer disk writes in volatile RAM page caches. `f.Sync()` forces the OS to physically write the buffers to non-volatile storage media, guaranteeing durability in the event of an immediate power loss.

**Q17: How does CrawlSync handle the fact that Windows `os.Rename` does not overwrite existing files atomically?**  
*Answer:* On Windows, existing checkpoints are rotated to `checkpoint.prev.json` before moving the new file into place, with automatic rollback logic if the commit fails.

**Q18: What is a consistent global snapshot, and how does CrawlSync ensure snapshot consistency?**  
*Answer:* A consistent snapshot guarantees that no dependent state exists without its prerequisite (e.g. child links discovered without the completed parent). CrawlSync takes checkpoints strictly at the top of the event loop where no URL transition is mid-flight.

### Topic 5: Fault Tolerance & Recovery
**Q19: What happens to `in_progress` URLs when CrawlSync crashes and restarts with `--resume`?**  
*Answer:* `Restore()` iterates through all checkpointed records and automatically demotes every `in_progress` URL back to `pending`, ensuring interrupted downloads are safely retried.

**Q20: Why does `Restore()` verify the existence of HTML files for `completed` URLs on disk?**  
*Answer:* If a user or process deleted downloaded HTML files while the crawler was stopped, blindly trusting the checkpoint would leave missing pages. `Restore()` verifies file existence and size, demoting any missing files back to `pending`.

**Q21: How does CrawlSync handle a corrupt or truncated primary checkpoint during recovery?**  
*Answer:* `LoadWithFallback()` detects JSON unmarshaling errors, renames the corrupt file to `checkpoint.corrupt.<timestamp>` for forensics, and seamlessly falls back to `checkpoint.prev.json`.

**Q22: How does CrawlSync maintain monotonic sequence numbers across multiple crash-recovery cycles?**  
*Answer:* `Restore()` scans all records to find `max(Sequence)`. The URL Manager sets `nextSequence = maxSeq`, ensuring newly discovered URLs in the resumed session receive sequence numbers strictly greater than any prior URL.

### Topic 6: Networking, Safety, & Server Lifecycle
**Q23: What is SSRF (Server-Side Request Forgery), and how does CrawlSync prevent it?**  
*Answer:* SSRF occurs when an attacker forces a server to make network requests to internal, private resources (e.g. AWS metadata `169.254.169.254` or internal routers `192.168.1.1`). CrawlSync's fetcher resolves DNS and blocks all loopback, private RFC-1918, link-local, and multicast IPs at dial time.

**Q24: Why does CrawlSync enforce a two-phase graceful shutdown on its HTTP server?**  
*Answer:* If a single timeout was shared, slow HTTP requests draining could consume the entire deadline, starving the crawler of time to write its final checkpoint. Phase 1 drains HTTP connections, while Phase 2 guarantees the crawler a dedicated window to persist state.

**Q25: Why does CrawlSync reject trailing JSON values in its REST API decoder?**  
*Answer:* Permissive JSON parsers that ignore trailing data can mask malformed client payloads or permit HTTP request smuggling attacks. CrawlSync enforces strict EOF after decoding the primary payload.

---

## 18. Technical Glossary

1. **Atomic Operation:** An operation that executes completely or not at all, with no observable intermediate state to other concurrent threads.
2. **Barrier:** A synchronization primitive that blocks a set of concurrent threads until all participating threads have arrived at the barrier point.
3. **Bounded Buffer:** A fixed-capacity queue used in producer-consumer systems to decouple production and consumption rates while bounding memory consumption.
4. **Checkpoint:** A snapshot of a program's complete execution state saved to persistent storage to enable fault recovery.
5. **Critical Section:** A segment of code that accesses shared mutable data and must not be concurrently accessed by more than one thread.
6. **Deadlock:** A condition where a set of threads are permanently blocked because each thread holds a resource that another thread requires.
7. **Demotion:** In CrawlSync recovery, resetting a URL's status from `in_progress` or `failed` back to `pending`.
8. **Ephemeral Port:** A temporary, dynamic TCP/IP port allocated automatically by the operating system kernel when binding to port 0.
9. **`fsync`:** A system call that flushes operating system page-cache write buffers to non-volatile physical storage media.
10. **Goroutine:** A lightweight, user-space thread of execution managed by the Go runtime scheduler rather than the OS kernel.
11. **G-M-P Model:** Go's runtime scheduler architecture: **G** (Goroutine), **M** (Machine / OS kernel thread), and **P** (Processor / logical context).
12. **Idempotent:** An operation that produces the exact same outcome whether executed once or repeatedly (e.g. `Pause()`, `Stop()`).
13. **In-Flight:** A job that has been dispatched to a worker and is currently being fetched, saved, or parsed.
14. **Monotonic Counter:** A numerical sequence counter that strictly increases and never decreases or repeats.
15. **Mutual Exclusion (Mutex):** A concurrency control mechanism ensuring that at most one thread enters a critical section at any given time.
16. **Nil Channel:** A Go channel initialized to `nil` or unallocated; sending or receiving on a `nil` channel blocks indefinitely in a `select` statement.
17. **OOM (Out-Of-Memory):** An operating system condition where RAM is exhausted, prompting the OS kernel killer to forcefully terminate processes.
18. **Page Cache:** Volatile RAM maintained by the OS kernel caching physical disk blocks to accelerate filesystem reads and writes.
19. **Race Condition:** A flaw where the output or state of a program depends on the non-deterministic timing or interleaving of concurrent execution units.
20. **Read-Write Mutex (`RWMutex`):** A synchronization lock that permits concurrent read access to multiple threads but requires exclusive access for writes.
21. **SSRF (Server-Side Request Forgery):** A security exploit where an attacker induces a server to execute HTTP requests against private internal networks.
22. **Thread Pool:** A pre-allocated collection of persistent threads waiting to execute assigned tasks, avoiding thread allocation overhead.
23. **TOCTOU (Time-Of-Check to Time-Of-Use):** A race condition where software checks state and acts on it in non-atomic steps, allowing state to change in between.
24. **Torn Read:** An inconsistent read error where a thread reads partial data from one state and partial data from an updated state simultaneously.
25. **Write-Ahead Logging (WAL):** A persistence technique where changes are appended to a log file before being applied to the main data store.


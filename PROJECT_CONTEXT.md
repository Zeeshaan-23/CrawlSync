# CRAWLSYNC: MASTER CONTEXT PROMPT

> Paste this whole file into Antigravity as the first message. Also save it in the repo root (e.g. as `AGENTS.md` or `PROJECT_CONTEXT.md`) so every later prompt can say "follow PROJECT_CONTEXT.md". Later prompts only need to say which phase to do.

---

## 0. HOW TO USE THIS DOCUMENT (instructions to the AI)

This document is the permanent source of truth for the project. Read it fully before every task. For every later prompt:

1. Re-read sections 1 to 8 and the Status Tracker (section 9).
2. Do only the phase or task requested. Do not build ahead.
3. Follow the Phase Protocol (section 7).
4. Update the Status Tracker and `docs/IMPLEMENTATION_LOG.md` at the end of every task.
5. If a request conflicts with this document, stop and say so instead of silently choosing.

---

## 1. PROJECT SUMMARY

**Name:** CrawlSync
**Title:** Multithreaded Web Crawler with Checkpoint and Resume Functionality
**Context:** College Operating Systems end-term project. The crawler is a vehicle for demonstrating OS concepts: concurrency, thread management, producer-consumer, mutual exclusion, synchronization, race conditions, deadlock avoidance, persistence, and fault recovery.
**Official description:** "Develop a web crawler capable of downloading webpages concurrently using multiple threads. Implement thread-pool management, URL tracking, duplicate URL detection, checkpoint creation, and recovery after interruption."
**Deadline:** about 1.5 days of build time. Correctness beats polish.

The student must be able to explain every line in a viva. Write simple, readable, well-commented Go. Do not write clever code.

---

## 2. TECH STACK (fixed, do not change)

- **Language:** Go (use the installed version; run `go version` first)
- **Backend:** standard library only where possible: `net/http`, `net/url`, `sync`, `context`, `encoding/json`, `os`, `time`
- **HTML parsing:** `golang.org/x/net/html` (the only required external dependency)
- **Persistence:** JSON checkpoint files and downloaded pages saved as local `.html` files. No database.
- **Frontend:** plain HTML, CSS, JavaScript. Chart.js only if needed. No React, no frameworks, no build step.
- **Run:** `go run ./cmd/crawlsync`, dashboard at `http://localhost:8080`

**Do NOT introduce:** Docker, microservices, auth, cloud services, databases, ORMs, large frontend frameworks, or extra dependencies without asking first.

---

## 3. ARCHITECTURE

```
Dashboard (JS) <--HTTP/JSON--> HTTP Server
                                  |
                          Crawler Controller   (single owner of scheduling)
                          /       |        \
                 URL Manager   Job channel   State Manager (checkpoint/recovery)
              (mutex-protected)      |
                              Worker 1..N (goroutines)
                                  |
                        Fetcher -> Parser -> results channel -> Controller
```

### Components and responsibilities

- **Crawler Controller:** starts/pauses/resumes/stops sessions, owns the worker pool, receives results from workers, decides what to schedule, triggers checkpoints, detects crawl completion.
- **URL Manager:** tracks every URL with a status: `pending`, `in_progress`, `completed`, `failed`. Provides an atomic `TrySchedule(url, depth)` that normalizes, checks for a duplicate, and inserts in one critical section. Also holds the discovered set and statistics.
- **URL Queue:** the producer-consumer buffer (a buffered Go channel of jobs).
- **Worker Pool:** a fixed number of goroutines (default 5, max 20, configurable). Each worker: waits for a job, fetches, saves HTML to disk, parses links, sends a result back to the controller, becomes idle. Workers never modify the queue or URL Manager scheduling state directly except through the defined methods.
- **HTTP Fetcher:** HTTP/HTTPS, request timeout, records status code, duration, size; rejects non-HTML content types; handles invalid URLs and errors.
- **HTML Parser:** extracts `<a href>` links, resolves relative to absolute, normalizes, validates scheme, applies the same-domain restriction.
- **State Manager:** serializes session state to JSON. **Atomic writes:** write to `checkpoint.json.tmp`, then `os.Rename` onto the real file. Snapshot state under the lock, then write to disk after releasing it.
- **Recovery Manager:** loads the latest valid checkpoint, restores config, discovered/completed/pending/failed sets, moves all `in_progress` URLs back to `pending`, restarts workers, continues. Completed URLs are never re-downloaded.

### Critical correctness rules

1. A URL is marked `completed` **only after** its HTML has been successfully written to disk.
2. Never hold a mutex during network or file I/O.
3. Workers send discovered links back to the controller via a results channel. They do not push into the job channel (this prevents the classic deadlock where workers and controller block on each other).
4. Each channel has exactly one closer, and it is closed exactly once.
5. Use `context.Context` for cancellation, pause, and stop. Shut down with a `sync.WaitGroup`.
6. Crawl-completion condition: `pending == 0 AND in_progress == 0`, checked under the same lock as the state change. An empty queue alone does NOT mean done.
7. Goroutines are lightweight units scheduled by the Go runtime onto OS threads (G-M-P model). Never describe each goroutine as an OS thread in code comments or docs.
8. All code must pass `go vet` and `go test -race ./...`.

---

## 4. FUNCTIONAL REQUIREMENTS

| ID | Requirement |
|---|---|
| FR-01 | Start crawling from a user-provided URL |
| FR-02 | Configurable worker count |
| FR-03 | Download webpage HTML |
| FR-04 | Extract hyperlinks |
| FR-05 | Discovered URLs go into a processing queue |
| FR-06 | Prevent duplicate URL scheduling |
| FR-07 | Every URL has an identifiable status |
| FR-08 | Periodic checkpoint creation |
| FR-09 | Recovery after interruption |
| FR-10 | Dashboard with statistics and worker activity |
| FR-11 | Pause and resume without losing progress |
| FR-12 | Graceful stop |

---

## 5. DASHBOARD REQUIREMENTS (built in later phases)

Dark-themed, clean developer dashboard containing:

- **Config panel:** start URL, max pages, worker count, max depth, request timeout, Start button
- **Stat cards:** URLs discovered, pages downloaded, pending, failed, active workers, average download time, crawl speed (pages/sec)
- **Worker monitor:** per-worker state (IDLE / DOWNLOADING) and current URL
- **URL table:** URL, status, HTTP code, size, duration, depth
- **Controls:** Start, Pause, Resume, Stop, Save Checkpoint, Restore Session
- **Performance analytics:** compare 1, 2, 4, 8 workers (execution time and throughput)

Use polling (`/api/stats`, about every 1 second) instead of WebSockets.

---

## 6. SAFETY AND SCOPE LIMITS

- HTTP and HTTPS only
- Same domain as the start URL by default
- Respect `robots.txt`
- Request timeouts, max pages, max depth, modest request rate
- Do not crawl private or internal addresses (localhost, 10.x, 172.16-31.x, 192.168.x, 169.254.x, link-local, etc.). Validate the resolved IP at dial time, and re-check on every redirect. Exception: a command-line flag or config option `-allow-local` so the local test site can be crawled during testing and benchmarking.
- Do not execute JavaScript, bypass authentication, or download images, video, or other non-HTML files

---

## 7. PHASE PROTOCOL (follow for every task)

For each phase, in this order:

1. **Plan:** state in a few lines what will be built and which files will be created or changed.
2. **Implement:** simple code, comments explaining the OS concept where relevant (e.g. `// CRITICAL SECTION: protects the visited map`).
3. **Test:** write unit tests, run `go vet`, run `go test -race ./...`, and show the output. Do not claim a test passed unless you ran it.
4. **Report:** end with this block:

```
PHASE REPORT
- Files created/changed: ...
- What works (verified by test): ...
- What is not done / known issues: ...
- OS concepts demonstrated: ...
- How to run and test this phase manually: ...
- Viva questions this code could prompt: (3 to 5)
```

5. **Update** the Status Tracker (section 9) and append to `docs/IMPLEMENTATION_LOG.md`.

Rules:
- Do not rewrite working code unless it is buggy. Prefer the smallest reliable fix.
- Do not invent features, files, or functions in documentation. Docs describe what actually exists.
- Use status words precisely: **Planned / In progress / Implemented / Tested / Verified**. "Verified" means tested manually or by a passing test the AI actually ran.
- If something is ambiguous or a decision is risky, ask a short question instead of guessing.

---

## 8. PROJECT STRUCTURE (target)

```
crawlsync/
├── cmd/crawlsync/main.go
├── internal/
│   ├── controller/   controller.go, worker.go
│   ├── urlmanager/   manager.go, normalize.go
│   ├── fetcher/      fetcher.go, robots.go
│   ├── parser/       links.go
│   ├── state/        checkpoint.go, recovery.go
│   └── server/       handlers.go
├── web/              index.html, style.css, app.js
├── testsite/         local test server with artificial per-page latency
├── data/             checkpoints/, pages/
├── docs/             PROJECT_OVERVIEW.md, ARCHITECTURE.md, OS_CONCEPTS.md,
│                     IMPLEMENTATION_LOG.md, TESTING.md
└── go.mod
```

This is a target. Create folders only when a phase needs them.

---

## 9. PHASES AND STATUS TRACKER

| Phase | Scope | Status |
|---|---|---|
| 1 | Project init (`go mod`, structure), fetcher, basic single-page crawl | Verified |
| 2 | Parser, URL normalization, `TrySchedule`, duplicate detection (+ unit tests) | Verified |
| 3 | Worker pool + controller, results channel, termination detection | Verified |
| 4 | Checkpoint Creation and Persistence (atomic JSON, periodic, shutdown) | Verified |
| 5 | Synchronization hardening, pause/resume via context | Verified |
| 6 | Recovery: restore session, `in_progress` to `pending`, resume | Planned |
| 7 | HTTP server + REST API (`/api/start`, `/stats`, `/pause`, `/resume`, `/stop`, `/checkpoint`, `/restore`) | Planned |
| 8 | Dashboard UI + live monitoring + controls | Planned |
| 9 | Local test site, benchmark 1/2/4/8 workers, test docs | Planned |
| 10 | Final docs and viva preparation (`OS_CONCEPTS.md` mapped to real code) | Planned |

Priority if time runs short: correctness, concurrency safety, checkpoint/recovery, dashboard, testing, polish (in that order). Cut visual polish and fancy charts first.

---

## 10. TEMPLATE FOR FUTURE PROMPTS

Every later prompt will look like this:

```
Follow PROJECT_CONTEXT.md.
Task: Phase <N>: <name>.
Extra notes: <anything specific, error output, or code to fix>
```

If the task is debugging, include the exact error output and the relevant file. Find the root cause, explain it, and apply the smallest fix.

---

## 11. YOUR FIRST TASK: PHASES 1 AND 2

Before writing code, run `go version` and confirm the Go version. Then:

**Phase 1:**
- Initialize the module (`go mod init crawlsync`) and create only the directories needed for Phases 1-2.
- Implement the fetcher in `internal/fetcher`: HTTP GET with a configurable timeout, a custom `User-Agent` ("CrawlSync/1.0 (academic project)"), a response size cap (e.g. 5 MB), non-HTML content-type rejection, and a result struct containing URL, status code, body, size, duration, and error.
- Add a minimal `main.go` that fetches one URL and prints the result.

**Phase 2:**
- Implement the link parser in `internal/parser` using `golang.org/x/net/html`: extract `<a href>`, resolve to absolute URLs, skip `mailto:`, `javascript:`, `tel:`, fragments-only links, and non-http(s) schemes.
- Implement URL normalization in `internal/urlmanager/normalize.go`: lowercase scheme and host, strip fragments, remove default ports (:80, :443), resolve `.` and `..` path segments, normalize empty path to `/`, strip trailing slash consistently (document the choice).
- Implement the URL Manager with statuses (`pending`, `in_progress`, `completed`, `failed`), and an atomic `TrySchedule(url, depth) bool` protected by `sync.Mutex`.
- Unit tests: normalization cases, parser extraction and relative resolution, and a concurrent test where many goroutines call `TrySchedule` on overlapping URLs and exactly one wins per unique URL. Run with `-race`.

Do NOT start the worker pool, checkpointing, server, or dashboard yet. End with the PHASE REPORT.

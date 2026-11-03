# Operating Systems Concepts in CrawlSync

This document maps core Operating Systems concepts to their real implementations in the CrawlSync codebase. Written for undergraduate OS students preparing for viva examinations.

---

### 1. Process State Persistence
- **Concept:** Volatile process memory (RAM) is erased when a program halts or loses power. To achieve longevity across runs, internal program state must be serialized to non-volatile secondary storage (disk).
- **Code Mapping:** `internal/state/checkpoint.go:Save()` serializes the state of the URL Manager into persistent JSON files (`checkpoint.json`).

---

### 2. Checkpoint and Restart
- **Concept:** Long-running scientific and distributed workloads periodically write their intermediate computation state to disk so that execution can be resumed from the most recent checkpoint rather than restarted from scratch.
- **Code Mapping:** `internal/controller/controller.go:saveCheckpoint()` captures periodic state snapshots every `--checkpoint-interval` seconds. Phase 6 will use these checkpoints to resume crawling without re-downloading completed pages.

---

### 3. Fault Tolerance
- **Concept:** A fault-tolerant system continues operating correctly even when unexpected failures occur (e.g., malformed HTML, network timeouts, invalid links, or sudden SIGINT cancellation).
- **Code Mapping:**
  - Non-fatal HTML parse errors log warnings without aborting the page or crashing the worker (`internal/controller/worker.go:processJob()`).
  - Network timeouts are bounded by `context.WithTimeout`.
  - Failed checkpoints log warnings but never crash the active crawl session.

---

### 4. Consistent Global Snapshots (Chandy-Lamport Concept)
- **Concept:** In a concurrent or distributed system, capturing the state of all components at an arbitrary instant can result in an inconsistent or "torn" view (e.g., marking a transaction completed while omitting its outputs).
- **Code Mapping:**
  - `internal/urlmanager/manager.go:Snapshot()` captures both `Stats` and all `URLItem` entries under a single `RLock()`.
  - In `internal/controller/controller.go`, checkpoints are taken only at safe event-loop boundaries, ensuring parent page completion and child URL discovery are committed as one atomic logical step.

---

### 5. Synchronization & Barriers
- **Concept:** Threads must synchronize execution phases to avoid race conditions and resource leaks. A barrier blocks execution until all participating threads reach the synchronization point.
- **Code Mapping:**
  - `sync.WaitGroup` acts as a thread barrier during worker shutdown (`internal/controller/controller.go:Run()`).
  - The controller closes `jobsChan` and executes `c.wg.Wait()`, ensuring every worker has fully exited before the final checkpoint is taken.

---

### 6. Mutual Exclusion & Critical Sections
- **Concept:** When multiple concurrent threads access shared mutable data, uncoordinated concurrent reads/writes lead to data corruption, lost updates, or panics. Mutual exclusion ensures at most one thread enters the critical section at any time.
- **Code Mapping:**
  - `internal/urlmanager/manager.go` guards `urls map[string]*URLItem` and `nextSequence` using `sync.RWMutex`.
  - `TrySchedule()` encapsulates test-and-set within `m.mu.Lock()`, eliminating Time-Of-Check to Time-Of-Use (TOCTOU) race conditions.
  - `internal/controller/worker.go` guards worker monitoring states using `wp.mu`.

---

### 7. Atomic File Replacement (Rename / MoveFileEx)
- **Concept:** Directly writing to an existing file modifies it in place. If the system crashes mid-write, the destination file is left corrupt or truncated. Atomic replacement writes to a temporary file first and swaps directory entries in one metadata operation.
- **Code Mapping:**
  - `internal/state/checkpoint.go:commitFile()` uses `os.Rename` to atomically swap the temporary checkpoint file with `checkpoint.json`.
  - On Windows, existing files are rotated to `checkpoint.prev.json` first, ensuring cross-platform safety.

---

### 8. Crash Consistency & Backup Rotation
- **Concept:** If power is cut while committing a new checkpoint, the system must not lose both the new and old state. Maintaining a prior valid version ensures rollback capability.
- **Code Mapping:**
  - `internal/state/checkpoint.go:commitFile()` rotates the existing `checkpoint.json` into `checkpoint.prev.json` before moving the new checkpoint into place.
  - If committing fails, the previous checkpoint is restored.

---

### 9. Filesystem Durability (fsync)
- **Concept:** Modern operating systems buffer disk writes in RAM (the OS page cache) for performance. If power fails before the cache is flushed to non-volatile storage, data is lost despite a successful `write()` system call.
- **Code Mapping:**
  - `internal/state/checkpoint.go` explicitly calls `tmpFile.Sync()` (the Go wrapper for `fsync` / `FlushFileBuffers`) before closing and renaming checkpoint files.
  - `internal/controller/worker.go:savePageToDisk()` similarly invokes `tmpFile.Sync()` on downloaded HTML pages.

---

### 10. Graceful Shutdown & Signal Handling
- **Concept:** Operating systems notify running processes of impending termination via signals (`SIGINT`, `SIGTERM`). A well-behaved process traps these signals, halts admission of new work, allows active tasks to cleanly finish, flushes state, and exits with code 0.
- **Code Mapping:**
  - `cmd/crawlsync/main.go` sets up `signal.NotifyContext` to intercept `os.Interrupt` and `syscall.SIGTERM`.
  - The Controller catches context cancellation, drains in-flight workers, resets worker states to `IDLE`, saves a final checkpoint, and exits cleanly.


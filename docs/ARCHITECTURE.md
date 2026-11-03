# CrawlSync Architecture

## Overview

CrawlSync is a concurrent web crawler written in Go, designed as an academic demonstration of core Operating Systems concurrency, synchronization, persistence, and fault-tolerance concepts.

```
                          +------------------------+
                          |   CLI / User Input     |
                          +-----------+------------+
                                      |
                                      v
                          +------------------------+
                          |   Crawler Controller   |  <--- Single Owner Scheduler
                          |   (select event loop)  |
                          +----+-------+-------+---+
                               |       |       |
                Dispatches jobs|       |       | Snapshots at safe boundaries
                               v       |       v
               +-------------------+   |   +--------------------------+
               |  jobsChan (Jobs)  |   |   |   Checkpoint Manager     |
               +---------+---------+   |   |   - Atomic writes (.tmp) |
                         |             |   |   - fsync (Sync)         |
                         v             |   |   - Rotation (.prev.json)|
               +-------------------+   |   +--------------------------+
               | Worker Pool (1..N)|   |
               | - HTTP Fetcher    |   |
               | - Disk Storage    |   | Results
               | - Link Parser     |   |
               +---------+---------+   |
                         |             |
                         +-------------+
                         resultsChan (CrawlResult)
                               |
                               v
                     +-------------------+
                     |    URL Manager    |  <--- Single Source of Truth
                     | (Mutex-protected) |
                     +-------------------+
```

---

## 1. Controller & Event Loop Coordination

The **Crawler Controller** is the single owner of the scheduling lifecycle. It ensures:
1. **Volatile vs. Authoritative Separation:**
   - Volatile runtime buffers (`pendingJobs []Job` slice, `inFlight` counter) exist solely in the Controller's memory to multiplex work.
   - The **URL Manager** is the single source of truth for URL statuses (`pending`, `in_progress`, `completed`, `failed`).
2. **Deadlock-Free Dispatch:**
   - Uses a dynamic Go `select` loop. If `pendingJobs` is empty, `sendChan` is `nil`, disabling the send case and keeping `resultsChan` active.
   - If `jobsChan` is full, the controller does not block; it remains ready to drain `resultsChan`.
3. **Safe Snapshot Boundaries:**
   - Checkpoints are taken only at the top of the event loop.
   - Result processing is an indivisible, logical controller operation:
     1. Drain worker result.
     2. Transition parent URL to `completed` or `failed`.
     3. Extract and schedule discovered child URLs into `URLManager`.
     4. Append new jobs to `pendingJobs`.
   - A checkpoint taken at the loop boundary is guaranteed never to contain a completed parent whose children were omitted.

---

## 2. URL Manager & Consistent Snapshots

The **URL Manager** acts as a thread-safe monitor guarding the URL registry.

- **Monotonic Sequence Assignment:**
  - Every newly discovered unique URL receives a monotonically increasing sequence number (`1, 2, 3...`) in the critical section of `TrySchedule`.
  - Duplicate URLs are rejected and do not consume sequence numbers.
- **Consistent Snapshotting (`Snapshot()`):**
  - Acquires a single `RLock()`.
  - Deep-copies all `URLItem` records into a slice sorted deterministically by sequence.
  - Aggregates status counts (`Stats`) under the same lock.
  - Releases `RLock()` immediately before any serialization or disk I/O.
  - Avoids torn reads: statistics and URL records always match.

---

## 3. Checkpoint Manager & Atomic Persistence

The **Checkpoint Manager** (`internal/state/checkpoint.go`) handles durability and crash consistency.

### Checkpoint Schema (Version 1)
```json
{
  "version": 1,
  "sessionId": "session_1793731685_a52e5a0bb3a46f09",
  "createdAt": "2026-11-04T12:00:00Z",
  "sequence": 1,
  "config": {
    "startURL": "https://go.dev",
    "workerCount": 3,
    "maxPages": 4,
    "maxDepth": 2,
    "requestTimeout": "10s",
    "sameDomainOnly": true
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
      "sequence": 1,
      "url": "https://go.dev",
      "normalized": "https://go.dev/",
      "status": "completed",
      "depth": 0,
      "discovered_at": "...",
      "updated_at": "...",
      "status_code": 200,
      "size": 64762,
      "duration_ms": 599,
      "saved_path": "data\\pages\\go.dev__ba6e07bbb602.html"
    }
  ]
}
```

### Atomic Checkpoint Replacement Procedure
1. Serialize checkpoint to formatted JSON.
2. Create unique temporary file in the checkpoint directory: `checkpoint-<nano>-*.tmp`.
3. Write payload and invoke `f.Sync()` to flush OS page cache to physical media.
4. Close temporary file.
5. Atomic rotation:
   - If `checkpoint.json` exists, rename it to `checkpoint.prev.json` (backup).
   - Rename temporary file to `checkpoint.json`.
   - If rename fails, rollback backup to `checkpoint.json`.
6. Cleanup: uncommitted temporary files are automatically removed via deferred cleanup.

---

## 4. Atomic Page Storage

Downloaded HTML pages in `internal/controller/worker.go` are saved atomically:
1. Content is written to `data/pages/<slug>_<hash>-*.tmp`.
2. `f.Sync()` flushes write buffers to disk.
3. `f.Close()` closes file handle.
4. `os.Rename` moves temporary file to final destination `<slug>_<hash>.html`.
5. URLs are marked `completed` only after the atomic save completes successfully.

---

## 5. Periodic & Graceful Shutdown Checkpoints

- **Periodic Checkpointing:**
  - Controlled by a configurable `time.Ticker` (default `30s`, `--checkpoint-interval`).
  - Fires at safe loop boundaries in the Controller select loop without interrupting or blocking active workers.
  - Serialization of checkpoint saves (`cm.mu`) prevents concurrent write contention.
- **Graceful Shutdown Checkpointing:**
  - Upon OS signal (`SIGINT`, `SIGTERM`) or context cancellation:
    1. Stop accepting new jobs; close `jobsChan`.
    2. Await worker termination via `wg.Wait()`.
    3. Ensure in-progress tasks stop cleanly without falsely marking interrupted URLs as completed.
    4. Write final checkpoint capturing stable state.
    5. Clean process termination.


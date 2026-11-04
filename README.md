# CrawlSync

CrawlSync is a multithreaded web crawler and HTTP REST API server implemented in Go. It demonstrates core operating system and concurrency principles: fixed worker goroutine thread pools, thread-safe synchronized task queues, URL normalization and deduplication, atomic snapshot checkpointing, crash recovery, and non-blocking HTTP server coordination.

---

## Operating Modes

CrawlSync supports two execution modes:

1. **CLI Crawler Mode**: Direct terminal-driven crawling with real-time statistics and crash recovery.
2. **REST API Server Mode**: Standalone HTTP server exposing the crawler lifecycle and data inspection through a REST API.

Both modes share the underlying crawler engine without interfering with one another.

---

## 1. CLI Crawler Mode

When `--serve` is omitted, CrawlSync runs in CLI mode.

### Running a New Crawl
```bash
go run ./cmd/crawlsync --url https://example.com --workers 5 --max-pages 20 --max-depth 2
```

### Resuming an Interrupted Crawl
```bash
go run ./cmd/crawlsync --resume
```

### Retrying Previously Failed URLs
```bash
go run ./cmd/crawlsync --resume --retry-failed
```

### CLI Flags
| Flag | Default | Description |
|---|---|---|
| `--url` | `https://example.com` | Target start URL to crawl |
| `--workers` | `5` | Number of concurrent worker goroutines (1–20) |
| `--max-pages` | `10` | Maximum number of pages to crawl |
| `--max-depth` | `2` | Maximum link discovery depth |
| `--timeout` | `10` | HTTP request timeout in seconds |
| `--checkpoint-interval` | `30s` | Interval between periodic checkpoints |
| `--allow-local` | `false` | Allow crawling localhost and private IP addresses (SSRF defense) |
| `--resume` | `false` | Resume crawl session from checkpoint |
| `--retry-failed` | `false` | Demote failed URLs to pending upon resume |
| `--dir` | `data/pages` | Directory for saved HTML files |
| `--checkpoint-dir` | *(auto)* | Directory for checkpoint files |

---

## 2. REST API Server Mode

When `--serve` is provided, CrawlSync starts a standalone REST API server.

### Starting the Server
```bash
# Default localhost binding (127.0.0.1:8080)
go run ./cmd/crawlsync --serve

# Custom bind address and port
go run ./cmd/crawlsync --serve --host 127.0.0.1 --port 8081

# Enabling local network crawling for server sessions (operator-controlled)
go run ./cmd/crawlsync --serve --allow-local
```

### Server Flags
| Flag | Default | Description |
|---|---|---|
| `--serve` | `false` | Enable REST API server mode |
| `--host` | `127.0.0.1` | REST API server bind host |
| `--port` | `8080` | REST API server port (1–65535) |
| `--allow-local` | `false` | Server-operator SSRF policy: allows crawling local/private IPs |
| `--dir` | `data/pages` | Default storage directory for downloaded HTML pages |
| `--checkpoint-dir` | *(auto)* | Default directory for checkpoints |
| `--workers` | `5` | Default worker count for API crawl sessions |
| `--checkpoint-interval` | `30s` | Default checkpoint interval for API crawl sessions |

> **Security Note:** CrawlSync binds to `127.0.0.1` by default. CrawlSync does not currently implement authentication or TLS. If binding beyond localhost (e.g. `0.0.0.0`), ensure the server is placed behind a secure reverse proxy with authentication and network access controls.

---

## 3. REST API Endpoints

All responses follow a consistent JSON envelope:
- **Success**: `{"success": true, "data": ...}`
- **Error**: `{"success": false, "error": {"code": "...", "message": "..."}}`

| Method | Endpoint | Description |
|---|---|---|
| `POST` | `/api/crawl/start` | Start a new crawl or resume an existing session |
| `GET` | `/api/crawl/status` | Current crawler lifecycle state, session ID, and active URL |
| `GET` | `/api/crawl/stats` | Real-time aggregate statistics (pages, rates, queue, errors) |
| `POST` | `/api/crawl/pause` | Pause worker dispatch (in-flight fetches finish) |
| `POST` | `/api/crawl/resume` | Resume worker dispatch |
| `POST` | `/api/crawl/stop` | Gracefully stop the crawl and save shutdown checkpoint |
| `POST` | `/api/crawl/checkpoint` | Trigger an immediate atomic manual checkpoint |
| `GET` | `/api/workers` | Inspect worker states (`idle`, `busy`), IDs, and current URLs |
| `GET` | `/api/urls` | Paginated and filtered URL list (`limit`, `offset`, `status`, `depth`, `sort`) |
| `GET` | `/api/urls/{id}` | Inspect single URL item by its sequence number |

---

## 4. Example API Requests

### Start a New Crawl
```bash
curl -X POST http://127.0.0.1:8080/api/crawl/start \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://example.com",
    "workers": 4,
    "max_pages": 50,
    "max_depth": 2
  }'
```

### Resume from Checkpoint
```bash
curl -X POST http://127.0.0.1:8080/api/crawl/start \
  -H "Content-Type: application/json" \
  -d '{
    "resume": true,
    "retry_failed": true
  }'
```

### Inspect Crawler Status
```bash
curl http://127.0.0.1:8080/api/crawl/status
```

### Inspect Real-Time Statistics
```bash
curl http://127.0.0.1:8080/api/crawl/stats
```

### Pause and Resume
```bash
curl -X POST http://127.0.0.1:8080/api/crawl/pause
curl -X POST http://127.0.0.1:8080/api/crawl/resume
```

### Query URLs with Filtering and Pagination
```bash
# Get completed URLs, sorted by depth, 20 per page
curl "http://127.0.0.1:8080/api/urls?limit=20&offset=0&status=completed&sort=depth_asc"
```

### Query Single URL by Sequence ID
```bash
curl http://127.0.0.1:8080/api/urls/1
```

### Gracefully Stop Active Crawl
```bash
curl -X POST http://127.0.0.1:8080/api/crawl/stop
```

---

## 5. Graceful Shutdown Behavior

CrawlSync intercepts `SIGINT` (Ctrl+C) and `SIGTERM`:

1. **Stops Accepting Requests**: The HTTP socket listener stops accepting new connections immediately.
2. **Drains HTTP Requests**: In-flight HTTP request handlers are allowed to finish within a bounded deadline (`15s`).
3. **Stops Active Crawler**: Signals the active Controller to finish active page downloads.
4. **Drains Results**: Workers finish and results in the queue are processed.
5. **Persists Checkpoint**: An atomic `"shutdown"` checkpoint snapshot is saved to disk.
6. **Exits Cleanly**: The process terminates cleanly with code 0 without data loss or file corruption.

---

## 6. Building and Testing

```bash
# Run unit and concurrency tests with race detection
go test -count=1 -race ./...

# Build standalone binary
go build -o crawlsync.exe ./cmd/crawlsync
```


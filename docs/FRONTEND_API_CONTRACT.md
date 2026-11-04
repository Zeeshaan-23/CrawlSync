# CrawlSync: Frontend API Contract & Integration Specification

> **Target Version:** CrawlSync Phase 8 (Dashboard UI)  
> **Backend Implementation Source:** `internal/server/` (`handlers.go`, `handlers_url.go`, `models.go`, `middleware.go`, `service.go`)  
> **Transport:** Standard HTTP/1.1 REST API  
> **Default Bind Address:** `http://127.0.0.1:8080`  
> **Format:** JSON (`Content-Type: application/json; charset=utf-8`)

---

## 1. Protocol & Response Envelopes

All API communication uses standard HTTP methods and JSON payloads. Every response emitted by the CrawlSync server follows a uniform top-level envelope.

### 1.1 Success Envelope
Every successful response returns HTTP 200 (or HTTP 202 for `POST /api/crawl/start`):
```json
{
  "success": true,
  "data": { ... }
}
```

### 1.2 Error Envelope
When a request fails, the server returns an appropriate HTTP 4xx or 5xx code with a structured error object:
```json
{
  "success": false,
  "error": {
    "code": "ERROR_CODE_STRING",
    "message": "Human-readable description of the error"
  }
}
```

---

## 2. API Endpoints Specification

### 2.1 POST `/api/crawl/start`
Initiates a new crawl session or resumes an existing session from a checkpoint.

- **HTTP Method:** `POST`
- **Request Headers:** `Content-Type: application/json`
- **Request Body (JSON):**
  ```json
  {
    "url": "https://example.com",
    "workers": 5,
    "max_pages": 100,
    "max_depth": 3,
    "timeout_seconds": 10,
    "same_domain": true,
    "resume": false,
    "retry_failed": false
  }
  ```
  *Field Rules:*
  - `url` (*required if resume is false*): Must be valid `http://` or `https://` with non-empty host.
  - `workers` (*optional*, integer `1` to `20`, default `5`).
  - `max_pages` (*optional*, integer $\ge 1$, default `10`).
  - `max_depth` (*optional*, integer $\ge 0$, default `2`).
  - `timeout_seconds` (*optional*, integer $\ge 1$, default `10`).
  - `same_domain` (*optional*, boolean, default `true`).
  - `resume` (*optional*, boolean, default `false`).
  - `retry_failed` (*optional*, boolean, default `false`).
  - **Important:** `allow_local` is intentionally omitted from client requests for SSRF safety; it is controlled strictly by server configuration.
- **Success Response (HTTP 202 Accepted):**
  ```json
  {
    "success": true,
    "data": {
      "session_id": "session_1793813541_b8319f6ad85642a1",
      "state": "RUNNING",
      "start_url": "https://example.com",
      "worker_count": 5,
      "max_pages": 100,
      "max_depth": 3,
      "same_domain": true,
      "storage_dir": "data/pages",
      "checkpoint_dir": "data/checkpoints",
      "checkpoint_interval": "30s",
      "is_resumed": false
    }
  }
  ```
- **Error Responses:**
  - `400 Bad Request` (`INVALID_JSON`, `INVALID_CONFIGURATION`)
  - `409 Conflict` (`CRAWL_ALREADY_RUNNING`, `CRAWL_STOPPING`, `PREVIOUS_SESSION_TERMINATING`)

---

### 2.2 GET `/api/crawl/status`
Returns high-level crawler lifecycle state, session metadata, and cumulative runtime.

- **HTTP Method:** `GET`
- **Success Response (HTTP 200 OK):**
  ```json
  {
    "success": true,
    "data": {
      "state": "RUNNING",
      "session_id": "session_1793813541_b8319f6ad85642a1",
      "start_url": "https://example.com",
      "worker_count": 5,
      "max_pages": 100,
      "max_depth": 3,
      "cumulative_elapsed_ms": 14250,
      "is_resumed": false,
      "is_paused": false,
      "last_run_error": ""
    }
  }
  ```
  *Lifecycle States:*
  - `IDLE`: Server running, no crawl initiated.
  - `RUNNING`: Crawler actively dispatching and executing jobs.
  - `PAUSED`: Job dispatch frozen; in-flight tasks drain.
  - `STOPPING`: Cancellation signaled; workers draining.
  - `STOPPED`: Cleanly stopped after user request or signal.
  - `COMPLETED`: Crawl graph completed naturally.

---

### 2.3 GET `/api/crawl/stats`
Returns aggregated numerical metrics across all discovered URLs.

- **HTTP Method:** `GET`
- **Success Response (HTTP 200 OK):**
  ```json
  {
    "success": true,
    "data": {
      "total_discovered": 42,
      "pending": 12,
      "in_progress": 4,
      "completed": 25,
      "failed": 1
    }
  }
  ```
- **Error Response:**
  - `404 Not Found` (`NO_ACTIVE_CRAWL`): When the crawler has not yet started a session.

---

### 2.4 POST `/api/crawl/pause`
Freezes URL dispatch without terminating active worker downloads.

- **HTTP Method:** `POST`
- **Request Body:** Empty or `{}`
- **Success Response (HTTP 200 OK):**
  ```json
  {
    "success": true,
    "data": {
      "state": "PAUSED",
      "session_id": "session_1793813541_b8319f6ad85642a1",
      "message": "Crawl session paused"
    }
  }
  ```
- **Error Responses:**
  - `404 Not Found` (`NO_ACTIVE_CRAWL`)
  - `409 Conflict` (`INVALID_STATE_TRANSITION`): If not currently `RUNNING`.

---

### 2.5 POST `/api/crawl/resume`
Resumes job dispatch from a paused state.

- **HTTP Method:** `POST`
- **Request Body:** Empty or `{}`
- **Success Response (HTTP 200 OK):**
  ```json
  {
    "success": true,
    "data": {
      "state": "RUNNING",
      "session_id": "session_1793813541_b8319f6ad85642a1",
      "message": "Crawl session resumed"
    }
  }
  ```
- **Error Responses:**
  - `404 Not Found` (`NO_ACTIVE_CRAWL`)
  - `409 Conflict` (`INVALID_STATE_TRANSITION`): If not currently `PAUSED`.

---

### 2.6 POST `/api/crawl/stop`
Gracefully halts the crawl, drains remaining worker results, and saves a shutdown checkpoint.

- **HTTP Method:** `POST`
- **Request Body:** Empty or `{}`
- **Success Response (HTTP 200 OK):**
  ```json
  {
    "success": true,
    "data": {
      "state": "STOPPED",
      "session_id": "session_1793813541_b8319f6ad85642a1",
      "message": "Crawl session stopped"
    }
  }
  ```
- **Error Responses:**
  - `404 Not Found` (`NO_ACTIVE_CRAWL`)

---

### 2.7 POST `/api/crawl/checkpoint`
Triggers an immediate atomic state snapshot to disk at a clean event-loop boundary.

- **HTTP Method:** `POST`
- **Request Body:** Empty or `{}`
- **Success Response (HTTP 200 OK):**
  ```json
  {
    "success": true,
    "data": {
      "session_id": "session_1793813541_b8319f6ad85642a1",
      "sequence": 4,
      "timestamp": "2026-11-04T23:02:21.8421054+05:30",
      "stats": {
        "total_discovered": 42,
        "pending": 12,
        "in_progress": 4,
        "completed": 25,
        "failed": 1
      }
    }
  }
  ```
- **Error Responses:**
  - `404 Not Found` (`NO_ACTIVE_CRAWL`)
  - `500 Internal Server Error` (`INTERNAL_ERROR`): If disk write or filesystem rename fails.

---

### 2.8 GET `/api/workers`
Returns real-time status, state, and currently assigned URL for every worker in the worker pool.

- **HTTP Method:** `GET`
- **Success Response (HTTP 200 OK):**
  ```json
  {
    "success": true,
    "data": {
      "worker_count": 4,
      "workers": [
        {
          "id": 1,
          "state": "DOWNLOADING",
          "current_url": "https://example.com/blog",
          "updated_at": "2026-11-04T23:15:30.120Z"
        },
        {
          "id": 2,
          "state": "IDLE",
          "current_url": "",
          "updated_at": "2026-11-04T23:15:29.850Z"
        }
      ]
    }
  }
  ```
  *Worker States:*
  - `IDLE`: Worker waiting on `jobsChan` for a task assignment.
  - `DOWNLOADING`: Worker actively executing HTTP fetch, disk save, or HTML parsing.
- **Error Response:**
  - `404 Not Found` (`NO_ACTIVE_CRAWL`): If no crawl session is active.

---

### 2.9 GET `/api/urls`
Queries discovered URLs with filtering, sorting, and pagination.

- **HTTP Method:** `GET`
- **Query Parameters:**
  - `limit` (*optional*, integer `1` to `100`, default `20`).
  - `offset` (*optional*, integer $\ge 0$, default `0`).
  - `status` (*optional*, one of: `pending`, `in_progress`, `completed`, `failed`).
  - `depth` (*optional*, integer $\ge 0$).
  - `sort` (*optional*, one of: `seq_asc` [default], `seq_desc`, `depth_asc`, `depth_desc`, `time_desc`).
- **Success Response (HTTP 200 OK):**
  ```json
  {
    "success": true,
    "data": {
      "total": 42,
      "filtered": 25,
      "limit": 20,
      "offset": 0,
      "items": [
        {
          "sequence": 1,
          "url": "https://example.com",
          "normalized": "https://example.com/",
          "status": "completed",
          "depth": 0,
          "discovered_at": "2026-11-04T23:00:00Z",
          "updated_at": "2026-11-04T23:00:01Z",
          "status_code": 200,
          "error_message": "",
          "size": 4820,
          "duration_ms": 32,
          "saved_path": "data/pages/page_1.html"
        }
      ]
    }
  }
  ```
- **Error Responses:**
  - `400 Bad Request` (`INVALID_QUERY_PARAMETER`): If parameter bounds or values are invalid.
  - `404 Not Found` (`NO_ACTIVE_CRAWL`): If no session exists.

---

### 2.10 GET `/api/urls/{id}`
Returns details for a single URL record identified by its monotonic sequence ID.

- **HTTP Method:** `GET`
- **Path Variable:** `{id}` (positive 64-bit integer, e.g. `1`, `42`).
- **Success Response (HTTP 200 OK):**
  ```json
  {
    "success": true,
    "data": {
      "sequence": 1,
      "url": "https://example.com",
      "normalized": "https://example.com/",
      "status": "completed",
      "depth": 0,
      "discovered_at": "2026-11-04T23:00:00Z",
      "updated_at": "2026-11-04T23:00:01Z",
      "status_code": 200,
      "error_message": "",
      "size": 4820,
      "duration_ms": 32,
      "saved_path": "data/pages/page_1.html"
    }
  }
  ```
- **Error Responses:**
  - `400 Bad Request` (`INVALID_URL_ID`): If `{id}` is not a positive integer.
  - `404 Not Found` (`URL_NOT_FOUND`, `NO_ACTIVE_CRAWL`): If the sequence is not found.

---

## 3. Error Codes Reference Table

| Error Code | HTTP Status | Meaning / Trigger Condition |
| :--- | :--- | :--- |
| `INVALID_JSON` | `400 Bad Request` | Syntax error, unrecognized fields, or multiple JSON objects in request body. |
| `INVALID_CONFIGURATION` | `400 Bad Request` | Worker count out of range (1-20), invalid seed URL scheme, negative pages/depth. |
| `INVALID_QUERY_PARAMETER` | `400 Bad Request` | Limit out of range (1-100), negative offset, or unrecognized status/sort enum. |
| `INVALID_URL_ID` | `400 Bad Request` | Path parameter `{id}` is non-numeric, negative, or zero. |
| `PAYLOAD_TOO_LARGE` | `413 Payload Too Large` | Request body exceeds the strict 1 MB size limit. |
| `UNSUPPORTED_MEDIA_TYPE` | `415 Unsupported Media Type` | Missing or non-`application/json` Content-Type on POST requests. |
| `NO_ACTIVE_CRAWL` | `404 Not Found` | Requesting stats, workers, or control action when the crawler is IDLE. |
| `URL_NOT_FOUND` | `404 Not Found` | Requested sequence number does not exist in URL Manager. |
| `CRAWL_ALREADY_RUNNING` | `409 Conflict` | Attempted `POST /api/crawl/start` while a session is already RUNNING or PAUSED. |
| `CRAWL_STOPPING` | `409 Conflict` | Attempted start or mutation while the previous session is currently STOPPING. |
| `PREVIOUS_SESSION_TERMINATING` | `409 Conflict` | Controller termination goroutine has not completed its final shutdown. |
| `INVALID_STATE_TRANSITION` | `409 Conflict` | Attempting to pause when not RUNNING, or resume when not PAUSED. |
| `INTERNAL_ERROR` | `500 Internal Server Error` | Unhandled panic recovered by middleware or unrecoverable disk failure. |

---

## 4. Dashboard Lifecycle Control Matrix

The frontend dashboard uses this state matrix to determine enabled / disabled button states and polling intervals:

| Crawler State | Start Button | Pause Button | Resume Button | Stop Button | Checkpoint Button | Polling Frequency |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `IDLE` | **Enabled** | Disabled | Disabled | Disabled | Disabled | Low (3s - 5s) |
| `RUNNING` | Disabled | **Enabled** | Disabled | **Enabled** | **Enabled** | High (1s) |
| `PAUSED` | Disabled | Disabled | **Enabled** | **Enabled** | **Enabled** | Moderate (2s) |
| `STOPPING` | Disabled | Disabled | Disabled | Disabled | Disabled | High (500ms) |
| `STOPPED` | **Enabled** | Disabled | Disabled | Disabled | Disabled | Low (3s - 5s) |
| `COMPLETED` | **Enabled** | Disabled | Disabled | Disabled | Disabled | Low (3s - 5s) |


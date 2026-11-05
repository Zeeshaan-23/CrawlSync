# Phase 8: Dashboard UI — Progress Log

## Stage 8.1: Dashboard Frontend Foundation & Layout

- **Date:** 2026-11-04
- **Status:** Completed & Verified (Stage 8.1 Frontend Foundation)
- **Objective:** Establish the complete frontend architecture, layout, design system, API contract, and API client foundation for the CrawlSync developer dashboard.

---

### 1. Files Created and Modified

#### Frontend Architecture (`web/`)
1. **[`web/index.html`](file:///d:/College/Sem%205/CrawlSync/web/index.html):**
   - Clean HTML5 semantic layout structuring:
     - Collapsible sidebar with SVG branding, navigation links, and real-time backend connection status indicator.
     - Global header displaying title, lifecycle state badge, active session ID, seed URL, uptime clock, and last updated timestamp.
     - Crawler control action bar with Start, Pause, Resume, Stop, and Save Checkpoint buttons.
     - 5 primary telemetry Stat Cards (Discovered, Completed, Pending, Failed, In-Flight) plus secondary metrics strip (Throughput speed, active workers, storage directories).
     - Main monitoring area featuring a Chart.js doughnut chart for URL status distribution and a progress bar with active session parameter list.
     - Worker pool monitor grid container.
     - URL Explorer with filtering (status, depth), sorting (sequence, depth, time), page-size selection, and full pagination controls.
     - Start Crawl configuration modal with form validation (workers, ceiling, depth, timeout, same-domain, resume, retry-failed).
     - Settings drawer for runtime API Base URL configuration and live connection testing.
     - Accessible floating toast notification container.
2. **[`web/css/styles.css`](file:///d:/College/Sem%205/CrawlSync/web/css/styles.css):**
   - Developer-oriented "Operate" design system inspired by infrastructure monitoring tools (Datadog, Grafana, Cloudflare dashboard, Raycast).
   - Deep charcoal background (`#090c10`), elevated surface cards (`#131722`), subtle borders (`rgba(255,255,255,0.08)`), and soft shadows.
   - Restrained semantic status accents: Emerald (`#10b981`), Amber (`#f59e0b`), Rose (`#f43f5e`), Indigo (`#6366f1`), Sky (`#38bdf8`).
   - Clean typography using `Inter` for prose and `JetBrains Mono` for sequence numbers, URLs, and timing metrics.
   - Comprehensive responsive breakpoints for Desktop ($>1200\text{px}$), Laptop ($992\text{px}-1200\text{px}$), Tablet ($768\text{px}-991\text{px}$), and Mobile ($<768\text{px}$).
3. **[`web/js/api.js`](file:///d:/College/Sem%205/CrawlSync/web/js/api.js):**
   - Centralized, dependency-free API client (`CrawlSyncAPI`).
   - Configurable base URL with `localStorage` persistence.
   - Uniform JSON response parsing (`{ success, data }` vs `{ success, error }`).
   - Structured error handling with status codes, error codes, and message strings.
   - Bounded request timeouts with `AbortController`.
   - Event-driven connection state tracking (`onConnectionChange`).
   - Methods implemented: `getStatus()`, `getStats()`, `getWorkers()`, `getURLs()`, `getURLDetail()`, `startCrawl()`, `pauseCrawl()`, `resumeCrawl()`, `stopCrawl()`, `createCheckpoint()`, `testConnection()`.
4. **[`web/js/components.js`](file:///d:/College/Sem%205/CrawlSync/web/js/components.js):**
   - Modular DOM components and data formatters:
     - HTML entity escaping (`escapeHTML`) for XSS defense.
     - Formatters: `formatNumber`, `formatBytes`, `formatDuration`, `formatTime`, `truncate`.
     - Visual badge renderers: `renderStateBadge`, `renderURLStatusBadge`, `renderHTTPCodeBadge`.
     - Worker card component (`renderWorkerCard`).
     - URL table row component (`renderURLTableRow`).
     - Empty states (`renderEmptyState`) and floating toast notifications (`showToast`).
5. **[`web/js/dashboard.js`](file:///d:/College/Sem%205/CrawlSync/web/js/dashboard.js):**
   - Dashboard view coordinator managing local view state and DOM synchronization.
   - Adaptive polling loop: 1s (`RUNNING`), 2s (`PAUSED`), 500ms (`STOPPING`), 4s (`IDLE`/`STOPPED`/`COMPLETED`).
   - Chart.js doughnut chart integration with dark theme palette.
   - Strict lifecycle control button state matrix synchronizer.
   - Clean handling of 404 `NO_ACTIVE_CRAWL` when the crawler is IDLE.
6. **[`web/js/app.js`](file:///d:/College/Sem%205/CrawlSync/web/js/app.js):**
   - Application bootstrap and event listener registrations.
   - Start Crawl modal lifecycle, input validation, and submission.
   - Settings drawer lifecycle, Base URL editing, and live connection testing.
   - Control button action triggers (Pause, Resume, Stop, Checkpoint, Refresh).
   - Filter, sort, and pagination event routing.
7. **[`web/assets/logo.svg`](file:///d:/College/Sem%205/CrawlSync/web/assets/logo.svg):**
   - Custom SVG crawler emblem featuring synchronized concurrency nodes and central scheduling core.

#### Documentation & Specifications
8. **[`docs/FRONTEND_API_CONTRACT.md`](file:///d:/College/Sem%205/CrawlSync/docs/FRONTEND_API_CONTRACT.md):**
   - Full specification of all 10 REST API endpoints, request/response DTO schemas, HTTP error codes, and button control lifecycle matrix.
9. **[`PROJECT_CONTEXT.md`](file:///d:/College/Sem%205/CrawlSync/PROJECT_CONTEXT.md):**
   - Updated Phase 8 status to `In progress (Stage 8.1 foundation verified)`.

---

### 2. Dashboard Design Decisions

- **No Frameworks / Vanilla Web Standards:**
  - Built strictly using semantic HTML5, CSS3, vanilla JavaScript (ES6+), and Chart.js via CDN.
  - Zero build step, zero npm dependencies in `web/`, preserving project constraints and academic clarity.
- **Developer & Infrastructure Aesthetic:**
  - Avoided flashy generic admin templates, gaudy glassmorphism, or excessive gradients.
  - Emphasized high information density, clean monospaced data alignment, distinct status pills, and purposeful micro-interactions.
- **Adaptive Polling Architecture:**
  - While WebSocket support was intentionally excluded (per project constraints), the polling engine dynamically throttles requests based on crawler state: 1-second intervals during active crawling, relaxing to 4 seconds when idle or completed.
- **Strict Separation of Concerns:**
  - Separated network communication (`api.js`), DOM component rendering (`components.js`), view state coordination (`dashboard.js`), and top-level user event binding (`app.js`).

---

### 3. API Endpoints Inspected

All endpoints in `internal/server/handlers.go` and `internal/server/handlers_url.go` were thoroughly inspected:
- `POST /api/crawl/start`: Start/resume crawl session.
- `GET /api/crawl/status`: High-level lifecycle state and elapsed runtime.
- `GET /api/crawl/stats`: Aggregated numerical URL metrics.
- `POST /api/crawl/pause`: Freeze job dispatch.
- `POST /api/crawl/resume`: Resume job dispatch.
- `POST /api/crawl/stop`: Gracefully halt crawler and commit shutdown checkpoint.
- `POST /api/crawl/checkpoint`: Trigger manual snapshot.
- `GET /api/workers`: Worker pool status and current assignments.
- `GET /api/urls`: Filtered, sorted, paginated list of URLs.
- `GET /api/urls/{id}`: Detail lookup for a single URL by sequence.

---

### 4. Tests Performed & Verification Results

1. **JavaScript Syntax Verification:**
   - Ran `node -c` on all 4 JavaScript files (`api.js`, `components.js`, `dashboard.js`, `app.js`):
   - **Result:** 0 syntax errors, 100% valid JavaScript.
2. **Live Go Server Communication Test:**
   - Started a live instance of `crawlsync.exe -serve -port 8899`.
   - Executed automated integration test suite (`test_client.mjs`):
     - `testConnection()`: Returned `true` (200 OK).
     - `getStatus()`: Successfully retrieved `{ state: "IDLE" }`.
     - `getStats()` on IDLE: Gracefully handled 404 `NO_ACTIVE_CRAWL` returning `null`.
     - `getWorkers()` on IDLE: Gracefully handled 404 `NO_ACTIVE_CRAWL` returning `null`.
     - `getURLs()` on IDLE: Gracefully returned `{ total: 0, filtered: 0, items: [] }`.
   - Clean shutdown with SIGINT confirmed.
   - **Result:** ALL 5 INTEGRATION CHECKS PASSED.
3. **Backend Regression Safety:**
   - Ran `go vet ./...`: Passed (0 warnings).
   - Ran `go test -count=1 -race ./...`:
     - `internal/controller`: PASS (5.244s)
     - `internal/fetcher`: PASS (2.507s)
     - `internal/parser`: PASS (1.708s)
     - `internal/server`: PASS (9.615s)
     - `internal/state`: PASS (2.184s)
     - `internal/urlmanager`: PASS (1.727s)
   - **Result:** ALL 155 TESTS PASSED across all packages with 0 data races.
4. **Git Workspace Cleanliness:**
   - No backend Go code files were modified. Only frontend assets and documentation were added.

---

### 5. Known Limitations & Blockers Reported (Resolved in Stage 8.2)

- **Static File Serving on Go Server:**
  - Resolved in Stage 8.2 via `web.DistFS` (`embed.FS`) mounted directly onto `http.ServeMux` at `GET /` with API route precedence preservation.

---

## Stage 8.2: Full Dashboard Functionality & Static File Serving

- **Date:** 2026-11-04
- **Status:** Completed & Verified (All 14 E2E Requirements Verified, 156/156 Unit & Race Tests Passed)
- **Objective:** Integrate static frontend asset serving into the Go HTTP server, connect all telemetry widgets, doughnut charts, worker pool grids, and URL explorers to live backend REST API endpoints via adaptive polling, enable runtime controls (Start, Pause, Resume, Stop, Save Checkpoint) governed by the lifecycle state machine, and perform end-to-end integration verification.

---

### 1. Go Backend Static Serving Integration

1. **Embedded Assets (`web/fs.go`):**
   - Packaged frontend assets using Go standard library `embed.FS` (`//go:embed index.html css/* js/* assets/*`).
   - Exported `DistFS` provides self-contained single-binary deployment without requiring external asset directories.
2. **Safe Static Handler (`internal/server/handlers.go`):**
   - Registered `GET /` on `http.ServeMux` via `createStaticHandler`.
   - Prevented directory traversal via standard `path.Clean`.
   - Disabled directory index browsing (e.g. `/js/` or `/css/` requests return `404 Not Found`).
   - Unknown `/api/*` paths return structured JSON 404 responses (`{"success":false,"error":{"code":"NOT_FOUND","message":"API route not found"}}`) rather than falling through to static file handling.
   - Handled `postOnlyAPIRoutes` gracefully: explicitly returns `405 Method Not Allowed` with `Allow: POST` header for POST-only API routes mistakenly hit with `GET`.
3. **MIME Type Preservation in Middleware (`internal/server/middleware.go`):**
   - Scoped `jsonHeaderMiddleware` exclusively to `/api/` route prefixes (`strings.HasPrefix(r.URL.Path, "/api/")`).
   - Static files (`index.html`, `styles.css`, `app.js`, `logo.svg`) correctly receive standard MIME types (`text/html`, `text/css`, `application/javascript`, `image/svg+xml`).
4. **Server Integration (`internal/server/server.go` & `cmd/crawlsync/main.go`):**
   - `ServerConfig` accepts `StaticFS` (`fs.FS`) and optional `StaticDir`.
   - CLI banner in `main.go` displays `Dashboard URL: http://127.0.0.1:8080/` when running in `--serve` mode.
5. **Unit & Race Test Coverage (`internal/server/server_test.go`):**
   - Added `TestServer_StaticFileServingAndPrecedence` testing:
     - Root `GET /` serves `index.html` with `text/html; charset=utf-8`.
     - Asset requests `GET /css/styles.css` serve CSS with `text/css; charset=utf-8`.
     - Asset requests `GET /js/app.js` serve JavaScript with `application/javascript` or `text/javascript`.
     - Asset requests `GET /assets/logo.svg` serve SVG with `image/svg+xml`.
     - API route precedence: `GET /api/crawl/status` routes to API handler with `application/json`.
     - Unknown API routes `GET /api/crawl/unknown` return JSON 404.
     - Directory index requests `GET /css/` return 404.
     - Non-existent files `GET /nonexistent.txt` return 404.
     - POST-only route `GET /api/crawl/start` returns 405 Method Not Allowed with `Allow: POST`.

---

### 2. Live Telemetry & Control Wire-Up (`web/js/`)

1. **Adaptive Polling Loop (`web/js/dashboard.js`):**
   - 1-second cadence during active crawling (`RUNNING`).
   - 2-second cadence when crawl is suspended (`PAUSED`).
   - 500-millisecond cadence during graceful shutdown transition (`STOPPING`).
   - 4-second relaxed cadence during terminal or inactive states (`IDLE`, `STOPPED`, `COMPLETED`).
   - Dynamically throttles to prevent socket saturation while maintaining near real-time telemetry.
2. **Telemetry Stat Cards & Secondary Metrics:**
   - 5 Primary Cards: Discovered (Total), Completed, Pending, Failed, In-Flight.
   - Secondary Metrics Strip: Crawl Speed (pages/sec calculation), Active Workers count, Storage Paths (`data/checkpoints/` and `data/pages/`).
   - Clean idle handling: Translates `NO_ACTIVE_CRAWL` (404) into zeroed counters without error toasts.
3. **URL Status Distribution Doughnut Chart & Progress:**
   - Chart.js 4.4.7 integration styled with developer dark palette (Emerald, Indigo, Rose, Amber).
   - In-place dataset updates avoiding layout shifts or flickering.
   - Crawl Progress bar displaying percentage towards ceiling target (`max_pages`).
4. **Worker Pool Grid Monitor:**
   - Dynamically creates worker cards mapping to worker IDs (`worker-0` .. `worker-N-1`).
   - Displays real-time status pill (`IDLE` vs `BUSY`), current active URL, sequence number, and duration elapsed.
5. **URL Explorer Table:**
   - Server-side query parameter assembly: `status`, `depth`, `sort_by` (`seq_asc`, `seq_desc`, `depth_asc`, `depth_desc`, `time_desc`), `limit`, `offset`.
   - Rich row formatting: discovery sequence, absolute URL, status badge, HTTP status code pill, tree depth, byte size, download duration, and saved file indicator.
   - Debounced search/filter inputs and pagination navigation (Previous, Next, page size selector).
   - Click-to-view URL detail modal fetching `/api/urls/{id}`.
6. **Crawler Runtime Controls:**
   - Start Crawl Modal: form validation for URL scheme (`http://` or `https://`), workers ($1-32$), max pages ($1-10000$), max depth ($0-20$), timeout ($1-120\text{s}$), same domain, resume, retry failed.
   - Lifecycle-dependent button gating:
     - `IDLE` / `STOPPED` / `COMPLETED`: Start enabled; Pause, Resume, Stop, Checkpoint disabled.
     - `RUNNING`: Pause, Stop, Checkpoint enabled; Start, Resume disabled.
     - `PAUSED`: Resume, Stop, Checkpoint enabled; Start, Pause disabled.
     - `STOPPING`: All control buttons disabled with loading spinners.
   - Toasts: Accessible floating notifications with success/error/warning alerts.

---

### 3. Verification & Test Results

1. **Go Unit and Race Test Suite:**
   - Command: `go test -count=1 -race ./...`
   - Result: **ALL 156 TESTS PASSED across all 6 packages with 0 data races**:
     - `crawlsync/internal/controller`: PASS (4.909s)
     - `crawlsync/internal/fetcher`: PASS (2.415s)
     - `crawlsync/internal/parser`: PASS (1.710s)
     - `crawlsync/internal/server`: PASS (4.252s)
     - `crawlsync/internal/state`: PASS (2.119s)
     - `crawlsync/internal/urlmanager`: PASS (1.678s)
   - Code hygiene: `go vet ./...` completed with 0 warnings.
2. **JavaScript Syntax Verification:**
   - Command: `node -c web/js/*.js`
   - Result: 0 syntax errors across `api.js`, `components.js`, `dashboard.js`, `app.js`.
3. **End-to-End Integration Verification (`test_dashboard_e2e.mjs`):**
   - Launched real CrawlSync HTTP server on ephemeral port alongside local mock HTTP multi-page target.
   - **Verification Results (14 of 14 passed):**
     1. [x] Dashboard UI served at root `GET /` with `Content-Type: text/html; charset=utf-8`.
     2. [x] Static assets (`styles.css`, `app.js`, `logo.svg`) served with valid MIME types.
     3. [x] Directory browsing blocked (e.g., `GET /js/` returns 404).
     4. [x] API route precedence preserved (`GET /api/crawl/status` routes to API, unknown `/api/` returns JSON 404).
     5. [x] Initial crawler state correctly reflects `IDLE` with 0 URLs and idle workers.
     6. [x] Start Crawl modal payload validation and crawl start via `POST /api/crawl/start`.
     7. [x] State transition from `IDLE` $\rightarrow$ `RUNNING` verified via `/api/crawl/status`.
     8. [x] Telemetry polling reflects active URL discovery, worker assignments, and completed pages.
     9. [x] Chart.js data feed accurately aggregates completed, pending, failed, and in-flight statuses.
     10. [x] Runtime Pause control (`POST /api/crawl/pause`) freezes workers and transitions state to `PAUSED`.
     11. [x] Runtime Resume control (`POST /api/crawl/resume`) unfreezes workers and transitions state to `RUNNING`.
     12. [x] Manual checkpoint creation (`POST /api/crawl/checkpoint`) generates valid checkpoint file on disk.
     13. [x] Graceful Stop control (`POST /api/crawl/stop`) halts crawler, saves shutdown checkpoint, and transitions state to `STOPPED`.
     14. [x] URL Explorer server-side filtering, sorting, pagination, and detail lookup (`/api/urls/{id}`) verified.


---

## Stage 8.3: Collapsible Sidebar, Bottom Control Pill & Quality Review

- **Date:** 2026-11-05
- **Status:** Completed & Verified (Stage 8.3 UI Enhancements & Responsive Audit)
- **Objective:** Implement an accessible collapsible sidebar with state persistence and off-canvas mobile drawer, migrate primary runtime controls to a fixed persistent bottom control pill, maintain existing button IDs and lifecycle state matrix, and perform a responsive visual audit across 1440px, 1024px, 768px, and 390px viewports.

---

### 1. Collapsible Sidebar Implementation

1. **Desktop Collapse & Expand:**
   - Accessible toggle button `#btn-toggle-sidebar` placed in `.sidebar-brand` with dynamic icon rotation (`❮` / `❯`), `aria-expanded`, and descriptive titles (`title="Collapse sidebar (Ctrl+B)"`).
   - Smooth CSS width transition from 250px to 68px (`--sidebar-collapsed-width`).
   - Navigation labels and brand text fade out smoothly (`opacity: 0`, `max-width: 0`) without causing layout instability or text wrapping.
   - Navigation icons center cleanly with increased tap target padding.
   - Dynamic CSS tooltips (`attr(data-tooltip)`) reveal item names on hover or keyboard `:focus-visible` in collapsed mode.
   - Chart.js integration: triggers `dashboard.onLayoutChange()` after sidebar transition to smoothly resize charts without layout flicker.
2. **Persistence:**
   - State stored in `localStorage` under key `crawlsync_sidebar_collapsed`.
   - Restored automatically on page initialization without flash of uncollapsed content.
3. **Keyboard Accessibility:**
   - Global keyboard shortcut: `Ctrl+B` / `Cmd+B` toggles the sidebar on desktop screens (>768px).
   - High-contrast `:focus-visible` outline rings on all interactive elements.
4. **Mobile Off-Canvas Drawer (≤768px):**
   - Transformed into an off-canvas drawer with `transform: translateX(-100%)`.
   - Hamburger menu toggle button (`#btn-mobile-menu`) added to `.app-header`.
   - Mobile close button (`#btn-close-sidebar-mobile`) and backdrop overlay (`#sidebar-backdrop`) with `backdrop-filter: blur(4px)`.
   - Tapping backdrop, clicking close button, selecting a navigation route, or pressing `Escape` closes the drawer automatically.

---

### 2. Persistent Bottom Floating Control Pill

1. **Floating Pill Layout (`nav.bottom-control-pill`):**
   - Fixed to viewport bottom: `bottom: calc(20px + env(safe-area-inset-bottom, 0px))`, centered horizontally (`left: 50%; transform: translateX(-50%)`).
   - Surface design: glassmorphic elevated dark panel (`rgba(19, 23, 34, 0.90)`, `backdrop-filter: blur(16px)`), subtle border (`var(--border-medium)`), multi-tier shadow (`0 16px 36px -4px rgba(0, 0, 0, 0.70)`), and inner highlight.
   - Main viewport bottom spacing: added `padding-bottom: calc(96px + env(safe-area-inset-bottom, 0px))` to `.dashboard-viewport` ensuring data tables and pagination are never obscured.
2. **Button IDs & Lifecycle State Matrix:**
   - Preserved all existing button IDs: `#btn-control-start`, `#btn-control-pause`, `#btn-control-resume`, `#btn-control-stop`, `#btn-control-checkpoint`.
   - Preserved lifecycle state matrix:
     - `IDLE` / `STOPPED` / `COMPLETED`: Start enabled; Pause, Resume, Stop, Checkpoint disabled.
     - `RUNNING`: Pause, Stop, Checkpoint enabled; Start, Resume disabled.
     - `PAUSED`: Resume, Stop, Checkpoint enabled; Start, Pause disabled.
     - `STOPPING`: all controls disabled.
3. **Visual Hierarchy:**
   - **Start:** High-contrast primary indigo button with soft glow on hover.
   - **Stop:** Distinct soft danger rose button (`var(--color-danger-subtle)`) turning solid danger red on hover, visually separated from destructive confusion.
   - **Pause / Resume:** Distinct amber warning and emerald success pill accents.
   - **Save Checkpoint:** Separated via a subtle vertical divider (`.pill-divider`) as a secondary snapshot action.
4. **Responsive Mobile Pill:**
   - On screens `<=480px` (e.g. 390px iPhone), extra label text is hidden (`.btn-label-extra { display: none; }`), displaying compact labels ("Start", "Pause", "Resume", "Stop", "Save") without horizontal overflow.
5. **Toast & Modal Stacking:**
   - Toast container positioned above the pill on mobile (`bottom: calc(84px + env(safe-area-inset-bottom, 0px))`).
   - Modals render at `z-index: 1000`, cleanly above the floating pill (`z-index: 100`).

---

### 3. API Client Port Flexibility

- **Bugfix in `web/js/api.js`:** Relaxed `defaultBase` check from strictly checking port `8080` to any HTTP protocol (`window.location.protocol.startsWith('http')`), allowing CrawlSync dashboard to communicate seamlessly when served on custom ports (e.g. `--port 8081` or `--port 9680`) without manual settings configuration.

---

### 4. Verification & Responsive Audit Results

1. **JavaScript Syntax Verification:**
   - Command: `node -c web/js/api.js; node -c web/js/components.js; node -c web/js/dashboard.js; node -c web/js/app.js`
   - Result: **0 errors, 100% valid JavaScript**.
2. **Go Backend Compatibility & Race Test:**
   - Command: `go vet ./...` $\rightarrow$ 0 warnings.
   - Command: `go test -count=1 -race ./...` $\rightarrow$ **ALL 159 TESTS PASSED** across all 6 packages with 0 data races.
3. **End-to-End Integration Tests:**
   - `test_dashboard_e2e.mjs`: **14 of 14 requirements passed**.
   - `test_server_resume_e2e.mjs`: **Passed (server crash recovery & resumption verified)**.
4. **Chrome DevTools Protocol (CDP) Real-Browser Audit:**
   - Automated interactive test suite executed via Chrome headless:
     - [x] **1440px Desktop Expanded:** Pass (no horizontal overflow, screenshot captured).
     - [x] **1440px Desktop Collapsed:** Pass (sidebar collapse toggle, CSS tooltips, screenshot captured).
     - [x] **Persistence Check:** Pass (reloaded page, verified sidebar remained collapsed from `localStorage`).
     - [x] **1024px Tablet Landscape:** Pass (no horizontal overflow, screenshot captured).
     - [x] **768px Tablet Portrait:** Pass (no horizontal overflow, screenshot captured).
     - [x] **390px Mobile Viewport:** Pass (no horizontal overflow, compact pill buttons verified, screenshot captured).
     - [x] **Mobile Drawer Navigation:** Pass (hamburger opens drawer, backdrop click closes drawer, screenshot captured).
     - [x] **Lifecycle State Controls:** Pass (verified in live crawl: IDLE $\rightarrow$ RUNNING $\rightarrow$ PAUSED $\rightarrow$ RESUME $\rightarrow$ STOPPED).
     - [x] **Overlay Non-Interference:** Pass (Start Crawl modal and floating toasts verified clear of bottom pill).

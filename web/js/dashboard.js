/**
 * CrawlSync Dashboard View Controller
 * Coordinates live state polling, UI element synchronization, Chart.js visualization,
 * worker monitoring, and URL explorer pagination.
 */

class DashboardController {
  constructor(apiClient) {
    this.api = apiClient;

    // Local View State
    this.state = {
      status: {
        state: 'IDLE',
        session_id: '',
        start_url: '',
        worker_count: 0,
        max_pages: 0,
        max_depth: 0,
        cumulative_elapsed_ms: 0,
        is_resumed: false,
        is_paused: false
      },
      stats: {
        total_discovered: 0,
        pending: 0,
        in_progress: 0,
        completed: 0,
        failed: 0
      },
      workers: [],
      urlQuery: {
        limit: 20,
        offset: 0,
        status: '',
        depth: '',
        sort: 'seq_asc'
      },
      urlList: {
        total: 0,
        filtered: 0,
        limit: 20,
        offset: 0,
        items: []
      },
      isPolling: false,
      pollTimer: null,
      lastUpdated: null,
      chartInstance: null
    };

    // Bound DOM Cache
    this.dom = {};
  }

  /**
   * Bind DOM elements and initialize Chart.js.
   */
  init() {
    this._cacheDOMElements();
    this._initChart();
    this._bindAPIEvents();
  }

  /**
   * Cache references to UI nodes for fast updates.
   * @private
   */
  _cacheDOMElements() {
    this.dom = {
      // Header & Global Status
      connectionBadge: document.getElementById('connection-status-badge'),
      connectionText: document.getElementById('connection-status-text'),
      crawlerStateBadge: document.getElementById('header-state-badge'),
      headerSessionID: document.getElementById('header-session-id'),
      headerSeedURL: document.getElementById('header-seed-url'),
      headerLastUpdated: document.getElementById('header-last-updated'),
      headerUptime: document.getElementById('header-uptime'),

      // Controls
      btnStart: document.getElementById('btn-control-start'),
      btnPause: document.getElementById('btn-control-pause'),
      btnResume: document.getElementById('btn-control-resume'),
      btnStop: document.getElementById('btn-control-stop'),
      btnCheckpoint: document.getElementById('btn-control-checkpoint'),
      btnRefresh: document.getElementById('btn-refresh-dashboard'),

      // Stat Cards
      statDiscovered: document.getElementById('stat-total-discovered'),
      statCompleted: document.getElementById('stat-completed-count'),
      statCompletedPct: document.getElementById('stat-completed-pct'),
      statPending: document.getElementById('stat-pending-count'),
      statFailed: document.getElementById('stat-failed-count'),
      statCrawling: document.getElementById('stat-crawling-count'),
      statSpeed: document.getElementById('stat-crawl-speed'),
      statActiveWorkers: document.getElementById('stat-active-workers'),

      // Progress & Details
      progressBar: document.getElementById('crawl-progress-fill'),
      progressText: document.getElementById('crawl-progress-text'),
      sessionStartURL: document.getElementById('detail-start-url'),
      sessionMaxPages: document.getElementById('detail-max-pages'),
      sessionMaxDepth: document.getElementById('detail-max-depth'),
      sessionWorkers: document.getElementById('detail-worker-count'),
      sessionDomainPolicy: document.getElementById('detail-domain-policy'),

      // Worker Pool Monitor
      workerCountBadge: document.getElementById('worker-count-badge'),
      workerGrid: document.getElementById('worker-cards-grid'),

      // URL Explorer
      urlFilterStatus: document.getElementById('filter-url-status'),
      urlFilterDepth: document.getElementById('filter-url-depth'),
      urlFilterSort: document.getElementById('filter-url-sort'),
      urlTableBody: document.getElementById('url-table-body'),
      urlPaginationInfo: document.getElementById('url-pagination-info'),
      btnPagePrev: document.getElementById('btn-page-prev'),
      btnPageNext: document.getElementById('btn-page-next'),
      urlPageSize: document.getElementById('select-page-size')
    };
  }

  /**
   * Initialize Chart.js doughnut chart for URL status distribution.
   * @private
   */
  _initChart() {
    const canvas = document.getElementById('chart-status-distribution');
    if (!canvas || typeof Chart === 'undefined') {
      console.warn('[Dashboard] Chart.js not loaded or canvas missing');
      return;
    }

    const ctx = canvas.getContext('2d');
    this.state.chartInstance = new Chart(ctx, {
      type: 'doughnut',
      data: {
        labels: ['Completed', 'Crawling', 'Pending', 'Failed'],
        datasets: [{
          data: [0, 0, 0, 0],
          backgroundColor: [
            '#10b981', // Emerald (Completed)
            '#6366f1', // Indigo (In Progress)
            '#f59e0b', // Amber (Pending)
            '#f43f5e'  // Rose (Failed)
          ],
          borderColor: '#14171f',
          borderWidth: 2,
          hoverOffset: 4
        }]
      },
      options: {
        responsive: true,
        maintainAspectRatio: false,
        plugins: {
          legend: {
            position: 'bottom',
            labels: {
              boxWidth: 12,
              padding: 16,
              color: '#94a3b8',
              font: {
                family: 'Inter, system-ui, sans-serif',
                size: 11
              }
            }
          },
          tooltip: {
            backgroundColor: '#1b202c',
            titleColor: '#f1f5f9',
            bodyColor: '#cbd5e1',
            borderColor: '#242b3b',
            borderWidth: 1,
            padding: 10,
            boxPadding: 4
          }
        },
        cutout: '72%'
      }
    });
  }

  /**
   * Listen to API client connection changes.
   * @private
   */
  _bindAPIEvents() {
    this.api.onConnectionChange((isOnline, error) => {
      this._updateConnectionUI(isOnline, error);
    });
  }

  /**
   * Update the connection badge in header and sidebar.
   * @private
   */
  _updateConnectionUI(isOnline, error) {
    if (!this.dom.connectionBadge) return;

    if (isOnline) {
      this.dom.connectionBadge.className = 'conn-indicator conn-online';
      if (this.dom.connectionText) this.dom.connectionText.textContent = 'Connected (127.0.0.1:8080)';
    } else {
      this.dom.connectionBadge.className = 'conn-indicator conn-offline';
      if (this.dom.connectionText) {
        this.dom.connectionText.textContent = error ? `Offline: ${error}` : 'Disconnected';
      }
    }
  }

  // ==========================================
  // Polling & Lifecycle Coordinator
  // ==========================================

  /**
   * Start adaptive background polling.
   */
  startPolling() {
    if (this.state.isPolling) return;
    this.state.isPolling = true;
    this._pollCycle();
  }

  /**
   * Stop background polling.
   */
  stopPolling() {
    this.state.isPolling = false;
    if (this.state.pollTimer) {
      clearTimeout(this.state.pollTimer);
      this.state.pollTimer = null;
    }
  }

  /**
   * Single poll cycle with dynamic adaptive frequency.
   * @private
   */
  async _pollCycle() {
    if (!this.state.isPolling) return;

    try {
      await this.refreshAll();
    } catch (err) {
      // Handled cleanly in refreshAll
    }

    // Determine poll interval based on crawler state
    let intervalMs = 2500;
    const currentState = (this.state.status.state || 'IDLE').toUpperCase();

    if (currentState === 'RUNNING') {
      intervalMs = 1000;
    } else if (currentState === 'STOPPING') {
      intervalMs = 500;
    } else if (currentState === 'PAUSED') {
      intervalMs = 2000;
    } else {
      intervalMs = 4000; // IDLE, STOPPED, COMPLETED
    }

    if (this.state.isPolling) {
      this.state.pollTimer = setTimeout(() => this._pollCycle(), intervalMs);
    }
  }

  /**
   * Refresh all dashboard telemetry data in parallel.
   */
  async refreshAll() {
    try {
      // 1. Fetch crawler status
      const statusData = await this.api.getStatus();
      this.state.status = statusData;

      // 2. Fetch stats and workers if session is active or recently ran
      const hasActiveSession = statusData.state !== 'IDLE';

      let statsData = null;
      let workersData = null;

      if (hasActiveSession) {
        const results = await Promise.allSettled([
          this.api.getStats(),
          this.api.getWorkers(),
          this.refreshURLs()
        ]);

        if (results[0].status === 'fulfilled' && results[0].value) {
          statsData = results[0].value;
          this.state.stats = statsData;
        }
        if (results[1].status === 'fulfilled' && results[1].value) {
          workersData = results[1].value;
          this.state.workers = workersData.workers || [];
        }
      } else {
        // Reset when IDLE
        this.state.stats = { total_discovered: 0, pending: 0, in_progress: 0, completed: 0, failed: 0 };
        this.state.workers = [];
        this.state.urlList = { total: 0, filtered: 0, limit: 20, offset: 0, items: [] };
      }

      this.state.lastUpdated = new Date();
      this.render();

    } catch (err) {
      console.warn('[Dashboard] Telemetry fetch error:', err.message);
      this._updateConnectionUI(false, err.message);
      this._syncControlButtons(); // Ensure controls reflect disabled state on network failure
    }
  }

  /**
   * Fetch current page of URLs based on active query filters.
   */
  async refreshURLs() {
    try {
      const data = await this.api.getURLs(this.state.urlQuery);
      this.state.urlList = data || { total: 0, filtered: 0, limit: 20, offset: 0, items: [] };
      this._renderURLTable();
    } catch (err) {
      console.warn('[Dashboard] URL query error:', err.message);
    }
  }

  // ==========================================
  // Renderers & UI Synchronizers
  // ==========================================

  /**
   * Primary render method: syncs all DOM widgets with current state.
   */
  render() {
    this._renderHeader();
    this._syncControlButtons();
    this._renderStatCards();
    this._renderProgressAndDetails();
    this._renderChart();
    this._renderWorkers();
    this._renderURLTable();
  }

  /**
   * Synchronize Header state badge, session ID, and uptime.
   * @private
   */
  _renderHeader() {
    const { status, lastUpdated } = this.state;

    if (this.dom.crawlerStateBadge) {
      this.dom.crawlerStateBadge.innerHTML = Components.renderStateBadge(status.state, status.is_paused);
    }

    if (this.dom.headerSessionID) {
      const sid = status.session_id;
      this.dom.headerSessionID.textContent = sid ? Components.truncate(sid, 28) : 'No Active Session';
      this.dom.headerSessionID.title = sid || '';
    }

    if (this.dom.headerSeedURL) {
      this.dom.headerSeedURL.textContent = status.start_url || '--';
      this.dom.headerSeedURL.title = status.start_url || '';
    }

    if (this.dom.headerUptime) {
      this.dom.headerUptime.textContent = Components.formatDuration(status.cumulative_elapsed_ms);
    }

    if (this.dom.headerLastUpdated) {
      this.dom.headerLastUpdated.textContent = lastUpdated ? Components.formatTime(lastUpdated) : '--:--:--';
    }
  }

  /**
   * Enable/Disable control buttons strictly according to state matrix.
   * @private
   */
  _syncControlButtons() {
    const state = (this.state.status.state || 'IDLE').toUpperCase();
    const isPaused = this.state.status.is_paused;
    const isOnline = this.api.isOnline !== false;

    const setBtn = (btn, enabled, title = '') => {
      if (!btn) return;
      btn.disabled = !enabled || !isOnline;
      if (title) btn.title = title;
    };

    switch (state) {
      case 'RUNNING':
        setBtn(this.dom.btnStart, false, 'A crawl session is currently active');
        setBtn(this.dom.btnPause, true, 'Pause job dispatch');
        setBtn(this.dom.btnResume, false, 'Session is already running');
        setBtn(this.dom.btnStop, true, 'Stop crawl session');
        setBtn(this.dom.btnCheckpoint, true, 'Create manual snapshot');
        break;

      case 'PAUSED':
        setBtn(this.dom.btnStart, false, 'A crawl session is currently paused');
        setBtn(this.dom.btnPause, false, 'Session is already paused');
        setBtn(this.dom.btnResume, true, 'Resume crawling');
        setBtn(this.dom.btnStop, true, 'Stop crawl session');
        setBtn(this.dom.btnCheckpoint, true, 'Create manual snapshot');
        break;

      case 'STOPPING':
        setBtn(this.dom.btnStart, false, 'Crawler is currently stopping...');
        setBtn(this.dom.btnPause, false, 'Stopping...');
        setBtn(this.dom.btnResume, false, 'Stopping...');
        setBtn(this.dom.btnStop, false, 'Stopping...');
        setBtn(this.dom.btnCheckpoint, false, 'Stopping...');
        break;

      case 'STOPPED':
      case 'COMPLETED':
      case 'IDLE':
      default:
        setBtn(this.dom.btnStart, true, 'Start a new crawl session');
        setBtn(this.dom.btnPause, false, 'No active crawl to pause');
        setBtn(this.dom.btnResume, false, 'No paused crawl to resume');
        setBtn(this.dom.btnStop, false, 'No active crawl to stop');
        setBtn(this.dom.btnCheckpoint, false, 'No active crawl to checkpoint');
        break;
    }
  }

  /**
   * Update the 5 primary Stat Cards and secondary metrics.
   * @private
   */
  _renderStatCards() {
    const { stats, status, workers } = this.state;

    if (this.dom.statDiscovered) this.dom.statDiscovered.textContent = Components.formatNumber(stats.total_discovered);
    if (this.dom.statCompleted) this.dom.statCompleted.textContent = Components.formatNumber(stats.completed);
    if (this.dom.statPending) this.dom.statPending.textContent = Components.formatNumber(stats.pending);
    if (this.dom.statFailed) this.dom.statFailed.textContent = Components.formatNumber(stats.failed);
    if (this.dom.statCrawling) this.dom.statCrawling.textContent = Components.formatNumber(stats.in_progress);

    // Calculate Completed Percentage
    if (this.dom.statCompletedPct) {
      if (stats.total_discovered > 0) {
        const pct = ((stats.completed / stats.total_discovered) * 100).toFixed(0);
        this.dom.statCompletedPct.textContent = `${pct}% of total`;
      } else {
        this.dom.statCompletedPct.textContent = '0%';
      }
    }

    // Active downloading workers count
    const activeWorkers = workers.filter(w => w.state === 'DOWNLOADING').length;
    if (this.dom.statActiveWorkers) {
      this.dom.statActiveWorkers.textContent = `${activeWorkers} / ${status.worker_count || workers.length || 0}`;
    }

    // Crawl speed (pages/sec)
    if (this.dom.statSpeed) {
      const elapsedSec = (status.cumulative_elapsed_ms || 0) / 1000;
      if (elapsedSec > 0 && stats.completed > 0) {
        const speed = (stats.completed / elapsedSec).toFixed(1);
        this.dom.statSpeed.textContent = `${speed} /s`;
      } else {
        this.dom.statSpeed.textContent = '0.0 /s';
      }
    }
  }

  /**
   * Render Crawl Progress Bar and Session Details Card.
   * @private
   */
  _renderProgressAndDetails() {
    const { stats, status } = this.state;

    // Progress bar towards max_pages or discovered
    let target = status.max_pages || stats.total_discovered || 0;
    let current = stats.completed || 0;
    let pct = 0;

    if (target > 0) {
      pct = Math.min(100, Math.round((current / target) * 100));
    }

    if (this.dom.progressBar) {
      this.dom.progressBar.style.width = `${pct}%`;
    }
    if (this.dom.progressText) {
      this.dom.progressText.textContent = target > 0 ? `${current} of ${target} pages (${pct}%)` : 'Ready to crawl';
    }

    // Session Details
    if (this.dom.sessionStartURL) this.dom.sessionStartURL.textContent = status.start_url || '--';
    if (this.dom.sessionMaxPages) this.dom.sessionMaxPages.textContent = status.max_pages ? String(status.max_pages) : '--';
    if (this.dom.sessionMaxDepth) this.dom.sessionMaxDepth.textContent = status.max_depth !== undefined ? String(status.max_depth) : '--';
    if (this.dom.sessionWorkers) this.dom.sessionWorkers.textContent = status.worker_count ? `${status.worker_count} Goroutines` : '--';
    if (this.dom.sessionDomainPolicy) this.dom.sessionDomainPolicy.textContent = status.session_id ? 'Same Domain Only' : '--';
  }

  /**
   * Update Chart.js doughnut datasets.
   * @private
   */
  _renderChart() {
    if (!this.state.chartInstance) return;

    const { stats } = this.state;
    const hasData = stats.total_discovered > 0;

    if (hasData) {
      this.state.chartInstance.data.datasets[0].data = [
        stats.completed,
        stats.in_progress,
        stats.pending,
        stats.failed
      ];
      this.state.chartInstance.data.datasets[0].backgroundColor = [
        '#10b981',
        '#6366f1',
        '#f59e0b',
        '#f43f5e'
      ];
    } else {
      // Empty placeholder doughnut
      this.state.chartInstance.data.datasets[0].data = [1];
      this.state.chartInstance.data.datasets[0].backgroundColor = ['#242b3b'];
    }

    this.state.chartInstance.update('none'); // Update without jarring re-animations
  }

  /**
   * Render Worker Cards Grid.
   * @private
   */
  _renderWorkers() {
    if (!this.dom.workerGrid) return;

    const workers = this.state.workers || [];

    if (this.dom.workerCountBadge) {
      this.dom.workerCountBadge.textContent = `${workers.length} Workers`;
    }

    if (workers.length === 0) {
      this.dom.workerGrid.innerHTML = `
        <div class="worker-grid-empty">
          <div class="empty-icon">&#9881;</div>
          <div class="empty-text">No active worker pool. Start a crawl session to spawn worker goroutines.</div>
        </div>
      `;
      return;
    }

    let html = '';
    for (const w of workers) {
      html += Components.renderWorkerCard(w);
    }
    this.dom.workerGrid.innerHTML = html;
  }

  /**
   * Render URL Explorer Table and Pagination Controls.
   * @private
   */
  _renderURLTable() {
    if (!this.dom.urlTableBody) return;

    const { items, total, filtered, limit, offset } = this.state.urlList;

    if (!items || items.length === 0) {
      this.dom.urlTableBody.innerHTML = `
        <tr>
          <td colspan="8" class="table-empty-cell">
            ${Components.renderEmptyState('No Discovered URLs', 'No URLs match the current filter or no crawl session is active.')}
          </td>
        </tr>
      `;
      if (this.dom.urlPaginationInfo) this.dom.urlPaginationInfo.textContent = 'Showing 0 of 0';
      if (this.dom.btnPagePrev) this.dom.btnPagePrev.disabled = true;
      if (this.dom.btnPageNext) this.dom.btnPageNext.disabled = true;
      return;
    }

    let html = '';
    for (const item of items) {
      html += Components.renderURLTableRow(item);
    }
    this.dom.urlTableBody.innerHTML = html;

    // Update Pagination Summary
    const startIdx = offset + 1;
    const endIdx = Math.min(offset + items.length, filtered);
    if (this.dom.urlPaginationInfo) {
      this.dom.urlPaginationInfo.textContent = `Showing ${startIdx} - ${endIdx} of ${filtered} filtered (Total: ${total})`;
    }

    // Enable/Disable Pagination Buttons
    if (this.dom.btnPagePrev) {
      this.dom.btnPagePrev.disabled = (offset <= 0);
    }
    if (this.dom.btnPageNext) {
      this.dom.btnPageNext.disabled = (offset + limit >= filtered);
    }
  }

  // ==========================================
  // URL Explorer Filter & Pagination Handlers
  // ==========================================

  setFilterStatus(status) {
    this.state.urlQuery.status = status;
    this.state.urlQuery.offset = 0;
    this.refreshURLs();
  }

  setFilterDepth(depth) {
    this.state.urlQuery.depth = depth !== '' ? Number(depth) : '';
    this.state.urlQuery.offset = 0;
    this.refreshURLs();
  }

  setFilterSort(sort) {
    this.state.urlQuery.sort = sort;
    this.state.urlQuery.offset = 0;
    this.refreshURLs();
  }

  setPageSize(size) {
    this.state.urlQuery.limit = Number(size);
    this.state.urlQuery.offset = 0;
    this.refreshURLs();
  }

  prevPage() {
    const { limit, offset } = this.state.urlQuery;
    if (offset > 0) {
      this.state.urlQuery.offset = Math.max(0, offset - limit);
      this.refreshURLs();
    }
  }

  nextPage() {
    const { limit, offset } = this.state.urlQuery;
    const { filtered } = this.state.urlList;
    if (offset + limit < filtered) {
      this.state.urlQuery.offset = offset + limit;
      this.refreshURLs();
    }
  }
}


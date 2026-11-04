/**
 * CrawlSync API Client
 * Centralized, robust HTTP client for CrawlSync REST API.
 * 
 * Supports:
 * - Configurable Base URL (persisted in localStorage)
 * - Automatic timeout handling via AbortController
 * - Standardized JSON envelope parsing ({ success, data } / { success, error })
 * - Network failure and offline detection
 * - Connection state event dispatching
 */

class CrawlSyncAPI {
  /**
   * @param {Object} options
   * @param {string} [options.baseURL] Default API root (defaults to current origin or http://127.0.0.1:8080)
   * @param {number} [options.timeoutMs=8000] Request timeout in milliseconds
   */
  constructor(options = {}) {
    const savedBase = localStorage.getItem('crawlsync_api_base');
    // If running in browser served from server, default to relative root, else 127.0.0.1:8080
    const defaultBase = (window.location.protocol.startsWith('http') && window.location.port === '8080')
      ? ''
      : 'http://127.0.0.1:8080';

    this.baseURL = savedBase || options.baseURL || defaultBase;
    this.timeoutMs = options.timeoutMs || 8000;
    this.isOnline = null; // null = unprobed, true = online, false = offline
    this.connectionListeners = new Set();
  }

  /**
   * Update and persist the base URL.
   * @param {string} newBase
   */
  setBaseURL(newBase) {
    let clean = (newBase || '').trim();
    if (clean.endsWith('/')) {
      clean = clean.slice(0, -1);
    }
    this.baseURL = clean;
    localStorage.setItem('crawlsync_api_base', clean);
  }

  /**
   * Register a connection status listener.
   * @param {Function} callback (isOnline: boolean, error?: string) => void
   */
  onConnectionChange(callback) {
    this.connectionListeners.add(callback);
    return () => this.connectionListeners.delete(callback);
  }

  /**
   * Notify registered listeners of connectivity changes.
   * @private
   */
  _notifyConnection(status, error = null) {
    if (this.isOnline !== status) {
      this.isOnline = status;
      for (const listener of this.connectionListeners) {
        try {
          listener(status, error);
        } catch (e) {
          console.error('[API] Error in connection listener:', e);
        }
      }
    }
  }

  /**
   * Core fetch wrapper handling timeouts, headers, and standard error envelopes.
   * @private
   */
  async _request(endpoint, options = {}) {
    const controller = new AbortController();
    const timeoutId = setTimeout(() => controller.abort(), this.timeoutMs);

    const url = `${this.baseURL}${endpoint}`;
    const headers = {
      'Accept': 'application/json',
      ...(options.headers || {})
    };

    if (options.body && typeof options.body === 'object') {
      headers['Content-Type'] = 'application/json';
      options.body = JSON.stringify(options.body);
    }

    try {
      const response = await fetch(url, {
        ...options,
        headers,
        signal: controller.signal
      });

      clearTimeout(timeoutId);

      // Successfully reached server
      this._notifyConnection(true);

      const contentType = response.headers.get('content-type') || '';
      let json = null;
      if (contentType.includes('application/json')) {
        json = await response.json();
      }

      if (!response.ok) {
        const errorDetail = (json && json.error) ? json.error : {};
        const err = new Error(errorDetail.message || `HTTP ${response.status}: ${response.statusText}`);
        err.status = response.status;
        err.code = errorDetail.code || 'HTTP_ERROR';
        err.details = errorDetail;
        throw err;
      }

      if (json && json.success !== undefined) {
        return json.data;
      }
      return json;

    } catch (err) {
      clearTimeout(timeoutId);

      if (err.name === 'AbortError') {
        const timeoutErr = new Error(`Request timed out after ${this.timeoutMs}ms`);
        timeoutErr.code = 'TIMEOUT';
        timeoutErr.status = 408;
        this._notifyConnection(false, 'Request timeout');
        throw timeoutErr;
      }

      // Check for network errors (Failed to fetch / Connection refused)
      if (err instanceof TypeError && err.message.includes('fetch')) {
        const netErr = new Error(`Cannot connect to CrawlSync server at ${this.baseURL}`);
        netErr.code = 'NETWORK_ERROR';
        netErr.status = 0;
        this._notifyConnection(false, netErr.message);
        throw netErr;
      }

      throw err;
    }
  }

  // ==========================================
  // API Method Implementations
  // ==========================================

  /**
   * Test connection to the backend server.
   * @returns {Promise<boolean>}
   */
  async testConnection() {
    try {
      await this.getStatus();
      return true;
    } catch (err) {
      return false;
    }
  }

  /**
   * GET /api/crawl/status
   * Current crawler lifecycle and session status.
   * @returns {Promise<CrawlStatus>}
   */
  async getStatus() {
    return this._request('/api/crawl/status', { method: 'GET' });
  }

  /**
   * GET /api/crawl/stats
   * Aggregated URL counts (total, pending, in_progress, completed, failed).
   * Note: May return 404 NO_ACTIVE_CRAWL when idle.
   * @returns {Promise<CrawlStats|null>}
   */
  async getStats() {
    try {
      return await this._request('/api/crawl/stats', { method: 'GET' });
    } catch (err) {
      if (err.status === 404 && err.code === 'NO_ACTIVE_CRAWL') {
        return null; // Clean handling for idle state
      }
      throw err;
    }
  }

  /**
   * GET /api/workers
   * Real-time worker pool state and current assignments.
   * Note: May return 404 NO_ACTIVE_CRAWL when idle.
   * @returns {Promise<WorkerList|null>}
   */
  async getWorkers() {
    try {
      return await this._request('/api/workers', { method: 'GET' });
    } catch (err) {
      if (err.status === 404 && err.code === 'NO_ACTIVE_CRAWL') {
        return null;
      }
      throw err;
    }
  }

  /**
   * GET /api/urls
   * Paginated, sorted, and filtered list of discovered URLs.
   * @param {Object} [params]
   * @param {number} [params.limit=20]
   * @param {number} [params.offset=0]
   * @param {string} [params.status] 'pending' | 'in_progress' | 'completed' | 'failed'
   * @param {number} [params.depth]
   * @param {string} [params.sort] 'seq_asc' | 'seq_desc' | 'depth_asc' | 'depth_desc' | 'time_desc'
   * @returns {Promise<URLListResponse>}
   */
  async getURLs(params = {}) {
    const query = new URLSearchParams();
    if (params.limit !== undefined) query.set('limit', String(params.limit));
    if (params.offset !== undefined) query.set('offset', String(params.offset));
    if (params.status) query.set('status', params.status);
    if (params.depth !== undefined && params.depth !== '') query.set('depth', String(params.depth));
    if (params.sort) query.set('sort', params.sort);

    const qs = query.toString();
    const endpoint = `/api/urls${qs ? `?${qs}` : ''}`;

    try {
      return await this._request(endpoint, { method: 'GET' });
    } catch (err) {
      if (err.status === 404 && err.code === 'NO_ACTIVE_CRAWL') {
        return { total: 0, filtered: 0, limit: params.limit || 20, offset: params.offset || 0, items: [] };
      }
      throw err;
    }
  }

  /**
   * GET /api/urls/{id}
   * Metadata for a single URL by sequence ID.
   * @param {number|string} id
   * @returns {Promise<URLItem>}
   */
  async getURLDetail(id) {
    return this._request(`/api/urls/${encodeURIComponent(id)}`, { method: 'GET' });
  }

  /**
   * POST /api/crawl/start
   * Start a new crawl session or resume from checkpoint.
   * @param {Object} config
   * @param {string} [config.url] Target URL (required unless resume is true)
   * @param {number} [config.workers] 1-20
   * @param {number} [config.max_pages] >= 1
   * @param {number} [config.max_depth] >= 0
   * @param {number} [config.timeout_seconds] >= 1
   * @param {boolean} [config.same_domain]
   * @param {boolean} [config.resume]
   * @param {boolean} [config.retry_failed]
   * @returns {Promise<CrawlSessionResponse>}
   */
  async startCrawl(config) {
    return this._request('/api/crawl/start', {
      method: 'POST',
      body: config
    });
  }

  /**
   * POST /api/crawl/pause
   * Freeze URL dispatching.
   * @returns {Promise<ControlActionResponse>}
   */
  async pauseCrawl() {
    return this._request('/api/crawl/pause', {
      method: 'POST',
      body: {}
    });
  }

  /**
   * POST /api/crawl/resume
   * Resume URL dispatching.
   * @returns {Promise<ControlActionResponse>}
   */
  async resumeCrawl() {
    return this._request('/api/crawl/resume', {
      method: 'POST',
      body: {}
    });
  }

  /**
   * POST /api/crawl/stop
   * Gracefully stop the active crawl session and commit final checkpoint.
   * @returns {Promise<ControlActionResponse>}
   */
  async stopCrawl() {
    return this._request('/api/crawl/stop', {
      method: 'POST',
      body: {}
    });
  }

  /**
   * POST /api/crawl/checkpoint
   * Trigger an immediate manual state checkpoint.
   * @returns {Promise<CheckpointResponse>}
   */
  async createCheckpoint() {
    return this._request('/api/crawl/checkpoint', {
      method: 'POST',
      body: {}
    });
  }
}

// Global default instance
const api = new CrawlSyncAPI();


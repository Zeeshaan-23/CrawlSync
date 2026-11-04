/**
 * CrawlSync UI Components & Formatting Helpers
 * Modular HTML component generators and data formatters.
 */

const Components = {
  /**
   * Escape raw strings to prevent HTML injection (XSS defense).
   * @param {string} str
   * @returns {string}
   */
  escapeHTML(str) {
    if (str === null || str === undefined) return '';
    return String(str)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#039;');
  },

  /**
   * Format numbers with commas (e.g. 14,250).
   * @param {number} num
   * @returns {string}
   */
  formatNumber(num) {
    if (num === null || num === undefined || isNaN(num)) return '0';
    return Number(num).toLocaleString('en-US');
  },

  /**
   * Format bytes into B, KB, MB, GB.
   * @param {number} bytes
   * @returns {string}
   */
  formatBytes(bytes) {
    if (!bytes || bytes <= 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB'];
    const i = Math.floor(Math.log(bytes) / Math.log(1024));
    const val = bytes / Math.pow(1024, i);
    return `${val.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
  },

  /**
   * Format milliseconds into human readable duration (e.g. 35 ms, 2.4s, 1m 12s).
   * @param {number} ms
   * @returns {string}
   */
  formatDuration(ms) {
    if (ms === null || ms === undefined || ms < 0) return '0 ms';
    if (ms < 1000) return `${Math.round(ms)} ms`;
    const sec = ms / 1000;
    if (sec < 60) return `${sec.toFixed(1)}s`;
    const min = Math.floor(sec / 60);
    const remSec = Math.floor(sec % 60);
    return `${min}m ${remSec}s`;
  },

  /**
   * Format ISO timestamp to local readable time.
   * @param {string|Date} dateVal
   * @returns {string}
   */
  formatTime(dateVal) {
    if (!dateVal) return '--:--:--';
    const d = new Date(dateVal);
    if (isNaN(d.getTime())) return '--:--:--';
    return d.toLocaleTimeString('en-US', { hour12: false });
  },

  /**
   * Truncate text with ellipsis.
   * @param {string} text
   * @param {number} maxLen
   * @returns {string}
   */
  truncate(text, maxLen = 45) {
    if (!text) return '';
    return text.length > maxLen ? text.substring(0, maxLen - 3) + '...' : text;
  },

  /**
   * Render Crawler Lifecycle State Badge.
   * @param {string} state 'IDLE' | 'RUNNING' | 'PAUSED' | 'STOPPING' | 'STOPPED' | 'COMPLETED'
   * @param {boolean} [isPaused=false]
   * @returns {string} HTML string
   */
  renderStateBadge(state = 'IDLE', isPaused = false) {
    const norm = (state || 'IDLE').toUpperCase();
    let badgeClass = 'badge-idle';
    let label = norm;

    switch (norm) {
      case 'RUNNING':
        badgeClass = isPaused ? 'badge-paused' : 'badge-running';
        label = isPaused ? 'PAUSED' : 'RUNNING';
        break;
      case 'PAUSED':
        badgeClass = 'badge-paused';
        label = 'PAUSED';
        break;
      case 'STOPPING':
        badgeClass = 'badge-stopping';
        label = 'STOPPING';
        break;
      case 'STOPPED':
        badgeClass = 'badge-stopped';
        label = 'STOPPED';
        break;
      case 'COMPLETED':
        badgeClass = 'badge-completed';
        label = 'COMPLETED';
        break;
      case 'IDLE':
      default:
        badgeClass = 'badge-idle';
        label = 'IDLE';
        break;
    }

    return `
      <span class="state-badge ${badgeClass}" title="Crawler Lifecycle State: ${label}">
        <span class="badge-dot"></span>
        <span class="badge-text">${label}</span>
      </span>
    `;
  },

  /**
   * Render URL Status Badge.
   * @param {string} status 'pending' | 'in_progress' | 'completed' | 'failed'
   * @returns {string} HTML string
   */
  renderURLStatusBadge(status) {
    const s = (status || '').toLowerCase();
    let cls = 'badge-pending';
    let text = s;

    switch (s) {
      case 'completed':
        cls = 'badge-url-completed';
        text = 'Completed';
        break;
      case 'in_progress':
        cls = 'badge-url-in-progress';
        text = 'Crawling';
        break;
      case 'pending':
        cls = 'badge-url-pending';
        text = 'Pending';
        break;
      case 'failed':
        cls = 'badge-url-failed';
        text = 'Failed';
        break;
      default:
        cls = 'badge-idle';
        text = s || 'Unknown';
    }

    return `<span class="url-status-badge ${cls}">${text}</span>`;
  },

  /**
   * Render HTTP Status Code Badge.
   * @param {number} code
   * @returns {string} HTML string
   */
  renderHTTPCodeBadge(code) {
    if (!code || code === 0) {
      return '<span class="http-code code-na">--</span>';
    }
    let cls = 'code-2xx';
    if (code >= 300 && code < 400) cls = 'code-3xx';
    else if (code >= 400 && code < 500) cls = 'code-4xx';
    else if (code >= 500) cls = 'code-5xx';

    return `<span class="http-code ${cls}">${code}</span>`;
  },

  /**
   * Render a single Worker Pool Card.
   * @param {Object} worker { id, state, current_url, updated_at }
   * @returns {string} HTML string
   */
  renderWorkerCard(worker) {
    const isDownloading = (worker.state === 'DOWNLOADING');
    const stateBadgeClass = isDownloading ? 'worker-state-active' : 'worker-state-idle';
    const stateText = isDownloading ? 'DOWNLOADING' : 'IDLE';
    const currentURL = worker.current_url || '';
    const updatedTime = this.formatTime(worker.updated_at);

    return `
      <div class="worker-card ${isDownloading ? 'worker-active' : 'worker-idle'}" id="worker-card-${worker.id}">
        <div class="worker-card-header">
          <div class="worker-id">
            <span class="worker-icon">&#9881;</span>
            <span>Worker #${worker.id}</span>
          </div>
          <span class="worker-status-pill ${stateBadgeClass}">
            <span class="pulse-dot"></span>
            ${stateText}
          </span>
        </div>
        <div class="worker-card-body">
          <div class="worker-url-label">Current Task:</div>
          <div class="worker-url ${!currentURL ? 'worker-url-empty' : ''}" title="${this.escapeHTML(currentURL)}">
            ${currentURL ? this.escapeHTML(this.truncate(currentURL, 50)) : 'Idle & waiting for job...'}
          </div>
        </div>
        <div class="worker-card-footer">
          <span class="worker-updated">Updated: ${updatedTime}</span>
        </div>
      </div>
    `;
  },

  /**
   * Render a URL Explorer Table Row.
   * @param {Object} item URLItemResponseDTO
   * @returns {string} HTML string
   */
  renderURLTableRow(item) {
    const rawURL = item.url || item.normalized || '';
    const statusBadge = this.renderURLStatusBadge(item.status);
    const codeBadge = this.renderHTTPCodeBadge(item.status_code);
    const sizeStr = item.size ? this.formatBytes(item.size) : '--';
    const durationStr = item.duration_ms ? `${item.duration_ms} ms` : '--';
    const pathStr = item.saved_path ? this.escapeHTML(item.saved_path) : '--';

    return `
      <tr class="url-table-row status-${item.status}">
        <td class="col-seq">#${item.sequence}</td>
        <td class="col-url" title="${this.escapeHTML(rawURL)}">
          <a href="${this.escapeHTML(rawURL)}" target="_blank" rel="noopener noreferrer" class="url-link">
            ${this.escapeHTML(this.truncate(rawURL, 65))}
          </a>
          ${item.error_message ? `<div class="url-error" title="${this.escapeHTML(item.error_message)}">${this.escapeHTML(this.truncate(item.error_message, 40))}</div>` : ''}
        </td>
        <td class="col-status">${statusBadge}</td>
        <td class="col-depth"><span class="depth-badge">d:${item.depth}</span></td>
        <td class="col-code">${codeBadge}</td>
        <td class="col-size">${sizeStr}</td>
        <td class="col-duration">${durationStr}</td>
        <td class="col-path" title="${pathStr}">${this.truncate(pathStr, 25)}</td>
      </tr>
    `;
  },

  /**
   * Render Empty State block.
   * @param {string} title
   * @param {string} message
   * @param {string} [actionBtn]
   * @returns {string} HTML string
   */
  renderEmptyState(title, message, actionBtn = '') {
    return `
      <div class="empty-state">
        <div class="empty-state-icon">&#128269;</div>
        <h4 class="empty-state-title">${this.escapeHTML(title)}</h4>
        <p class="empty-state-desc">${this.escapeHTML(message)}</p>
        ${actionBtn ? `<div class="empty-state-action">${actionBtn}</div>` : ''}
      </div>
    `;
  },

  /**
   * Render a Floating Toast Notification.
   * @param {string} message
   * @param {'info'|'success'|'warning'|'error'} [type='info']
   * @param {number} [durationMs=4000]
   */
  showToast(message, type = 'info', durationMs = 4000) {
    let container = document.getElementById('toast-container');
    if (!container) {
      container = document.createElement('div');
      container.id = 'toast-container';
      container.className = 'toast-container';
      document.body.appendChild(container);
    }

    const toast = document.createElement('div');
    toast.className = `toast toast-${type} toast-enter`;

    const iconMap = {
      success: '&#10003;',
      error: '&#10007;',
      warning: '&#9888;',
      info: '&#8505;'
    };

    toast.innerHTML = `
      <span class="toast-icon">${iconMap[type] || '&#8505;'}</span>
      <span class="toast-msg">${this.escapeHTML(message)}</span>
      <button class="toast-close" aria-label="Close">&times;</button>
    `;

    const closeBtn = toast.querySelector('.toast-close');
    const removeToast = () => {
      toast.classList.remove('toast-enter');
      toast.classList.add('toast-exit');
      setTimeout(() => toast.remove(), 250);
    };

    closeBtn.addEventListener('click', removeToast);
    container.appendChild(toast);

    if (durationMs > 0) {
      setTimeout(removeToast, durationMs);
    }
  }
};


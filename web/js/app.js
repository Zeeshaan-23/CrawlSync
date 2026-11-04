/**
 * CrawlSync Application Bootstrap
 * Event bindings, modal management, control actions, and view routing.
 */

document.addEventListener('DOMContentLoaded', () => {
  // 1. Initialize Dashboard Controller
  const dashboard = new DashboardController(api);
  dashboard.init();

  // 2. Cache Modal & Form Elements
  const modalStart = document.getElementById('modal-start-crawl');
  const formStart = document.getElementById('form-start-crawl');
  const btnCloseStartModal = document.getElementById('btn-close-start-modal');
  const btnCancelStartModal = document.getElementById('btn-cancel-start-modal');

  const modalSettings = document.getElementById('modal-settings');
  const formSettings = document.getElementById('form-settings');
  const btnOpenSettings = document.getElementById('btn-open-settings');
  const btnCloseSettingsModal = document.getElementById('btn-close-settings-modal');
  const btnCancelSettingsModal = document.getElementById('btn-cancel-settings-modal');
  const inputApiBase = document.getElementById('setting-api-base');
  const btnTestConn = document.getElementById('btn-test-connection');
  const testConnResult = document.getElementById('test-connection-result');

  // Populate settings input
  if (inputApiBase) {
    inputApiBase.value = api.baseURL;
  }

  // ==========================================
  // Navigation & View Switching
  // ==========================================
  const navLinks = document.querySelectorAll('.sidebar-nav-item');
  navLinks.forEach(link => {
    link.addEventListener('click', (e) => {
      e.preventDefault();
      const targetId = link.getAttribute('data-target');
      if (!targetId) return;

      navLinks.forEach(l => l.classList.remove('active'));
      link.classList.add('active');

      const targetEl = document.getElementById(targetId);
      if (targetEl) {
        targetEl.scrollIntoView({ behavior: 'smooth', block: 'start' });
      }
    });
  });

  // ==========================================
  // Crawler Control Actions
  // ==========================================

  // Start Button -> Open Configuration Modal
  if (dashboard.dom.btnStart) {
    dashboard.dom.btnStart.addEventListener('click', () => {
      if (modalStart) {
        modalStart.classList.add('modal-visible');
        const urlInput = document.getElementById('input-crawl-url');
        if (urlInput) urlInput.focus();
      }
    });
  }

  // Pause Button
  if (dashboard.dom.btnPause) {
    dashboard.dom.btnPause.addEventListener('click', async () => {
      try {
        dashboard.dom.btnPause.disabled = true;
        const res = await api.pauseCrawl();
        Components.showToast(res.message || 'Crawl session paused', 'warning');
        await dashboard.refreshAll();
      } catch (err) {
        Components.showToast(err.message || 'Failed to pause crawl', 'error');
      } finally {
        dashboard._syncControlButtons();
      }
    });
  }

  // Resume Button
  if (dashboard.dom.btnResume) {
    dashboard.dom.btnResume.addEventListener('click', async () => {
      try {
        dashboard.dom.btnResume.disabled = true;
        const res = await api.resumeCrawl();
        Components.showToast(res.message || 'Crawl session resumed', 'success');
        await dashboard.refreshAll();
      } catch (err) {
        Components.showToast(err.message || 'Failed to resume crawl', 'error');
      } finally {
        dashboard._syncControlButtons();
      }
    });
  }

  // Stop Button
  if (dashboard.dom.btnStop) {
    dashboard.dom.btnStop.addEventListener('click', async () => {
      const confirmed = window.confirm('Are you sure you want to stop the current crawl session? Active workers will drain and a final shutdown checkpoint will be committed.');
      if (!confirmed) return;

      try {
        dashboard.dom.btnStop.disabled = true;
        const res = await api.stopCrawl();
        Components.showToast(res.message || 'Crawl session stopped', 'info');
        await dashboard.refreshAll();
      } catch (err) {
        Components.showToast(err.message || 'Failed to stop crawl', 'error');
      } finally {
        dashboard._syncControlButtons();
      }
    });
  }

  // Checkpoint Button
  if (dashboard.dom.btnCheckpoint) {
    dashboard.dom.btnCheckpoint.addEventListener('click', async () => {
      try {
        dashboard.dom.btnCheckpoint.disabled = true;
        const res = await api.createCheckpoint();
        Components.showToast(`Manual checkpoint #${res.sequence} committed to disk successfully!`, 'success');
        await dashboard.refreshAll();
      } catch (err) {
        Components.showToast(err.message || 'Failed to create checkpoint', 'error');
      } finally {
        dashboard._syncControlButtons();
      }
    });
  }

  // Manual Refresh Button
  if (dashboard.dom.btnRefresh) {
    dashboard.dom.btnRefresh.addEventListener('click', async () => {
      dashboard.dom.btnRefresh.classList.add('rotating');
      await dashboard.refreshAll();
      setTimeout(() => dashboard.dom.btnRefresh.classList.remove('rotating'), 600);
    });
  }

  // ==========================================
  // Start Crawl Modal Form Handling
  // ==========================================
  const closeStartModal = () => {
    if (modalStart) modalStart.classList.remove('modal-visible');
  };

  if (btnCloseStartModal) btnCloseStartModal.addEventListener('click', closeStartModal);
  if (btnCancelStartModal) btnCancelStartModal.addEventListener('click', closeStartModal);

  // Resume checkbox toggles URL requirement
  const checkResume = document.getElementById('input-crawl-resume');
  const inputUrl = document.getElementById('input-crawl-url');
  if (checkResume && inputUrl) {
    checkResume.addEventListener('change', () => {
      if (checkResume.checked) {
        inputUrl.placeholder = 'Optional when resuming from checkpoint';
        inputUrl.required = false;
      } else {
        inputUrl.placeholder = 'https://example.com';
        inputUrl.required = true;
      }
    });
  }

  if (formStart) {
    formStart.addEventListener('submit', async (e) => {
      e.preventDefault();

      const urlVal = inputUrl ? inputUrl.value.trim() : '';
      const workersVal = parseInt(document.getElementById('input-crawl-workers')?.value || '5', 10);
      const maxPagesVal = parseInt(document.getElementById('input-crawl-max-pages')?.value || '10', 10);
      const maxDepthVal = parseInt(document.getElementById('input-crawl-max-depth')?.value || '2', 10);
      const timeoutVal = parseInt(document.getElementById('input-crawl-timeout')?.value || '10', 10);
      const sameDomainVal = document.getElementById('input-crawl-same-domain')?.checked ?? true;
      const isResume = checkResume ? checkResume.checked : false;
      const retryFailedVal = document.getElementById('input-crawl-retry-failed')?.checked ?? false;

      const payload = {
        workers: workersVal,
        max_pages: maxPagesVal,
        max_depth: maxDepthVal,
        timeout_seconds: timeoutVal,
        same_domain: sameDomainVal,
        resume: isResume,
        retry_failed: retryFailedVal
      };

      if (urlVal) {
        payload.url = urlVal;
      }

      const submitBtn = document.getElementById('btn-submit-start-crawl');
      if (submitBtn) {
        submitBtn.disabled = true;
        submitBtn.textContent = 'Initiating session...';
      }

      try {
        const res = await api.startCrawl(payload);
        closeStartModal();
        Components.showToast(`Crawl session ${res.session_id} accepted!`, 'success');
        await dashboard.refreshAll();
      } catch (err) {
        Components.showToast(err.message || 'Failed to start crawl session', 'error');
      } finally {
        if (submitBtn) {
          submitBtn.disabled = false;
          submitBtn.textContent = 'Start Crawl Session';
        }
      }
    });
  }

  // ==========================================
  // Settings Modal & Drawer
  // ==========================================
  const closeSettingsModal = () => {
    if (modalSettings) modalSettings.classList.remove('modal-visible');
    if (testConnResult) testConnResult.textContent = '';
  };

  if (btnOpenSettings) {
    btnOpenSettings.addEventListener('click', (e) => {
      e.preventDefault();
      if (modalSettings) {
        if (inputApiBase) inputApiBase.value = api.baseURL;
        modalSettings.classList.add('modal-visible');
      }
    });
  }

  if (btnCloseSettingsModal) btnCloseSettingsModal.addEventListener('click', closeSettingsModal);
  if (btnCancelSettingsModal) btnCancelSettingsModal.addEventListener('click', closeSettingsModal);

  // Test Connection Button in Settings
  if (btnTestConn && inputApiBase && testConnResult) {
    btnTestConn.addEventListener('click', async () => {
      btnTestConn.disabled = true;
      testConnResult.textContent = 'Testing connection...';
      testConnResult.className = 'test-result-pending';

      const tempBase = inputApiBase.value.trim();
      const originalBase = api.baseURL;
      api.setBaseURL(tempBase);

      try {
        const ok = await api.testConnection();
        if (ok) {
          testConnResult.textContent = 'Connection successful! (200 OK)';
          testConnResult.className = 'test-result-success';
        } else {
          testConnResult.textContent = 'Server reachable, but returned non-OK status';
          testConnResult.className = 'test-result-warn';
        }
      } catch (err) {
        testConnResult.textContent = `Connection failed: ${err.message}`;
        testConnResult.className = 'test-result-error';
        api.setBaseURL(originalBase); // Revert on failure
      } finally {
        btnTestConn.disabled = false;
      }
    });
  }

  // Save Settings Form
  if (formSettings) {
    formSettings.addEventListener('submit', async (e) => {
      e.preventDefault();
      if (inputApiBase) {
        const newBase = inputApiBase.value.trim();
        api.setBaseURL(newBase);
        Components.showToast(`API Base URL updated to: ${newBase || 'default'}`, 'info');
      }
      closeSettingsModal();
      await dashboard.refreshAll();
    });
  }

  // ==========================================
  // URL Explorer Filter & Pagination Event Listeners
  // ==========================================
  if (dashboard.dom.urlFilterStatus) {
    dashboard.dom.urlFilterStatus.addEventListener('change', (e) => {
      dashboard.setFilterStatus(e.target.value);
    });
  }

  if (dashboard.dom.urlFilterDepth) {
    let depthDebounce = null;
    dashboard.dom.urlFilterDepth.addEventListener('input', (e) => {
      clearTimeout(depthDebounce);
      depthDebounce = setTimeout(() => {
        dashboard.setFilterDepth(e.target.value);
      }, 300);
    });
  }

  if (dashboard.dom.urlFilterSort) {
    dashboard.dom.urlFilterSort.addEventListener('change', (e) => {
      dashboard.setFilterSort(e.target.value);
    });
  }

  if (dashboard.dom.urlPageSize) {
    dashboard.dom.urlPageSize.addEventListener('change', (e) => {
      dashboard.setPageSize(e.target.value);
    });
  }

  if (dashboard.dom.btnPagePrev) {
    dashboard.dom.btnPagePrev.addEventListener('click', () => {
      dashboard.prevPage();
    });
  }

  if (dashboard.dom.btnPageNext) {
    dashboard.dom.btnPageNext.addEventListener('click', () => {
      dashboard.nextPage();
    });
  }

  // ==========================================
  // Keyboard Shortcut Esc to Close Modals
  // ==========================================
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') {
      closeStartModal();
      closeSettingsModal();
    }
  });

  // Start polling loop
  dashboard.startPolling();
});


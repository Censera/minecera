(() => {
  'use strict';

  const REFRESH_KEY = 'minecera.refresh-seconds';
  const LOG_CACHE_KEY = 'minecera.log-cache';
  const LOG_CACHE_LIMIT = 5000;
  const MIN = 2;
  const MAX = 60;
  const DEFAULT = 10;

  let refreshTimer = null;
  let cacheWriteTimer = null;
  let localLogs = [];
  let cachedPID = null;

  const readRefresh = () => {
    const value = Number(localStorage.getItem(REFRESH_KEY));
    return Number.isFinite(value) ? Math.min(MAX, Math.max(MIN, value)) : DEFAULT;
  };

  const saveCache = () => {
    clearTimeout(cacheWriteTimer);
    cacheWriteTimer = setTimeout(() => {
      try {
        localStorage.setItem(LOG_CACHE_KEY, JSON.stringify({
          pid: cachedPID,
          lines: localLogs.slice(-LOG_CACHE_LIMIT)
        }));
      } catch (_) {}
    }, 750);
  };

  const loadCache = () => {
    try {
      const value = JSON.parse(localStorage.getItem(LOG_CACHE_KEY) || '{}');
      if (Array.isArray(value.lines)) {
        localLogs = value.lines.filter(line => typeof line === 'string').slice(-LOG_CACHE_LIMIT);
      }
      if (Number.isInteger(value.pid)) cachedPID = value.pid;
    } catch (_) {}
  };

  const resetCache = () => {
    localLogs = [];
    try { localStorage.removeItem(LOG_CACHE_KEY); } catch (_) {}
    if (typeof renderLogs === 'function') renderLogs([]);
  };

  const appendLogs = lines => {
    if (!Array.isArray(lines) || !lines.length) return;
    const valid = lines.filter(line => typeof line === 'string');
    if (!valid.length) return;

    localLogs.push(...valid);
    if (localLogs.length > LOG_CACHE_LIMIT) {
      localLogs.splice(0, localLogs.length - LOG_CACHE_LIMIT);
    }

    if (typeof renderLogs === 'function') renderLogs(localLogs);
    saveCache();
  };

  const mergeInitialLogs = lines => {
    if (!Array.isArray(lines) || !lines.length) return;
    if (!localLogs.length) {
      localLogs = lines.slice(-LOG_CACHE_LIMIT);
      if (typeof renderLogs === 'function') renderLogs(localLogs);
      saveCache();
      return;
    }

    const overlap = Math.min(100, localLogs.length, lines.length);
    let matched = false;
    for (let i = 0; i <= lines.length - overlap && overlap > 0; i++) {
      let same = true;
      for (let j = 0; j < overlap; j++) {
        if (lines[i + j] !== localLogs[localLogs.length - overlap + j]) {
          same = false;
          break;
        }
      }
      if (same) {
        localLogs.push(...lines.slice(i + overlap));
        matched = true;
        break;
      }
    }

    if (!matched) localLogs = lines.slice(-LOG_CACHE_LIMIT);
    localLogs = localLogs.slice(-LOG_CACHE_LIMIT);
    if (typeof renderLogs === 'function') renderLogs(localLogs);
    saveCache();
  };

  const loadInitialLogs = async () => {
    if (document.hidden) return;
    try {
      const response = await fetch('/api/logs?lines=500', { cache: 'no-store' });
      if (!response.ok) return;
      const data = await response.json();
      mergeInitialLogs(data.logs || data);
    } catch (_) {}
  };

  const handleSnapshot = data => {
    const status = data?.status;
    if (status) {
      const pid = Number(status.pid) || null;
      if (pid !== null && cachedPID !== null && cachedPID !== pid) resetCache();
      if (pid !== null) cachedPID = pid;
    }

    if (Array.isArray(data?.logs)) appendLogs(data.logs);
  };

  const NativeEventSource = window.EventSource;
  window.EventSource = function MineceraEventSource(url, options) {
    const source = new NativeEventSource(url, options);
    if (String(url).startsWith('/api/events')) {
      source.addEventListener('snapshot', event => {
        try { handleSnapshot(JSON.parse(event.data)); } catch (_) {}
      });
    }
    return source;
  };
  window.EventSource.prototype = NativeEventSource.prototype;

  const addRefreshControl = () => {
    const footer = document.querySelector('.actions');
    if (!footer || document.getElementById('refresh-control')) return;

    const wrap = document.createElement('label');
    wrap.id = 'refresh-control';
    wrap.title = 'How often player telemetry refreshes';
    wrap.style.cssText = 'display:flex;align-items:center;gap:6px;margin-left:10px;color:var(--dim);font-size:10px;white-space:nowrap';

    const text = document.createElement('span');
    const slider = document.createElement('input');
    const value = document.createElement('span');
    slider.type = 'range';
    slider.min = String(MIN);
    slider.max = String(MAX);
    slider.step = '1';
    slider.value = String(readRefresh());
    slider.style.cssText = 'width:90px;accent-color:var(--blue)';

    const update = () => { value.textContent = `${slider.value}s`; };
    slider.addEventListener('input', () => {
      localStorage.setItem(REFRESH_KEY, slider.value);
      update();
      schedulePlayers();
    });

    text.textContent = 'refresh';
    update();
    wrap.append(text, slider, value);
    footer.insertBefore(wrap, document.getElementById('updated'));
  };

  const updatePlayers = async () => {
    if (document.hidden || typeof updatePlayerGraph !== 'function') return;
    try { await updatePlayerGraph(); } catch (_) {}
  };

  const schedulePlayers = () => {
    if (refreshTimer) clearInterval(refreshTimer);
    refreshTimer = setInterval(updatePlayers, readRefresh() * 1000);
  };

  window.addEventListener('DOMContentLoaded', () => {
    loadCache();
    addRefreshControl();
    if (localLogs.length && typeof renderLogs === 'function') renderLogs(localLogs);
    loadInitialLogs();
    schedulePlayers();
  });

  document.addEventListener('visibilitychange', () => {
    if (document.hidden) return;
    loadInitialLogs();
    updatePlayers();
    schedulePlayers();
  });
})();

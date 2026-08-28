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
  let stream = null;

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
      } catch (error) {
        console.warn('Failed to save log cache:', error);
      }
    }, 750);
  };

  const loadCache = () => {
    try {
      const value = JSON.parse(localStorage.getItem(LOG_CACHE_KEY) || '{}');
      if (Array.isArray(value.lines)) localLogs = value.lines.filter(line => typeof line === 'string').slice(-LOG_CACHE_LIMIT);
      if (Number.isInteger(value.pid)) cachedPID = value.pid;
    } catch (error) {
      console.warn('Failed to load log cache:', error);
    }
  };

  const resetCache = () => {
    localLogs = [];
    try { localStorage.removeItem(LOG_CACHE_KEY); } catch (_) {}
    if (typeof renderLogs === 'function') renderLogs([]);
  };

  const appendDOMLine = line => {
    if (typeof renderLogs !== 'function') return;
    const container = document.getElementById('logs');
    if (!container) return;

    const empty = container.querySelector('.empty');
    if (empty) empty.remove();

    const element = document.createElement('div');
    element.className = 'line';
    element.innerHTML = minecraft(line);
    container.append(element);

    while (container.childElementCount > LOG_CACHE_LIMIT) container.firstElementChild?.remove();

    if (container.scrollHeight - container.scrollTop - container.clientHeight < 80) {
      container.scrollTop = container.scrollHeight;
    }
  };

  const appendLog = line => {
    if (typeof line !== 'string') return;
    localLogs.push(line);
    if (localLogs.length > LOG_CACHE_LIMIT) localLogs.splice(0, localLogs.length - LOG_CACHE_LIMIT);
    appendDOMLine(line);
    saveCache();
  };

  const mergeInitialLogs = lines => {
    if (!Array.isArray(lines) || !lines.length) return;

    if (!localLogs.length) {
      localLogs = lines.slice(-LOG_CACHE_LIMIT);
      if (typeof renderLogs === 'function') renderLogs(localLogs);
      return;
    }

    const overlap = Math.min(100, localLogs.length, lines.length);
    let matched = false;
    if (overlap > 0) {
      for (let i = 0; i <= lines.length - overlap; i++) {
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
    }

    if (!matched) localLogs = lines.slice(-LOG_CACHE_LIMIT);
    localLogs = localLogs.slice(-LOG_CACHE_LIMIT);
    if (typeof renderLogs === 'function') renderLogs(localLogs);
  };

  const loadInitialLogs = async () => {
    if (document.hidden) return;
    try {
      const response = await fetch('/api/logs?lines=500', {
        cache: 'no-store',
        headers: { Accept: 'application/json' }
      });
      if (!response.ok) throw new Error(await response.text());
      const data = await response.json();
      mergeInitialLogs(data.logs || data);
      saveCache();
    } catch (error) {
      console.error('Initial log load failed:', error);
    }
  };

  const handleSnapshot = data => {
    const status = data?.status?.status || data?.status;
    if (!status) return;

    const pid = Number(status.pid) || null;
    if (pid !== null) {
      if (cachedPID !== null && cachedPID !== pid) resetCache();
      cachedPID = pid;
      saveCache();
    }
  };

  const NativeEventSource = window.EventSource;
  window.EventSource = function MineceraEventSource(url, options) {
    const source = new NativeEventSource(url, options);
    if (String(url).startsWith('/api/events')) {
      stream = source;
      source.addEventListener('snapshot', event => {
        try { handleSnapshot(JSON.parse(event.data)); }
        catch (error) { console.error('Invalid snapshot:', error); }
      });
      source.addEventListener('log', event => {
        try { appendLog(JSON.parse(event.data)); }
        catch (error) { console.error('Invalid log event:', error); }
      });
      source.addEventListener('error', () => {
        if (source.readyState === NativeEventSource.CLOSED) {
          $('event').textContent = 'disconnected';
        }
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
    try {
      await updatePlayerGraph();
    } catch (_) {}
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

    const copy = document.getElementById('copy-log');
    if (copy) {
      copy.addEventListener('click', async event => {
        event.stopImmediatePropagation();
        if (!localLogs.length) return;
        try {
          await navigator.clipboard.writeText(localLogs.join('\n'));
          copy.textContent = 'copied';
        } catch (error) {
          copy.textContent = 'copy failed';
        }
        setTimeout(() => { copy.textContent = 'copy log'; }, 1400);
      });
    }
  });

  document.addEventListener('visibilitychange', () => {
    if (document.hidden) return;
    loadInitialLogs();
    updatePlayers();
    schedulePlayers();
  });
})();

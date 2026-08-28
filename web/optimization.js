(() => {
  'use strict';

  const STORAGE_KEY = 'minecera.refresh-seconds';
  const LOG_CACHE_KEY = 'minecera.log-cache';
  const LOG_CACHE_LIMIT = 5000;
  const MIN = 2;
  const MAX = 60;
  const DEFAULT = 10;
  let timer = null;
  let logEvents = null;
  let cachedPID = null;

  const readInterval = () => {
    const value = Number(localStorage.getItem(STORAGE_KEY));
    return Number.isFinite(value) ? Math.min(MAX, Math.max(MIN, value)) : DEFAULT;
  };

  const setIntervalValue = value => {
    const seconds = Math.min(MAX, Math.max(MIN, Number(value)));
    localStorage.setItem(STORAGE_KEY, String(seconds));
    return seconds;
  };

  const readCache = () => {
    try {
      const parsed = JSON.parse(localStorage.getItem(LOG_CACHE_KEY) || '{}');
      if (!Array.isArray(parsed.lines)) return [];
      cachedPID = parsed.pid ?? null;
      return parsed.lines.filter(line => typeof line === 'string').slice(-LOG_CACHE_LIMIT);
    } catch (error) {
      console.warn('Failed to load log cache:', error);
      return [];
    }
  };

  let localLogs = readCache();

  const writeCache = () => {
    try {
      localStorage.setItem(LOG_CACHE_KEY, JSON.stringify({
        pid: cachedPID,
        lines: localLogs.slice(-LOG_CACHE_LIMIT)
      }));
    } catch (error) {
      console.warn('Failed to save log cache:', error);
    }
  };

  const showCachedLogs = () => {
    if (localLogs.length) renderLogs(localLogs);
  };

  const resetCache = () => {
    localLogs = [];
    writeCache();
    renderLogs([]);
  };

  const appendLog = line => {
    if (typeof line !== 'string') return;
    localLogs.push(line);
    if (localLogs.length > LOG_CACHE_LIMIT) {
      localLogs.splice(0, localLogs.length - LOG_CACHE_LIMIT);
    }
    renderLogs(localLogs);
    writeCache();
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
      const lines = Array.isArray(data.logs) ? data.logs : Array.isArray(data) ? data : [];
      if (!localLogs.length) {
        localLogs = lines.slice(-LOG_CACHE_LIMIT);
      } else if (lines.length) {
        const overlap = Math.min(localLogs.length, lines.length, 100);
        let match = -1;
        for (let i = 0; i <= lines.length - overlap; i++) {
          let same = true;
          for (let j = 0; j < overlap; j++) {
            if (lines[i + j] !== localLogs[localLogs.length - overlap + j]) {
              same = false;
              break;
            }
          }
          if (same) {
            match = i + overlap;
            break;
          }
        }
        if (match >= 0) localLogs.push(...lines.slice(match));
        else localLogs = lines.slice(-LOG_CACHE_LIMIT);
        localLogs = localLogs.slice(-LOG_CACHE_LIMIT);
      }
      showCachedLogs();
      writeCache();
    } catch (error) {
      console.error('Initial log load failed:', error);
    }
  };

  const handleSnapshot = data => {
    const status = data?.status?.status || data?.status;
    if (!status) return;

    const pid = Number(status.pid) || null;
    if (cachedPID !== null && pid !== null && cachedPID !== pid) {
      resetCache();
    }
    if (pid !== null) {
      cachedPID = pid;
      writeCache();
    }
  };

  const connectLogs = () => {
    if (logEvents) logEvents.close();
    logEvents = new EventSource('/api/events');

    logEvents.addEventListener('snapshot', event => {
      try {
        handleSnapshot(JSON.parse(event.data));
      } catch (error) {
        console.error('Invalid snapshot:', error);
      }
    });

    logEvents.addEventListener('log', event => {
      try {
        appendLog(JSON.parse(event.data));
      } catch (error) {
        console.error('Invalid log event:', error);
      }
    });

    logEvents.addEventListener('error', () => {
      if (logEvents.readyState === EventSource.CLOSED) {
        setTimeout(connectLogs, 2000);
      }
    });
  };

  const addControls = () => {
    const footer = document.querySelector('.actions');
    if (!footer || document.getElementById('refresh-control')) return;

    const wrap = document.createElement('label');
    wrap.id = 'refresh-control';
    wrap.title = 'How often dashboard telemetry and player history refresh';
    wrap.style.cssText = 'display:flex;align-items:center;gap:6px;margin-left:10px;color:var(--dim);font-size:10px;white-space:nowrap';

    const text = document.createElement('span');
    const slider = document.createElement('input');
    const value = document.createElement('span');

    slider.type = 'range';
    slider.min = String(MIN);
    slider.max = String(MAX);
    slider.step = '1';
    slider.value = String(readInterval());
    slider.style.cssText = 'width:90px;accent-color:var(--blue)';

    const updateLabel = () => {
      value.textContent = `${slider.value}s`;
    };

    slider.addEventListener('input', () => {
      setIntervalValue(slider.value);
      updateLabel();
      schedule();
    });

    text.textContent = 'refresh';
    updateLabel();
    wrap.append(text, slider, value);
    footer.insertBefore(wrap, document.getElementById('updated'));
  };

  const fetchPlayers = async () => {
    if (document.hidden) return;
    try {
      const response = await fetch('/api/player-count?points=120', { cache: 'no-store' });
      if (!response.ok) throw new Error(await response.text());
      const data = await response.json();
      renderPlayerGraph(data.points || []);
    } catch (error) {
      console.error('Player graph failed:', error);
    }
  };

  const schedule = () => {
    if (timer) clearInterval(timer);
    timer = setInterval(fetchPlayers, readInterval() * 1000);
  };

  document.addEventListener('visibilitychange', () => {
    if (document.hidden) return;
    loadInitialLogs();
    fetchPlayers();
    schedule();
  });

  addControls();
  showCachedLogs();
  loadInitialLogs();
  fetchPlayers();
  connectLogs();
  schedule();
})();

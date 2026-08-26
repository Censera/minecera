(() => {
  'use strict';

  const STORAGE_KEY = 'minecera.refresh-seconds';
  const MIN = 2;
  const MAX = 60;
  const DEFAULT = 10;
  let timer = null;
  let busy = false;

  const readInterval = () => {
    const value = Number(localStorage.getItem(STORAGE_KEY));
    return Number.isFinite(value) ? Math.min(MAX, Math.max(MIN, value)) : DEFAULT;
  };

  const setIntervalValue = value => {
    const seconds = Math.min(MAX, Math.max(MIN, Number(value)));
    localStorage.setItem(STORAGE_KEY, String(seconds));
    return seconds;
  };

  const addControls = () => {
    const footer = document.querySelector('.actions');
    if (!footer || document.getElementById('refresh-control')) return;

    const wrap = document.createElement('label');
    wrap.id = 'refresh-control';
    wrap.title = 'How often the dashboard fetches logs and player history';
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

  const fetchLogs = async () => {
    if (busy || document.hidden) return;
    busy = true;
    try {
      const response = await fetch('/api/logs?lines=200', {
        cache: 'no-store',
        headers: { 'Accept': 'application/json' }
      });
      if (!response.ok) throw new Error(await response.text());
      const data = await response.json();
      if (Array.isArray(data)) renderLogs(data);
    } catch (error) {
      console.error('Log update failed:', error);
    } finally {
      busy = false;
    }
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
    timer = setInterval(() => {
      fetchLogs();
      fetchPlayers();
    }, readInterval() * 1000);
  };

  document.addEventListener('visibilitychange', () => {
    if (document.hidden) return;
    fetchLogs();
    fetchPlayers();
    schedule();
  });

  addControls();
  fetchLogs();
  fetchPlayers();
  schedule();
})();

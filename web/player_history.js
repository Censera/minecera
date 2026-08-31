(() => {
'use strict';

const RANGES = { day: 86400, week: 7 * 86400, month: 30 * 86400, year: 365 * 86400, all: 0 };
let range = 'week';
let rawPoints = [];
let visibleData = [];
let hoveredIndex = -1;

function startOfDay(timestamp) {
  const date = new Date(timestamp * 1000);
  date.setHours(0, 0, 0, 0);
  return Math.floor(date.getTime() / 1000);
}

function aggregateDaily(points) {
  const days = new Map();
  for (const point of points) {
    const time = Number(point.time);
    const count = Number(point.count);
    if (!Number.isFinite(time) || !Number.isFinite(count)) continue;
    const day = startOfDay(time);
    const entry = days.get(day) || { time: day, sum: 0, samples: 0 };
    entry.sum += count;
    entry.samples++;
    days.set(day, entry);
  }
  return [...days.values()]
    .sort((a, b) => a.time - b.time)
    .map(entry => ({ time: entry.time, count: Math.round(entry.sum / entry.samples) }));
}

function dataForRange() {
  if (!rawPoints.length) return [];
  const now = Math.floor(Date.now() / 1000);
  if (range === 'day') {
    return rawPoints.filter(point => Number(point.time) >= now - RANGES.day);
  }
  const data = aggregateDaily(rawPoints);
  const seconds = RANGES[range];
  return seconds ? data.filter(point => point.time >= now - seconds) : data;
}

function formatDate(timestamp) {
  const date = new Date(timestamp * 1000);
  if (range === 'day') {
    return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  }
  return date.toLocaleDateString([], { month: 'short', day: 'numeric' });
}

function style() {
  if (document.getElementById('player-history-style')) return;
  const style = document.createElement('style');
  style.id = 'player-history-style';
  style.textContent = `
    .chart-panel.player-history-panel{padding:0;overflow:hidden;background:#1e1e1e;border:0;border-radius:12px}
    .player-history{width:100%;padding:2rem;color:#e0e0e0}
    .player-history-title{margin:0 0 1.5rem;font-weight:400;font-size:1.2rem;letter-spacing:-.01em;color:#f0f0f0}
    #historyChart{display:block;width:100%;height:auto;background:#1a1a1a;border-radius:8px;cursor:crosshair}
    .player-history-stats{display:flex;gap:1.5rem;margin:1rem 0;font-size:.85rem;color:#b0b0b0}
    .player-history-stats div{display:flex;align-items:baseline;gap:.5rem}
    .player-history-stats span{color:#888;font-size:.8rem;font-weight:400}
    .player-history-stats strong{color:#f5f5f5;font-size:.9rem;font-weight:400}
    .player-history-controls{display:flex;align-items:center;margin-top:1rem}
    .player-history-ranges{display:flex;gap:.6rem;flex-wrap:wrap}
    .player-history-range{background:none;border:0;border-radius:8px;padding:.5rem 1rem;font-size:.85rem;cursor:pointer;color:#ccc;transition:background .15s,color .15s;font-weight:400}
    .player-history-range.active{background:#3a7afe;color:white}
    .player-history-range:hover:not(.active){background:#2a2a2a}
    @media(max-width:600px){.player-history{padding:1.25rem}.player-history-ranges{gap:.25rem}.player-history-range{padding:.45rem .75rem}}
  `;
  document.head.append(style);
}

function panelHTML() {
  return `<div class="player-history">
    <h3 class="player-history-title">Player History</h3>
    <canvas id="historyChart" width="500" height="200" aria-label="Player history line chart"></canvas>
    <div class="player-history-stats">
      <div><span>Peak</span> <strong id="peakPlayers">--</strong></div>
      <div><span>Avg</span> <strong id="avgPlayers">--</strong></div>
    </div>
    <div class="player-history-controls">
      <div class="player-history-ranges">
        ${Object.keys(RANGES).map(name => `<button type="button" class="player-history-range${name === range ? ' active' : ''}" data-range="${name}">${name[0].toUpperCase()}${name.slice(1)}</button>`).join('')}
      </div>
    </div>
  </div>`;
}

function updateStats() {
  const peak = document.getElementById('peakPlayers');
  const avg = document.getElementById('avgPlayers');
  if (!visibleData.length) {
    peak.textContent = '--';
    avg.textContent = '--';
    return;
  }
  const counts = visibleData.map(point => point.count);
  peak.textContent = String(Math.max(...counts));
  avg.textContent = String(Math.round(counts.reduce((sum, count) => sum + count, 0) / counts.length));
}

function renderChart() {
  const canvas = document.getElementById('historyChart');
  if (!canvas) return;
  const rect = canvas.getBoundingClientRect();
  const dpr = Math.max(1, window.devicePixelRatio || 1);
  const width = Math.max(500, Math.round(rect.width * dpr));
  const height = Math.round(200 * dpr);
  if (canvas.width !== width || canvas.height !== height) {
    canvas.width = width;
    canvas.height = height;
  }

  const ctx = canvas.getContext('2d');
  const scale = dpr;
  const w = canvas.width / scale;
  const h = canvas.height / scale;
  ctx.setTransform(scale, 0, 0, scale, 0, 0);
  ctx.clearRect(0, 0, w, h);
  if (!visibleData.length) return;

  const left = 35, right = 10, top = 10, bottom = 25;
  const graphWidth = w - left - right;
  const graphHeight = h - top - bottom;
  const values = visibleData.map(point => point.count);
  let min = Math.min(...values);
  let max = Math.max(...values);
  const padding = Math.max(1, (max - min) * 0.1);
  min -= padding;
  max += padding;
  if (max === min) max = min + 1;

  const step = graphWidth / (visibleData.length - 1 || 1);
  const pointAt = index => ({
    x: left + index * step,
    y: top + graphHeight - ((visibleData[index].count - min) / (max - min)) * graphHeight
  });
  const points = visibleData.map((_, index) => pointAt(index));

  ctx.strokeStyle = '#2a2a2a';
  ctx.lineWidth = .5;
  ctx.font = '10px system-ui, sans-serif';
  ctx.fillStyle = '#888';
  ctx.textAlign = 'right';
  ctx.textBaseline = 'middle';

  for (let i = 0; i <= 4; i++) {
    const value = min + i / 4 * (max - min);
    const y = top + graphHeight - i / 4 * graphHeight;
    ctx.beginPath();
    ctx.moveTo(left, y);
    ctx.lineTo(w - right, y);
    ctx.stroke();
    ctx.fillText(String(Math.round(value)), left - 5, y);
  }

  ctx.textAlign = 'center';
  ctx.textBaseline = 'alphabetic';
  const last = visibleData.length - 1;
  ctx.fillText(formatDate(visibleData[0].time), left, h - 8);
  if (last > 1) {
    const middle = Math.floor(last / 2);
    ctx.fillText(formatDate(visibleData[middle].time), points[middle].x, h - 8);
  }
  ctx.fillText(formatDate(visibleData[last].time), points[last].x, h - 8);

  ctx.beginPath();
  points.forEach((point, index) => index ? ctx.lineTo(point.x, point.y) : ctx.moveTo(point.x, point.y));
  ctx.strokeStyle = '#4a8cff';
  ctx.lineWidth = 2;
  ctx.lineJoin = 'round';
  ctx.stroke();

  points.forEach((point, index) => {
    ctx.beginPath();
    ctx.arc(point.x, point.y, index === hoveredIndex ? 5 : (range === 'day' ? 2.5 : 3), 0, Math.PI * 2);
    ctx.fillStyle = index === hoveredIndex ? '#7fadff' : '#4a8cff';
    ctx.fill();
  });

  if (hoveredIndex >= 0 && hoveredIndex < points.length) {
    const point = points[hoveredIndex];
    ctx.beginPath();
    ctx.moveTo(point.x, top);
    ctx.lineTo(point.x, top + graphHeight);
    ctx.strokeStyle = '#555';
    ctx.lineWidth = .8;
    ctx.setLineDash([3, 3]);
    ctx.stroke();
    ctx.setLineDash([]);

    const label = `${visibleData[hoveredIndex].count} players`;
    ctx.font = '11px system-ui, sans-serif';
    const boxWidth = ctx.measureText(label).width + 10;
    const boxX = Math.min(point.x + 10, w - boxWidth - 5);
    const boxY = Math.max(point.y - 25, 5);
    ctx.fillStyle = '#2a2a2a';
    ctx.fillRect(boxX, boxY, boxWidth, 20);
    ctx.fillStyle = '#f0f0f0';
    ctx.textAlign = 'left';
    ctx.textBaseline = 'middle';
    ctx.fillText(label, boxX + 5, boxY + 10);
  }
}

function update() {
  visibleData = dataForRange();
  updateStats();
  hoveredIndex = -1;
  renderChart();
}

async function load() {
  try {
    const response = await fetch('/api/player-count?points=2000', { cache: 'no-store' });
    if (!response.ok) throw new Error('player history request failed');
    const data = await response.json();
    rawPoints = Array.isArray(data.points) ? data.points : [];
  } catch (_) {
    rawPoints = [];
  }
  update();
}

function mount() {
  const panel = document.querySelector('.chart-panel');
  if (!panel) return;
  style();
  panel.className = 'chart-panel player-history-panel';
  panel.innerHTML = panelHTML();

  panel.querySelectorAll('.player-history-range').forEach(button => {
    button.addEventListener('click', () => {
      range = button.dataset.range;
      panel.querySelectorAll('.player-history-range').forEach(item => item.classList.toggle('active', item === button));
      update();
    });
  });

  const canvas = document.getElementById('historyChart');
  canvas.addEventListener('mousemove', event => {
    const rect = canvas.getBoundingClientRect();
    const x = (event.clientX - rect.left) * canvas.width / rect.width / (window.devicePixelRatio || 1);
    const left = 35;
    const right = 10;
    if (!visibleData.length || x < left - 5 || x > 500 - right + 5) {
      hoveredIndex = -1;
    } else {
      const step = (500 - left - right) / (visibleData.length - 1 || 1);
      hoveredIndex = Math.max(0, Math.min(visibleData.length - 1, Math.round((x - left) / step)));
    }
    renderChart();
  });
  canvas.addEventListener('mouseleave', () => {
    hoveredIndex = -1;
    renderChart();
  });
  window.addEventListener('resize', renderChart);
  load();
}

if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', mount, { once: true });
} else {
  mount();
}
})();

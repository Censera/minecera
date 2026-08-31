(() => {
'use strict';

const RANGE_SECONDS = { day: 86400, week: 7 * 86400, month: 30 * 86400, year: 365 * 86400, all: 0 };
const MAX_POINTS = 1200;
const Y_MAX = 15;
let range = 'week';
let rawPoints = [];
let visibleData = [];
let hoveredIndex = -1;

function startOfDay(timestamp) {
  const d = new Date(timestamp * 1000);
  d.setHours(0, 0, 0, 0);
  return Math.floor(d.getTime() / 1000);
}

function aggregate(points, bucketSeconds) {
  const buckets = new Map();
  for (const point of points) {
    const time = Number(point.time);
    const count = Number(point.count);
    if (!Number.isFinite(time) || !Number.isFinite(count)) continue;
    const bucket = bucketSeconds > 1 ? Math.floor(time / bucketSeconds) * bucketSeconds : time;
    const entry = buckets.get(bucket) || { time: bucket, sum: 0, samples: 0 };
    entry.sum += count;
    entry.samples++;
    buckets.set(bucket, entry);
  }
  return [...buckets.values()]
    .sort((a, b) => a.time - b.time)
    .map(entry => ({ time: entry.time, count: entry.sum / entry.samples }));
}

function decimate(points) {
  if (points.length <= MAX_POINTS) return points;
  const bucketSize = Math.ceil(points.length / MAX_POINTS);
  const result = [];
  for (let i = 0; i < points.length; i += bucketSize) {
    const bucket = points.slice(i, i + bucketSize);
    const sum = bucket.reduce((total, point) => total + point.count, 0);
    result.push({
      time: bucket[Math.floor(bucket.length / 2)].time,
      count: sum / bucket.length
    });
  }
  return result;
}

function dataForRange() {
  const now = Math.floor(Date.now() / 1000);
  const seconds = RANGE_SECONDS[range];
  const filtered = seconds
    ? rawPoints.filter(point => Number(point.time) >= now - seconds)
    : rawPoints.slice();

  if (!filtered.length) return [];
  if (range === 'day') return decimate(filtered);

  const days = new Set(filtered.map(point => startOfDay(Number(point.time))));
  if (days.size <= 1) return decimate(filtered);
  return aggregate(filtered, 86400);
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
        ${Object.keys(RANGE_SECONDS).map(name => `<button type="button" class="player-history-range${name === range ? ' active' : ''}" data-range="${name}">${name[0].toUpperCase()}${name.slice(1)}</button>`).join('')}
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
  peak.textContent = String(Math.round(Math.max(...counts)));
  avg.textContent = String(Math.round(counts.reduce((sum, value) => sum + value, 0) / counts.length));
}

function formatDate(timestamp) {
  const date = new Date(timestamp * 1000);
  if (range === 'day') return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  return date.toLocaleDateString([], { month: 'short', day: 'numeric' });
}

function renderChart() {
  const canvas = document.getElementById('historyChart');
  if (!canvas) return;
  const cssWidth = Math.max(300, canvas.parentElement.clientWidth);
  const cssHeight = 200;
  const dpr = Math.max(1, window.devicePixelRatio || 1);
  canvas.width = Math.round(cssWidth * dpr);
  canvas.height = Math.round(cssHeight * dpr);
  const ctx = canvas.getContext('2d');
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, cssWidth, cssHeight);
  if (!visibleData.length) return;

  const left = 42, right = 12, top = 12, bottom = 28;
  const graphWidth = cssWidth - left - right;
  const graphHeight = cssHeight - top - bottom;
  const step = graphWidth / Math.max(1, visibleData.length - 1);
  const points = visibleData.map((point, index) => ({
    x: left + index * step,
    y: top + graphHeight - (Math.max(0, Math.min(Y_MAX, point.count)) / Y_MAX) * graphHeight
  }));

  ctx.font = '10px system-ui, sans-serif';
  ctx.textBaseline = 'middle';
  ctx.textAlign = 'right';
  for (let value = 0; value <= Y_MAX; value += 5) {
    const y = top + graphHeight - (value / Y_MAX) * graphHeight;
    ctx.strokeStyle = '#2a2a2a';
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(left, y);
    ctx.lineTo(cssWidth - right, y);
    ctx.stroke();
    ctx.fillStyle = '#888';
    ctx.fillText(String(value), left - 7, y);
  }

  ctx.textAlign = 'center';
  ctx.textBaseline = 'alphabetic';
  const last = visibleData.length - 1;
  ctx.fillText(formatDate(visibleData[0].time), points[0].x, cssHeight - 8);
  if (last > 1) {
    const middle = Math.floor(last / 2);
    ctx.fillText(formatDate(visibleData[middle].time), points[middle].x, cssHeight - 8);
  }
  if (last > 0) ctx.fillText(formatDate(visibleData[last].time), points[last].x, cssHeight - 8);

  ctx.beginPath();
  points.forEach((point, index) => index ? ctx.lineTo(point.x, point.y) : ctx.moveTo(point.x, point.y));
  ctx.strokeStyle = '#4a8cff';
  ctx.lineWidth = 2;
  ctx.lineJoin = 'round';
  ctx.lineCap = 'round';
  ctx.stroke();

  for (const [index, point] of points.entries()) {
    if (visibleData.length > 100 && index !== hoveredIndex) continue;
    ctx.beginPath();
    ctx.arc(point.x, point.y, index === hoveredIndex ? 5 : 3, 0, Math.PI * 2);
    ctx.fillStyle = index === hoveredIndex ? '#7fadff' : '#4a8cff';
    ctx.fill();
  }

  if (hoveredIndex >= 0 && hoveredIndex < points.length) {
    const point = points[hoveredIndex];
    ctx.beginPath();
    ctx.moveTo(point.x, top);
    ctx.lineTo(point.x, top + graphHeight);
    ctx.strokeStyle = '#555';
    ctx.lineWidth = 1;
    ctx.setLineDash([3, 3]);
    ctx.stroke();
    ctx.setLineDash([]);

    const label = `${Math.round(visibleData[hoveredIndex].count)} players`;
    ctx.font = '11px system-ui, sans-serif';
    const boxWidth = ctx.measureText(label).width + 10;
    const boxX = Math.min(point.x + 10, cssWidth - boxWidth - 5);
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
    const response = await fetch('/api/player-count?points=20000', { cache: 'no-store' });
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
    const x = (event.clientX - rect.left) * (canvas.width / (window.devicePixelRatio * rect.width));
    const cssWidth = rect.width;
    const left = 42;
    const right = 12;
    const graphWidth = cssWidth - left - right;
    if (!visibleData.length || x < left - 5 || x > cssWidth - right + 5) {
      hoveredIndex = -1;
    } else {
      const step = graphWidth / (visibleData.length - 1 || 1);
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

if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', mount, { once: true });
else mount();
})();

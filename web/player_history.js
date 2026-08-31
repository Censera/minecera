(() => {
'use strict';

const RANGE_SECONDS = {
  day: 86400,
  week: 7 * 86400,
  month: 30 * 86400,
  year: 365 * 86400,
  all: 0
};
const MAX_PLAYERS = 15;
const TARGET_POINTS = 220;

let range = 'week';
let rawPoints = [];
let visibleData = [];
let hoveredIndex = -1;

function startOfDay(timestamp) {
  const date = new Date(timestamp * 1000);
  date.setHours(0, 0, 0, 0);
  return Math.floor(date.getTime() / 1000);
}

function averageByTime(points, bucketSeconds) {
  if (!points.length || bucketSeconds <= 0) return points.slice();

  const buckets = new Map();
  for (const point of points) {
    const time = Number(point.time);
    const count = Number(point.count);
    if (!Number.isFinite(time) || !Number.isFinite(count)) continue;

    const bucket = Math.floor(time / bucketSeconds) * bucketSeconds;
    const entry = buckets.get(bucket) || { time: bucket, sum: 0, samples: 0 };
    entry.sum += count;
    entry.samples++;
    buckets.set(bucket, entry);
  }

  return [...buckets.values()]
    .sort((a, b) => a.time - b.time)
    .map(entry => ({
      time: entry.time + bucketSeconds / 2,
      count: Math.round(entry.sum / entry.samples)
    }));
}

function dataForRange() {
  if (!rawPoints.length) return [];

  const now = Math.floor(Date.now() / 1000);
  const seconds = RANGE_SECONDS[range];
  const filtered = seconds
    ? rawPoints.filter(point => Number(point.time) >= now - seconds)
    : rawPoints.slice();

  filtered.sort((a, b) => Number(a.time) - Number(b.time));
  if (filtered.length < TARGET_POINTS) return filtered;

  const first = Number(filtered[0].time);
  const last = Number(filtered[filtered.length - 1].time);
  const span = Math.max(1, last - first);
  const bucketSeconds = Math.max(1, Math.ceil(span / TARGET_POINTS));
  return averageByTime(filtered, bucketSeconds);
}

function formatTime(timestamp) {
  const date = new Date(timestamp * 1000);
  if (range === 'day') {
    return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  }
  return date.toLocaleDateString([], { month: 'short', day: 'numeric' });
}

function injectStyle() {
  if (document.getElementById('player-history-style')) return;
  const style = document.createElement('style');
  style.id = 'player-history-style';
  style.textContent = `
    .chart-panel.player-history-panel{padding:14px;overflow:hidden}
    .player-history{width:100%}
    .player-history-title{margin:0;font-size:11px;font-weight:700;color:var(--text);text-transform:lowercase}
    .player-history-subtitle{margin-top:3px;color:var(--dim);font-size:10px}
    #historyChart{display:block;width:100%;height:auto;margin-top:10px;background:transparent;border:0;cursor:crosshair}
    .player-history-stats{display:flex;gap:18px;margin-top:9px;color:var(--muted);font-size:10px}
    .player-history-stats div{display:flex;align-items:baseline;gap:5px}
    .player-history-stats span{color:var(--dim);font-size:9px;text-transform:uppercase;letter-spacing:.06em}
    .player-history-stats strong{font:600 11px var(--mono);color:var(--text)}
    .player-history-controls{display:flex;align-items:center;margin-top:10px}
    .player-history-ranges{display:flex;gap:2px;flex-wrap:wrap;padding:2px;border:1px solid var(--line);border-radius:5px;background:var(--panel2)}
    .player-history-range{border:0;border-radius:3px;padding:4px 8px;background:transparent;color:var(--dim);font:10px var(--sans);cursor:pointer}
    .player-history-range:hover{color:var(--text)}
    .player-history-range.active{background:var(--line2);color:var(--text)}
    @media(max-width:600px){.chart-panel.player-history-panel{padding:11px}.player-history-range{padding:4px 7px}}
  `;
  document.head.append(style);
}

function panelHTML() {
  return `<div class="player-history">
    <h3 class="player-history-title">player history</h3>
    <div class="player-history-subtitle">online players over time</div>
    <canvas id="historyChart" width="700" height="230" aria-label="Player history line chart"></canvas>
    <div class="player-history-stats">
      <div><span>peak</span><strong id="peakPlayers">--</strong></div>
      <div><span>avg</span><strong id="avgPlayers">--</strong></div>
      <div><span>samples</span><strong id="sampleCount">--</strong></div>
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
  const samples = document.getElementById('sampleCount');
  if (!visibleData.length) {
    peak.textContent = '--';
    avg.textContent = '--';
    samples.textContent = '0';
    return;
  }

  const counts = visibleData.map(point => point.count);
  peak.textContent = String(Math.max(...counts));
  avg.textContent = String(Math.round(counts.reduce((sum, count) => sum + count, 0) / counts.length));
  samples.textContent = String(visibleData.length);
}

function resizeCanvas(canvas) {
  const rect = canvas.getBoundingClientRect();
  const dpr = Math.max(1, window.devicePixelRatio || 1);
  const width = Math.max(320, Math.round(rect.width * dpr));
  const height = Math.round(230 * dpr);
  if (canvas.width !== width || canvas.height !== height) {
    canvas.width = width;
    canvas.height = height;
  }
  return { dpr, width: canvas.width / dpr, height: canvas.height / dpr };
}

function renderChart() {
  const canvas = document.getElementById('historyChart');
  if (!canvas) return;

  const { dpr, width: w, height: h } = resizeCanvas(canvas);
  const ctx = canvas.getContext('2d');
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, w, h);

  const left = 32;
  const right = 8;
  const top = 8;
  const bottom = 27;
  const graphWidth = w - left - right;
  const graphHeight = h - top - bottom;

  ctx.font = '9px system-ui, sans-serif';
  ctx.textBaseline = 'middle';
  ctx.textAlign = 'right';
  ctx.fillStyle = 'var(--dim)';

  const ticks = [0, 5, 10, 15];
  for (const value of ticks) {
    const y = top + graphHeight - (value / MAX_PLAYERS) * graphHeight;
    ctx.strokeStyle = value === 0 ? '#30363d' : '#20262d';
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(left, y);
    ctx.lineTo(w - right, y);
    ctx.stroke();
    ctx.fillStyle = '#6e7271';
    ctx.fillText(String(value), left - 6, y);
  }

  if (!visibleData.length) {
    ctx.textAlign = 'center';
    ctx.fillStyle = '#6e7271';
    ctx.fillText('no player history yet', left + graphWidth / 2, top + graphHeight / 2);
    return;
  }

  const first = Number(visibleData[0].time);
  const last = Number(visibleData[visibleData.length - 1].time);
  const span = Math.max(1, last - first);
  const pointAt = index => {
    const point = visibleData[index];
    const x = left + ((Number(point.time) - first) / span) * graphWidth;
    const y = top + graphHeight - (Math.max(0, Math.min(MAX_PLAYERS, Number(point.count))) / MAX_PLAYERS) * graphHeight;
    return { x, y };
  };
  const points = visibleData.map((_, index) => pointAt(index));

  ctx.beginPath();
  points.forEach((point, index) => {
    if (index === 0) ctx.moveTo(point.x, point.y);
    else ctx.lineTo(point.x, point.y);
  });
  ctx.strokeStyle = '#6aa6ff';
  ctx.lineWidth = 2;
  ctx.lineJoin = 'round';
  ctx.lineCap = 'round';
  ctx.stroke();

  if (points.length <= 80) {
    points.forEach((point, index) => {
      ctx.beginPath();
      ctx.arc(point.x, point.y, index === hoveredIndex ? 4 : 2, 0, Math.PI * 2);
      ctx.fillStyle = index === hoveredIndex ? '#b8e88a' : '#6aa6ff';
      ctx.fill();
    });
  }

  ctx.textBaseline = 'alphabetic';
  ctx.textAlign = 'center';
  ctx.fillStyle = '#6e7271';
  ctx.fillText(formatTime(first), left, h - 7);
  if (points.length > 2) {
    const middle = Math.floor((points.length - 1) / 2);
    ctx.fillText(formatTime(visibleData[middle].time), points[middle].x, h - 7);
  }
  ctx.fillText(formatTime(last), points[points.length - 1].x, h - 7);

  if (hoveredIndex >= 0 && hoveredIndex < points.length) {
    const point = points[hoveredIndex];
    ctx.beginPath();
    ctx.moveTo(point.x, top);
    ctx.lineTo(point.x, top + graphHeight);
    ctx.setLineDash([3, 3]);
    ctx.strokeStyle = '#3a434e';
    ctx.lineWidth = 1;
    ctx.stroke();
    ctx.setLineDash([]);

    const label = `${visibleData[hoveredIndex].count} players`;
    ctx.font = '10px system-ui, sans-serif';
    const boxWidth = ctx.measureText(label).width + 12;
    const boxX = Math.max(4, Math.min(point.x + 8, w - boxWidth - 4));
    const boxY = Math.max(4, point.y - 27);
    ctx.fillStyle = '#151a20';
    ctx.fillRect(boxX, boxY, boxWidth, 19);
    ctx.fillStyle = '#f2f0e9';
    ctx.textAlign = 'left';
    ctx.textBaseline = 'middle';
    ctx.fillText(label, boxX + 6, boxY + 9.5);
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
  } catch (error) {
    rawPoints = [];
    console.error('Player history:', error);
  }
  update();
}

function mount() {
  const panel = document.querySelector('.chart-panel');
  if (!panel) return;

  injectStyle();
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
    const x = (event.clientX - rect.left) * 500 / rect.width;
    const left = 32;
    const right = 8;

    if (!visibleData.length || x < left - 6 || x > 500 - right + 6) {
      hoveredIndex = -1;
      renderChart();
      return;
    }

    const first = Number(visibleData[0].time);
    const last = Number(visibleData[visibleData.length - 1].time);
    const span = Math.max(1, last - first);
    let nearest = 0;
    let distance = Infinity;
    for (let index = 0; index < visibleData.length; index++) {
      const pointX = left + ((Number(visibleData[index].time) - first) / span) * (500 - left - right);
      const candidate = Math.abs(pointX - x);
      if (candidate < distance) {
        distance = candidate;
        nearest = index;
      }
    }
    hoveredIndex = nearest;
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

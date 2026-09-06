// scatter.js — SVG scatter chart
function setupScatterToggle() {
  $('#showScatterLabels').addEventListener('change', () => { if (state.currentView === 'scatter') renderScatter(); });
}

function renderScatter() {
  const data = state.mods.filter(m => m.total_downloads > 0 && m.difficulty > 0 && m.has_codacy);
  if (data.length === 0) return;
  const svg = $('#scatterSvg');
  const width = 800, height = 500;
  const margin = { top: 30, right: 40, bottom: 50, left: 55 };
  const pw = width - margin.left - margin.right, ph = height - margin.top - margin.bottom;
  svg.setAttribute('viewBox', `0 0 ${width} ${height}`);

  const xMax = Math.max(5, Math.ceil(Math.max(...data.map(d => d.difficulty))));
  const yMin = 1, yMax = Math.max(yMin + 1, Math.ceil(Math.max(...data.map(d => d.popularity))));
  const mapX = v => margin.left + (v / xMax) * pw;
  const mapY = v => margin.top + ph - ((v - yMin) / (yMax - yMin)) * ph;

  let h = `<rect x="0" y="0" width="${width}" height="${height}" fill="white" rx="8"/>`;
  for (let i = 0; i <= 5; i++) { const v = yMin + ((yMax - yMin) / 5) * i, y = mapY(v); h += `<line x1="${margin.left}" y1="${y}" x2="${width - margin.right}" y2="${y}" class="scatter-grid"/><text x="${margin.left - 8}" y="${y + 4}" text-anchor="end" class="scatter-label">${v.toFixed(1)}</text>`; }
  for (let i = 0; i <= xMax; i++) { const x = mapX(i); h += `<line x1="${x}" y1="${margin.top}" x2="${x}" y2="${margin.top + ph}" class="scatter-grid"/><text x="${x}" y="${margin.top + ph + 20}" text-anchor="middle" class="scatter-label">${i}</text>`; }
  h += `<line x1="${margin.left}" y1="${margin.top}" x2="${margin.left}" y2="${margin.top + ph}" stroke="#d1d5db" stroke-width="1.5"/>`;
  h += `<line x1="${margin.left}" y1="${margin.top + ph}" x2="${width - margin.right}" y2="${margin.top + ph}" stroke="#d1d5db" stroke-width="1.5"/>`;
  h += `<text x="${width / 2}" y="${height - 6}" text-anchor="middle" class="scatter-label" font-weight="600">${t('difficultyAxis')}</text>`;
  h += `<text x="16" y="${height / 2}" text-anchor="middle" class="scatter-label" font-weight="600" transform="rotate(-90, 16, ${height / 2})">${t('popularityAxis')}</text>`;

  data.forEach(d => {
    const cx = mapX(d.difficulty), cy = mapY(d.popularity), r = 6, color = getStatusColor(d.status);
    h += `<circle cx="${cx}" cy="${cy}" r="${r}" fill="${color}" fill-opacity="0.8" stroke="${color}" stroke-opacity="0.3" stroke-width="3" class="scatter-dot" data-mod-id="${d.id}" data-diff="${d.difficulty}" data-pop="${d.popularity.toFixed(2)}" data-status="${esc(d.status || '')}" data-cf="${formatNum(d.cf_downloads || 0)}" style="cursor:pointer"/>`;
    h += `<text x="${cx + r + 4}" y="${cy + 4}" class="scatter-name-label" font-size="10" fill="#6b7280">${esc(d.name)}</text>`;
  });
  svg.innerHTML = h;

  const showLabels = document.getElementById('showScatterLabels').checked;
  svg.querySelectorAll('.scatter-name-label').forEach(t => { t.style.display = showLabels ? '' : 'none'; });
  svg.querySelectorAll('.scatter-dot').forEach(c => { c.addEventListener('mouseenter', showTooltip); c.addEventListener('mousemove', moveTooltip); c.addEventListener('mouseleave', hideTooltip); c.addEventListener('click', () => openDetail(c.dataset.modId)); });
  renderScatterLegend();
}

function renderScatterLegend() {
  const order = state.statusOrder || [];
  const statuses = [...new Set(state.mods.map(m => m.status).filter(Boolean))];
  statuses.sort((a, b) => { const ai = order.indexOf(a), bi = order.indexOf(b); if (ai >= 0 && bi >= 0) return ai - bi; if (ai >= 0) return -1; if (bi >= 0) return 1; return (a || '').localeCompare(b || ''); });
  $('#scatterLegend').innerHTML = statuses.map(s => `<div class="legend-item"><span class="legend-dot" style="background:${getStatusColor(s)}"></span><span>${esc(s)}</span></div>`).join('');
}

function showTooltip(e) {
  const tt = $('#tooltip'), d = e.target.dataset, mod = state.mods.find(m => m.id === d.modId), arch = mod?.mod_meta?.architecture || [];
  tt.innerHTML = `<div class="tt-name">${esc(mod?.name || d.modId)}</div><div class="tt-row"><span>${t('ttDifficulty')}:</span><span>${d.diff}</span></div><div class="tt-row"><span>${t('ttPopularity')}:</span><span>${d.pop}</span></div><div class="tt-row"><span>${t('ttCFDownloads')}:</span><span>${d.cf}</span></div><div class="tt-row"><span>${t('ttStatus')}:</span><span>${d.status}</span></div>${arch.length ? `<div class="tt-row"><span>${t('ttArchitecture')}:</span><span>${arch.join(', ')}</span></div>` : ''}`;
  tt.classList.add('visible'); moveTooltip(e);
}
function moveTooltip(e) { const tt = $('#tooltip'); let x = e.clientX + 16, y = e.clientY + 12; const tw = tt.offsetWidth, th = tt.offsetHeight; if (x + tw > window.innerWidth) x = e.clientX - tw - 10; if (y + th > window.innerHeight) y = e.clientY - th - 10; tt.style.left = x + 'px'; tt.style.top = y + 'px'; }
function hideTooltip() { $('#tooltip').classList.remove('visible'); }

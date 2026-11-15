// Lizard Monitor Frontend Controller with Comprehensive Error Diagnostics

(function () {
  let globalContainers = [];
  let pollIntervalMs = 2000;
  let pollTimer = null;
  let isFetching = false;

  // DOM Elements
  const hostPill = document.getElementById('hostPill');
  const sysHealthPill = document.getElementById('sysHealthPill');
  const dockHealthPill = document.getElementById('dockHealthPill');
  const borgHealthPill = document.getElementById('borgHealthPill');
  const lastUpdated = document.getElementById('lastUpdated');
  const liveDot = document.getElementById('liveDot');
  const globalAlert = document.getElementById('globalAlert');

  // KPI elements
  const cpuCard = document.getElementById('cpuCard');
  const cpuBadge = document.getElementById('cpuBadge');
  const cpuValue = document.getElementById('cpuValue');
  const cpuProgressBar = document.getElementById('cpuProgressBar');
  const cpuSub = document.getElementById('cpuSub');

  const memCard = document.getElementById('memCard');
  const memBadge = document.getElementById('memBadge');
  const memValue = document.getElementById('memValue');
  const memProgressBar = document.getElementById('memProgressBar');
  const memSub = document.getElementById('memSub');

  const diskCard = document.getElementById('diskCard');
  const diskBadge = document.getElementById('diskBadge');
  const diskValue = document.getElementById('diskValue');
  const diskProgressBar = document.getElementById('diskProgressBar');
  const diskSub = document.getElementById('diskSub');

  const dockerCard = document.getElementById('dockerCard');
  const dockerCountBadge = document.getElementById('dockerCountBadge');
  const dockerRunningCount = document.getElementById('dockerRunningCount');
  const dockerTotalSub = document.getElementById('dockerTotalSub');

  const borgCard = document.getElementById('borgCard');
  const borgCountBadge = document.getElementById('borgCountBadge');
  const borgArchiveCount = document.getElementById('borgArchiveCount');
  const borgLatestSub = document.getElementById('borgLatestSub');

  // Alerts & Chart elements
  const chartAlert = document.getElementById('chartAlert');
  const diskAlert = document.getElementById('diskAlert');
  const dockerAlert = document.getElementById('dockerAlert');
  const borgAlert = document.getElementById('borgAlert');

  const chartCpuCurrent = document.getElementById('chartCpuCurrent');
  const chartMemCurrent = document.getElementById('chartMemCurrent');
  const chartDiskCurrent = document.getElementById('chartDiskCurrent');
  const cpuChartSvg = document.getElementById('cpuChart');
  const memChartSvg = document.getElementById('memChart');
  const diskChartSvg = document.getElementById('diskChart');
  const cpuTooltip = document.getElementById('cpuTooltip');
  const memTooltip = document.getElementById('memTooltip');
  const diskTooltip = document.getElementById('diskTooltip');

  // Table elements
  const topDirsBody = document.getElementById('topDirsBody');
  const diskSummaryDesc = document.getElementById('diskSummaryDesc');
  const containersBody = document.getElementById('containersBody');
  const containerSearch = document.getElementById('containerSearch');
  const borgBody = document.getElementById('borgBody');
  const borgSubtitle = document.getElementById('borgSubtitle');

  const footerUptime = document.getElementById('footerUptime');
  const footerTime = document.getElementById('footerTime');

  // Initialize
  async function init() {
    containerSearch.addEventListener('input', () => {
      renderContainersTable(globalContainers, containerSearch.value);
    });

    await fetchData();
    startPolling();
  }

  function startPolling() {
    if (pollTimer) clearInterval(pollTimer);
    pollTimer = setInterval(fetchData, pollIntervalMs);
  }

  // Fetch telemetry payload
  async function fetchData() {
    if (isFetching) return;
    isFetching = true;
    try {
      const resp = await fetch('/api/all');
      if (!resp.ok) {
        throw new Error(`HTTP error ${resp.status} (${resp.statusText})`);
      }
      const data = await resp.json();
      globalAlert.style.display = 'none';
      liveDot.classList.remove('error');
      render(data);
    } catch (err) {
      console.error('Failed to fetch lizard telemetry:', err);
      lastUpdated.textContent = 'Connection error';
      liveDot.classList.add('error');
      globalAlert.style.display = 'block';
      globalAlert.innerHTML = `<strong>Backend Communication Error:</strong> Unable to connect to Lizard API (${escapeHtml(err.message)}). Ensure the container is active and network ports are accessible.`;
    } finally {
      isFetching = false;
    }
  }

  // Render all dashboard sections
  function render(data) {
    const sys = data.system || {};
    const history = data.history || [];
    const containers = data.containers || [];
    const borg = data.borgmatic || {};

    globalContainers = containers;

    // Header info & Subsystem health
    hostPill.textContent = sys.hostname ? `Host: ${sys.hostname}` : 'Connected';
    const now = new Date();
    lastUpdated.textContent = `Live (2s) • ${now.toLocaleTimeString()}`;
    footerUptime.textContent = `Server Uptime: ${sys.uptime || 'N/A'}`;
    footerTime.textContent = now.toUTCString();

    updateHealthPills(sys, data.docker_error, borg);

    // 1. KPI Cards
    renderKPIs(sys, containers, data.docker_error, borg);

    // 2. Historical Charts & Alerts
    renderCharts(history, sys);

    // 3. Top Directories Table & Alerts
    renderTopDirs(sys.disk);

    // 4. Docker Containers Table & Alerts
    renderContainersTable(containers, containerSearch.value, data.docker_error);

    // 5. Borgmatic Table & Alerts
    renderBorgmatic(borg);
  }

  function updateHealthPills(sys, dockerErr, borg) {
    const hasSysErr = sys.cpu_error || (sys.memory && sys.memory.error) || (sys.disk && sys.disk.error);
    if (hasSysErr) {
      sysHealthPill.className = 'status-indicator-pill status-warn';
      sysHealthPill.textContent = 'System: Access Warning';
    } else {
      sysHealthPill.className = 'status-indicator-pill status-ok';
      sysHealthPill.textContent = 'System: Healthy';
    }

    if (dockerErr) {
      dockHealthPill.className = 'status-indicator-pill status-err';
      dockHealthPill.textContent = 'Docker: Offline / Denied';
    } else {
      dockHealthPill.className = 'status-indicator-pill status-ok';
      dockHealthPill.textContent = 'Docker: Connected';
    }

    if (borg.error) {
      if (borg.config_status === 'Not Configured') {
        borgHealthPill.className = 'status-indicator-pill status-warn';
        borgHealthPill.textContent = 'Borgmatic: Unconfigured';
      } else {
        borgHealthPill.className = 'status-indicator-pill status-err';
        borgHealthPill.textContent = `Borgmatic: ${borg.config_status || 'Error'}`;
      }
    } else {
      borgHealthPill.className = 'status-indicator-pill status-ok';
      borgHealthPill.textContent = 'Borgmatic: Synced';
    }
  }

  function renderKPIs(sys, containers, dockerErr, borg) {
    // CPU
    if (sys.cpu_error) {
      cpuCard.classList.add('card-error');
      cpuBadge.className = 'stat-badge badge-err';
      cpuBadge.textContent = 'Error';
      cpuValue.innerHTML = `<span style="font-size: 18px; color: #f87171;">Access Denied</span>`;
      cpuSub.textContent = sys.cpu_error;
      cpuProgressBar.style.width = '0%';
    } else {
      cpuCard.classList.remove('card-error');
      cpuBadge.className = 'stat-badge';
      const cpuPct = sys.cpu_percent || 0;
      cpuBadge.textContent = `${cpuPct.toFixed(1)}%`;
      cpuValue.innerHTML = `${cpuPct.toFixed(1)}<span class="unit">%</span>`;
      cpuProgressBar.style.width = `${Math.min(100, Math.max(0, cpuPct))}%`;
      cpuSub.textContent = `Uptime: ${sys.uptime || 'N/A'}`;
    }

    // Memory
    const mem = sys.memory || {};
    if (mem.error) {
      memCard.classList.add('card-error');
      memBadge.className = 'stat-badge badge-err';
      memBadge.textContent = 'Error';
      memValue.innerHTML = `<span style="font-size: 18px; color: #f87171;">Access Denied</span>`;
      memSub.textContent = mem.error;
      memProgressBar.style.width = '0%';
    } else {
      memCard.classList.remove('card-error');
      memBadge.className = 'stat-badge';
      const memPct = mem.used_percent || 0;
      memBadge.textContent = `${memPct.toFixed(1)}%`;
      memValue.innerHTML = `${memPct.toFixed(1)}<span class="unit">%</span>`;
      memProgressBar.style.width = `${Math.min(100, Math.max(0, memPct))}%`;
      memSub.textContent = `${mem.human_used || '0 B'} / ${mem.human_total || '0 B'}`;
    }

    // Disk
    const disk = sys.disk || {};
    if (disk.error) {
      diskCard.classList.add('card-error');
      diskBadge.className = 'stat-badge badge-err';
      diskBadge.textContent = 'Error';
      diskValue.innerHTML = `<span style="font-size: 18px; color: #f87171;">Disk Error</span>`;
      diskSub.textContent = disk.error;
      diskProgressBar.style.width = '0%';
    } else {
      diskCard.classList.remove('card-error');
      diskBadge.className = 'stat-badge';
      const diskPct = disk.used_percent || 0;
      diskBadge.textContent = `${diskPct.toFixed(1)}%`;
      diskValue.innerHTML = `${diskPct.toFixed(1)}<span class="unit">%</span>`;
      diskProgressBar.style.width = `${Math.min(100, Math.max(0, diskPct))}%`;
      diskSub.textContent = `${disk.human_used || '0 B'} used of ${disk.human_total || '0 B'}`;
    }

    // Containers count
    if (dockerErr) {
      dockerCard.classList.add('card-error');
      dockerCountBadge.className = 'stat-badge badge-err';
      dockerCountBadge.textContent = 'Error';
      dockerRunningCount.innerHTML = `<span style="font-size: 18px; color: #f87171;">Unavailable</span>`;
      dockerTotalSub.textContent = 'Socket access failed';
    } else {
      dockerCard.classList.remove('card-error');
      dockerCountBadge.className = 'stat-badge count-badge';
      const running = containers.filter(c => c.state === 'running').length;
      dockerCountBadge.textContent = `${running} Active`;
      dockerRunningCount.innerHTML = `${running}<span class="unit"> running</span>`;
      dockerTotalSub.textContent = `Total: ${containers.length} containers`;
    }

    // Borgmatic
    const archives = borg.all_archives || [];
    if (borg.error) {
      if (borg.config_status === 'Not Configured') {
        borgCountBadge.className = 'stat-badge';
        borgCountBadge.textContent = 'Unconfigured';
      } else {
        borgCard.classList.add('card-error');
        borgCountBadge.className = 'stat-badge badge-err';
        borgCountBadge.textContent = 'Error';
      }
      borgArchiveCount.innerHTML = `0<span class="unit"> snapshots</span>`;
      borgLatestSub.textContent = borg.error.length > 50 ? borg.error.slice(0, 50) + '...' : borg.error;
    } else {
      borgCard.classList.remove('card-error');
      borgCountBadge.className = 'stat-badge count-badge';
      borgCountBadge.textContent = `${archives.length} Backups`;
      borgArchiveCount.innerHTML = `${archives.length}<span class="unit"> snapshots</span>`;
      if (archives.length > 0) {
        const latest = archives[0];
        borgLatestSub.textContent = `Latest: ${latest.name} (${latest.human_deduplicated})`;
      } else {
        borgLatestSub.textContent = 'No archives found';
      }
    }
  }

  // Pure SVG Interactive Time-Series Charts
  function renderCharts(history, sys) {
    if (sys.cpu_error || (sys.memory && sys.memory.error)) {
      chartAlert.style.display = 'block';
      chartAlert.innerHTML = `
        <div class="alert-header">⚠️ System Metric Access Notice</div>
        <div>${escapeHtml(sys.cpu_error || sys.memory.error)}. Verify the container has <code>pid: "host"</code> or access to <code>/proc</code>.</div>
      `;
    } else {
      chartAlert.style.display = 'none';
    }

    if (!history || history.length === 0) return;

    const latest = history[history.length - 1];
    chartCpuCurrent.textContent = `${latest.cpu_percent.toFixed(1)}%`;
    chartMemCurrent.textContent = `${latest.mem_percent.toFixed(1)}%`;
    chartDiskCurrent.textContent = `${latest.disk_percent.toFixed(1)}%`;

    drawSvgChart(cpuChartSvg, cpuTooltip, history, p => p.cpu_percent, '#f59e0b', 'rgba(245, 158, 11, 0.25)', '%');
    drawSvgChart(memChartSvg, memTooltip, history, p => p.mem_percent, '#38bdf8', 'rgba(56, 189, 248, 0.25)', '%');
    drawSvgChart(diskChartSvg, diskTooltip, history, p => p.disk_percent, '#a855f7', 'rgba(168, 85, 247, 0.25)', '%');
  }

  function drawSvgChart(svg, tooltip, data, valFn, strokeColor, fillColor, unit) {
    const width = 600;
    const height = 200;
    const padTop = 20;
    const padBottom = 25;
    const padLeft = 40;
    const padRight = 15;

    const chartW = width - padLeft - padRight;
    const chartH = height - padTop - padBottom;

    let points = [];
    const count = data.length;

    for (let i = 0; i < count; i++) {
      const x = count === 1 ? padLeft + chartW / 2 : padLeft + (i / (count - 1)) * chartW;
      const v = Math.min(100, Math.max(0, valFn(data[i])));
      const y = padTop + chartH - (v / 100) * chartH;
      points.push({ x, y, val: v, time: new Date(data[i].timestamp) });
    }

    let svgContent = '';

    const guides = [0, 25, 50, 75, 100];
    guides.forEach(g => {
      const gy = padTop + chartH - (g / 100) * chartH;
      svgContent += `<line x1="${padLeft}" y1="${gy}" x2="${width - padRight}" y2="${gy}" stroke="rgba(255,255,255,0.06)" stroke-width="1" />`;
      svgContent += `<text x="${padLeft - 8}" y="${gy + 4}" fill="#64748b" font-size="10" text-anchor="end">${g}%</text>`;
    });

    if (points.length > 0) {
      let areaD = `M ${points[0].x} ${padTop + chartH}`;
      for (const p of points) {
        areaD += ` L ${p.x.toFixed(1)} ${p.y.toFixed(1)}`;
      }
      areaD += ` L ${points[points.length - 1].x} ${padTop + chartH} Z`;

      let lineD = `M ${points[0].x.toFixed(1)} ${points[0].y.toFixed(1)}`;
      for (let i = 1; i < points.length; i++) {
        lineD += ` L ${points[i].x.toFixed(1)} ${points[i].y.toFixed(1)}`;
      }

      const gradId = `grad-${strokeColor.replace('#', '')}`;
      svgContent += `
        <defs>
          <linearGradient id="${gradId}" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stop-color="${strokeColor}" stop-opacity="0.35" />
            <stop offset="100%" stop-color="${strokeColor}" stop-opacity="0.0" />
          </linearGradient>
        </defs>
        <path d="${areaD}" fill="url(#${gradId})" />
        <path d="${lineD}" fill="none" stroke="${strokeColor}" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" />
      `;

      const last = points[points.length - 1];
      svgContent += `<circle cx="${last.x.toFixed(1)}" cy="${last.y.toFixed(1)}" r="4" fill="${strokeColor}" stroke="#0f172a" stroke-width="2" />`;
    }

    svg.innerHTML = svgContent;

    svg.onmousemove = function (e) {
      if (points.length === 0) return;
      const rect = svg.getBoundingClientRect();
      const mouseX = ((e.clientX - rect.left) / rect.width) * width;

      let closest = points[0];
      let minDiff = Math.abs(mouseX - closest.x);
      for (let i = 1; i < points.length; i++) {
        const diff = Math.abs(mouseX - points[i].x);
        if (diff < minDiff) {
          minDiff = diff;
          closest = points[i];
        }
      }

      const tooltipLeft = (closest.x / width) * rect.width;
      const tooltipTop = (closest.y / height) * rect.height;

      tooltip.style.opacity = '1';
      tooltip.style.left = `${Math.min(rect.width - 120, Math.max(10, tooltipLeft - 50))}px`;
      tooltip.style.top = `${Math.max(10, tooltipTop - 45)}px`;
      tooltip.innerHTML = `<strong>${closest.val.toFixed(1)}${unit}</strong><br><span style="color:#94a3b8">${closest.time.toLocaleTimeString()}</span>`;
    };

    svg.onmouseleave = function () {
      tooltip.style.opacity = '0';
    };
  }

  // Top Directories & Storage
  function renderTopDirs(disk) {
    if (!disk) return;

    if (disk.error) {
      diskSummaryDesc.textContent = `Disk error encountered`;
      diskAlert.style.display = 'block';
      diskAlert.className = 'alert-box alert-danger';
      diskAlert.innerHTML = `<strong>Disk Access Error:</strong> ${escapeHtml(disk.error)}. Check directory mount path in <code>LIZARD_DISK_DIRS</code> and <code>LIZARD_HOST_ROOT</code>.`;
      topDirsBody.innerHTML = `<tr><td colspan="4" class="empty-state" style="color: #f87171;">Disk inaccessible: ${escapeHtml(disk.error)}</td></tr>`;
      return;
    }

    diskSummaryDesc.textContent = `Mount ${disk.path || '/'}: ${disk.human_used || '0 B'} used of ${disk.human_total || '0 B'} (${(disk.used_percent || 0).toFixed(1)}% full)`;

    if (disk.top_dirs_warning) {
      diskAlert.style.display = 'block';
      diskAlert.className = 'alert-box alert-warning';
      const notes = (disk.permission_notes && disk.permission_notes.length > 0)
        ? ` (${disk.permission_notes.join(', ')})`
        : '';
      diskAlert.innerHTML = `
        <div class="alert-header">⚠️ Directory Permission Warning</div>
        <div>${escapeHtml(disk.top_dirs_warning)}${escapeHtml(notes)}. To scan all restricted host directories (like <code>/var/lib/docker</code> or <code>/root</code>), run the container with <code>user: "0:0"</code>.</div>
      `;
    } else {
      diskAlert.style.display = 'none';
    }

    const topDirs = disk.top_dirs || [];
    if (topDirs.length === 0) {
      topDirsBody.innerHTML = `<tr><td colspan="4" class="empty-state">No directory statistics available or scan in progress...</td></tr>`;
      return;
    }

    let rowsHtml = '';
    topDirs.forEach(dir => {
      const pct = (dir.percent || 0).toFixed(1);
      const fileCountStr = dir.file_count ? dir.file_count.toLocaleString() : 'N/A';
      rowsHtml += `
        <tr>
          <td><span class="code-pill">${escapeHtml(dir.path)}</span></td>
          <td><strong>${escapeHtml(dir.human_size)}</strong></td>
          <td>
            <div class="table-bar-container">
              <div class="table-mini-bar">
                <div class="table-mini-fill" style="width: ${Math.min(100, pct)}%; background: #a855f7;"></div>
              </div>
              <span class="table-bar-pct">${pct}%</span>
            </div>
          </td>
          <td><span class="dim-sub">${fileCountStr} files</span></td>
        </tr>
      `;
    });
    topDirsBody.innerHTML = rowsHtml;
  }

  // Docker Containers Table
  function renderContainersTable(containers, filterQuery, dockerErr) {
    if (dockerErr) {
      dockerAlert.style.display = 'block';
      dockerAlert.innerHTML = `
        <div class="alert-header">🚫 Docker Engine Access Error</div>
        <div>${escapeHtml(dockerErr)}</div>
        <div style="margin-top: 8px;">
          <strong>Resolution:</strong> In <code>docker-compose.yml</code>, ensure:
          <br>1. Socket is mounted: <code class="code-cmd">- /var/run/docker.sock:/var/run/docker.sock</code>
          <br>2. Container has permission to access the socket: <code class="code-cmd">user: "0:0"</code>
        </div>
      `;
      containersBody.innerHTML = `<tr><td colspan="6" class="empty-state" style="color: #f87171;">Docker socket unavailable: ${escapeHtml(dockerErr)}</td></tr>`;
      return;
    }

    dockerAlert.style.display = 'none';

    if (!containers || containers.length === 0) {
      containersBody.innerHTML = `<tr><td colspan="6" class="empty-state">No active Docker containers found</td></tr>`;
      return;
    }

    let filtered = containers;
    if (filterQuery && filterQuery.trim() !== '') {
      const q = filterQuery.toLowerCase().trim();
      filtered = containers.filter(c =>
        (c.name && c.name.toLowerCase().includes(q)) ||
        (c.image && c.image.toLowerCase().includes(q)) ||
        (c.status && c.status.toLowerCase().includes(q))
      );
    }

    if (filtered.length === 0) {
      containersBody.innerHTML = `<tr><td colspan="6" class="empty-state">No containers matching "${escapeHtml(filterQuery)}"</td></tr>`;
      return;
    }

    let rowsHtml = '';
    filtered.forEach(c => {
      const isRunning = c.state === 'running';
      const stateBadge = isRunning
        ? `<span class="badge badge-success">running</span>`
        : `<span class="badge badge-neutral">${escapeHtml(c.state || 'stopped')}</span>`;

      const cpuPct = (c.cpu_percent || 0).toFixed(1);
      const memPct = (c.mem_percent || 0).toFixed(1);

      rowsHtml += `
        <tr>
          <td>
            <strong>${escapeHtml(c.name || c.id)}</strong>
            <span class="dim-sub">${escapeHtml(c.id)}</span>
          </td>
          <td><span class="code-pill">${escapeHtml(c.image || 'N/A')}</span></td>
          <td>${stateBadge}</td>
          <td>${escapeHtml(c.uptime || c.status || 'N/A')}</td>
          <td>
            <div class="table-bar-container">
              <div class="table-mini-bar" style="width: 80px;">
                <div class="table-mini-fill" style="width: ${Math.min(100, cpuPct)}%; background: #f59e0b;"></div>
              </div>
              <span class="table-bar-pct">${cpuPct}%</span>
            </div>
          </td>
          <td>
            <div><strong>${escapeHtml(c.human_mem_usage || '0 B')}</strong> <span class="dim-sub">/ ${escapeHtml(c.human_mem_limit || '0 B')} (${memPct}%)</span></div>
            <div class="table-mini-bar" style="margin-top: 4px;">
              <div class="table-mini-fill" style="width: ${Math.min(100, memPct)}%; background: #38bdf8;"></div>
            </div>
          </td>
        </tr>
      `;
    });

    containersBody.innerHTML = rowsHtml;
  }

  // Borgmatic Table & Diagnostics
  function renderBorgmatic(borg) {
    if (borg.error) {
      const isUnconfigured = borg.config_status === 'Not Configured';
      borgAlert.style.display = 'block';
      borgAlert.className = isUnconfigured ? 'alert-box alert-info' : 'alert-box alert-danger';
      
      let guidance = '';
      if (isUnconfigured) {
        guidance = `
          <div class="alert-header">ℹ️ Borgmatic Configuration Needed</div>
          <div>Borgmatic is not yet configured for Lizard. To enable Borgmatic metrics, set one of the following environment variables:</div>
          <div style="margin-top: 8px;">
            • Command execution: <code class="code-cmd">LIZARD_BORGMATIC_CMD="docker exec -i borgmatic borgmatic info --json"</code><br>
            • Or JSON file path: <code class="code-cmd">LIZARD_BORGMATIC_FILE="/var/log/borgmatic/info.json"</code>
          </div>
        `;
      } else {
        guidance = `
          <div class="alert-header">⚠️ Borgmatic Error (${escapeHtml(borg.config_status || 'Error')})</div>
          <div>${escapeHtml(borg.error)}</div>
        `;
      }

      borgAlert.innerHTML = guidance;
      borgSubtitle.textContent = borg.config_status ? `Status: ${borg.config_status}` : 'No backups available';
      borgBody.innerHTML = `<tr><td colspan="8" class="empty-state" style="${isUnconfigured ? '' : 'color: #f87171;'}">${escapeHtml(borg.error)}</td></tr>`;
      return;
    }

    borgAlert.style.display = 'none';

    const archives = borg.all_archives || [];
    if (archives.length === 0) {
      borgBody.innerHTML = `<tr><td colspan="8" class="empty-state">No repository archives found in Borgmatic output</td></tr>`;
      borgSubtitle.textContent = 'Repository is empty';
      return;
    }

    borgSubtitle.textContent = `${archives.length} archive snapshot(s) found across ${borg.repositories ? borg.repositories.length : 1} repository`;

    let rowsHtml = '';
    archives.forEach(arch => {
      const fileCountStr = arch.nfiles ? arch.nfiles.toLocaleString() : 'N/A';
      
      let dedupRatio = '';
      if (arch.original_size > 0 && arch.deduplicated_size > 0) {
        const ratio = (arch.deduplicated_size / arch.original_size) * 100;
        dedupRatio = `<span class="dim-sub">${ratio.toFixed(1)}% of original</span>`;
      }

      rowsHtml += `
        <tr>
          <td>
            <strong>${escapeHtml(arch.name)}</strong>
          </td>
          <td><span class="badge badge-indigo">${escapeHtml(arch.repo_location || 'default')}</span></td>
          <td>
            ${escapeHtml(arch.start_time || 'N/A')}
            <span class="dim-sub">${formatRelativeTime(arch.start)}</span>
          </td>
          <td><span class="badge badge-neutral">${escapeHtml(arch.duration || 'N/A')}</span></td>
          <td>${escapeHtml(arch.human_original || '0 B')}</td>
          <td>${escapeHtml(arch.human_compressed || '0 B')}</td>
          <td>
            <strong>${escapeHtml(arch.human_deduplicated || '0 B')}</strong>
            ${dedupRatio}
          </td>
          <td>${fileCountStr}</td>
        </tr>
      `;
    });

    borgBody.innerHTML = rowsHtml;
  }

  function formatRelativeTime(dateStr) {
    if (!dateStr) return '';
    const date = new Date(dateStr);
    if (isNaN(date.getTime())) return '';

    const diffSec = Math.floor((new Date() - date) / 1000);
    if (diffSec < 60) return 'just now';
    const diffMin = Math.floor(diffSec / 60);
    if (diffMin < 60) return `${diffMin}m ago`;
    const diffHours = Math.floor(diffMin / 60);
    if (diffHours < 24) return `${diffHours}h ago`;
    const diffDays = Math.floor(diffHours / 24);
    return `${diffDays}d ago`;
  }

  function escapeHtml(str) {
    if (!str) return '';
    return String(str)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#039;');
  }

  document.addEventListener('DOMContentLoaded', init);
})();

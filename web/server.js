// VPS Control — page Server : logique complète.
// Chargé après app.js (qui fournit api(), toast(), escapeHtml(), etc.).

// ============ STATE ============
let currentApp = null;
let currentAppPermissions = [];
let appFilesPath = '';
let consoleEventSource = null;
let consoleAutoScroll = true;
let selectedFiles = new Set();
let liveStatsTimer = null;

const params = new URLSearchParams(location.search);
const appId = params.get('id');
const activeTab = params.get('tab') || 'console';

if (!appId) location.href = '/servers.html';

function hasPerm(perm) {
  if (!currentAppPermissions) return false;
  if (currentAppPermissions.includes('*')) return true;
  if (currentAppPermissions.includes(perm)) return true;
  if (perm === 'files.read' && currentAppPermissions.includes('files.write')) return true;
  return false;
}

// ============ SIDEBAR MOBILE ============
const sidebar = document.getElementById('sidebar');
const overlay = document.getElementById('sidebar-overlay');
const appSidebar = document.getElementById('app-sidebar');
const appOverlay = document.getElementById('app-sidebar-overlay');

document.getElementById('btn-menu')?.addEventListener('click', () => {
  sidebar.classList.remove('hidden');
  sidebar.classList.remove('-translate-x-full');
  overlay.classList.remove('hidden');
});
document.getElementById('btn-close-sidebar')?.addEventListener('click', () => {
  sidebar.classList.add('-translate-x-full');
  overlay.classList.add('hidden');
});
overlay?.addEventListener('click', () => {
  sidebar.classList.add('-translate-x-full');
  overlay.classList.add('hidden');
});
document.getElementById('btn-menu-app')?.addEventListener('click', () => {
  appSidebar.classList.remove('hidden');
  appSidebar.classList.remove('-translate-x-full');
  appOverlay.classList.remove('hidden');
});
appOverlay?.addEventListener('click', () => {
  appSidebar.classList.add('-translate-x-full');
  appOverlay.classList.add('hidden');
});

// ============ NAV PRINCIPALE ============
function buildNav(active, isAdmin) {
  const items = [
    { href: '/dashboard.html', icon: 'fa-gauge-high', label: 'Dashboard', id: 'dashboard' },
    { href: '/servers.html', icon: 'fa-server', label: 'My apps', id: 'servers' },
    { href: isAdmin ? '/deploy.html' : '/deploy-user.html', icon: 'fa-rocket', label: 'Deploy', id: 'deploy' },
    { href: '/servers-admin.html', icon: 'fa-layer-group', label: 'Servers', id: 'servers-admin', admin: true },
    { href: '/files.html', icon: 'fa-folder', label: 'Files', id: 'files', admin: true },
    { href: '/services.html', icon: 'fa-cube', label: 'Services', id: 'services', admin: true },
    { href: '/databases.html', icon: 'fa-database', label: 'Databases', id: 'databases', admin: true },
    { href: '/users.html', icon: 'fa-users', label: 'Users', id: 'users', admin: true },
    { href: '/activity.html', icon: 'fa-clock-rotate-left', label: 'Activity', id: 'activity', admin: true },
    { href: '/tokens.html', icon: 'fa-key', label: 'API Tokens', id: 'tokens' },
    { href: '/system.html', icon: 'fa-gear', label: 'System', id: 'system', admin: true },
  ];
  document.getElementById('nav-container').innerHTML = items.filter(i => !i.admin || isAdmin).map(i =>
    `<a href="${i.href}" class="flex items-center gap-3 px-3 py-2.5 rounded-lg text-sm font-medium ${i.id === active ? 'bg-accent/10 text-accent' : 'text-gray-400 hover:bg-panel2 hover:text-white transition'}"><i class="fas ${i.icon} w-4"></i><span>${i.label}</span></a>`
  ).join('');
}

// ============ APP TABS ============
const APP_TABS = [
  { id: 'console', icon: 'fa-terminal', label: 'Console', perm: 'console' },
  { id: 'files', icon: 'fa-folder', label: 'Files', perm: 'files.read' },
  { id: 'env', icon: 'fa-wrench', label: 'Environment', perm: 'settings' },
  { id: 'allocations', icon: 'fa-plug', label: 'Allocations', perm: 'settings' },
  { id: 'domains', icon: 'fa-globe', label: 'Domains', perm: 'settings' },
  { id: 'limits', icon: 'fa-bolt', label: 'Limits', perm: 'settings' },
  { id: 'backups', icon: 'fa-database', label: 'Backups', perm: 'backup' },
  { id: 'schedules', icon: 'fa-clock', label: 'Schedules', perm: 'settings' },
  { id: 'settings', icon: 'fa-gear', label: 'Settings', perm: 'settings' },
  { id: 'subusers', icon: 'fa-users-gear', label: 'Collaborators', perm: 'settings' },
];

function buildAppNav(active) {
  const visible = APP_TABS.filter(t => hasPerm(t.perm));
  document.getElementById('app-nav-container').innerHTML = visible.map(t =>
    `<a href="#" data-tab="${t.id}" class="app-nav-item flex items-center gap-3 px-3 py-2.5 rounded-lg text-sm font-medium ${t.id === active ? 'bg-accent/10 text-accent' : 'text-gray-400 hover:bg-panel2 hover:text-white transition'}"><i class="fas ${t.icon} w-4"></i><span>${t.label}</span></a>`
  ).join('');
  document.querySelectorAll('.app-nav-item').forEach(el => {
    el.addEventListener('click', (e) => {
      e.preventDefault();
      activateTab(el.dataset.tab);
      if (window.innerWidth < 1024) {
        appSidebar.classList.add('-translate-x-full');
        appOverlay.classList.add('hidden');
      }
    });
  });
  document.querySelectorAll('#bottom-tabs button').forEach(btn => {
    const tab = APP_TABS.find(t => t.id === btn.dataset.tab);
    btn.style.display = (tab && hasPerm(tab.perm)) ? '' : 'none';
    btn.classList.toggle('text-accent', btn.dataset.tab === active);
    btn.classList.toggle('text-gray-500', btn.dataset.tab !== active);
  });
}

document.querySelectorAll('#bottom-tabs button').forEach(btn => {
  btn.addEventListener('click', () => activateTab(btn.dataset.tab));
});

function activateTab(tab) {
  const meta = APP_TABS.find(t => t.id === tab);
  if (!meta || !hasPerm(meta.perm)) {
    const first = APP_TABS.find(t => hasPerm(t.perm));
    tab = first ? first.id : 'console';
  }
  document.querySelectorAll('.tab-content').forEach(el => el.classList.add('hidden'));
  const el = document.getElementById('tab-' + tab);
  if (el) el.classList.remove('hidden');
  buildAppNav(tab);
  const titleMap = { console: 'Console', files: 'Files', env: 'Environment', allocations: 'Ports', domains: 'Domains', limits: 'Limits', backups: 'Backups', schedules: 'Schedules', settings: 'Settings', subusers: 'Collaborators' };
  document.getElementById('mobile-title').textContent = titleMap[tab] || 'Server';

  if (tab !== 'console' && consoleEventSource) { consoleEventSource.close(); consoleEventSource = null; }
  if (tab !== 'console' && liveStatsTimer) { clearInterval(liveStatsTimer); liveStatsTimer = null; }

  const url = new URL(location.href);
  url.searchParams.set('tab', tab);
  history.replaceState({}, '', url);

  if (tab === 'console') initConsole();
  if (tab === 'files') loadAppFiles('');
  if (tab === 'env') loadEnv();
  if (tab === 'allocations') loadAllocations();
  if (tab === 'domains') loadDomains();
  if (tab === 'limits') loadLimits();
  if (tab === 'backups') loadBackups();
  if (tab === 'schedules') loadSchedules();
  if (tab === 'settings') loadAppSettings();
  if (tab === 'subusers') loadSubusers();
}

// ============ INIT ============
(async function() {
  const me = await requireAuth();
  if (!me) return;
  window.currentUser = me;
  document.getElementById('current-user').textContent = `${me.username} (${me.role})`;
  buildNav('servers', me.role === 'admin');
  document.getElementById('btn-logout').addEventListener('click', async () => {
    await api('/api/logout', { method: 'POST' }).catch(() => {});
    location.href = '/index.html';
  });
  await loadApp();
})();

async function loadApp() {
  try {
    const res = await api(`/api/deployments/${appId}`);
    currentApp = res.deployment;
    currentAppPermissions = res.permissions || [];

    document.getElementById('app-name').textContent = currentApp.name;
    const sl = stackLabel(currentApp.stack, currentApp);
    document.getElementById('app-stack').textContent = (currentApp.stack === 'auto' || currentApp.status === 'draft') ? 'Draft' : sl.base + ' · ' + sl.ver;
    document.getElementById('app-domain').textContent = currentApp.status === 'draft' ? 'Not deployed yet' : `http://${location.hostname}:${currentApp.port}`;
    document.getElementById('app-role').textContent = currentApp.ownerId === window.currentUser.id ? 'Owner' : 'Collaborator';

    const subdirEl = document.getElementById('app-subdir');
    if (currentApp.appSubdir) {
      subdirEl.textContent = `📁 ${currentApp.appSubdir}/`;
      subdirEl.title = `App subdirectory: ${currentApp.appSubdir}`;
    } else {
      subdirEl.textContent = '📁 (repo root)';
    }

    const serverEl = document.getElementById('app-server');
    if (res.serverName) {
      serverEl.textContent = `🖥️ ${res.serverName}`;
      serverEl.title = `Server: ${res.serverName}`;
    } else {
      serverEl.textContent = '';
    }

    if (currentApp.status === 'draft') {
      document.getElementById('draft-banner').classList.remove('hidden');
      document.getElementById('app-status').textContent = 'Draft';
      document.getElementById('app-status').className = 'inline-block px-2 py-0.5 rounded-md text-[10px] font-semibold bg-amber-light text-amber';
    } else if (currentApp.status === 'error') {
      showErrorBanner(currentApp.lastError, currentApp.lastErrorAt);
      document.getElementById('app-status').textContent = 'Error';
      document.getElementById('app-status').className = 'inline-block px-2 py-0.5 rounded-md text-[10px] font-semibold bg-danger/15 text-danger';
    } else if (currentApp.status === 'running') {
      document.getElementById('app-status').textContent = 'Running';
      document.getElementById('app-status').className = 'inline-block px-2 py-0.5 rounded-md text-[10px] font-semibold bg-accent/15 text-accent';
    }

    activateTab(activeTab);
  } catch (err) {
    toast(err.message, true);
    location.href = '/servers.html';
  }
}

// ============ ERROR BANNER ============
function showErrorBanner(lastError, at) {
  const banner = document.getElementById('error-banner');
  if (!lastError) { banner.classList.add('hidden'); return; }
  banner.classList.remove('hidden');
  document.getElementById('error-content').textContent = lastError;
  document.getElementById('error-time').textContent = at ? 'Survenu le ' + new Date(at).toLocaleString() : '';
}
function hideErrorBanner() { document.getElementById('error-banner').classList.add('hidden'); }
document.getElementById('btn-dismiss-error').addEventListener('click', hideErrorBanner);
document.getElementById('btn-go-console').addEventListener('click', () => { hideErrorBanner(); activateTab('console'); });

// ============ DRAFT DEPLOY ============
document.getElementById('btn-deploy-draft').addEventListener('click', async () => {
  if (!confirm('Build and deploy this app now?')) return;
  const btn = document.getElementById('btn-deploy-draft');
  btn.disabled = true;
  btn.innerHTML = '<i class="fas fa-spinner fa-spin mr-2"></i>Deploying...';
  try {
    await api(`/api/deployments/${appId}/deploy`, { method: 'POST' });
    toast('App deployed!');
    setTimeout(() => location.reload(), 800);
  } catch (err) {
    btn.disabled = false;
    btn.innerHTML = '<i class="fas fa-rocket mr-2"></i>Deploy now';
    showErrorBanner(err.message);
    toast(err.message, true);
  }
});

// ============ LIVE STATS ============
async function refreshLiveStats() {
  try {
    const s = await api(`/api/deployments/${appId}/stats/live`);
    document.getElementById('live-cpu').textContent = s.cpuPercent.toFixed(1) + '%';
    document.getElementById('live-cpu-bar').style.width = Math.min(100, s.cpuPercent) + '%';
    const ramPct = s.memoryMax > 0 ? (s.memoryUsed / s.memoryMax) * 100 : 0;
    document.getElementById('live-ram').textContent = formatBytes(s.memoryUsed) + (s.memoryMax > 0 ? ' / ' + formatBytes(s.memoryMax) : '');
    document.getElementById('live-ram-bar').style.width = Math.min(100, ramPct) + '%';
    document.getElementById('live-netrx').textContent = formatBytes(s.netRx);
    document.getElementById('live-nettx').textContent = formatBytes(s.netTx);
    document.getElementById('live-pids').textContent = String(s.pids);
  } catch (_) {}
}

// ============ CONSOLE ============
const CONSOLE_TAG = 'vpscontrol-deamon';

async function initConsole() {
  const out = document.getElementById('console-output');
  out.innerHTML = '';
  await refreshLiveStats();
  if (liveStatsTimer) clearInterval(liveStatsTimer);
  liveStatsTimer = setInterval(refreshLiveStats, 3000);
  try {
    const res = await api(`/api/deployments/${appId}/console`);
    const isRunning = res.status === 'running';
    document.getElementById('console-status-text').textContent = res.status || 'unknown';
    document.getElementById('console-status-dot').className = 'w-2 h-2 rounded-full ' + (isRunning ? 'bg-accent' : 'bg-danger');

    if (res.lastParsed) {
      showErrorBanner(res.lastError, res.finishedAt);
      window._lastParsedError = res.lastParsed;
      const btn = document.getElementById('btn-edit-error');
      btn.classList.remove('hidden');
      btn.onclick = () => {
        const e = window._lastParsedError;
        if (!e) return;
        openInEditor(appId, e.file, e.line, e.column || 0, e.message || '');
      };
    } else if (res.lastError) {
      showErrorBanner(res.lastError, res.finishedAt);
    }

    if (isRunning && res.logs && res.logs.trim()) {
      res.logs.split('\n').forEach(line => {
        if (!line.trim()) return;
        const isErr = /error|exception|fatal|failed|crash|traceback/i.test(line);
        appendConsoleLine('', line, isErr ? 'error-line' : '', res.parsedErrors);
      });
    }
  } catch (err) { appendConsoleLine('[error]', err.message, 'muted'); }
  if (document.getElementById('console-status-dot').classList.contains('bg-accent')) {
    startConsoleStream();
  }
}

function startConsoleStream() {
  if (consoleEventSource) { consoleEventSource.close(); consoleEventSource = null; }
  try {
    consoleEventSource = new EventSource(`/api/deployments/${appId}/console/stream`);
    consoleEventSource.onopen = () => appendConsoleLine(CONSOLE_TAG, 'Stream connected.', 'muted');
    consoleEventSource.onmessage = (e) => {
      if (!e.data) return;
      const isErr = /error|exception|fatal|failed|crash|traceback/i.test(e.data);
      appendConsoleLine('', e.data, isErr ? 'error-line' : '');
    };
    consoleEventSource.onerror = () => appendConsoleLine(CONSOLE_TAG, 'Stream disconnected — retrying...', 'muted');
  } catch (err) { appendConsoleLine('[error]', 'Stream error: ' + err.message, 'muted'); }
}

function appendConsoleLine(tag, text, cls = '', parsedErrors = null) {
  const out = document.getElementById('console-output');
  const line = document.createElement('div');
  line.className = 'console-line ' + cls;

  let inner = '';
  if (tag) inner += `<span class="console-tag">${escapeHtml(tag)}</span> `;
  inner += `<span class="flex-1 break-all">${escapeHtml(text)}</span>`;

  if (parsedErrors && Array.isArray(parsedErrors)) {
    const match = parsedErrors.find(pe => text.includes(pe.raw) || text.includes(pe.file));
    if (match) {
      inner += `<button class="edit-btn text-xs px-2 py-0.5 rounded-md bg-accent/20 border border-accent/40 text-accent hover:bg-accent hover:text-slate-900 transition whitespace-nowrap ml-2" title="Open in editor">
        <i class="fas fa-pen-to-square mr-1"></i>Line ${match.line}
      </button>`;
    }
  }

  line.innerHTML = inner;

  const btn = line.querySelector('.edit-btn');
  if (btn && parsedErrors) {
    const match = parsedErrors.find(pe => text.includes(pe.raw) || text.includes(pe.file));
    btn.addEventListener('click', () => {
      openInEditor(appId, match.file, match.line, match.column || 0, match.message || '');
    });
  }

  out.appendChild(line);
  if (consoleAutoScroll) out.scrollTop = out.scrollHeight;
}

// Power buttons
document.querySelectorAll('[data-power]').forEach(btn => {
  btn.addEventListener('click', async () => {
    if (!currentApp) return;
    const action = btn.dataset.power;
    appendConsoleLine(CONSOLE_TAG, `Sending "${action}" to container...`, 'muted');
    btn.disabled = true;
    const oldHTML = btn.innerHTML;
    btn.innerHTML = '<i class="fas fa-spinner fa-spin mr-1"></i>' + action.charAt(0).toUpperCase() + action.slice(1);
    try {
      const res = await api(`/api/deployments/${appId}/power/${action}`, { method: 'POST' });
      if (res.mode === 'fast') {
        appendConsoleLine(CONSOLE_TAG, `Restart fast — code NOT rebuilt.`, 'muted');
      } else if (res.trigger) {
        appendConsoleLine(CONSOLE_TAG, `Rebuilt via "${res.trigger}" (stack: ${res.stack || 'unknown'}).`, 'muted');
      }
      toast(`Container ${action} done`);
      hideErrorBanner();
      setTimeout(initConsole, 800);
    } catch (err) {
      toast(err.message, true);
      appendConsoleLine('[error]', err.message, 'muted');
    } finally {
      btn.disabled = false;
      btn.innerHTML = oldHTML;
    }
  });
});

async function sendConsoleCommand() {
  const input = document.getElementById('console-command');
  const cmd = input.value.trim();
  if (!cmd) return;
  appendConsoleLine('$', cmd, 'cmd');
  input.value = '';
  try {
    const res = await api(`/api/deployments/${appId}/console/exec`, { method: 'POST', body: JSON.stringify({ command: cmd }) });
    if (res.output) {
      res.output.split('\n').forEach(line => {
        if (!line) return;
        const isErr = /error|exception|fatal|failed|crash/i.test(line);
        appendConsoleLine('', line, isErr ? 'error-line' : '');
      });
    }
    if (res.exitCode !== 0 && res.exitCode !== undefined) appendConsoleLine('[exit]', String(res.exitCode), 'muted');
  } catch (err) { appendConsoleLine('[error]', err.message, 'muted'); }
}

document.getElementById('console-command').addEventListener('keydown', (e) => {
  if (e.key === 'Enter') { e.preventDefault(); sendConsoleCommand(); }
});
document.getElementById('btn-console-send').addEventListener('click', sendConsoleCommand);
document.getElementById('btn-console-clear').addEventListener('click', () => { document.getElementById('console-output').innerHTML = ''; });
document.getElementById('btn-console-autoscroll').addEventListener('click', (e) => {
  consoleAutoScroll = !consoleAutoScroll;
  e.currentTarget.classList.toggle('text-accent', consoleAutoScroll);
  e.currentTarget.classList.toggle('text-gray-500', !consoleAutoScroll);
  if (consoleAutoScroll) { const out = document.getElementById('console-output'); out.scrollTop = out.scrollHeight; }
});

// ============ FILES ============
async function loadAppFiles(path) {
  appFilesPath = path || '';
  selectedFiles.clear();
  updateSelectionBar();
  renderAppBreadcrumb();
  const tbody = document.getElementById('app-files-table');
  tbody.innerHTML = '<tr><td colspan="5" class="text-center text-gray-500 py-6">Loading...</td></tr>';
  try {
    const entries = await api(`/api/deployments/${appId}/files?path=${encodeURIComponent(appFilesPath)}`);
    if (entries.length === 0) {
      tbody.innerHTML = '<tr><td colspan="5" class="text-center text-gray-500 py-6">Empty folder.</td></tr>';
      return;
    }
    tbody.innerHTML = '';
    entries.forEach(entry => {
      const isArchive = /\.(zip|tar|tar\.gz|tgz)$/i.test(entry.name);
      const tr = document.createElement('tr');
      tr.className = 'border-b border-border hover:bg-panel2 transition';
      tr.innerHTML = `
        <td class="px-3 py-2"><input type="checkbox" class="file-select accent-accent w-auto" data-path="${escapeHtml(entry.path)}"></td>
        <td class="px-3 py-2">
          <button class="file-open flex items-center gap-2 text-sm text-gray-200 hover:text-accent transition text-left w-full">
            <i class="fas ${entry.isDir ? 'fa-folder text-accent' : 'fa-file text-gray-500'}"></i>
            <span class="truncate">${escapeHtml(entry.name)}</span>
          </button>
        </td>
        <td class="px-3 py-2 text-xs text-gray-500 hidden sm:table-cell">${entry.isDir ? '—' : formatSize(entry.size)}</td>
        <td class="px-3 py-2 text-xs text-gray-500 hidden md:table-cell">${escapeHtml(entry.modTime)}</td>
        <td class="px-3 py-2 text-right whitespace-nowrap"></td>`;
      tr.querySelector('.file-open').addEventListener('click', () => {
        if (entry.isDir) loadAppFiles(entry.path);
        else openInEditor(appId, entry.path);
      });
      const actions = tr.querySelector('td:last-child');
      if (!entry.isDir) {
        actions.appendChild(iconBtn('pen', 'Edit in Monaco', () => openInEditor(appId, entry.path)));
        actions.appendChild(iconBtn('download', 'Download', () => {
          window.location = `/api/deployments/${appId}/files/download?path=${encodeURIComponent(entry.path)}`;
        }));
      }
      if (isArchive && hasPerm('files.write')) actions.appendChild(iconBtn('file-zipper', 'Extract', () => extractArchive(entry.path)));
      if (hasPerm('files.write')) {
        actions.appendChild(iconBtn('i-cursor', 'Rename', () => renameFile(entry.path, entry.name)));
        actions.appendChild(iconBtn('trash', 'Delete', () => deleteFile(entry.path), true));
      }
      tbody.appendChild(tr);
    });
    document.querySelectorAll('.file-select').forEach(cb => {
      cb.addEventListener('change', () => {
        if (cb.checked) selectedFiles.add(cb.dataset.path);
        else selectedFiles.delete(cb.dataset.path);
        updateSelectionBar();
      });
    });
  } catch (err) {
    tbody.innerHTML = `<tr><td colspan="5" class="text-center text-danger py-6">${escapeHtml(err.message)}</td></tr>`;
  }
}

function iconBtn(icon, title, onClick, danger = false) {
  const b = document.createElement('button');
  b.className = `text-xs px-2 py-1.5 rounded-md bg-panel2 border transition ml-1 ${danger ? 'border-border text-gray-300 hover:border-danger hover:text-danger' : 'border-border text-gray-300 hover:border-accent hover:text-accent'}`;
  b.title = title;
  b.innerHTML = `<i class="fas fa-${icon}"></i>`;
  b.addEventListener('click', onClick);
  return b;
}

function updateSelectionBar() {
  const bar = document.getElementById('file-selection-bar');
  const count = document.getElementById('file-selection-count');
  if (selectedFiles.size > 0) {
    bar.classList.remove('hidden');
    count.textContent = `${selectedFiles.size} file(s) selected`;
  } else bar.classList.add('hidden');
}

document.getElementById('files-select-all').addEventListener('change', (e) => {
  const checked = e.target.checked;
  document.querySelectorAll('.file-select').forEach(cb => {
    cb.checked = checked;
    if (checked) selectedFiles.add(cb.dataset.path);
    else selectedFiles.delete(cb.dataset.path);
  });
  updateSelectionBar();
});

document.getElementById('btn-sel-clear').addEventListener('click', () => {
  selectedFiles.clear();
  document.querySelectorAll('.file-select').forEach(cb => cb.checked = false);
  document.getElementById('files-select-all').checked = false;
  updateSelectionBar();
});

document.getElementById('btn-sel-delete').addEventListener('click', async () => {
  if (!confirm(`Delete ${selectedFiles.size} item(s)?`)) return;
  try {
    await api(`/api/deployments/${appId}/files/delete-many`, { method: 'POST', body: JSON.stringify({ paths: Array.from(selectedFiles) }) });
    toast('Deleted');
    loadAppFiles(appFilesPath);
  } catch (err) { toast(err.message, true); }
});

document.getElementById('btn-sel-download').addEventListener('click', () => {
  if (selectedFiles.size === 1) {
    const p = Array.from(selectedFiles)[0];
    window.location = `/api/deployments/${appId}/files/download?path=${encodeURIComponent(p)}`;
    return;
  }
  downloadSelectedAsZip();
});

document.getElementById('btn-sel-download-zip').addEventListener('click', downloadSelectedAsZip);

function downloadSelectedAsZip() {
  const paths = Array.from(selectedFiles);
  if (paths.length === 0) return;
  fetch(`/api/deployments/${appId}/files/download-zip`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'same-origin',
    body: JSON.stringify({ paths }),
  }).then(async res => {
    if (!res.ok) throw new Error('Download failed');
    const blob = await res.blob();
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `${currentApp.name}-export.zip`;
    a.click();
    URL.revokeObjectURL(url);
  }).catch(err => toast(err.message, true));
}

document.getElementById('btn-sel-move').addEventListener('click', async () => {
  const dest = prompt('Move to folder (ex: src/ or .):', appFilesPath || '.');
  if (dest === null) return;
  try {
    const res = await api(`/api/deployments/${appId}/files/move`, {
      method: 'POST', body: JSON.stringify({ paths: Array.from(selectedFiles), dest }),
    });
    if (res.ok === false && res.errors) toast(`Some errors: ${res.errors.join(', ')}`, true);
    else toast('Moved');
    loadAppFiles(appFilesPath);
  } catch (err) { toast(err.message, true); }
});

document.getElementById('btn-sel-compress').addEventListener('click', async () => {
  const name = prompt('Archive name (without extension):', 'archive');
  if (!name) return;
  try {
    const res = await api(`/api/deployments/${appId}/files/compress`, {
      method: 'POST', body: JSON.stringify({ paths: Array.from(selectedFiles), output: name + '.zip', destDir: appFilesPath }),
    });
    toast(`Created ${res.output}`);
    loadAppFiles(appFilesPath);
  } catch (err) { toast(err.message, true); }
});

function renderAppBreadcrumb() {
  const container = document.getElementById('files-breadcrumb');
  const parts = appFilesPath.split('/').filter(Boolean);
  container.innerHTML = '';
  const root = document.createElement('span');
  root.className = 'cursor-pointer hover:text-accent transition';
  root.innerHTML = `<i class="fas fa-home"></i> ${currentApp.name}`;
  root.addEventListener('click', () => loadAppFiles(''));
  container.appendChild(root);
  let acc = '';
  parts.forEach(p => {
    acc += '/' + p;
    container.appendChild(document.createTextNode(' / '));
    const span = document.createElement('span');
    span.className = 'cursor-pointer hover:text-accent transition';
    span.textContent = p;
    const target = acc;
    span.addEventListener('click', () => loadAppFiles(target));
    container.appendChild(span);
  });
}

async function deleteFile(relPath) {
  if (!confirm(`Delete "${relPath}"?`)) return;
  try {
    await api(`/api/deployments/${appId}/files?path=${encodeURIComponent(relPath)}`, { method: 'DELETE' });
    toast('Deleted');
    loadAppFiles(appFilesPath);
  } catch (err) { toast(err.message, true); }
}

async function renameFile(relPath, oldName) {
  const newName = prompt('New name:', oldName);
  if (!newName || newName === oldName) return;
  const dir = relPath.includes('/') ? relPath.substring(0, relPath.lastIndexOf('/')) : '';
  const newPath = dir ? dir + '/' + newName : newName;
  try {
    await api(`/api/deployments/${appId}/files/rename`, { method: 'POST', body: JSON.stringify({ from: relPath, to: newPath }) });
    toast('Renamed');
    loadAppFiles(appFilesPath);
  } catch (err) { toast(err.message, true); }
}

async function extractArchive(relPath) {
  try {
    toast('Extracting...');
    await api(`/api/deployments/${appId}/files/extract?path=${encodeURIComponent(relPath)}`, { method: 'POST' });
    toast('Extracted');
    loadAppFiles(appFilesPath);
  } catch (err) { toast(err.message, true); }
}

document.getElementById('btn-file-new-file').addEventListener('click', async () => {
  const name = prompt('File name:');
  if (!name) return;
  const fullPath = (appFilesPath ? appFilesPath + '/' : '') + name;
  try {
    await api(`/api/deployments/${appId}/files/create?path=${encodeURIComponent(fullPath)}`, { method: 'POST' });
    toast('File created');
    loadAppFiles(appFilesPath);
  } catch (err) { toast(err.message, true); }
});

document.getElementById('btn-file-new-folder').addEventListener('click', async () => {
  const name = prompt('Folder name:');
  if (!name) return;
  const fullPath = (appFilesPath ? appFilesPath + '/' : '') + name;
  try {
    await api(`/api/deployments/${appId}/files/mkdir?path=${encodeURIComponent(fullPath)}`, { method: 'POST' });
    toast('Folder created');
    loadAppFiles(appFilesPath);
  } catch (err) { toast(err.message, true); }
});

document.getElementById('input-file-upload').addEventListener('change', async (e) => {
  const files = e.target.files;
  if (!files || files.length === 0) return;
  const fd = new FormData();
  for (const f of files) fd.append('file', f);
  try {
    const res = await api(`/api/deployments/${appId}/files/upload?path=${encodeURIComponent(appFilesPath)}`, { method: 'POST', body: fd });
    toast(`${res.saved.length} file(s) uploaded`);
    e.target.value = '';
    loadAppFiles(appFilesPath);
  } catch (err) { toast(err.message, true); e.target.value = ''; }
});

document.getElementById('input-zip-upload').addEventListener('change', async (e) => {
  const file = e.target.files[0];
  if (!file) return;
  const fd = new FormData();
  fd.append('file', file);
  try {
    toast('Uploading...');
    await api(`/api/deployments/${appId}/files/upload?path=${encodeURIComponent(appFilesPath)}`, { method: 'POST', body: fd });
    toast('Extracting...');
    const archivePath = (appFilesPath ? appFilesPath + '/' : '') + file.name;
    await api(`/api/deployments/${appId}/files/extract?path=${encodeURIComponent(archivePath)}`, { method: 'POST' });
    toast('Imported');
    e.target.value = '';
    loadAppFiles(appFilesPath);
  } catch (err) { toast(err.message, true); e.target.value = ''; }
});

// ============ ENV ============
let envVars = [];

async function loadEnv() {
  const tbody = document.getElementById('env-table');
  tbody.innerHTML = '<tr><td colspan="3" class="text-center text-gray-500 py-6">Loading...</td></tr>';
  try {
    envVars = await api(`/api/deployments/${appId}/env`) || [];
    renderEnvTable();
  } catch (err) {
    tbody.innerHTML = `<tr><td colspan="3" class="text-center text-danger py-6">${escapeHtml(err.message)}</td></tr>`;
  }
}

function renderEnvTable() {
  const tbody = document.getElementById('env-table');
  if (envVars.length === 0) {
    tbody.innerHTML = '<tr><td colspan="3" class="text-center text-gray-500 py-6">No environment variable yet.</td></tr>';
    return;
  }
  tbody.innerHTML = '';
  envVars.forEach((v, i) => {
    const isSecret = /SECRET|KEY|PASSWORD|TOKEN|PWD/i.test(v.key);
    const tr = document.createElement('tr');
    tr.className = 'border-b border-border';
    tr.innerHTML = `
      <td class="px-3 py-2"><input class="env-key mono !text-xs" data-idx="${i}" value="${escapeHtml(v.key)}"></td>
      <td class="px-3 py-2">
        <div class="flex gap-1">
          <input class="env-value mono !text-xs flex-1" data-idx="${i}" type="${isSecret ? 'password' : 'text'}" value="${escapeHtml(v.value)}">
          ${isSecret ? `<button class="env-toggle text-xs px-2 rounded-md bg-panel2 border border-border text-gray-300"><i class="fas fa-eye"></i></button>` : ''}
        </div>
      </td>
      <td class="px-3 py-2 text-right"><button class="env-remove text-xs px-2 py-1.5 rounded-md bg-panel2 border border-border text-gray-300 hover:border-danger hover:text-danger transition"><i class="fas fa-trash"></i></button></td>`;
    tbody.appendChild(tr);
  });
  document.querySelectorAll('.env-key').forEach(inp => inp.addEventListener('input', () => { envVars[+inp.dataset.idx].key = inp.value; }));
  document.querySelectorAll('.env-value').forEach(inp => inp.addEventListener('input', () => { envVars[+inp.dataset.idx].value = inp.value; }));
  document.querySelectorAll('.env-toggle').forEach(btn => {
    btn.addEventListener('click', () => {
      const inp = btn.parentElement.querySelector('.env-value');
      if (inp) inp.type = inp.type === 'password' ? 'text' : 'password';
    });
  });
  document.querySelectorAll('.env-remove').forEach(btn => {
    btn.addEventListener('click', () => {
      const row = btn.closest('tr');
      const inputs = row.querySelectorAll('input');
      const idx = +inputs[0].dataset.idx;
      envVars.splice(idx, 1);
      renderEnvTable();
    });
  });
}

document.getElementById('btn-env-add').addEventListener('click', () => { envVars.push({ key: '', value: '' }); renderEnvTable(); });

document.getElementById('btn-env-save').addEventListener('click', async () => {
  const status = document.getElementById('env-status');
  status.textContent = 'Saving...';
  try {
    const cleaned = envVars.filter(v => v.key.trim() !== '');
    await api(`/api/deployments/${appId}/env`, { method: 'PUT', body: JSON.stringify({ vars: cleaned }) });
    status.textContent = 'Saved. Restart the container to apply.';
    toast('Environment saved');
    setTimeout(() => { status.textContent = ''; }, 3000);
  } catch (err) { status.textContent = 'Error: ' + err.message; toast(err.message, true); }
});

// ============ ALLOCATIONS ============
async function loadAllocations() {
  const tbody = document.getElementById('alloc-table');
  tbody.innerHTML = '<tr><td colspan="5" class="text-center text-gray-500 py-6">Loading...</td></tr>';
  try {
    const allocs = await api(`/api/deployments/${appId}/allocations`) || [];
    if (allocs.length === 0) { tbody.innerHTML = '<tr><td colspan="5" class="text-center text-gray-500 py-6">No allocation.</td></tr>'; return; }
    tbody.innerHTML = '';
    allocs.forEach(a => {
      const tr = document.createElement('tr');
      tr.className = 'border-b border-border hover:bg-panel2 transition';
      tr.innerHTML = `
        <td class="px-4 py-3 text-xs"><code>${escapeHtml(a.port)}</code></td>
        <td class="px-4 py-3 text-sm text-gray-400 hidden sm:table-cell">${escapeHtml(a.label || '—')}</td>
        <td class="px-4 py-3">${a.primary ? '<span class="inline-block px-2 py-1 rounded-md text-xs font-semibold bg-accent/15 text-accent">Primary</span>' : ''}</td>
        <td class="px-4 py-3 text-sm text-gray-500 hidden md:table-cell">${new Date(a.createdAt).toLocaleDateString()}</td>
        <td class="px-4 py-3 text-right"></td>`;
      if (!a.primary) {
        const actions = tr.querySelector('td:last-child');
        actions.appendChild(iconBtn('trash', 'Remove', () => removeAllocation(a.id), true));
      }
      tbody.appendChild(tr);
    });
  } catch (err) {
    tbody.innerHTML = `<tr><td colspan="5" class="text-center text-danger py-6">${escapeHtml(err.message)}</td></tr>`;
  }
}

async function removeAllocation(id) {
  if (!confirm('Remove this port? The container will restart.')) return;
  try {
    await api(`/api/deployments/${appId}/allocations/${id}`, { method: 'DELETE' });
    toast('Port removed');
    loadAllocations();
  } catch (err) { toast(err.message, true); }
}

document.getElementById('btn-alloc-add').addEventListener('click', () => {
  document.getElementById('alloc-modal').classList.remove('hidden');
  document.getElementById('alloc-port').value = '';
  document.getElementById('alloc-label').value = '';
});
document.getElementById('btn-alloc-cancel').addEventListener('click', () => document.getElementById('alloc-modal').classList.add('hidden'));
document.getElementById('btn-alloc-confirm').addEventListener('click', async () => {
  const port = document.getElementById('alloc-port').value.trim();
  const label = document.getElementById('alloc-label').value.trim();
  if (!port) { toast('Port is required', true); return; }
  try {
    await api(`/api/deployments/${appId}/allocations`, { method: 'POST', body: JSON.stringify({ port, label }) });
    toast('Port added');
    document.getElementById('alloc-modal').classList.add('hidden');
    loadAllocations();
  } catch (err) { toast(err.message, true); }
});

// ============ DOMAINS ============
function sslBadge(dom) {
  if (dom.sslStatus === 'active') return '<span class="inline-block px-2 py-1 rounded-md text-xs font-semibold bg-green-500/15 text-green-400"><i class="fas fa-lock mr-1"></i>Active</span>';
  if (dom.sslStatus === 'error') return `<span class="inline-block px-2 py-1 rounded-md text-xs font-semibold bg-danger/15 text-danger" title="${escapeHtml(dom.sslError || '')}"><i class="fas fa-triangle-exclamation mr-1"></i>Error</span>`;
  return '<span class="inline-block px-2 py-1 rounded-md text-xs font-semibold bg-panel2 text-gray-400">HTTP only</span>';
}

async function loadDomains() {
  const tbody = document.getElementById('domain-table');
  tbody.innerHTML = '<tr><td colspan="5" class="text-center text-gray-500 py-6">Loading...</td></tr>';
  try {
    const domains = await api(`/api/deployments/${appId}/domains`) || [];
    if (domains.length === 0) { tbody.innerHTML = '<tr><td colspan="5" class="text-center text-gray-500 py-6">No domain yet.</td></tr>'; return; }
    tbody.innerHTML = '';
    domains.forEach(d => {
      const tr = document.createElement('tr');
      tr.className = 'border-b border-border hover:bg-panel2 transition';
      tr.innerHTML = `
        <td class="px-4 py-3 text-sm"><a href="https://${escapeHtml(d.hostname)}" target="_blank" rel="noopener" class="text-accent hover:underline">${escapeHtml(d.hostname)}</a></td>
        <td class="px-4 py-3 text-xs hidden sm:table-cell"><code>${escapeHtml(d.targetPort)}</code></td>
        <td class="px-4 py-3">${sslBadge(d)}</td>
        <td class="px-4 py-3 text-sm text-gray-500 hidden md:table-cell">${new Date(d.createdAt).toLocaleDateString()}</td>
        <td class="px-4 py-3 text-right"></td>`;
      const actions = tr.querySelector('td:last-child');
      if (d.sslStatus !== 'active') {
        actions.appendChild(iconBtn('lock', 'Request/retry SSL', () => issueDomainSSL(d.id)));
      }
      actions.appendChild(iconBtn('trash', 'Remove', () => removeDomain(d.id), true));
      tbody.appendChild(tr);
    });
  } catch (err) {
    tbody.innerHTML = `<tr><td colspan="5" class="text-center text-danger py-6">${escapeHtml(err.message)}</td></tr>`;
  }
}

async function issueDomainSSL(id) {
  try {
    toast('Requesting the certificate — this can take up to a minute...');
    await api(`/api/deployments/${appId}/domains/${id}/ssl`, { method: 'POST', body: JSON.stringify({}) });
    toast('SSL certificate active');
    loadDomains();
  } catch (err) { toast(err.message, true); loadDomains(); }
}

async function removeDomain(id) {
  if (!confirm('Remove this domain? Its Nginx config and certificate will be deleted.')) return;
  try {
    await api(`/api/deployments/${appId}/domains/${id}`, { method: 'DELETE' });
    toast('Domain removed');
    loadDomains();
  } catch (err) { toast(err.message, true); }
}

document.getElementById('btn-domain-add').addEventListener('click', () => {
  document.getElementById('domain-modal').classList.remove('hidden');
  document.getElementById('domain-hostname').value = '';
  document.getElementById('domain-port').value = '';
  document.getElementById('domain-email').value = '';
  document.getElementById('domain-ssl').checked = true;
});
document.getElementById('btn-domain-cancel').addEventListener('click', () => document.getElementById('domain-modal').classList.add('hidden'));
document.getElementById('btn-domain-confirm').addEventListener('click', async () => {
  const hostname = document.getElementById('domain-hostname').value.trim();
  const port = document.getElementById('domain-port').value.trim();
  const ssl = document.getElementById('domain-ssl').checked;
  const email = document.getElementById('domain-email').value.trim();
  if (!hostname) { toast('Domain name is required', true); return; }
  try {
    document.getElementById('btn-domain-confirm').disabled = true;
    const res = await api(`/api/deployments/${appId}/domains`, { method: 'POST', body: JSON.stringify({ hostname, port, ssl, email }) });
    document.getElementById('domain-modal').classList.add('hidden');
    if (res && res.warning) { toast(res.warning, true); } else { toast('Domain added'); }
    loadDomains();
  } catch (err) { toast(err.message, true); } finally {
    document.getElementById('btn-domain-confirm').disabled = false;
  }
});

// ============ LIMITS ============
function loadLimits() {
  const lim = currentApp.limits || {};
  document.getElementById('limit-cpu').value = lim.cpuQuota || 0;
  document.getElementById('limit-memory').value = lim.memoryMB || 0;
  document.getElementById('limit-pids').value = lim.pidsLimit || 0;
  document.getElementById('limit-disk').value = lim.diskMB || 0;
}

document.getElementById('btn-limits-save').addEventListener('click', async () => {
  const status = document.getElementById('limits-status');
  status.textContent = 'Applying... (container restart)';
  try {
    await api(`/api/deployments/${appId}/limits`, {
      method: 'PUT',
      body: JSON.stringify({
        cpuQuota: parseFloat(document.getElementById('limit-cpu').value) || 0,
        memoryMB: parseInt(document.getElementById('limit-memory').value) || 0,
        pidsLimit: parseInt(document.getElementById('limit-pids').value) || 0,
        diskMB: parseInt(document.getElementById('limit-disk').value) || 0,
      }),
    });
    status.textContent = 'Limits applied.';
    toast('Limits applied');
    setTimeout(() => { status.textContent = ''; }, 3000);
  } catch (err) { status.textContent = 'Error: ' + err.message; toast(err.message, true); }
});

// ============ BACKUPS ============
async function loadBackups() {
  const tbody = document.getElementById('backups-table');
  tbody.innerHTML = '<tr><td colspan="4" class="text-center text-gray-500 py-6">Loading...</td></tr>';
  try {
    const backups = await api(`/api/deployments/${appId}/backups`);
    if (backups.length === 0) { tbody.innerHTML = '<tr><td colspan="4" class="text-center text-gray-500 py-6">No backup yet.</td></tr>'; return; }
    tbody.innerHTML = '';
    backups.forEach(b => {
      const tr = document.createElement('tr');
      tr.className = 'border-b border-border hover:bg-panel2 transition';
      tr.innerHTML = `
        <td class="px-4 py-3 text-sm text-white">${escapeHtml(b.name)}</td>
        <td class="px-4 py-3 text-sm text-gray-400">${formatBytes(b.sizeBytes)}</td>
        <td class="px-4 py-3 text-sm text-gray-500 hidden sm:table-cell">${new Date(b.createdAt).toLocaleString()}</td>
        <td class="px-4 py-3 text-right whitespace-nowrap"></td>`;
      const actions = tr.querySelector('td:last-child');
      actions.appendChild(iconBtn('download', 'Download', () => { window.location = `/api/deployments/${appId}/backups/${b.id}/download`; }));
      actions.appendChild(iconBtn('clock-rotate-left', 'Restore', () => restoreBackup(b.id)));
      actions.appendChild(iconBtn('trash', 'Delete', () => deleteBackup(b.id), true));
      tbody.appendChild(tr);
    });
  } catch (err) {
    tbody.innerHTML = `<tr><td colspan="4" class="text-center text-danger py-6">${escapeHtml(err.message)}</td></tr>`;
  }
}

async function restoreBackup(backupId) {
  if (!confirm('Restore this backup? The container will be stopped and restarted.')) return;
  try {
    await api(`/api/deployments/${appId}/backups/${backupId}/restore`, { method: 'POST' });
    toast('Restore complete');
    loadBackups();
  } catch (err) { toast(err.message, true); }
}

async function deleteBackup(backupId) {
  if (!confirm('Delete this backup?')) return;
  try {
    await api(`/api/deployments/${appId}/backups/${backupId}`, { method: 'DELETE' });
    toast('Backup deleted');
    loadBackups();
  } catch (err) { toast(err.message, true); }
}

document.getElementById('btn-create-backup').addEventListener('click', () => document.getElementById('backup-modal').classList.remove('hidden'));
document.getElementById('btn-cancel-backup').addEventListener('click', () => document.getElementById('backup-modal').classList.add('hidden'));
document.getElementById('btn-confirm-backup').addEventListener('click', async () => {
  const name = document.getElementById('backup-name').value.trim();
  const notes = document.getElementById('backup-notes').value.trim();
  try {
    await api(`/api/deployments/${appId}/backups`, { method: 'POST', body: JSON.stringify({ name, notes }) });
    toast('Backup created');
    document.getElementById('backup-modal').classList.add('hidden');
    document.getElementById('backup-name').value = '';
    document.getElementById('backup-notes').value = '';
    loadBackups();
  } catch (err) { toast(err.message, true); }
});

// ============ SCHEDULES ============
async function loadSchedules() {
  const tbody = document.getElementById('schedules-table');
  tbody.innerHTML = '<tr><td colspan="6" class="text-center text-gray-500 py-6">Loading...</td></tr>';
  try {
    const schedules = await api(`/api/deployments/${appId}/schedules`);
    if (schedules.length === 0) { tbody.innerHTML = '<tr><td colspan="6" class="text-center text-gray-500 py-6">No schedule yet.</td></tr>'; return; }
    tbody.innerHTML = '';
    schedules.forEach(sc => {
      const lastRun = sc.lastRunAt && sc.lastRunAt !== '0001-01-01T00:00:00Z'
        ? new Date(sc.lastRunAt).toLocaleString() + ' (' + (sc.lastStatus || '—') + ')' : '—';
      const tr = document.createElement('tr');
      tr.className = 'border-b border-border hover:bg-panel2 transition';
      tr.innerHTML = `
        <td class="px-4 py-3 text-sm text-white">${escapeHtml(sc.name)}</td>
        <td class="px-4 py-3 text-xs"><code>${escapeHtml(sc.cronExpr)}</code></td>
        <td class="px-4 py-3 text-sm text-gray-400 hidden md:table-cell">${escapeHtml(sc.action)}${sc.action === 'command' ? ': ' + escapeHtml(sc.command) : ''}</td>
        <td class="px-4 py-3 hidden sm:table-cell">${sc.enabled ? '<i class="fas fa-circle-check text-accent"></i>' : '<i class="fas fa-pause text-gray-500"></i>'}</td>
        <td class="px-4 py-3 text-xs text-gray-500 hidden lg:table-cell">${escapeHtml(lastRun)}</td>
        <td class="px-4 py-3 text-right whitespace-nowrap"></td>`;
      const actions = tr.querySelector('td:last-child');
      actions.appendChild(iconBtn('play', 'Run now', () => runScheduleNow(sc.id)));
      actions.appendChild(iconBtn('pen', 'Edit', () => openScheduleModal(sc)));
      actions.appendChild(iconBtn('trash', 'Delete', () => deleteSchedule(sc.id), true));
      tbody.appendChild(tr);
    });
  } catch (err) {
    tbody.innerHTML = `<tr><td colspan="6" class="text-center text-danger py-6">${escapeHtml(err.message)}</td></tr>`;
  }
}

async function runScheduleNow(scheduleId) {
  try { await api(`/api/deployments/${appId}/schedules/${scheduleId}/run`, { method: 'POST' }); toast('Schedule executed'); loadSchedules(); }
  catch (err) { toast(err.message, true); }
}

async function deleteSchedule(scheduleId) {
  if (!confirm('Delete this schedule?')) return;
  try { await api(`/api/deployments/${appId}/schedules/${scheduleId}`, { method: 'DELETE' }); toast('Schedule deleted'); loadSchedules(); }
  catch (err) { toast(err.message, true); }
}

function openScheduleModal(sc) {
  document.getElementById('schedule-modal').classList.remove('hidden');
  document.getElementById('schedule-modal-title').textContent = sc ? 'Edit schedule' : 'Add a schedule';
  document.getElementById('schedule-id').value = sc ? sc.id : '';
  document.getElementById('schedule-name').value = sc ? sc.name : '';
  document.getElementById('schedule-cron').value = sc ? sc.cronExpr : '0 3 * * *';
  document.getElementById('schedule-action').value = sc ? sc.action : 'backup';
  document.getElementById('schedule-command').value = sc ? sc.command || '' : '';
  document.getElementById('schedule-enabled').checked = sc ? sc.enabled : true;
  updateScheduleCommandVisibility();
}
function updateScheduleCommandVisibility() {
  const action = document.getElementById('schedule-action').value;
  document.getElementById('schedule-command-row').classList.toggle('hidden', action !== 'command');
}
document.getElementById('schedule-action').addEventListener('change', updateScheduleCommandVisibility);
document.getElementById('btn-create-schedule').addEventListener('click', () => openScheduleModal(null));
document.getElementById('btn-cancel-schedule').addEventListener('click', () => document.getElementById('schedule-modal').classList.add('hidden'));
document.getElementById('btn-confirm-schedule').addEventListener('click', async () => {
  const id = document.getElementById('schedule-id').value;
  const payload = {
    name: document.getElementById('schedule-name').value.trim(),
    cronExpr: document.getElementById('schedule-cron').value.trim(),
    action: document.getElementById('schedule-action').value,
    command: document.getElementById('schedule-command').value.trim(),
    enabled: document.getElementById('schedule-enabled').checked,
  };
  try {
    if (id) await api(`/api/deployments/${appId}/schedules/${id}`, { method: 'PUT', body: JSON.stringify(payload) });
    else await api(`/api/deployments/${appId}/schedules`, { method: 'POST', body: JSON.stringify(payload) });
    toast('Schedule saved');
    document.getElementById('schedule-modal').classList.add('hidden');
    loadSchedules();
  } catch (err) { toast(err.message, true); }
});

// ============ SETTINGS ============
function loadAppSettings() {
  document.getElementById('set-stack').value = currentApp.stack === 'auto' ? 'node' : (currentApp.stack || 'node');
  document.getElementById('set-app-subdir').value = currentApp.appSubdir || '';
  document.getElementById('set-node-version').value = currentApp.nodeVersion || '22';
  document.getElementById('set-python-version').value = currentApp.pythonVersion || '3.12';
  document.getElementById('set-php-version').value = currentApp.phpVersion || '8.3';
  document.getElementById('set-host-port').value = currentApp.port || '';
  updateSettingsVisibility();
}
function updateSettingsVisibility() {
  const stack = document.getElementById('set-stack').value;
  const nodeStacks = ['node', 'react', 'next', 'astro', 'sveltekit', 'nuxt'];
  document.querySelector('#tab-settings .version-node').classList.toggle('hidden', !nodeStacks.includes(stack));
  document.querySelector('#tab-settings .version-python').classList.toggle('hidden', stack !== 'python');
  document.querySelector('#tab-settings .version-php').classList.toggle('hidden', stack !== 'laravel');
}
document.getElementById('set-stack').addEventListener('change', updateSettingsVisibility);

document.getElementById('btn-detect-subdir').addEventListener('click', async () => {
  const input = document.getElementById('set-app-subdir');
  const btn = document.getElementById('btn-detect-subdir');
  btn.disabled = true;
  const oldHTML = btn.innerHTML;
  btn.innerHTML = '<i class="fas fa-spinner fa-spin"></i>';
  try {
    const res = await api(`/api/deployments/${currentApp.id}/settings`, {
      method: 'PUT',
      body: JSON.stringify({ appSubdir: 'auto' }),
    });
    if (res.deployment && typeof res.deployment.appSubdir !== 'undefined') {
      input.value = res.deployment.appSubdir || '';
      currentApp.appSubdir = res.deployment.appSubdir || '';
      const subdirEl = document.getElementById('app-subdir');
      if (currentApp.appSubdir) {
        subdirEl.textContent = `📁 ${currentApp.appSubdir}/`;
      } else {
        subdirEl.textContent = '📁 (repo root)';
      }
      toast(res.changed ? 'Subdirectory updated, container rebuilt' : 'Subdirectory re-detected (no change)');
      if (res.changed) {
        setTimeout(() => location.reload(), 1000);
      }
    } else {
      toast('Detection returned no value');
    }
  } catch (err) {
    toast(err.message, true);
  } finally {
    btn.disabled = false;
    btn.innerHTML = oldHTML;
  }
});

document.getElementById('btn-save-settings').addEventListener('click', async () => {
  if (!currentApp) return;

  const status = document.getElementById('settings-status');
  const btn = document.getElementById('btn-save-settings');

  btn.disabled = true;
  const oldHTML = btn.innerHTML;
  btn.innerHTML = '<i class="fas fa-spinner fa-spin mr-2"></i>Saving & rebuilding...';

  const stack = document.getElementById('set-stack').value;
  const nodeVersion = document.getElementById('set-node-version').value;
  const pythonVersion = document.getElementById('set-python-version').value;
  const phpVersion = document.getElementById('set-php-version').value;
  const hostPort = document.getElementById('set-host-port').value;
  const internalPort = document.getElementById('set-internal-port').value;
  const appSubdir = document.getElementById('set-app-subdir').value.trim();

  let progressMsg = 'Saving and rebuilding... this can take 30s to 3min.';
  if (['node', 'react', 'next', 'astro', 'sveltekit', 'nuxt'].includes(stack)) {
    progressMsg = `Rebuilding with Node ${nodeVersion}... this can take 30s to 3min.`;
  } else if (stack === 'python') {
    progressMsg = `Rebuilding with Python ${pythonVersion}...`;
  } else if (stack === 'laravel') {
    progressMsg = `Rebuilding with PHP ${phpVersion}...`;
  }
  status.textContent = progressMsg;
  status.className = 'text-xs text-accent mt-3';

  const payload = { stack, appSubdir, nodeVersion, pythonVersion, phpVersion, hostPort, internalPort };

  try {
    const res = await api(`/api/deployments/${currentApp.id}/settings`, {
      method: 'PUT',
      body: JSON.stringify(payload),
    });

    if (res.changed === false) {
      status.textContent = 'No changes detected.';
      status.className = 'text-xs text-gray-500 mt-3';
      toast('No changes');
      return;
    }

    status.textContent = 'Rebuilt successfully. Reloading...';
    status.className = 'text-xs text-accent mt-3';

    let successMsg = 'Settings updated';
    if (['node', 'react', 'next', 'astro', 'sveltekit', 'nuxt'].includes(stack)) {
      successMsg = `Rebuilt with Node ${nodeVersion}`;
    } else if (stack === 'python') {
      successMsg = `Rebuilt with Python ${pythonVersion}`;
    } else if (stack === 'laravel') {
      successMsg = `Rebuilt with PHP ${phpVersion}`;
    }
    toast(successMsg);

    setTimeout(() => location.reload(), 600);
  } catch (err) {
    status.textContent = 'Error: ' + err.message;
    status.className = 'text-xs text-danger mt-3';
    showErrorBanner(err.message);
    toast(err.message, true);
  } finally {
    btn.disabled = false;
    btn.innerHTML = oldHTML;
  }
});

// ============ REINSTALL ============
document.getElementById('btn-reinstall').addEventListener('click', async () => {
  if (!currentApp) return;
  if (!confirm('Rebuild the Docker image and recreate the container?\n\nUse this when you changed package.json or any code file. It takes 30s-3min.')) return;

  const btn = document.getElementById('btn-reinstall');
  const status = document.getElementById('reinstall-status');
  btn.disabled = true;
  const oldHTML = btn.innerHTML;
  btn.innerHTML = '<i class="fas fa-spinner fa-spin mr-2"></i>Reinstalling...';
  status.textContent = 'Rebuilding image and recreating container...';
  status.className = 'text-xs text-accent mt-3';

  try {
    const res = await api(`/api/deployments/${appId}/power/reinstall`, { method: 'POST' });
    status.textContent = 'Reinstalled successfully.';
    status.className = 'text-xs text-accent mt-3';
    toast(res.message || 'Reinstall complete');
    setTimeout(() => location.reload(), 800);
  } catch (err) {
    status.textContent = 'Error: ' + err.message;
    status.className = 'text-xs text-danger mt-3';
    showErrorBanner(err.message);
    toast(err.message, true);
  } finally {
    btn.disabled = false;
    btn.innerHTML = oldHTML;
  }
});

// ============ DELETE APP ============
document.getElementById('btn-delete-app').addEventListener('click', async () => {
  if (!confirm(`Delete "${currentApp.name}"?`)) return;
  const removeFiles = confirm('Also delete the files from disk?');
  try {
    await api(`/api/deployments/${currentApp.id}?removeFiles=${removeFiles}`, { method: 'DELETE' });
    toast('App deleted');
    location.href = '/servers.html';
  } catch (err) { toast(err.message, true); }
});

// ============ SUBUSERS ============
async function loadSubusers() {
  const tbody = document.getElementById('subusers-table');
  tbody.innerHTML = '<tr><td colspan="4" class="text-center text-gray-500 py-6">Loading...</td></tr>';
  try {
    const subs = await api(`/api/deployments/${currentApp.id}/subusers`);
    if (subs.length === 0) { tbody.innerHTML = '<tr><td colspan="4" class="text-center text-gray-500 py-6">No collaborator yet.</td></tr>'; return; }
    tbody.innerHTML = '';
    subs.forEach(sub => {
      const tr = document.createElement('tr');
      tr.className = 'border-b border-border hover:bg-panel2 transition';
      tr.innerHTML = `
        <td class="px-4 py-3 text-sm text-white">${escapeHtml(sub.username)}</td>
        <td class="px-4 py-3 text-xs text-gray-400 hidden sm:table-cell">${(sub.permissions || []).map(escapeHtml).join(', ') || '—'}</td>
        <td class="px-4 py-3 text-xs text-gray-500 hidden md:table-cell">${new Date(sub.createdAt).toLocaleDateString()}</td>
        <td class="px-4 py-3 text-right"></td>`;
      const actions = tr.querySelector('td:last-child');
      actions.appendChild(iconBtn('trash', 'Remove', () => removeSubuser(sub.id), true));
      tbody.appendChild(tr);
    });
  } catch (err) {
    tbody.innerHTML = `<tr><td colspan="4" class="text-center text-danger py-6">${escapeHtml(err.message)}</td></tr>`;
  }
}

async function removeSubuser(subId) {
  if (!confirm('Remove this collaborator?')) return;
  try {
    await api(`/api/deployments/${currentApp.id}/subusers/${subId}`, { method: 'DELETE' });
    toast('Removed');
    loadSubusers();
  } catch (err) { toast(err.message, true); }
}

document.getElementById('btn-open-invite').addEventListener('click', () => {
  document.getElementById('invite-modal').classList.remove('hidden');
  document.getElementById('invite-link-result').classList.add('hidden');
});
document.getElementById('btn-cancel-invite').addEventListener('click', () => document.getElementById('invite-modal').classList.add('hidden'));
document.getElementById('btn-confirm-invite').addEventListener('click', async () => {
  const perms = Array.from(document.querySelectorAll('#invite-modal input[type="checkbox"]:checked')).map(i => i.value);
  if (perms.length === 0) { toast('Select at least one permission', true); return; }
  const expires = parseInt(document.getElementById('invite-expires').value, 10) || 24;
  try {
    const res = await api(`/api/deployments/${currentApp.id}/invite`, {
      method: 'POST', body: JSON.stringify({ permissions: perms, expiresInHours: expires }),
    });
    document.getElementById('invite-link').textContent = res.inviteLink;
    document.getElementById('invite-link-meta').textContent = `Expires: ${new Date(res.expiresAt).toLocaleString()}`;
    document.getElementById('invite-link-result').classList.remove('hidden');
    toast('Invite link generated');
  } catch (err) { toast(err.message, true); }
});
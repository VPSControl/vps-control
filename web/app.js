// VPS Control — utilitaires partagés par toutes les pages.

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

// ---- API ----
async function api(path, options = {}) {
  const res = await fetch(path, {
    ...options,
    headers: options.body instanceof FormData
      ? options.headers
      : { 'Content-Type': 'application/json', ...(options.headers || {}) },
    credentials: 'same-origin',
  });
  let data = null;
  try { data = await res.json(); } catch (_) {}
  if (!res.ok) {
    const message = (data && data.error) || `Error ${res.status}`;
    const err = new Error(message);
    err.status = res.status;
    throw err;
  }
  return data;
}

// ---- Toast ----
function toast(message, isError = false) {
  const el = document.getElementById('toast');
  if (!el) { console.log(isError ? '❌ ' : 'ℹ️ ', message); return; }
  el.textContent = message;
  el.classList.toggle('error', isError);
  el.classList.toggle('border-danger', isError);
  el.classList.toggle('border-accent', !isError);
  el.classList.remove('hidden');
  clearTimeout(toast._t);
  toast._t = setTimeout(() => el.classList.add('hidden'), 4000);
}

// ---- Utils ----
function escapeHtml(str) {
  return String(str ?? '').replace(/[&<>"']/g, m => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  }[m]));
}

function formatBytes(bytes) {
  if (!bytes) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0, n = bytes;
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
  return `${n.toFixed(1)} ${units[i]}`;
}

function formatSize(bytes) {
  if (bytes < 1024) return bytes + ' B';
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
  return (bytes / (1024 * 1024)).toFixed(1) + ' MB';
}

function actionBtn(label, onClick, danger = false) {
  const b = document.createElement('button');
  b.className = 'text-xs px-2.5 py-1.5 rounded-md bg-panel2 border transition mr-1 ' +
    (danger ? 'border-border text-gray-300 hover:border-danger hover:text-danger' : 'border-border text-gray-300 hover:border-accent hover:text-accent');
  b.textContent = label;
  b.addEventListener('click', onClick);
  return b;
}

function stackLabel(stack, dep) {
  const base = {
    laravel: 'Laravel / PHP', node: 'Node.js', react: 'React SPA', next: 'Next.js',
    astro: 'Astro', sveltekit: 'SvelteKit', nuxt: 'Nuxt', python: 'Python', static: 'Static',
  }[stack] || stack || 'Auto';
  let ver = '—';
  if (['node', 'react', 'next', 'astro', 'sveltekit', 'nuxt'].includes(stack) && dep?.nodeVersion) {
    ver = `Node ${dep.nodeVersion}`;
  } else if (stack === 'python' && dep?.pythonVersion) {
    ver = `Python ${dep.pythonVersion}`;
  } else if (stack === 'laravel' && dep?.phpVersion) {
    ver = `PHP ${dep.phpVersion}`;
  }
  return { base, ver };
}

// ---- Auth guard ----
async function requireAuth(opts = {}) {
  try {
    const me = await api('/api/me');
    window.currentUser = me;
    if (opts.adminOnly && me.role !== 'admin') {
      location.href = '/dashboard.html';
      return null;
    }
    return me;
  } catch (err) {
    if (err.status === 401) {
      location.href = '/index.html';
      return null;
    }
    throw err;
  }
}

// ---- Sidebar builder (partagé) ----
function buildSidebarNav(activePage, isAdmin) {
  const items = [
    { href: '/dashboard.html', icon: 'fa-gauge-high', label: 'Dashboard', id: 'dashboard' },
    { href: '/servers.html', icon: 'fa-server', label: 'Servers', id: 'servers' },
    { href: '/deploy.html', icon: 'fa-rocket', label: 'Deploy', id: 'deploy', admin: true },
    { href: '/files.html', icon: 'fa-folder', label: 'Files', id: 'files', admin: true },
    { href: '/services.html', icon: 'fa-cube', label: 'Services', id: 'services', admin: true },
    { href: '/databases.html', icon: 'fa-database', label: 'Databases', id: 'databases', admin: true },
    { href: '/users.html', icon: 'fa-users', label: 'Users', id: 'users', admin: true },
    { href: '/activity.html', icon: 'fa-clock-rotate-left', label: 'Activity', id: 'activity', admin: true },
    { href: '/tokens.html', icon: 'fa-key', label: 'API Tokens', id: 'tokens' },
    { href: '/system.html', icon: 'fa-gear', label: 'System', id: 'system', admin: true },
  ];
  return items.filter(i => !i.admin || isAdmin).map(i =>
    `<a href="${i.href}" class="flex items-center gap-3 px-3 py-2.5 rounded-lg text-sm font-medium ${i.id === activePage ? 'bg-accent/10 text-accent' : 'text-gray-400 hover:bg-panel2 hover:text-white transition'}">
      <i class="fas ${i.icon} w-4"></i><span>${i.label}</span>
    </a>`
  ).join('');
}

function renderSidebar(activePage) {
  const container = document.getElementById('nav-container');
  if (!container || !window.currentUser) return;
  container.innerHTML = buildSidebarNav(activePage, window.currentUser.role === 'admin');
}

// ---- Mobile sidebar helpers ----
function initMobileSidebar(opts = {}) {
  const sidebar = document.getElementById(opts.sidebarId || 'sidebar');
  const overlay = document.getElementById(opts.overlayId || 'sidebar-overlay');
  const menuBtn = document.getElementById(opts.menuBtnId || 'btn-menu');
  const closeBtn = document.getElementById(opts.closeBtnId || 'btn-close-sidebar');

  function open() {
    if (sidebar) { sidebar.classList.remove('hidden'); sidebar.classList.remove('-translate-x-full'); }
    if (overlay) overlay.classList.remove('hidden');
  }
  function close() {
    if (sidebar) sidebar.classList.add('-translate-x-full');
    if (overlay) overlay.classList.add('hidden');
  }

  menuBtn?.addEventListener('click', open);
  closeBtn?.addEventListener('click', close);
  overlay?.addEventListener('click', close);
}

// ---- Open a file in Monaco editor ----
// Usage : openInEditor(appId, path, line, col, errorMsg)
function openInEditor(appId, path, line = 0, col = 0, errorMsg = '') {
  const params = new URLSearchParams({ id: appId, path });
  if (line > 0) params.set('line', line);
  if (col > 0) params.set('col', col);
  if (errorMsg) params.set('error', errorMsg.slice(0, 300));
  location.href = `/edit.html?${params.toString()}`;
}

// Copier les boutons .copy-btn génériques (data-target)
document.addEventListener('click', async (e) => {
  const btn = e.target.closest('.copy-btn');
  if (!btn) return;
  let text = '';
  if (btn.dataset.target) {
    const el = document.getElementById(btn.dataset.target);
    text = el ? el.textContent.trim() : '';
  } else {
    const code = btn.previousElementSibling;
    text = code ? code.textContent.trim() : '';
  }
  if (!text) return;
  try {
    await navigator.clipboard.writeText(text);
    toast('Copied to clipboard');
  } catch (err) {
    toast('Copy failed: ' + err.message, true);
  }
});
// VPS Control — frontend vanilla JS, sans dépendance externe (léger, pas de build step).

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

let currentUser = null;
let filesCurrentPath = '';
let activeDbConnectionId = null;
let activeDbConnectionDriver = null;

function showScreen(id) {
  $$('.screen').forEach(el => el.classList.add('hidden'));
  $(`#${id}`).classList.remove('hidden');
}

function toast(message, isError = false) {
  const el = $('#toast');
  el.textContent = message;
  el.classList.toggle('error', isError);
  el.classList.remove('hidden');
  clearTimeout(toast._t);
  toast._t = setTimeout(() => el.classList.add('hidden'), 4000);
}

async function api(path, options = {}) {
  const res = await fetch(path, {
    ...options,
    headers: options.body instanceof FormData
      ? options.headers
      : { 'Content-Type': 'application/json', ...(options.headers || {}) },
    credentials: 'same-origin',
  });
  let data = null;
  try { data = await res.json(); } catch (_) { /* réponse vide */ }
  if (!res.ok) {
    const message = (data && data.error) || `Erreur ${res.status}`;
    throw new Error(message);
  }
  return data;
}

// ---------------------------------------------------------------------
// Démarrage : vérifie si le panel a besoin d'être initialisé, sinon login
// ---------------------------------------------------------------------
async function boot() {
  try {
    const status = await api('/api/setup/status');
    if (status.needsSetup) {
      showScreen('screen-setup');
      return;
    }
    const me = await api('/api/me').catch(() => null);
    if (me) {
      enterApp(me);
    } else {
      showScreen('screen-login');
    }
  } catch (e) {
    showScreen('screen-login');
  }
}

function enterApp(user) {
  currentUser = user;
  $('#current-user').textContent = `${user.username} (${user.role})`;
  $('#nav-users').classList.toggle('hidden', user.role !== 'admin');
  showScreen('screen-app');
  loadServices();
}

$('#form-setup').addEventListener('submit', async (e) => {
  e.preventDefault();
  const fd = new FormData(e.target);
  $('#setup-error').textContent = '';
  try {
    const user = await api('/api/setup', { method: 'POST', body: JSON.stringify(Object.fromEntries(fd)) });
    enterApp(user);
  } catch (err) {
    $('#setup-error').textContent = err.message;
  }
});

$('#form-login').addEventListener('submit', async (e) => {
  e.preventDefault();
  const fd = new FormData(e.target);
  $('#login-error').textContent = '';
  try {
    const user = await api('/api/login', { method: 'POST', body: JSON.stringify(Object.fromEntries(fd)) });
    enterApp(user);
  } catch (err) {
    $('#login-error').textContent = err.message;
  }
});

$('#btn-logout').addEventListener('click', async () => {
  await api('/api/logout', { method: 'POST' }).catch(() => {});
  location.reload();
});

// ---------------------------------------------------------------------
// Navigation entre onglets
// ---------------------------------------------------------------------
$$('.nav-item').forEach(btn => {
  btn.addEventListener('click', () => {
    $$('.nav-item').forEach(b => b.classList.remove('active'));
    $$('.tab').forEach(t => t.classList.remove('active'));
    btn.classList.add('active');
    $(`#tab-${btn.dataset.tab}`).classList.add('active');
    if (btn.dataset.tab === 'files') loadFiles(filesCurrentPath);
    if (btn.dataset.tab === 'deploy') loadDeployments();
    if (btn.dataset.tab === 'databases') loadDbConnections();
    if (btn.dataset.tab === 'users') loadUsers();
  });
});

// ---------------------------------------------------------------------
// Services (Docker)
// ---------------------------------------------------------------------
async function loadServices() {
  const tbody = $('#services-table tbody');
  tbody.innerHTML = '<tr><td colspan="5" class="muted">Chargement...</td></tr>';
  try {
    const services = await api('/api/services');
    if (services.length === 0) {
      tbody.innerHTML = '<tr><td colspan="5" class="muted">Aucun service Docker pour le moment.</td></tr>';
      return;
    }
    tbody.innerHTML = '';
    services.forEach(s => {
      const isUp = /up/i.test(s.Status);
      const tr = document.createElement('tr');
      tr.innerHTML = `
        <td>${escapeHtml(s.Names)}</td>
        <td class="muted">${escapeHtml(s.Image)}</td>
        <td><span class="status-pill ${isUp ? 'status-up' : 'status-down'}">${escapeHtml(s.Status)}</span></td>
        <td class="muted">${escapeHtml(s.Ports || '—')}</td>
        <td></td>`;
      const actionsCell = tr.querySelector('td:last-child');
      actionsCell.append(
        actionBtn('Démarrer', () => serviceAction(s.Names, 'start')),
        actionBtn('Stop', () => serviceAction(s.Names, 'stop')),
        actionBtn('Redémarrer', () => serviceAction(s.Names, 'restart')),
        actionBtn('Logs', () => showLogs(s.Names)),
        actionBtn('Supprimer', () => deleteService(s.Names), true),
      );
      tbody.appendChild(tr);
    });
  } catch (err) {
    tbody.innerHTML = `<tr><td colspan="5" class="error">${escapeHtml(err.message)}</td></tr>`;
  }
}

function actionBtn(label, onClick, danger = false) {
  const b = document.createElement('button');
  b.className = 'action-btn';
  b.textContent = label;
  if (danger) b.style.color = 'var(--danger)';
  b.addEventListener('click', onClick);
  return b;
}

async function serviceAction(name, action) {
  try {
    await api(`/api/services/${encodeURIComponent(name)}/${action}`, { method: 'POST' });
    toast(`Service ${name} : ${action} effectué`);
    loadServices();
  } catch (err) {
    toast(err.message, true);
  }
}

async function deleteService(name) {
  if (!confirm(`Supprimer définitivement le service "${name}" ?`)) return;
  try {
    await api(`/api/services/${encodeURIComponent(name)}`, { method: 'DELETE' });
    toast(`Service ${name} supprimé`);
    loadServices();
  } catch (err) {
    toast(err.message, true);
  }
}

async function showLogs(name) {
  try {
    const data = await api(`/api/services/${encodeURIComponent(name)}/logs`);
    alert(`Logs de ${name} (300 dernières lignes) :\n\n${data.logs || '(vide)'}`);
  } catch (err) {
    toast(err.message, true);
  }
}

$('#btn-refresh-services').addEventListener('click', loadServices);

$('#form-db-service').addEventListener('submit', async (e) => {
  e.preventDefault();
  const fd = Object.fromEntries(new FormData(e.target));
  try {
    const res = await api('/api/services/database', { method: 'POST', body: JSON.stringify(fd) });
    toast(`Base de données créée : conteneur ${res.container}`);
    e.target.reset();
    loadServices();
  } catch (err) {
    toast(err.message, true);
  }
});

// ---------------------------------------------------------------------
// Fichiers
// ---------------------------------------------------------------------
async function loadFiles(path) {
  filesCurrentPath = path || '';
  renderBreadcrumb();
  const tbody = $('#files-table tbody');
  tbody.innerHTML = '<tr><td colspan="4" class="muted">Chargement...</td></tr>';
  try {
    const entries = await api(`/api/files?path=${encodeURIComponent(filesCurrentPath)}`);
    if (entries.length === 0) {
      tbody.innerHTML = '<tr><td colspan="4" class="muted">Dossier vide.</td></tr>';
      return;
    }
    tbody.innerHTML = '';
    entries.forEach(entry => {
      const tr = document.createElement('tr');
      tr.innerHTML = `
        <td>${entry.isDir ? '📁' : '📄'} ${escapeHtml(entry.name)}</td>
        <td class="muted">${entry.isDir ? '—' : formatSize(entry.size)}</td>
        <td class="muted">${escapeHtml(entry.modTime)}</td>
        <td></td>`;
      const nameCell = tr.querySelector('td');
      if (entry.isDir) {
        nameCell.style.cursor = 'pointer';
        nameCell.addEventListener('click', () => loadFiles(entry.path));
      } else {
        nameCell.style.cursor = 'pointer';
        nameCell.addEventListener('click', () => openFileEditor(entry.path, entry.name));
      }
      const actions = tr.querySelector('td:last-child');
      if (!entry.isDir) {
        actions.append(actionBtn('Télécharger', () => {
          window.location = `/api/files/download?path=${encodeURIComponent(entry.path)}`;
        }));
      }
      actions.append(actionBtn('Supprimer', () => deleteFile(entry.path), true));
      tbody.appendChild(tr);
    });
  } catch (err) {
    tbody.innerHTML = `<tr><td colspan="4" class="error">${escapeHtml(err.message)}</td></tr>`;
  }
}

function renderBreadcrumb() {
  const container = $('#files-breadcrumb');
  const parts = filesCurrentPath.split('/').filter(Boolean);
  container.innerHTML = '';
  const root = document.createElement('span');
  root.textContent = '/ racine';
  root.addEventListener('click', () => loadFiles(''));
  container.appendChild(root);
  let acc = '';
  parts.forEach(p => {
    acc += '/' + p;
    const sep = document.createTextNode(' / ');
    container.appendChild(sep);
    const span = document.createElement('span');
    span.textContent = p;
    const target = acc;
    span.addEventListener('click', () => loadFiles(target));
    container.appendChild(span);
  });
}

async function deleteFile(path) {
  if (!confirm(`Supprimer "${path}" ?`)) return;
  try {
    await api(`/api/files?path=${encodeURIComponent(path)}`, { method: 'DELETE' });
    toast('Supprimé');
    loadFiles(filesCurrentPath);
  } catch (err) {
    toast(err.message, true);
  }
}

let editingFilePath = null;
async function openFileEditor(path, name) {
  try {
    const data = await api(`/api/files/content?path=${encodeURIComponent(path)}`);
    editingFilePath = path;
    $('#file-editor-name').textContent = name;
    $('#file-editor-content').value = data.content;
    $('#file-editor').classList.remove('hidden');
  } catch (err) {
    toast(err.message, true);
  }
}

$('#btn-close-editor').addEventListener('click', () => {
  $('#file-editor').classList.add('hidden');
  editingFilePath = null;
});

$('#btn-save-file').addEventListener('click', async () => {
  if (!editingFilePath) return;
  try {
    await api(`/api/files/content?path=${encodeURIComponent(editingFilePath)}`, {
      method: 'PUT',
      body: $('#file-editor-content').value,
      headers: { 'Content-Type': 'text/plain' },
    });
    toast('Fichier enregistré');
  } catch (err) {
    toast(err.message, true);
  }
});

$('#btn-new-folder').addEventListener('click', async () => {
  const name = prompt('Nom du nouveau dossier :');
  if (!name) return;
  const path = (filesCurrentPath ? filesCurrentPath + '/' : '') + name;
  try {
    await api(`/api/files/mkdir?path=${encodeURIComponent(path)}`, { method: 'POST' });
    loadFiles(filesCurrentPath);
  } catch (err) {
    toast(err.message, true);
  }
});

$('#form-upload').addEventListener('submit', async (e) => {
  e.preventDefault();
  const fd = new FormData(e.target);
  try {
    await api(`/api/files/upload?path=${encodeURIComponent(filesCurrentPath)}`, { method: 'POST', body: fd });
    toast('Upload terminé');
    e.target.reset();
    loadFiles(filesCurrentPath);
  } catch (err) {
    toast(err.message, true);
  }
});

// ---------------------------------------------------------------------
// Déploiement
// ---------------------------------------------------------------------
async function loadDeployments() {
  const tbody = $('#deployments-table tbody');
  tbody.innerHTML = '<tr><td colspan="5" class="muted">Chargement...</td></tr>';
  try {
    const deployments = await api('/api/deployments');
    if (deployments.length === 0) {
      tbody.innerHTML = '<tr><td colspan="5" class="muted">Aucune application déployée pour le moment.</td></tr>';
      return;
    }
    tbody.innerHTML = '';
    deployments.forEach(d => {
      const tr = document.createElement('tr');
      tr.innerHTML = `
        <td>${escapeHtml(d.name)}</td>
        <td class="muted">${escapeHtml(d.stack)}</td>
        <td><a href="http://${location.hostname}:${d.port}" target="_blank" class="muted">${escapeHtml(d.port)}</a></td>
        <td class="muted">${escapeHtml(d.sourceType)}</td>
        <td></td>`;
      const actions = tr.querySelector('td:last-child');
      actions.append(
        actionBtn('Redéployer', () => redeploy(d.name)),
        actionBtn('Supprimer', () => deleteDeployment(d.name), true),
      );
      tbody.appendChild(tr);
    });
  } catch (err) {
    tbody.innerHTML = `<tr><td colspan="5" class="error">${escapeHtml(err.message)}</td></tr>`;
  }
}

$('#form-deploy-git').addEventListener('submit', async (e) => {
  e.preventDefault();
  const fd = Object.fromEntries(new FormData(e.target));
  $('#deploy-status').textContent = 'Déploiement en cours (clone + build docker)... cela peut prendre une à deux minutes.';
  try {
    await api('/api/deploy/git', { method: 'POST', body: JSON.stringify(fd) });
    $('#deploy-status').textContent = '';
    toast(`Application "${fd.name}" déployée`);
    e.target.reset();
    loadDeployments();
  } catch (err) {
    $('#deploy-status').textContent = '';
    toast(err.message, true);
  }
});

$('#form-deploy-upload').addEventListener('submit', async (e) => {
  e.preventDefault();
  const fd = new FormData(e.target);
  $('#deploy-status').textContent = 'Déploiement en cours (décompression + build docker)...';
  try {
    await api('/api/deploy/upload', { method: 'POST', body: fd });
    $('#deploy-status').textContent = '';
    toast('Application déployée');
    e.target.reset();
    loadDeployments();
  } catch (err) {
    $('#deploy-status').textContent = '';
    toast(err.message, true);
  }
});

async function redeploy(name) {
  toast(`Redéploiement de ${name} en cours...`);
  try {
    await api(`/api/deployments/${encodeURIComponent(name)}/redeploy`, { method: 'POST' });
    toast(`${name} redéployé`);
  } catch (err) {
    toast(err.message, true);
  }
}

async function deleteDeployment(name) {
  if (!confirm(`Supprimer le déploiement "${name}" ? Le conteneur sera arrêté.`)) return;
  const removeFiles = confirm('Supprimer aussi les fichiers du projet sur le disque ?');
  try {
    await api(`/api/deployments/${encodeURIComponent(name)}?removeFiles=${removeFiles}`, { method: 'DELETE' });
    toast('Déploiement supprimé');
    loadDeployments();
  } catch (err) {
    toast(err.message, true);
  }
}

// ---------------------------------------------------------------------
// Bases de données
// ---------------------------------------------------------------------
async function loadDbConnections() {
  const list = $('#db-connections-list');
  list.innerHTML = '<li class="muted">Chargement...</li>';
  try {
    const conns = await api('/api/db/connections');
    if (conns.length === 0) {
      list.innerHTML = '<li class="muted">Aucune connexion enregistrée.</li>';
      return;
    }
    list.innerHTML = '';
    conns.forEach(c => {
      const li = document.createElement('li');
      li.textContent = `${c.name} (${c.driver})`;
      li.addEventListener('click', () => selectDbConnection(c));
      list.appendChild(li);
    });
  } catch (err) {
    list.innerHTML = `<li class="error">${escapeHtml(err.message)}</li>`;
  }
}

$('#form-db-connection').addEventListener('submit', async (e) => {
  e.preventDefault();
  const fd = Object.fromEntries(new FormData(e.target));
  try {
    await api('/api/db/connections', { method: 'POST', body: JSON.stringify(fd) });
    toast('Connexion ajoutée');
    e.target.reset();
    loadDbConnections();
  } catch (err) {
    toast(err.message, true);
  }
});

async function selectDbConnection(conn) {
  activeDbConnectionId = conn.id;
  activeDbConnectionDriver = conn.driver;
  $$('#db-connections-list li').forEach(li => li.classList.remove('active'));
  $('#db-rows-area').classList.add('hidden');
  const tablesList = $('#db-tables-list');
  tablesList.innerHTML = '<li class="muted">Chargement...</li>';
  try {
    const tables = await api(`/api/db/connections/${conn.id}/tables`);
    if (tables.length === 0) {
      tablesList.innerHTML = '<li class="muted">Aucune table.</li>';
      return;
    }
    tablesList.innerHTML = '';
    tables.forEach(t => {
      const li = document.createElement('li');
      li.textContent = t;
      li.addEventListener('click', () => loadTableRows(conn.id, t));
      tablesList.appendChild(li);
    });
  } catch (err) {
    tablesList.innerHTML = `<li class="error">${escapeHtml(err.message)}</li>`;
  }
}

async function loadTableRows(connId, table) {
  $('#db-rows-area').classList.remove('hidden');
  $('#db-rows-title').textContent = table;
  const el = $('#db-rows-table');
  el.innerHTML = '<tr><td class="muted">Chargement...</td></tr>';
  try {
    const result = await api(`/api/db/connections/${connId}/tables/${encodeURIComponent(table)}/rows?limit=100`);
    renderResultTable(el, result);
  } catch (err) {
    el.innerHTML = `<tr><td class="error">${escapeHtml(err.message)}</td></tr>`;
  }
}

function renderResultTable(el, result) {
  if (!result.columns || result.rows.length === 0) {
    el.innerHTML = '<tr><td class="muted">Aucune donnée.</td></tr>';
    return;
  }
  const thead = `<thead><tr>${result.columns.map(c => `<th>${escapeHtml(c)}</th>`).join('')}</tr></thead>`;
  const tbody = `<tbody>${result.rows.map(row =>
    `<tr>${result.columns.map(c => `<td>${escapeHtml(String(row[c] ?? ''))}</td>`).join('')}</tr>`
  ).join('')}</tbody>`;
  el.innerHTML = thead + tbody;
}

$('#btn-run-query').addEventListener('click', async () => {
  if (!activeDbConnectionId) { toast('Sélectionnez une connexion d\'abord', true); return; }
  const sql = $('#db-query-input').value.trim();
  if (!sql) return;
  const resultEl = $('#db-query-result');
  resultEl.textContent = 'Exécution...';
  try {
    const result = await api(`/api/db/connections/${activeDbConnectionId}/query`, {
      method: 'POST', body: JSON.stringify({ sql }),
    });
    resultEl.textContent = JSON.stringify(result, null, 2);
  } catch (err) {
    resultEl.textContent = 'Erreur : ' + err.message;
  }
});

// ---------------------------------------------------------------------
// Utilisateurs
// ---------------------------------------------------------------------
async function loadUsers() {
  const tbody = $('#users-table tbody');
  tbody.innerHTML = '<tr><td colspan="4" class="muted">Chargement...</td></tr>';
  try {
    const users = await api('/api/users');
    tbody.innerHTML = '';
    users.forEach(u => {
      const tr = document.createElement('tr');
      tr.innerHTML = `
        <td>${escapeHtml(u.username)}</td>
        <td class="muted">${escapeHtml(u.role)}</td>
        <td class="muted">${new Date(u.createdAt).toLocaleDateString()}</td>
        <td></td>`;
      const actions = tr.querySelector('td:last-child');
      if (u.id !== currentUser.id) {
        actions.append(actionBtn('Supprimer', () => deleteUser(u.id), true));
      }
      tbody.appendChild(tr);
    });
  } catch (err) {
    tbody.innerHTML = `<tr><td colspan="4" class="error">${escapeHtml(err.message)}</td></tr>`;
  }
}

$('#form-add-user').addEventListener('submit', async (e) => {
  e.preventDefault();
  const fd = Object.fromEntries(new FormData(e.target));
  try {
    await api('/api/users', { method: 'POST', body: JSON.stringify(fd) });
    toast('Compte créé');
    e.target.reset();
    loadUsers();
  } catch (err) {
    toast(err.message, true);
  }
});

async function deleteUser(id) {
  if (!confirm('Supprimer ce compte ?')) return;
  try {
    await api(`/api/users/${id}`, { method: 'DELETE' });
    toast('Compte supprimé');
    loadUsers();
  } catch (err) {
    toast(err.message, true);
  }
}

// ---------------------------------------------------------------------
// Utilitaires
// ---------------------------------------------------------------------
function escapeHtml(str) {
  return String(str ?? '').replace(/[&<>"']/g, m => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  }[m]));
}

function formatSize(bytes) {
  if (bytes < 1024) return bytes + ' o';
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' Ko';
  return (bytes / (1024 * 1024)).toFixed(1) + ' Mo';
}

boot();

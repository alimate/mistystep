'use strict';

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------
function escapeHtml(str) {
  return str
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

function escapeAttr(str) {
  return str.replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}

function fmtBytes(n) {
  if (n < 1024)       return n + ' B';
  if (n < 1048576)    return (n / 1024).toFixed(1) + ' KB';
  if (n < 1073741824) return (n / 1048576).toFixed(1) + ' MB';
  return (n / 1073741824).toFixed(2) + ' GB';
}

function showFeedback(el, msg, type /* 'ok'|'error'|'' */, timeoutMs = 3000) {
  el.textContent = msg;
  el.className = 'feedback ' + (type || '');
  if (timeoutMs > 0) {
    setTimeout(() => { el.textContent = ''; el.className = 'feedback'; }, timeoutMs);
  }
}

// ---------------------------------------------------------------------------
// Clipboard copy with execCommand fallback (needed on HTTP origins)
// ---------------------------------------------------------------------------
async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.cssText = 'position:fixed;opacity:0;top:0;left:0';
    document.body.appendChild(ta);
    ta.focus();
    ta.select();
    document.execCommand('copy');
    document.body.removeChild(ta);
  }
}

// ---------------------------------------------------------------------------
// DOM refs
// ---------------------------------------------------------------------------
const statusDot    = document.getElementById('status-dot');
const statusText   = document.getElementById('status-text');
const tabBtns      = document.querySelectorAll('.tab-btn');
const panes        = document.querySelectorAll('.pane');

// Send tab
const clipboardInput   = document.getElementById('clipboard-input');
const btnSendClipboard = document.getElementById('btn-send-clipboard');
const clipboardMsg     = document.getElementById('clipboard-msg');
const dropZone         = document.getElementById('drop-zone');
const fileInput        = document.getElementById('file-input');
const progressWrap     = document.getElementById('progress-wrap');
const progressFill     = document.getElementById('progress-fill');
const progressLabel    = document.getElementById('progress-label');
const uploadMsg        = document.getElementById('upload-msg');

// Get tab
const clipboardDisplay   = document.getElementById('clipboard-display');
const btnCopyFromLinux   = document.getElementById('btn-copy-from-linux');
const copyMsg            = document.getElementById('copy-msg');
const fileList           = document.getElementById('file-list');

// ---------------------------------------------------------------------------
// Tab switching
// ---------------------------------------------------------------------------
tabBtns.forEach(btn => {
  btn.addEventListener('click', () => {
    tabBtns.forEach(b => b.classList.remove('active'));
    panes.forEach(p => p.classList.remove('active'));
    btn.classList.add('active');
    document.getElementById('pane-' + btn.dataset.tab).classList.add('active');
  });
});

// ---------------------------------------------------------------------------
// WebSocket
// ---------------------------------------------------------------------------
let ws = null;
let reconnectAttempts = 0;
let latestClipboard = '';

function setStatus(state /* 'connected'|'connecting'|'disconnected' */, label) {
  statusDot.className = 'status-dot status-' + state;
  statusText.textContent = label;
}

function connectWebSocket() {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const url = `${proto}://${location.host}/ws`;

  setStatus('connecting', 'Connecting…');
  ws = new WebSocket(url);

  ws.onopen = () => {
    setStatus('connected', 'Connected');
    reconnectAttempts = 0;
  };

  ws.onmessage = (event) => {
    let msg;
    try { msg = JSON.parse(event.data); } catch { return; }

    if (msg.type === 'clipboard') {
      latestClipboard = msg.content || '';
      // Use textContent — never innerHTML — to prevent XSS
      clipboardDisplay.textContent = latestClipboard || '(clipboard is empty)';
    } else if (msg.type === 'files') {
      renderFileList(msg.files || []);
    }
  };

  ws.onerror = () => {
    setStatus('disconnected', 'Error');
  };

  ws.onclose = () => {
    setStatus('disconnected', 'Offline');
    const delay = Math.min(1000 * 2 ** reconnectAttempts, 30000);
    reconnectAttempts++;
    setTimeout(connectWebSocket, delay);
  };
}

// ---------------------------------------------------------------------------
// File list rendering
// ---------------------------------------------------------------------------
const SHARE_ICON = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
  <path d="M4 12v8a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-8"/>
  <polyline points="16 6 12 2 8 6"/>
  <line x1="12" y1="2" x2="12" y2="15"/>
</svg>`;

function renderFileList(files) {
  if (!files.length) {
    fileList.innerHTML = '<p class="empty-state">No files in ~/Downloads</p>';
    return;
  }
  fileList.innerHTML = files.map(f => {
    const href = '/api/files/' + encodeURIComponent(f.name);
    const date = new Date(f.modified * 1000).toLocaleDateString();
    return `<div class="file-item">
      <a href="${escapeAttr(href)}" download="${escapeAttr(f.name)}" class="file-info">
        <span class="file-name">${escapeHtml(f.name)}</span>
        <span class="file-meta">${escapeHtml(fmtBytes(f.size))} · ${escapeHtml(date)}</span>
      </a>
      <button class="file-share-btn" data-filename="${escapeAttr(f.name)}" aria-label="Share ${escapeAttr(f.name)}">${SHARE_ICON}</button>
    </div>`;
  }).join('');
}

async function shareFile(name) {
  const url = '/api/files/' + encodeURIComponent(name);
  try {
    const resp = await fetch(url);
    if (!resp.ok) throw new Error('HTTP ' + resp.status);
    const blob = await resp.blob();
    const file = new File([blob], name, { type: blob.type });
    if (navigator.canShare && navigator.canShare({ files: [file] })) {
      await navigator.share({ files: [file], title: name });
    } else {
      const a = document.createElement('a');
      a.href = URL.createObjectURL(blob);
      a.download = name;
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      setTimeout(() => URL.revokeObjectURL(a.href), 10000);
    }
  } catch (err) {
    if (err.name !== 'AbortError') console.error('Share failed:', err);
  }
}

fileList.addEventListener('click', e => {
  const btn = e.target.closest('.file-share-btn');
  if (!btn || btn.disabled) return;
  e.preventDefault();
  btn.disabled = true;
  shareFile(btn.dataset.filename).finally(() => { btn.disabled = false; });
});

// ---------------------------------------------------------------------------
// Send clipboard to Linux
// ---------------------------------------------------------------------------
btnSendClipboard.addEventListener('click', async () => {
  const text = clipboardInput.value;
  if (!text.trim()) {
    showFeedback(clipboardMsg, 'Nothing to send.', 'error');
    return;
  }
  btnSendClipboard.disabled = true;
  try {
    const res = await fetch('/api/clipboard', {
      method: 'POST',
      body: text,
      headers: { 'Content-Type': 'text/plain' },
    });
    if (res.ok) {
      showFeedback(clipboardMsg, 'Copied to Linux clipboard!', 'ok');
    } else {
      showFeedback(clipboardMsg, 'Error: ' + res.status, 'error');
    }
  } catch (err) {
    showFeedback(clipboardMsg, 'Network error', 'error');
  } finally {
    btnSendClipboard.disabled = false;
  }
});

// ---------------------------------------------------------------------------
// Copy Linux clipboard to device
// ---------------------------------------------------------------------------
btnCopyFromLinux.addEventListener('click', async () => {
  if (!latestClipboard) {
    showFeedback(copyMsg, 'Nothing to copy yet.', 'error');
    return;
  }
  try {
    await copyText(latestClipboard);
    showFeedback(copyMsg, 'Copied!', 'ok');
  } catch (err) {
    showFeedback(copyMsg, 'Copy failed: ' + err.message, 'error');
  }
});

// ---------------------------------------------------------------------------
// File upload
// ---------------------------------------------------------------------------
function uploadFile(file) {
  progressWrap.classList.remove('hidden');
  progressFill.style.width = '0%';
  progressLabel.textContent = '0%';
  showFeedback(uploadMsg, 'Uploading ' + file.name + '…', '', 0);

  const formData = new FormData();
  formData.append('file', file);

  const xhr = new XMLHttpRequest();

  xhr.upload.addEventListener('progress', e => {
    if (e.lengthComputable) {
      const pct = Math.round(e.loaded / e.total * 100);
      progressFill.style.width = pct + '%';
      progressLabel.textContent = pct + '%';
    }
  });

  xhr.addEventListener('load', () => {
    progressWrap.classList.add('hidden');
    if (xhr.status >= 200 && xhr.status < 300) {
      showFeedback(uploadMsg, file.name + ' uploaded!', 'ok');
    } else {
      showFeedback(uploadMsg, 'Upload failed: ' + xhr.status, 'error');
    }
  });

  xhr.addEventListener('error', () => {
    progressWrap.classList.add('hidden');
    showFeedback(uploadMsg, 'Network error during upload.', 'error');
  });

  xhr.open('POST', '/api/upload');
  xhr.send(formData);
}

// Drop zone — click to open file picker
dropZone.addEventListener('click', () => fileInput.click());
dropZone.addEventListener('keydown', e => {
  if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); fileInput.click(); }
});

fileInput.addEventListener('change', () => {
  const files = Array.from(fileInput.files || []);
  files.forEach(uploadFile);
  fileInput.value = '';
});

// Drag-and-drop (desktop)
dropZone.addEventListener('dragover', e => {
  e.preventDefault();
  dropZone.classList.add('drag-over');
});
dropZone.addEventListener('dragleave', () => dropZone.classList.remove('drag-over'));
dropZone.addEventListener('drop', e => {
  e.preventDefault();
  dropZone.classList.remove('drag-over');
  Array.from(e.dataTransfer.files).forEach(uploadFile);
});

// ---------------------------------------------------------------------------
// Boot
// ---------------------------------------------------------------------------
connectWebSocket();

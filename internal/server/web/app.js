/* flowstate web — vanilla JS frontend.
 *
 * No build step, no framework, no CDN. All state lives in localStorage so
 * the server never persists transcripts:
 *
 *   flowstate.sessions          [{id, startedAt, items: [...]}, ...]
 *   flowstate.currentSessionId  string
 *   flowstate.token             optional Bearer token for /api/* calls
 */

(function () {
  'use strict';

  // ---- State ---------------------------------------------------------

  var S = {
    sessions: [],
    currentSessionId: null,
    token: ''
  };

  var REC = {
    mediaRecorder: null,
    stream: null,
    chunks: [],
    mime: '',
    starting: false,
    state: 'idle' // idle | recording | uploading
  };

  var SESSION_REUSE_MS = 30 * 60 * 1000;

  function loadState() {
    try {
      var raw = localStorage.getItem('flowstate.sessions');
      S.sessions = raw ? JSON.parse(raw) : [];
    } catch (e) { S.sessions = []; }
    S.currentSessionId = localStorage.getItem('flowstate.currentSessionId') || null;
    S.token = localStorage.getItem('flowstate.token') || '';
    if (!Array.isArray(S.sessions)) S.sessions = [];
  }

  function persist() {
    try {
      localStorage.setItem('flowstate.sessions', JSON.stringify(S.sessions));
      if (S.currentSessionId) {
        localStorage.setItem('flowstate.currentSessionId', S.currentSessionId);
      }
      if (S.token) {
        localStorage.setItem('flowstate.token', S.token);
      } else {
        localStorage.removeItem('flowstate.token');
      }
    } catch (e) { /* quota or private-mode — best effort */ }
  }

  function currentSession() {
    for (var i = 0; i < S.sessions.length; i++) {
      if (S.sessions[i].id === S.currentSessionId) return S.sessions[i];
    }
    return null;
  }

  function newSessionId() {
    // Short, human-readable, just-unique-enough for localStorage.
    var bytes = new Uint8Array(6);
    (window.crypto || window.msCrypto).getRandomValues(bytes);
    var hex = '';
    for (var i = 0; i < bytes.length; i++) {
      hex += bytes[i].toString(16).padStart(2, '0');
    }
    return hex;
  }

  function createSession() {
    var s = { id: newSessionId(), startedAt: Date.now(), items: [] };
    S.sessions.unshift(s);
    S.currentSessionId = s.id;
    persist();
    return s;
  }

  function loadOrCreateSession() {
    if (S.sessions.length > 0 && S.currentSessionId) {
      var cur = currentSession();
      if (cur && cur.items.length === 0 && (Date.now() - cur.startedAt) < SESSION_REUSE_MS) {
        return cur;
      }
    }
    // Empty + recent latest? Reuse it.
    var latest = S.sessions[0];
    if (latest && latest.items.length === 0 && (Date.now() - latest.startedAt) < SESSION_REUSE_MS) {
      S.currentSessionId = latest.id;
      persist();
      return latest;
    }
    return createSession();
  }

  // ---- DOM helpers ---------------------------------------------------

  var $ = function (id) { return document.getElementById(id); };

  function setRecState(name) {
    REC.state = name;
    var btn = $('rec');
    btn.dataset.state = name === 'idle' ? '' : name;
    btn.setAttribute('aria-pressed', name === 'recording' ? 'true' : 'false');
    var label = '';
    if (name === 'recording') label = 'Recording…';
    if (name === 'uploading') label = 'Uploading…';
    $('rec-status').textContent = label;
  }

  function renderSessionLabel() {
    var s = currentSession();
    if (!s) { $('session-label').textContent = 'Session'; return; }
    $('session-label').textContent = 'Session #' + s.id.slice(0, 6);
  }

  function renderSessionSelect() {
    var sel = $('session-select');
    sel.innerHTML = '';
    for (var i = 0; i < S.sessions.length; i++) {
      var s = S.sessions[i];
      var opt = document.createElement('option');
      opt.value = s.id;
      var when = new Date(s.startedAt);
      opt.textContent = '#' + s.id.slice(0, 6) + ' — ' + when.toLocaleString() +
                        ' (' + s.items.length + ')';
      if (s.id === S.currentSessionId) opt.selected = true;
      sel.appendChild(opt);
    }
  }

  function renderHistory() {
    var s = currentSession();
    var list = $('history-list');
    list.innerHTML = '';
    var items = s ? s.items.slice().reverse() : [];
    if (items.length === 0) {
      $('history-empty').hidden = false;
      $('result').hidden = true;
      return;
    }
    $('history-empty').hidden = true;
    for (var i = 0; i < items.length; i++) {
      var it = items[i];
      var li = document.createElement('li');
      var ts = document.createElement('span');
      ts.className = 'ts';
      ts.textContent = new Date(it.ts).toLocaleString();
      li.appendChild(ts);
      var p = document.createElement('p');
      p.className = 'cleaned';
      p.textContent = it.cleaned || '(empty cleaned output)';
      li.appendChild(p);
      if (it.raw && it.raw !== it.cleaned) {
        var det = document.createElement('details');
        det.className = 'raw-wrap';
        var sum = document.createElement('summary');
        sum.textContent = 'Show raw';
        det.appendChild(sum);
        var rp = document.createElement('p');
        rp.className = 'raw-text';
        rp.textContent = it.raw;
        det.appendChild(rp);
        li.appendChild(det);
      }
      list.appendChild(li);
    }
    // Latest card
    var latest = items[0];
    $('result-cleaned').textContent = latest.cleaned || '(no cleaned output)';
    $('result-raw').textContent = latest.raw || '';
    $('result').hidden = false;
  }

  function rerender() {
    renderSessionLabel();
    renderSessionSelect();
    renderHistory();
  }

  // ---- Recording / upload -------------------------------------------

  function pickMimeAndExt() {
    var candidates = [
      ['audio/webm;codecs=opus', 'webm'],
      ['audio/webm',             'webm'],
      ['audio/ogg;codecs=opus',  'ogg'],
      ['audio/ogg',              'ogg'],
      ['audio/mp4',              'mp4']
    ];
    if (typeof MediaRecorder === 'undefined' || !MediaRecorder.isTypeSupported) {
      return ['', 'webm'];
    }
    for (var i = 0; i < candidates.length; i++) {
      if (MediaRecorder.isTypeSupported(candidates[i][0])) return candidates[i];
    }
    return ['', 'webm'];
  }

  async function startRecording() {
    if (REC.state !== 'idle' || REC.starting) return;
    REC.starting = true;
    try {
      var stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      REC.stream = stream;
      var pick = pickMimeAndExt();
      REC.mime = pick[0];
      var opts = REC.mime ? { mimeType: REC.mime } : {};
      var mr;
      try { mr = new MediaRecorder(stream, opts); }
      catch (e) { mr = new MediaRecorder(stream); }
      REC.mediaRecorder = mr;
      REC.chunks = [];
      mr.ondataavailable = function (ev) {
        if (ev.data && ev.data.size > 0) REC.chunks.push(ev.data);
      };
      mr.onstop = function () {
        var ext = pick[1];
        var actualMime = mr.mimeType || REC.mime || 'audio/webm';
        if (actualMime.indexOf('ogg') >= 0) ext = 'ogg';
        else if (actualMime.indexOf('mp4') >= 0) ext = 'mp4';
        else ext = 'webm';
        var blob = new Blob(REC.chunks, { type: actualMime });
        if (REC.stream) {
          REC.stream.getTracks().forEach(function (t) { t.stop(); });
          REC.stream = null;
        }
        upload(blob, ext);
      };
      mr.start();
      setRecState('recording');
    } catch (err) {
      setRecState('idle');
      $('rec-status').textContent = 'Microphone error: ' + (err && err.message ? err.message : err);
    } finally {
      REC.starting = false;
    }
  }

  function stopRecording() {
    if (REC.state !== 'recording') return;
    setRecState('uploading');
    try {
      if (REC.mediaRecorder && REC.mediaRecorder.state !== 'inactive') {
        REC.mediaRecorder.stop();
      }
    } catch (e) { /* already stopped */ }
  }

  async function upload(blob, ext) {
    if (blob.size === 0) {
      setRecState('idle');
      $('rec-status').textContent = '(no audio captured)';
      return;
    }
    var session = currentSession() || createSession();
    var didRetry = false;
    var send = async function () {
      var fd = new FormData();
      fd.append('audio', blob, 'recording.' + ext);
      fd.append('session_id', session.id);
      var headers = {};
      if (S.token) headers['Authorization'] = 'Bearer ' + S.token;
      var resp = await fetch('/api/transcribe', { method: 'POST', body: fd, headers: headers });
      return resp;
    };
    try {
      var resp = await send();
      if (resp.status === 401 && !didRetry) {
        didRetry = true;
        var t = window.prompt('Server requires an API token. Enter it now:');
        if (t) {
          S.token = t.trim();
          persist();
          resp = await send();
        }
      }
      if (!resp.ok) {
        var err = await safeJSON(resp);
        setRecState('idle');
        $('rec-status').textContent = 'Error ' + resp.status + ': ' +
          (err && err.error ? err.error : resp.statusText);
        return;
      }
      var data = await resp.json();
      var item = {
        id: newSessionId(),
        ts: Date.now(),
        raw: data.raw || '',
        cleaned: data.cleaned || '',
        durationMs: data.duration_ms || 0
      };
      if (!item.raw && !item.cleaned) {
        setRecState('idle');
        $('rec-status').textContent = '(no speech detected)';
        return;
      }
      session.items.push(item);
      persist();
      rerender();
      setRecState('idle');
      $('rec-status').textContent = '';
    } catch (e) {
      setRecState('idle');
      $('rec-status').textContent = 'Upload failed: ' + (e && e.message ? e.message : e);
    }
  }

  async function safeJSON(resp) {
    try { return await resp.json(); } catch (e) { return null; }
  }

  // ---- Export --------------------------------------------------------

  function download(filename, data, type) {
    var blob = new Blob([data], { type: type });
    var url = URL.createObjectURL(blob);
    var a = document.createElement('a');
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    setTimeout(function () { URL.revokeObjectURL(url); }, 1000);
  }

  function exportJSON() {
    var s = currentSession();
    if (!s) return;
    download('flowstate-session-' + s.id.slice(0, 6) + '.json',
             JSON.stringify(s, null, 2), 'application/json');
  }

  function exportMarkdown() {
    var s = currentSession();
    if (!s) return;
    var lines = [];
    lines.push('# flowstate session ' + s.id.slice(0, 6));
    lines.push('');
    lines.push('Started: ' + new Date(s.startedAt).toLocaleString());
    lines.push('');
    for (var i = 0; i < s.items.length; i++) {
      var it = s.items[i];
      lines.push('## ' + new Date(it.ts).toLocaleString());
      lines.push('');
      lines.push(it.cleaned || '_(empty)_');
      lines.push('');
      if (it.raw && it.raw !== it.cleaned) {
        lines.push('<details><summary>Raw</summary>');
        lines.push('');
        lines.push(it.raw);
        lines.push('');
        lines.push('</details>');
        lines.push('');
      }
      lines.push('---');
      lines.push('');
    }
    download('flowstate-session-' + s.id.slice(0, 6) + '.md',
             lines.join('\n'), 'text/markdown');
  }

  // ---- Input wiring --------------------------------------------------

  function isTextTarget(el) {
    if (!el) return false;
    var tag = (el.tagName || '').toUpperCase();
    return tag === 'INPUT' || tag === 'TEXTAREA' || el.isContentEditable;
  }

  function isPTTKey(ev) {
    return ev.key === 'Enter' || ev.key === 'End' || ev.code === 'Space' || ev.key === ' ';
  }

  function bindKeys() {
    window.addEventListener('keydown', function (ev) {
      if (ev.repeat) return;
      if (isTextTarget(document.activeElement)) return;
      if (!isPTTKey(ev)) return;
      ev.preventDefault();
      startRecording();
    });
    window.addEventListener('keyup', function (ev) {
      if (isTextTarget(document.activeElement)) return;
      if (!isPTTKey(ev)) return;
      ev.preventDefault();
      stopRecording();
    });
  }

  function bindPointer() {
    var btn = $('rec');
    btn.addEventListener('pointerdown', function (ev) { ev.preventDefault(); startRecording(); });
    btn.addEventListener('pointerup',     function (ev) { ev.preventDefault(); stopRecording(); });
    btn.addEventListener('pointercancel', function ()   { stopRecording(); });
    btn.addEventListener('pointerleave',  function ()   {
      if (REC.state === 'recording') stopRecording();
    });
  }

  function bindUI() {
    $('new-session').addEventListener('click', function () {
      createSession();
      rerender();
    });
    $('export-json').addEventListener('click', exportJSON);
    $('export-md').addEventListener('click', exportMarkdown);
    $('toggle-settings').addEventListener('click', function () {
      var s = $('settings');
      var open = !s.hidden;
      s.hidden = open;
      this.setAttribute('aria-expanded', open ? 'false' : 'true');
    });
    $('token-input').value = S.token;
    $('token-input').addEventListener('change', function () {
      S.token = this.value.trim();
      persist();
    });
    $('session-select').addEventListener('change', function () {
      S.currentSessionId = this.value;
      persist();
      rerender();
    });
  }

  async function loadInfo() {
    try {
      var resp = await fetch('/api/info');
      if (!resp.ok) return;
      var info = await resp.json();
      var bits = ['flowstate ' + (info.version || 'dev')];
      bits.push(info.auth_required ? 'auth: required' : 'auth: off');
      $('version-line').textContent = bits.join(' · ');
    } catch (e) { /* offline-OK */ }
  }

  // ---- Boot ----------------------------------------------------------

  document.addEventListener('DOMContentLoaded', function () {
    loadState();
    loadOrCreateSession();
    bindKeys();
    bindPointer();
    bindUI();
    setRecState('idle');
    rerender();
    loadInfo();
  });
})();

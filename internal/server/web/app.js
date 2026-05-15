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
    // Auto-copy preference. Default off so the first run doesn't surprise
    // the user by stomping their clipboard. Stored as the literal string
    // "1" / "0" rather than JSON so this single-bit setting reads cleanly
    // in the browser's storage inspector.
    S.autoCopy = localStorage.getItem('flowstate.autoCopy') === '1';
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
      localStorage.setItem('flowstate.autoCopy', S.autoCopy ? '1' : '0');
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

  function fmtHistoryTime(d) {
    // Short, scannable: "8:04 AM · May 15". Falls back to locale string
    // if Intl.DateTimeFormat is unavailable.
    try {
      var t = d.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
      var dt = d.toLocaleDateString([], { month: 'short', day: 'numeric' });
      return t + ' · ' + dt;
    } catch (e) {
      return d.toLocaleString();
    }
  }

  // makeRawToggle wires a button + hidden <p> pair to behave like a
  // <details>/<summary>, but with predictable focus styling. The caller
  // owns the elements; we just hook the click + ARIA.
  function makeRawToggle(button, body) {
    button.setAttribute('aria-expanded', 'false');
    body.hidden = true;
    button.addEventListener('click', function () {
      var open = button.getAttribute('aria-expanded') === 'true';
      button.setAttribute('aria-expanded', open ? 'false' : 'true');
      body.hidden = open;
    });
  }

  // wireCopy attaches a click handler that copies getText()'s return
  // value to the clipboard, then flashes a "Copied" state on the button
  // for ~1.4s. getText is called at click time so the latest value is
  // always copied (important for the result card, which is restyled on
  // every transcription).
  function wireCopy(button, getText) {
    if (!button) return;
    var label = button.querySelector('.copy-label');
    var origLabel = label ? label.textContent : 'Copy';
    var resetTimer;
    button.addEventListener('click', function () {
      var text = getText();
      if (!text) return;
      var done = function (ok) {
        if (resetTimer) clearTimeout(resetTimer);
        if (label) label.textContent = ok ? 'Copied' : 'Failed';
        button.setAttribute('data-state', ok ? 'copied' : 'failed');
        resetTimer = setTimeout(function () {
          if (label) label.textContent = origLabel;
          button.removeAttribute('data-state');
        }, 1400);
      };
      // navigator.clipboard is available on localhost (secure context
      // exemption) and on https://. Fall back to a textarea-execCommand
      // trick for older browsers — unlikely on a desktop dictation tool
      // but cheap insurance.
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(
          function () { done(true); },
          function () { done(legacyCopy(text)); }
        );
      } else {
        done(legacyCopy(text));
      }
    });
  }

  // autoCopyToClipboard runs the same writeText → legacyCopy fallback as
  // the manual Copy buttons, but with no DOM button to flash. Success
  // flashes a brief "Copied to clipboard" status on the recorder hint;
  // failure is silent (we don't want a noisy error every time the
  // browser denies clipboard access).
  function autoCopyToClipboard(text) {
    var statusEl = $('rec-status');
    var show = function (ok) {
      if (!ok || !statusEl) return;
      statusEl.textContent = '✓ Copied to clipboard';
      // Reset after a moment so the next recording sees a clean status.
      // The whole notification is best-effort — no need to clear a prior
      // timer; the final value will resolve to "" within ~1.4s.
      setTimeout(function () {
        if (statusEl.textContent === '✓ Copied to clipboard') {
          statusEl.textContent = '';
        }
      }, 1400);
    };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(
        function () { show(true); },
        function () { show(legacyCopy(text)); }
      );
    } else {
      show(legacyCopy(text));
    }
  }

  // legacyCopy uses the deprecated execCommand path as a last-resort
  // fallback. Returns true on success.
  function legacyCopy(text) {
    try {
      var ta = document.createElement('textarea');
      ta.value = text;
      ta.style.position = 'fixed';
      ta.style.left = '-9999px';
      document.body.appendChild(ta);
      ta.select();
      var ok = document.execCommand('copy');
      document.body.removeChild(ta);
      return ok;
    } catch (e) {
      return false;
    }
  }

  // makeCopyButton builds the same "Copy" button used on the result
  // card so per-history-item rows can render one identical to the one
  // in the HTML template. Returns the wired <button>.
  function makeCopyButton(getText) {
    var btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'copy-btn';
    btn.setAttribute('aria-label', 'Copy cleaned text');
    var icon = document.createElement('span');
    icon.className = 'copy-icon';
    icon.setAttribute('aria-hidden', 'true');
    icon.textContent = '⧉';
    var label = document.createElement('span');
    label.className = 'copy-label';
    label.textContent = 'Copy';
    btn.appendChild(icon);
    btn.appendChild(label);
    wireCopy(btn, getText);
    return btn;
  }

  function renderHistory() {
    var s = currentSession();
    var list = $('history-list');
    list.innerHTML = '';
    var items = s ? s.items.slice().reverse() : [];
    var countEl = $('history-count');
    if (items.length === 0) {
      $('history-empty').hidden = false;
      $('result').hidden = true;
      if (countEl) countEl.textContent = '';
      return;
    }
    $('history-empty').hidden = true;
    if (countEl) countEl.textContent = items.length + (items.length === 1 ? ' transcript' : ' transcripts');
    for (var i = 0; i < items.length; i++) {
      var it = items[i];
      var li = document.createElement('li');
      li.className = 'history-item';

      var meta = document.createElement('div');
      meta.className = 'history-meta';
      var ts = document.createElement('span');
      ts.textContent = fmtHistoryTime(new Date(it.ts));
      meta.appendChild(ts);
      // Per-item copy button. Closure captures `it` so each row copies
      // its own cleaned text, not whichever row was added last.
      (function (item) {
        meta.appendChild(makeCopyButton(function () { return item.cleaned || ''; }));
      })(it);
      li.appendChild(meta);

      var p = document.createElement('p');
      p.className = 'history-cleaned';
      p.textContent = it.cleaned || '(empty cleaned output)';
      li.appendChild(p);

      if (it.raw && it.raw !== it.cleaned) {
        var toggle = document.createElement('button');
        toggle.type = 'button';
        toggle.className = 'raw-toggle';
        toggle.textContent = 'Show raw';
        var raw = document.createElement('p');
        raw.className = 'history-raw';
        raw.textContent = it.raw;
        makeRawToggle(toggle, raw);
        li.appendChild(toggle);
        li.appendChild(raw);
      }

      list.appendChild(li);
    }
    // Latest card
    var latest = items[0];
    $('result-cleaned').textContent = latest.cleaned || '(no cleaned output)';
    var rawEl = $('result-raw');
    var rawToggle = $('result-raw-toggle');
    if (latest.raw && latest.raw !== latest.cleaned) {
      rawEl.textContent = latest.raw;
      rawToggle.hidden = false;
      // Reset to collapsed on each render so the panel doesn't surprise
      // the user with an open raw block on a new transcription.
      rawToggle.setAttribute('aria-expanded', 'false');
      rawEl.hidden = true;
    } else {
      rawEl.textContent = '';
      rawToggle.hidden = true;
      rawEl.hidden = true;
    }
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
      // Auto-copy the cleaned text if the user opted in. Uses the same
      // clipboard path as the manual Copy buttons (Promise → fallback)
      // and flashes a small status hint instead of taking over a button,
      // since no specific button is "the" target here.
      if (S.autoCopy && item.cleaned) {
        autoCopyToClipboard(item.cleaned);
      }
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

  function isToggleKey(ev) {
    return ev.key === 'Enter' || ev.key === 'End' || ev.code === 'Space' || ev.key === ' ';
  }

  // toggleRecording flips recording state. Idle → start; recording → stop.
  // Uploading is a no-op so a stray click during the network round trip
  // doesn't kick off a second capture before the first response lands.
  function toggleRecording() {
    if (REC.state === 'recording') {
      stopRecording();
    } else if (REC.state === 'idle' || !REC.state) {
      startRecording();
    }
  }

  function bindKeys() {
    // Click-to-toggle: one keydown starts, the next stops. We listen on
    // keydown only (no keyup) so holding the key doesn't accidentally
    // toggle off when released. ev.repeat is filtered so a user holding
    // the key down doesn't spam toggles.
    window.addEventListener('keydown', function (ev) {
      if (ev.repeat) return;
      if (isTextTarget(document.activeElement)) return;
      if (!isToggleKey(ev)) return;
      ev.preventDefault();
      toggleRecording();
    });
  }

  function bindPointer() {
    var btn = $('rec');
    // Use 'click' (not pointerdown/up) so the native button semantics
    // are preserved: Space/Enter while the button is focused still fires
    // the click via the browser's default activation behavior, and a
    // stray pointerdown that doesn't land in a release won't toggle.
    btn.addEventListener('click', function (ev) {
      ev.preventDefault();
      toggleRecording();
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
    // Auto-copy toggle. Initialize from the loaded S.autoCopy so the
    // checkbox state survives a page reload.
    var autoCopyBox = $('auto-copy');
    if (autoCopyBox) {
      autoCopyBox.checked = !!S.autoCopy;
      autoCopyBox.addEventListener('change', function () {
        S.autoCopy = this.checked;
        persist();
      });
    }
    // Clear-local-data button. Confirms first since this wipes every
    // session, transcript, and the saved token. After clearing we reload
    // the page so the in-memory state restarts cleanly without us having
    // to reset every UI surface manually.
    var clearBtn = $('clear-storage');
    if (clearBtn) {
      clearBtn.addEventListener('click', function () {
        var ok = window.confirm(
          'Clear all local data?\n\n' +
          'This wipes every session, transcript, and the saved API token ' +
          'from this browser. The server is untouched. This cannot be undone.'
        );
        if (!ok) return;
        try {
          localStorage.removeItem('flowstate.sessions');
          localStorage.removeItem('flowstate.currentSessionId');
          localStorage.removeItem('flowstate.token');
          localStorage.removeItem('flowstate.autoCopy');
        } catch (e) { /* ignore quota / disabled storage errors */ }
        location.reload();
      });
    }
    // Result-card raw toggle is static markup; per-item toggles are wired
    // in renderHistory() since they're recreated on every render.
    var resultToggle = $('result-raw-toggle');
    var resultRaw = $('result-raw');
    if (resultToggle && resultRaw) {
      resultToggle.addEventListener('click', function () {
        var open = resultToggle.getAttribute('aria-expanded') === 'true';
        resultToggle.setAttribute('aria-expanded', open ? 'false' : 'true');
        resultRaw.hidden = open;
      });
    }
    // Result-card copy button is also static markup. The getText callback
    // pulls the cleaned text fresh on each click so it always copies the
    // latest transcript, not whatever was here when bindUI ran.
    wireCopy($('result-copy'), function () {
      var el = $('result-cleaned');
      return el ? el.textContent : '';
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

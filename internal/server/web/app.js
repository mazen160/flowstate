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
    state: 'idle', // idle | recording | uploading
    // Live mic-level visualization. The AudioContext + AnalyserNode are
    // created on demand the first time we record (so we don't trigger an
    // AudioContext at page load and bump into Chrome's autoplay policy)
    // and torn down when the stream stops.
    audioCtx: null,
    analyser: null,
    sourceNode: null,
    rafId: 0,
    timeData: null,
    // Press timestamp captured on press; consulted on release to decide
    // whether the gesture was a tap (toggle) or a hold (stop now).
    pressStartTs: 0
  };

  // METER_BARS must match the count of <span class="rec-meter-bar"> nodes
  // in index.html. Each bar reads its height from CSS custom property
  // --lvl0 … --lvl(N-1) on the .rec-btn element.
  var METER_BARS = 7;

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

  // ---- Toasts --------------------------------------------------------
  //
  // Transient bottom-right confirmations. Each toast is appended to the
  // host (#toast-host), animated in, and auto-dismissed after a delay.
  // A small queue cap prevents spam if a user mashes a button.

  var TOAST_MAX = 4;
  var TOAST_DEFAULT_MS = 2200;

  // showToast(message, opts?) — minimal API.
  //   message       primary line ("Copied to clipboard")
  //   opts.desc     optional second line ("44 characters")
  //   opts.variant  "success" | "info" | "warn"  (default "success")
  //   opts.icon     glyph for the icon dot (default "✓")
  //   opts.duration milliseconds before auto-dismiss (default 2200)
  function showToast(message, opts) {
    var host = $('toast-host');
    if (!host) return;
    opts = opts || {};

    // Cap the queue. Drop the oldest toast(s) so we never stack more
    // than TOAST_MAX at once; this keeps repeated copies from drifting
    // up off the screen.
    while (host.children.length >= TOAST_MAX) {
      var oldest = host.firstElementChild;
      if (!oldest) break;
      dismissToast(oldest);
    }

    var toast = document.createElement('div');
    toast.className = 'toast';
    toast.setAttribute('data-variant', opts.variant || 'success');
    toast.setAttribute('role', 'status');

    var icon = document.createElement('span');
    icon.className = 'toast-icon';
    icon.setAttribute('aria-hidden', 'true');
    icon.textContent = opts.icon || '✓';
    toast.appendChild(icon);

    var text = document.createElement('span');
    text.className = 'toast-text';
    var title = document.createElement('span');
    title.className = 'toast-title';
    title.textContent = message;
    text.appendChild(title);
    if (opts.desc) {
      var desc = document.createElement('span');
      desc.className = 'toast-desc';
      desc.textContent = opts.desc;
      text.appendChild(desc);
    }
    toast.appendChild(text);

    var close = document.createElement('button');
    close.type = 'button';
    close.className = 'toast-close';
    close.setAttribute('aria-label', 'Dismiss notification');
    close.innerHTML = '&times;';
    close.addEventListener('click', function () { dismissToast(toast); });
    toast.appendChild(close);

    host.appendChild(toast);
    // Force a layout flush before adding the "in" class so the CSS
    // transition runs on first render.
    void toast.offsetHeight;
    toast.classList.add('in');

    var ms = typeof opts.duration === 'number' ? opts.duration : TOAST_DEFAULT_MS;
    if (ms > 0) {
      toast._dismissTimer = setTimeout(function () { dismissToast(toast); }, ms);
    }
    return toast;
  }

  function dismissToast(toast) {
    if (!toast || !toast.parentNode) return;
    if (toast._dismissTimer) { clearTimeout(toast._dismissTimer); toast._dismissTimer = null; }
    toast.classList.remove('in');
    toast.classList.add('out');
    // Remove from the DOM after the slide-out completes. 280 > the 220ms
    // CSS transition so we're safe even on slower machines.
    setTimeout(function () {
      if (toast.parentNode) toast.parentNode.removeChild(toast);
    }, 280);
  }

  function setRecState(name) {
    REC.state = name;
    var btn = $('rec');
    btn.dataset.state = name === 'idle' ? '' : name;
    btn.setAttribute('aria-pressed', name === 'recording' ? 'true' : 'false');
    var label = '';
    if (name === 'recording') label = '● Recording…';
    if (name === 'uploading') label = '● Uploading…';
    $('rec-status').textContent = label;
    // Snap the meter bars to a state-appropriate resting pose so the
    // visual matches what the CSS expects. This is purely cosmetic;
    // AnalyserNode teardown happens in stopMeter() once mediaRecorder.onstop
    // fires and we close the stream.
    //
    // - idle: collapse to 0 so the brand mark gets the spotlight.
    // - uploading: paint a fixed bell curve so the CSS opacity pulse
    //   has something to animate (tickMeter is no longer running).
    // - recording: leave as-is; tickMeter will overwrite within ~16ms.
    if (name === 'idle') {
      writeMeter([0, 0, 0, 0, 0, 0, 0]);
    } else if (name === 'uploading') {
      writeMeter([0.25, 0.45, 0.7, 0.85, 0.7, 0.45, 0.25]);
    }
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
        // Confirm the action with a toast as well as the in-button flash.
        // The toast is the primary affordance for visibility; the button
        // flash is the local feedback. Showing both is redundant by
        // design — copy is silent enough that a single subtle signal
        // gets missed, especially when the button is far from the user's
        // gaze (history rows, for example).
        if (ok) {
          showToast('Copied to clipboard', {
            desc: text.length === 1 ? '1 character' : (text.length + ' characters'),
            variant: 'success',
            icon: '✓'
          });
        } else {
          showToast('Copy failed', {
            desc: 'Your browser blocked clipboard access.',
            variant: 'warn',
            icon: '!'
          });
        }
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
  // the manual Copy buttons, but with no DOM button to flash. We surface
  // a toast on success so the user has a clear confirmation that the
  // cleaned text is on the clipboard — exactly the same toast shape the
  // manual Copy buttons use, so the feedback feels uniform. Failures
  // stay silent: an auto-copy error every transcription would be noise,
  // and the user can always copy manually from the result card.
  function autoCopyToClipboard(text) {
    var done = function (ok) {
      if (!ok) return;
      showToast('Auto-copied to clipboard', {
        desc: text.length === 1 ? '1 character' : (text.length + ' characters'),
        variant: 'success',
        icon: '✓'
      });
    };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(
        function () { done(true); },
        function () { done(legacyCopy(text)); }
      );
    } else {
      done(legacyCopy(text));
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

  // ---- Live mic-level meter -----------------------------------------
  //
  // The meter is a pure visual layer: 7 bars over the rec-btn whose heights
  // are driven from an AnalyserNode tap on the live MediaStream. We bucket
  // the time-domain samples into 7 bands and write each band's amplitude
  // to a CSS custom property (--lvl0 … --lvl6) that style.css consumes.
  //
  // The MediaRecorder is left untouched — both nodes share the same stream
  // without contention, so the upload pipeline keeps full fidelity.

  // Min/max bar heights in px. Kept in sync with style.css min/max-height
  // on .rec-meter-bar so the visual cap matches what we tween to.
  var METER_MIN_PX = 8;
  var METER_MAX_PX = 56;

  function writeMeter(values) {
    var btn = $('rec');
    if (!btn) return;
    for (var i = 0; i < METER_BARS; i++) {
      var v = values[i] || 0;                                // 0…1
      var h = METER_MIN_PX + Math.round(v * (METER_MAX_PX - METER_MIN_PX));
      btn.style.setProperty('--lvl' + i, h + 'px');
    }
  }

  function startMeter(stream) {
    if (!stream || typeof window.AudioContext === 'undefined' && typeof window.webkitAudioContext === 'undefined') {
      return;
    }
    try {
      var Ctor = window.AudioContext || window.webkitAudioContext;
      REC.audioCtx = new Ctor();
      REC.sourceNode = REC.audioCtx.createMediaStreamSource(stream);
      REC.analyser = REC.audioCtx.createAnalyser();
      REC.analyser.fftSize = 1024;            // ~512 time-domain samples
      REC.analyser.smoothingTimeConstant = 0.6;
      REC.sourceNode.connect(REC.analyser);   // analyser is a sink, no destination connect
      REC.timeData = new Uint8Array(REC.analyser.fftSize);
      tickMeter();
    } catch (e) {
      // Best effort. The page still records; we just won't show levels.
      stopMeter();
    }
  }

  function tickMeter() {
    if (!REC.analyser || REC.state !== 'recording') return;
    REC.analyser.getByteTimeDomainData(REC.timeData);
    // Bucket the samples into METER_BARS bands. For each band, take
    // peak amplitude (deviation from 128, the silent center of u8 PCM).
    var n = REC.timeData.length;
    var per = Math.floor(n / METER_BARS) || 1;
    var levels = new Array(METER_BARS);
    for (var b = 0; b < METER_BARS; b++) {
      var start = b * per;
      var end   = (b === METER_BARS - 1) ? n : start + per;
      var peak = 0;
      for (var i = start; i < end; i++) {
        var dev = Math.abs(REC.timeData[i] - 128);
        if (dev > peak) peak = dev;
      }
      // Normalize to 0…1 with a small floor so silent rooms still show
      // a hint of life, and a soft ceiling so loud talkers don't clip
      // the visual.
      var v = Math.min(1, peak / 96);
      // Light edge-bias: outermost bars are slightly damped so the meter
      // reads as a "wave" rather than a flat block.
      if (b === 0 || b === METER_BARS - 1) v *= 0.7;
      else if (b === 1 || b === METER_BARS - 2) v *= 0.85;
      levels[b] = Math.max(0, v);
    }
    writeMeter(levels);
    REC.rafId = window.requestAnimationFrame(tickMeter);
  }

  function stopMeter() {
    if (REC.rafId) {
      window.cancelAnimationFrame(REC.rafId);
      REC.rafId = 0;
    }
    try { if (REC.sourceNode) REC.sourceNode.disconnect(); } catch (e) { /* idempotent */ }
    try { if (REC.analyser)   REC.analyser.disconnect();   } catch (e) { /* idempotent */ }
    if (REC.audioCtx && typeof REC.audioCtx.close === 'function') {
      // close() returns a promise on modern browsers; we don't await it
      // because failures here are silent (the page is moving on).
      try { REC.audioCtx.close(); } catch (e) { /* best effort */ }
    }
    REC.audioCtx = null;
    REC.analyser = null;
    REC.sourceNode = null;
    REC.timeData = null;
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
        // Tear down the analyser before we stop the stream — order isn't
        // strictly necessary (disconnect() is idempotent on dead nodes),
        // but doing it first keeps the visual disappearance synchronous
        // with the state change we're about to fire.
        stopMeter();
        if (REC.stream) {
          REC.stream.getTracks().forEach(function (t) { t.stop(); });
          REC.stream = null;
        }
        upload(blob, ext);
      };
      mr.start();
      setRecState('recording');
      // The meter is created once we know we're actually recording so a
      // permission prompt or device error doesn't leave a stale audio
      // context behind. setRecState above flips data-state to "recording"
      // which makes the .rec-meter visible; startMeter starts driving it.
      startMeter(stream);
    } catch (err) {
      setRecState('idle');
      $('rec-status').textContent = friendlyMicError(err);
    } finally {
      REC.starting = false;
    }
  }

  // friendlyMicError maps the DOMException name from getUserMedia to a
  // short user-facing line. The native messages from Chrome/Firefox/Safari
  // are inconsistent ("Permission denied", "Permission dismissed",
  // "The request is not allowed by the user agent…"); the name field is
  // standardized. Returning a single sentence keeps it readable in the
  // small status line below the record button.
  function friendlyMicError(err) {
    if (!err) return 'Microphone unavailable.';
    var name = err.name || '';
    switch (name) {
      case 'NotAllowedError':
      case 'SecurityError':
        return 'Microphone access denied — allow it in your browser settings, then try again.';
      case 'NotFoundError':
      case 'OverconstrainedError':
        return 'No microphone found. Plug one in or pick a different input device.';
      case 'NotReadableError':
        return 'Microphone is in use by another app. Close it and try again.';
      case 'AbortError':
        return 'Microphone request was aborted. Try again.';
      case 'TypeError':
        return 'Recording is not supported in this browser. Try a recent Chrome, Firefox, or Safari.';
      default:
        return 'Microphone error: ' + (err.message || name || 'unknown');
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

  // ---- Recording trigger (tap-or-hold, one model for keys + pointer) ---
  //
  // A single state machine drives the record button, the Space key, and
  // the Enter key. The model is:
  //
  //   On press:
  //     - If we're recording, stop. (This is the "tap again to stop" path
  //       after a previous tap-to-start.)
  //     - Otherwise start recording, and remember the press timestamp.
  //   On release:
  //     - If the press lasted >= HOLD_THRESHOLD_MS, treat it as a hold and
  //       stop recording now (release-to-send).
  //     - Otherwise treat it as a tap; leave recording running. The next
  //       press will hit the "stop" branch above.
  //
  // No source tracking, no timers, no pointer capture, no data-attributes.
  // Just one number (REC.pressStartTs) and one threshold.
  var HOLD_THRESHOLD_MS = 320;

  function pressRecord() {
    if (REC.state === 'uploading') return;     // ignore presses during upload
    if (REC.state === 'recording') {
      // A second press while already recording is the user explicitly
      // stopping a tap-started recording. Reset pressStartTs so the
      // upcoming release can't be misread as a hold.
      REC.pressStartTs = 0;
      stopRecording();
      return;
    }
    REC.pressStartTs = Date.now();
    startRecording();
  }

  function releaseRecord() {
    if (!REC.pressStartTs) return;             // no matching press → nothing to release
    var heldMs = Date.now() - REC.pressStartTs;
    REC.pressStartTs = 0;
    if (heldMs >= HOLD_THRESHOLD_MS && REC.state === 'recording') {
      stopRecording();
    }
    // else: a tap. Leave recording running; the next pressRecord() stops it.
  }

  function bindKeys() {
    // Enter and Space behave identically: tap to toggle, hold to stream.
    // We capture-phase the listener on document so we beat any focused
    // <button>'s default Space-activation, which would otherwise scroll
    // the page or trigger an unrelated button (New session, Settings,
    // etc). preventDefault on keydown stops the page-scroll on Space
    // when focus is on <body>.
    var isHotkey = function (ev) {
      return ev.key === 'Enter' || ev.code === 'Space' || ev.key === ' ';
    };
    document.addEventListener('keydown', function (ev) {
      if (!isHotkey(ev)) return;
      if (ev.repeat) { ev.preventDefault(); return; }
      if (isTextTarget(document.activeElement)) return;
      ev.preventDefault();
      pressRecord();
    }, true);
    document.addEventListener('keyup', function (ev) {
      if (!isHotkey(ev)) return;
      if (isTextTarget(document.activeElement)) return;
      ev.preventDefault();
      releaseRecord();
    }, true);
    // Window blur fires the release too: alt-tabbing while holding Space
    // shouldn't strand the page in recording mode forever.
    window.addEventListener('blur', function () {
      if (REC.pressStartTs) releaseRecord();
    });
  }

  function bindPointer() {
    var btn = $('rec');
    if (!btn) return;
    // One pointerdown/up pair handles mouse, touch, and pen. A right-
    // click (button !== 0) is ignored.
    btn.addEventListener('pointerdown', function (ev) {
      if (ev.button !== undefined && ev.button !== 0) return;
      ev.preventDefault();
      pressRecord();
    });
    btn.addEventListener('pointerup', function (ev) {
      ev.preventDefault();
      releaseRecord();
    });
    // pointercancel covers gestures that the OS reclaims (system menus,
    // touch interruptions). Treat it as a release so we don't strand a
    // recording.
    btn.addEventListener('pointercancel', function () {
      if (REC.pressStartTs) releaseRecord();
    });
    // The browser also fires a synthetic click on Space/Enter when the
    // button has focus — we already handled that via the document-level
    // keydown above, so swallow this duplicate.
    btn.addEventListener('click', function (ev) { ev.preventDefault(); });
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
      // When opening (open was true → we just flipped to !hidden), move
      // focus into the drawer so keyboard users can immediately tab
      // through it. We focus the token input because it's the field
      // most users came here to edit. The setTimeout deferral lets the
      // browser apply the unhide before we focus — otherwise some
      // browsers refuse to focus a still-hidden element.
      if (open) {
        var tokenInput = $('token-input');
        if (tokenInput) {
          setTimeout(function () { tokenInput.focus({ preventScroll: false }); }, 0);
        }
      }
    });
    $('token-input').value = S.token;
    $('token-input').addEventListener('change', function () {
      S.token = this.value.trim();
      persist();
      // Token changes fire on blur (the 'change' event), so each save
      // is a deliberate user action — toast every time so they know
      // it landed.
      showToast('Settings saved', {
        desc: S.token ? 'API token stored locally.' : 'API token cleared.',
        variant: 'info',
        icon: '✓'
      });
    });
    $('session-select').addEventListener('change', function () {
      S.currentSessionId = this.value;
      persist();
      rerender();
      var s = currentSession();
      showToast('Switched session', {
        desc: s ? ('#' + s.id.slice(0, 6) + ' · ' + s.items.length +
                   (s.items.length === 1 ? ' transcript' : ' transcripts'))
                : null,
        variant: 'info',
        icon: '↻'
      });
    });
    // Auto-copy toggle. Initialize from the loaded S.autoCopy so the
    // checkbox state survives a page reload.
    var autoCopyBox = $('auto-copy');
    if (autoCopyBox) {
      autoCopyBox.checked = !!S.autoCopy;
      autoCopyBox.addEventListener('change', function () {
        S.autoCopy = this.checked;
        persist();
        showToast('Settings saved', {
          desc: S.autoCopy
            ? 'Cleaned text will be copied automatically.'
            : 'Cleaned text will no longer be copied automatically.',
          variant: 'info',
          icon: '✓'
        });
      });
    }
    // Clear-local-data button. Confirms first since this wipes every
    // session, transcript, and the saved token. After clearing we set
    // a one-shot flag in sessionStorage so the post-reload boot can
    // surface a confirmation toast (the toast wouldn't survive the
    // reload otherwise), then reload so the in-memory state restarts
    // cleanly without us having to reset every UI surface manually.
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
        try { sessionStorage.setItem('flowstate.cleared', '1'); }
        catch (e) { /* private mode — toast just won't fire */ }
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
    // Surface the post-clear confirmation if the previous page wrote
    // the one-shot flag before reloading. We consume the flag on read
    // so a second navigation doesn't re-trigger the toast.
    try {
      if (sessionStorage.getItem('flowstate.cleared') === '1') {
        sessionStorage.removeItem('flowstate.cleared');
        showToast('Local data cleared', {
          desc: 'Every session, transcript, and saved token has been removed from this browser.',
          variant: 'info',
          icon: '✓',
          duration: 3600
        });
      }
    } catch (e) { /* sessionStorage disabled — silent skip */ }
  });
})();

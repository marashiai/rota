// voice.js: reading aloud and dictation on rota's pages: the app, the review pages, the reports.
// ElevenLabs does both through rota when its key may (rota keeps the key and asks it); else the
// browser's own voice and dictation do. Every field you write in gets a microphone while you
// write in it; anything you select gets a "Read aloud" button; the app's messages have a
// speaker of their own (rotaVoice.say).
(function () {
  'use strict';
  if (window.rotaVoice) return;
  const me = document.currentScript;
  const token = () => (me && me.dataset.token) || (document.body && document.body.dataset.token) || '';
  const SR = window.SpeechRecognition || window.webkitSpeechRecognition;
  const canRecord = !!(navigator.mediaDevices && navigator.mediaDevices.getUserMedia && window.MediaRecorder);
  const canSpeak = 'speechSynthesis' in window;
  const MOST = 20000; // the most one reading reads: a whole page selected is not read to the end
  const V = {known: false, key: false, say: false, listen: false, sayWhy: '', listenWhy: '', max: 5000};

  // ---- what rota found the key may do ----
  let asking = null;
  function look() {
    asking = fetch('/api/voice', {cache: 'no-store'}).then(r => r.ok ? r.json() : null).then(j => {
      if (j) Object.assign(V, j, {known: true});
      changed();
    }).catch(() => {});
    return asking;
  }
  function changed() { document.dispatchEvent(new CustomEvent('rotavoice')); placeMic(); }
  const elevenSays = () => V.say;
  const elevenListens = () => V.listen && canRecord;
  const canListen = () => window.isSecureContext !== false && (elevenListens() || !!SR); // a microphone needs https or this machine
  document.addEventListener('visibilitychange', () => { if (!document.hidden) look(); }); // a key set elsewhere meanwhile

  // ---- the pieces on the page ----
  const svg = d => `<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${d}</svg>`;
  const ICON = {
    mic: svg('<rect x="9" y="2" width="6" height="12" rx="3"/><path d="M5 10v1a7 7 0 0 0 14 0v-1"/><line x1="12" y1="18" x2="12" y2="22"/>'),
    speaker: svg('<polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"/><path d="M15.5 8.5a5 5 0 0 1 0 7"/><path d="M19 5a10 10 0 0 1 0 14"/>'),
    stop: svg('<rect x="6" y="6" width="12" height="12" rx="2" fill="currentColor"/>'),
    wait: svg('<circle cx="12" cy="12" r="9" stroke-dasharray="14 42"/>'),
  };
  const style = document.createElement('style');
  style.textContent = `
.vmic{position:fixed;z-index:2147483000;width:28px;height:28px;border-radius:50%;border:0;margin:0;padding:0;display:none;align-items:center;justify-content:center;background:transparent;color:#6b6b78;cursor:pointer;-webkit-tap-highlight-color:transparent;touch-action:manipulation}
.vmic.show{display:flex}.vmic:hover{background:rgba(127,127,140,.16)}
.vmic.rec{background:#d93025;color:#fff;animation:vpulse 1.2s ease-in-out infinite}
.vmic.busy{cursor:progress}.vmic.busy svg{animation:vspin 1s linear infinite}
.vmic-pad{padding-right:34px!important}
.vpill,.vstop,.vnote{position:fixed;z-index:2147483001;display:none;align-items:center;gap:6px;border:0;margin:0;border-radius:999px;padding:8px 13px;font:600 13px/1.2 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:#1b1b1f;color:#fff;box-shadow:0 4px 16px rgba(0,0,0,.28);cursor:pointer;-webkit-tap-highlight-color:transparent;touch-action:manipulation;white-space:nowrap}
.vpill.show,.vstop.show,.vnote.show{display:flex}
.vstop{left:50%;transform:translateX(-50%);bottom:calc(env(safe-area-inset-bottom,0px) + 92px)}
.vnote{left:50%;transform:translateX(-50%);top:calc(env(safe-area-inset-top,0px) + 12px);cursor:default;font-weight:500;white-space:normal;max-width:min(92vw,520px);border-radius:12px}
@media (prefers-color-scheme:dark){.vpill,.vstop,.vnote{background:#ececf1;color:#121216}.vmic{color:#a3a3ae}}
@keyframes vpulse{0%,100%{box-shadow:0 0 0 0 rgba(217,48,37,.5)}50%{box-shadow:0 0 0 7px rgba(217,48,37,0)}}
@keyframes vspin{to{transform:rotate(360deg)}}
.vsay.on{color:#d93025}`;
  const make = (cls, html, label) => {
    const b = document.createElement('button');
    b.type = 'button'; b.className = cls; b.innerHTML = html;
    if (label) { b.setAttribute('aria-label', label); b.title = label; }
    return b;
  };
  const mic = make('vmic', ICON.mic, 'Speak: what you say is written here');
  const pill = make('vpill', ICON.speaker + '<span>Read aloud</span>');
  const stopper = make('vstop', ICON.stop + '<span>Stop reading</span>');
  const noteEl = document.createElement('div');
  noteEl.className = 'vnote'; noteEl.setAttribute('role', 'status');
  let noteT = 0;
  function note(text) {
    noteEl.textContent = text; noteEl.classList.add('show');
    clearTimeout(noteT); noteT = setTimeout(() => noteEl.classList.remove('show'), 5000);
  }
  // A tap on these keeps the focus and the selection where they are: on a phone the keyboard
  // stays up and the selected words stay selected.
  for (const b of [mic, pill, stopper]) {
    b.addEventListener('pointerdown', e => e.preventDefault());
    b.addEventListener('mousedown', e => e.preventDefault());
  }
  function mount() {
    document.head.appendChild(style);
    for (const x of [mic, pill, stopper, noteEl]) document.body.appendChild(x);
  }
  if (document.body) mount(); else document.addEventListener('DOMContentLoaded', mount);

  // ---- reading aloud ----
  const audio = new Audio();
  let silentURL = '';
  function unlock() { // in the tap itself: a phone then lets this player play what comes later
    if (!silentURL) {
      const n = 800, b = new Uint8Array(44 + n), d = new DataView(b.buffer);
      'RIFF'.split('').forEach((c, i) => { b[i] = c.charCodeAt(0); });
      d.setUint32(4, 36 + n, true);
      'WAVEfmt '.split('').forEach((c, i) => { b[8 + i] = c.charCodeAt(0); });
      d.setUint32(16, 16, true); d.setUint16(20, 1, true); d.setUint16(22, 1, true);
      d.setUint32(24, 8000, true); d.setUint32(28, 8000, true); d.setUint16(32, 1, true); d.setUint16(34, 8, true);
      'data'.split('').forEach((c, i) => { b[36 + i] = c.charCodeAt(0); });
      d.setUint32(40, n, true); b.fill(128, 44);
      silentURL = URL.createObjectURL(new Blob([b], {type: 'audio/wav'}));
    }
    try { audio.src = silentURL; const p = audio.play(); if (p) p.catch(() => {}); } catch (e) { /* it plays the reading or the browser reads it */ }
  }

  // parts cuts text where a reader pauses: the first part short, so it starts soon.
  function parts(text, first, rest) {
    const out = [];
    let left = text, lim = first;
    while (left) {
      if (left.length <= lim) { out.push(left); break; }
      let cut = -1;
      for (const re of [/\n/g, /[.!?…](?=\s)/g, /[,;:](?=\s)/g, / /g]) {
        let m, at = -1;
        re.lastIndex = 0;
        while ((m = re.exec(left)) && m.index < lim) at = m.index + 1;
        if (at > lim / 3) { cut = at; break; }
      }
      if (cut < 1) cut = lim;
      out.push(left.slice(0, cut).trim());
      left = left.slice(cut).trim();
      lim = rest;
    }
    return out.filter(Boolean);
  }
  function clean(text) {
    return String(text || '').replace(/\r/g, '').replace(/[ \t ]+/g, ' ').replace(/ *\n[\n ]*/g, '\n').trim();
  }

  let reading = null; // {stopped, btn, cancel}
  function say(text, btn) {
    if (reading && btn && reading.btn === btn) { stop(); return; } // its button again: stop
    stop();
    text = clean(text);
    if (!text) return;
    if (text.length > MOST) { text = text.slice(0, MOST); note('Reads the first ' + MOST.toLocaleString() + ' characters'); }
    const run = {stopped: false, btn, cancel: null, ctl: window.AbortController ? new AbortController() : null};
    reading = run;
    if (btn) btn.classList.add('on');
    stopper.classList.add('show');
    if (elevenSays()) {
      unlock();
      // in the tap too: a phone lets the browser's voice speak later only when it spoke in a tap,
      // and it reads what ElevenLabs could not
      if (canSpeak) try { speechSynthesis.speak(new SpeechSynthesisUtterance('')); } catch (e) { /* it reads with ElevenLabs */ }
      readEleven(run, parts(text, 280, Math.min(1500, V.max || 1500)));
    } else readBrowser(run, text);
  }
  function stop() {
    const run = reading;
    if (!run) return;
    run.stopped = true;
    if (run.cancel) run.cancel();
    if (run.ctl) run.ctl.abort(); // the parts asked for ahead
    done(run);
  }
  function done(run) {
    if (reading !== run) return;
    reading = null;
    if (run.btn) run.btn.classList.remove('on');
    stopper.classList.remove('show');
  }
  function fetchPart(text, run) {
    const p = fetch('/api/voice/say', {method: 'POST', headers: {'X-Rota-Token': token()}, body: new URLSearchParams({text}), signal: run.ctl ? run.ctl.signal : undefined}).then(async r => {
      if (r.ok) return r.blob();
      const j = await r.json().catch(() => ({}));
      throw new Error(r.status === 403 ? 'rota restarted: reload the page to use ElevenLabs again' : j.error || 'ElevenLabs did not read it');
    });
    p.catch(() => {}); // a part fetched ahead and not played, as the reading stopped
    return p;
  }
  async function readEleven(run, ps) {
    let next = fetchPart(ps[0], run);
    for (let i = 0; i < ps.length; i++) {
      let blob;
      try { blob = await next; } catch (e) {
        if (run.stopped) return;
        note(e.message + (canSpeak ? '. The browser reads it.' : ''));
        look();
        readBrowser(run, ps.slice(i).join('\n'));
        return;
      }
      if (run.stopped) return;
      if (i + 1 < ps.length) next = fetchPart(ps[i + 1], run);
      const url = URL.createObjectURL(blob);
      const played = await play(url, run);
      URL.revokeObjectURL(url);
      if (run.stopped) return;
      if (!played) { if (canSpeak) readBrowser(run, ps.slice(i).join('\n')); else done(run); return; }
    }
    done(run);
  }
  function play(url, run) {
    return new Promise(res => {
      audio.onended = () => res(true);
      audio.onerror = () => res(false);
      run.cancel = () => { audio.pause(); res(true); };
      audio.src = url;
      const p = audio.play();
      if (p) p.catch(() => res(false));
    });
  }
  function readBrowser(run, text) {
    if (!canSpeak) { note('This browser cannot read aloud'); done(run); return; }
    const ps = parts(text, 200, 200); // a browser stops a long utterance midway
    let i = 0, started = false;
    const next = () => {
      if (run.stopped || reading !== run) return;
      if (i >= ps.length) { done(run); return; }
      const u = new SpeechSynthesisUtterance(ps[i++]);
      u.onstart = () => { started = true; };
      u.onend = next;
      u.onerror = e => { if (e.error === 'interrupted' || e.error === 'canceled') return; next(); };
      speechSynthesis.speak(u);
    };
    run.cancel = () => speechSynthesis.cancel();
    speechSynthesis.cancel();
    next();
    setTimeout(() => { // a phone may drop it without a word: Stop reading does not stay
      if (!started && reading === run && !speechSynthesis.speaking) { done(run); note('The browser did not read it: tap again to read it'); }
    }, 3000);
  }
  stopper.addEventListener('click', e => { e.preventDefault(); e.stopPropagation(); stop(); });

  // ---- read what is selected ----
  const FIELD_TYPES = ['', 'text', 'search', 'url', 'email'];
  const isField = x => !!x && (x.tagName === 'TEXTAREA' || x.tagName === 'INPUT' && FIELD_TYPES.includes((x.getAttribute('type') || '').toLowerCase()));
  function selection() { // the words selected and where they are on the screen
    const a = document.activeElement;
    if (isField(a) && a.selectionEnd > a.selectionStart) return {text: a.value.slice(a.selectionStart, a.selectionEnd), rect: a.getBoundingClientRect()};
    const s = window.getSelection();
    if (!s || s.isCollapsed || !s.rangeCount) return null;
    const text = s.toString();
    if (!text.trim()) return null;
    const range = s.getRangeAt(0), rects = range.getClientRects();
    return {text, rect: rects.length ? rects[rects.length - 1] : range.getBoundingClientRect()};
  }
  let shown = '', selT = 0;
  function placePill() {
    const sel = selection();
    if (!sel) { pill.classList.remove('show'); shown = ''; return; }
    shown = sel.text;
    pill.classList.add('show');
    const w = pill.offsetWidth, h = pill.offsetHeight, r = sel.rect;
    const left = Math.max(8, Math.min(innerWidth - w - 8, r.right - w / 2));
    let top = r.bottom + 10; // under the words: a phone shows its own menu over them
    if (top + h > innerHeight - 8) top = Math.max(8, r.top - h - 10);
    pill.style.left = left + 'px'; pill.style.top = top + 'px';
  }
  document.addEventListener('selectionchange', () => { clearTimeout(selT); selT = setTimeout(placePill, 180); });
  pill.addEventListener('click', e => {
    e.preventDefault(); e.stopPropagation();
    const text = shown || (selection() || {}).text;
    pill.classList.remove('show');
    say(text);
  });

  // ---- dictation ----
  let field = null, rec = null, busy = false;
  const writable = x => isField(x) && !x.disabled && !x.readOnly && x.type !== 'password';
  function placeMic() {
    const f = rec ? rec.field : field;
    const show = !!f && f.isConnected && (rec || busy || document.activeElement === f) && canListen();
    document.querySelectorAll('.vmic-pad').forEach(x => { if (x !== f || !show) x.classList.remove('vmic-pad'); });
    if (!show) { mic.classList.remove('show'); return; }
    f.classList.add('vmic-pad');
    const r = f.getBoundingClientRect();
    if (!r.width || !r.height || r.bottom < 0 || r.top > innerHeight) { mic.classList.remove('show'); return; }
    mic.classList.add('show');
    const tall = f.tagName === 'TEXTAREA' && r.height > 44;
    mic.style.left = (r.right - 32) + 'px';
    mic.style.top = (tall ? r.bottom - 32 : r.top + (r.height - 28) / 2) + 'px';
  }
  let rafT = 0;
  const soon = () => { if (!rafT) rafT = requestAnimationFrame(() => { rafT = 0; placeMic(); if (pill.classList.contains('show')) placePill(); }); };
  document.addEventListener('focusin', e => { if (writable(e.target)) field = e.target; soon(); }, true);
  document.addEventListener('focusout', () => setTimeout(soon, 0), true);
  document.addEventListener('input', soon, true);
  addEventListener('scroll', soon, true);
  addEventListener('resize', soon);
  if (window.visualViewport) { visualViewport.addEventListener('resize', soon); visualViewport.addEventListener('scroll', soon); }

  function setMic(state) {
    mic.classList.toggle('rec', state === 'rec');
    mic.classList.toggle('busy', state === 'busy');
    mic.innerHTML = state === 'rec' ? ICON.stop : state === 'busy' ? ICON.wait : ICON.mic;
    const label = state === 'rec' ? 'Stop: write down what you said' : state === 'busy' ? 'Writing it down…' : 'Speak: what you say is written here';
    mic.setAttribute('aria-label', label); mic.title = label;
    soon();
  }
  mic.addEventListener('click', e => {
    e.preventDefault(); e.stopPropagation();
    if (rec) { rec.stop(); return; } // recording, or opening the microphone
    if (busy) return; // writing it down
    if (field && field.isConnected) listen(field);
  });
  addEventListener('keydown', e => { if (e.key === 'Escape' && rec) { e.preventDefault(); e.stopPropagation(); rec.stop(); } }, true); // only the recording stops

  function listen(f) {
    if (elevenListens()) record(f); else if (SR) recognize(f);
  }
  async function record(f) {
    const at = {field: f, href: location.href}; // where what is said goes
    const session = {field: f, stop() { this.cancelled = true; rec = null; busy = false; setMic(''); }};
    rec = session; busy = true; setMic('busy'); // a second tap while the microphone opens stops it
    let stream;
    try { stream = await navigator.mediaDevices.getUserMedia({audio: true}); } catch (e) {
      if (rec === session) { rec = null; busy = false; setMic(''); }
      note('No microphone: ' + (e.name === 'NotAllowedError' ? 'the browser was not allowed to use it' : e.message || e.name));
      return;
    }
    if (session.cancelled || rec !== session) { stream.getTracks().forEach(t => t.stop()); return; }
    busy = false;
    const type = ['audio/webm;codecs=opus', 'audio/webm', 'audio/mp4', 'audio/ogg;codecs=opus'].find(t => MediaRecorder.isTypeSupported && MediaRecorder.isTypeSupported(t)) || '';
    let mr;
    try { mr = new MediaRecorder(stream, type ? {mimeType: type} : undefined); } catch (e) {
      stream.getTracks().forEach(t => t.stop());
      rec = null; setMic('');
      note('This browser cannot record: ' + e.message);
      return;
    }
    const chunks = [];
    mr.ondataavailable = e => { if (e.data && e.data.size) chunks.push(e.data); };
    mr.onstop = async () => {
      stream.getTracks().forEach(t => t.stop());
      clearTimeout(rec && rec.limit);
      rec = null; busy = true; setMic('busy');
      const blob = new Blob(chunks, {type: mr.mimeType || type || 'audio/webm'});
      const ext = /mp4|aac|m4a/.test(blob.type) ? 'm4a' : /ogg/.test(blob.type) ? 'ogg' : 'webm';
      const body = new FormData();
      body.append('audio', blob, 'speech.' + ext);
      try {
        const r = await fetch('/api/voice/listen', {method: 'POST', headers: {'X-Rota-Token': token()}, body});
        const j = await r.json().catch(() => ({}));
        if (!r.ok) throw new Error(r.status === 403 ? 'rota restarted: reload the page' : j.error || 'ElevenLabs did not write it down');
        if (j.text) put(at, j.text); else note('Nothing was heard');
      } catch (e) {
        await look();
        note(e.message + (!elevenListens() && SR ? '. Tap the microphone again: the browser listens now.' : ''));
      }
      busy = false; setMic('');
    };
    mr.start(1000);
    rec = {field: f, stop: () => { if (mr.state !== 'inactive') mr.stop(); }, limit: setTimeout(() => { if (mr.state !== 'inactive') mr.stop(); }, 5 * 60e3)};
    setMic('rec');
  }
  function recognize(f) {
    const at = {field: f, href: location.href};
    const r = new SR();
    r.continuous = true; r.interimResults = false;
    r.lang = navigator.language || 'en-US';
    let limit = 0;
    r.onresult = e => {
      for (let i = e.resultIndex; i < e.results.length; i++) if (e.results[i].isFinal) put(at, e.results[i][0].transcript.trim());
    };
    r.onerror = e => { if (e.error !== 'no-speech' && e.error !== 'aborted') note('Dictation stopped: ' + (e.error === 'not-allowed' ? 'the browser was not allowed to use the microphone' : e.error)); };
    r.onend = () => { clearTimeout(limit); if (rec && rec.r === r) { rec = null; setMic(''); } };
    try { r.start(); } catch (e) { note('Dictation: ' + e.message); return; }
    rec = {field: f, r, stop: () => r.stop()};
    limit = setTimeout(() => { try { r.abort(); } catch (e) { /* ended */ } if (rec && rec.r === r) { rec = null; setMic(''); } }, 5 * 60e3); // a browser that stops telling
    setMic('rec');
  }
  // put writes what was said into the field it was said for, where its cursor is, as if typed:
  // the page saves a draft, grows the box and so on, as it does for what is typed. A field the page
  // drew again is the same one only while the page shows the same place: another thread's reply
  // box has the same id.
  function put(at, text) {
    if (!text) return;
    let f = at.field;
    if (!f.isConnected && f.id && location.href === at.href) f = document.getElementById(f.id) || f;
    if (!f.isConnected || f.disabled || f.readOnly) {
      if (navigator.clipboard) navigator.clipboard.writeText(text).catch(() => {});
      note('The field is gone; what you said is copied: ' + text);
      return;
    }
    const v = f.value;
    let a = v.length, b = v.length;
    try { if (f.selectionStart != null) { a = f.selectionStart; b = f.selectionEnd; } } catch (e) { /* an email field has no cursor */ }
    const before = v.slice(0, a), words = (before && !/\s$/.test(before) ? ' ' : '') + text;
    f.focus({preventScroll: true});
    let done = false;
    try { f.setSelectionRange(a, b); done = document.execCommand('insertText', false, words); } catch (e) { done = false; }
    if (!done || f.value === v) {
      f.value = v.slice(0, a) + words + v.slice(b);
      try { f.setSelectionRange(a + words.length, a + words.length); } catch (e) { /* no cursor */ }
      f.dispatchEvent(new Event('input', {bubbles: true}));
    }
    soon();
  }

  window.rotaVoice = {
    info: V,
    refresh: look,
    say,
    stop,
    reading: () => !!reading,
    readsWith: () => elevenSays() ? 'ElevenLabs' : canSpeak ? 'browser' : '',
    listensWith: () => elevenListens() ? 'ElevenLabs' : SR ? 'browser' : '',
  };
  look();
})();

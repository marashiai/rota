// voice.js: reading aloud and dictation on rota's pages: the app, the review pages, the reports.
// ElevenLabs does both through rota when its key may (rota keeps the key and asks it); else the
// browser's own voice and dictation do. Every field you write in gets a microphone while you
// write in it; anything you select gets a "Read aloud" button; the app's messages have a
// speaker of their own (rotaVoice.say).
(function () {
  'use strict';
  if (window.rotaVoice) return;
  const me = document.currentScript;
  let tok = (me && me.dataset.token) || '';
  const token = () => tok || (document.body && document.body.dataset.token) || '';
  // ask posts to rota with the pages' token. rota makes a new one each time it restarts, as it does
  // when a new one is installed, and a page open since then fetches it and asks again.
  async function ask(path, body, signal) {
    const go = () => fetch(path, {method: 'POST', headers: {'X-Rota-Token': token()}, body, signal});
    let r = await go();
    if (r.status === 403) {
      const m = /data-token="([0-9a-f]+)"/.exec(await (await fetch('/', {cache: 'no-store'})).text());
      if (m) { tok = m[1]; r = await go(); }
    }
    return r;
  }
  // fell says why the browser's own voice or dictation was used where ElevenLabs could be, on the
  // page and in rota's log: a phone shows no console.
  function fell(why) {
    note(why);
    ask('/api/voice/fell', new URLSearchParams({why})).catch(() => {});
  }
  const SR = window.SpeechRecognition || window.webkitSpeechRecognition;
  const canRecord = !!(navigator.mediaDevices && navigator.mediaDevices.getUserMedia && window.MediaRecorder);
  const canSpeak = 'speechSynthesis' in window;
  const MOST = 20000; // the most one reading reads: a whole page selected is not read to the end
  const V = {known: false, key: false, say: false, listen: false, sayWhy: '', listenWhy: '', max: 5000, voice: '', speed: 1};
  // DEV is what this device keeps for itself: the browser's voice it reads with (its voiceURI; ''
  // its default), as each device has its own, and how loud it reads, 0 to 1.
  const DEV = {voice: '', volume: 1};
  try { Object.assign(DEV, JSON.parse(localStorage.getItem('rota.voice') || '{}')); } catch (e) { /* a private window: the defaults */ }
  function setDevice(patch) {
    Object.assign(DEV, patch);
    DEV.volume = Math.min(1, Math.max(0, Number(DEV.volume) || 0));
    try { localStorage.setItem('rota.voice', JSON.stringify(DEV)); } catch (e) { /* kept until the page closes */ }
    changed();
  }
  // browserVoices are this device's own voices, its default first; they come a moment after the
  // page opens.
  function browserVoices() {
    if (!canSpeakNow()) return [];
    return speechSynthesis.getVoices().map(v => ({uri: v.voiceURI, name: v.name, lang: v.lang, isDefault: v.default}));
  }
  function canSpeakNow() { return 'speechSynthesis' in window; }
  if (canSpeakNow()) try { speechSynthesis.addEventListener('voiceschanged', () => changed()); } catch (e) { /* an older browser: the list as it is */ }
  // voiceList is the ElevenLabs voices to choose from, as rota lists them, asked once.
  let voicesAsked = null;
  function voiceList(again) {
    if (!voicesAsked || again) voicesAsked = fetch('/api/voice/voices', {cache: 'no-store'}).then(r => r.ok ? r.json() : {voices: []}).catch(() => ({voices: []}));
    return voicesAsked;
  }

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
.vpill,.vstop{left:50%;transform:translateX(-50%);bottom:calc(env(safe-area-inset-bottom,0px) + 92px)} /* one place, away from the menu a phone or tablet shows by the selection */
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

  // audioMode tells an iPhone or iPad what the page does with sound (Safari's Audio Session):
  // playback plays with the ringer off, but takes the microphone away, so dictation asks for
  // play-and-record, and each gives it back to auto when it is done.
  function audioMode(type) {
    try { if (navigator.audioSession && navigator.audioSession.type !== type) navigator.audioSession.type = type; } catch (e) { /* not this browser's */ }
  }

  // ---- reading aloud ----
  // ElevenLabs' audio plays through Web Audio, opened in the tap: a phone keeps a context it let
  // play in a tap playing, where it may refuse a player that starts once the audio has come.
  let actx = null;
  function opened() {
    const AC = window.AudioContext || window.webkitAudioContext;
    if (!AC) return null;
    try {
      if (!rec) audioMode('playback'); // an iPhone or iPad plays it with its ringer off
      if (!actx) actx = new AC();
      if (actx.state !== 'running') actx.resume().catch(() => {});
      const s = actx.createBufferSource(); // a moment of silence in the tap: an older iPhone opens it only for a sound
      s.buffer = actx.createBuffer(1, 1, 22050);
      s.connect(actx.destination);
      s.start(0);
    } catch (e) { return null; }
    return actx;
  }

  // parts cuts text where a reader pauses, each part at most as long as limits says, the last limit
  // for every part after: a short first part starts soon, and each next one is made while the one
  // before plays.
  function parts(text, limits) {
    const out = [];
    let left = text, lim = limits[0];
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
      lim = limits[Math.min(out.length, limits.length - 1)];
    }
    return out.filter(Boolean);
  }
  function clean(text) {
    return String(text || '').replace(/\r/g, '').replace(/[ \t ]+/g, ' ').replace(/ *\n[\n ]*/g, '\n').trim();
  }

  let reading = null; // {stopped, btn, cancel}
  // say reads text aloud: with ElevenLabs when it may, else with this device's voice; with
  // browser true, with this device's voice whatever ElevenLabs may (its sample in Settings).
  function say(text, btn, browser) {
    if (reading && btn && reading.btn === btn) { stop(); return; } // its button again: stop
    stop();
    text = clean(text);
    if (!text) return;
    if (text.length > MOST) { text = text.slice(0, MOST); note('Reads the first ' + MOST.toLocaleString() + ' characters'); }
    const run = {stopped: false, btn, cancel: null, ctl: window.AbortController ? new AbortController() : null};
    reading = run;
    if (btn) btn.classList.add('on');
    pill.classList.remove('show');
    stopper.classList.add('show');
    if (!browser && elevenSays() && opened()) readEleven(run, parts(text, sizes()));
    else {
      if (browser) { /* asked for */ } else if (V.key && (V.say || !V.known)) fell(V.known ? 'This browser cannot play the audio ElevenLabs reads: the browser reads it' : 'rota had not said yet whether ElevenLabs reads: the browser reads it');
      else if (V.key && V.sayWhy) note(V.sayWhy + ': the browser reads it');
      readBrowser(run, text);
    }
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
    if (!rec && !busy) audioMode('auto');
    if (run.btn) run.btn.classList.remove('on');
    stopper.classList.remove('show');
    placePill();
  }
  function fetchPart(text, run) {
    const p = ask('/api/voice/say', new URLSearchParams({text}), run.ctl ? run.ctl.signal : undefined).then(async r => {
      if (r.ok) return r.arrayBuffer();
      const j = await r.json().catch(() => ({}));
      throw new Error(r.status === 403 ? 'rota refused the page: reload it' : j.error || 'ElevenLabs did not read it');
    });
    p.catch(() => {}); // a part fetched ahead and not played, as the reading stopped
    return p;
  }
  async function readEleven(run, ps) {
    // the parts asked for ahead, two at a time: read fast, a part plays quicker than ElevenLabs
    // makes the next one
    const ahead = [];
    let asked = 0;
    const more = () => { while (ahead.length < 2 && asked < ps.length) ahead.push(fetchPart(ps[asked++], run)); };
    more();
    for (let i = 0; i < ps.length; i++) {
      let bytes;
      try { bytes = await ahead.shift(); } catch (e) {
        if (run.stopped) return;
        fell(e.message + (canSpeak ? ': the browser reads it' : ''));
        look();
        readBrowser(run, ps.slice(i).join('\n'));
        return;
      }
      if (run.stopped) return;
      more();
      const why = await play(bytes, run);
      if (run.stopped) return;
      if (why) { fell(why + (canSpeak ? ': the browser reads it' : '')); if (canSpeak) readBrowser(run, ps.slice(i).join('\n')); else done(run); return; }
    }
    done(run);
  }
  // speed is how fast the voice reads, 1 to 3 (voice_speed).
  const speed = () => Math.min(3, Math.max(1, Number(V.speed) || 1));
  // sizes are how long the parts of an ElevenLabs reading are: the first short, so it starts soon,
  // the next ones made while those before play. ElevenLabs makes speech about 2.8 times faster
  // than it is spoken at 1x; read faster, the parts grow less.
  function sizes() {
    const grow = Math.max(1.25, 2.5 / speed()), out = [160];
    while (out.length < 12 && out[out.length - 1] < 1200) out.push(Math.min(1200, Math.round(out[out.length - 1] * grow)));
    return out.map(x => Math.min(x, V.max || 1200));
  }
  // stretched is the audio at the speed chosen, its pitch kept: SoundTouch's time stretch
  // (soundtouch.js, LGPL), loaded the first time it is needed.
  let soundTouch = null;
  async function stretched(buffer) {
    const sp = speed();
    if (Math.abs(sp - 1) < 0.01) return buffer;
    const ST = await (soundTouch = soundTouch || import('/soundtouch.js'));
    const st = new ST.SoundTouch();
    st.tempo = sp;
    const chans = [...Array(Math.min(2, buffer.numberOfChannels)).keys()].map(c => buffer.getChannelData(c));
    const L = chans[0], R = chans[1] || L, rate = buffer.sampleRate;
    // silence after it: SoundTouch keeps back the last 16,384 frames it was given
    const total = L.length + 16384 + Math.round(rate * 0.25);
    const source = {extract(t, n, pos) {
      let i = 0;
      for (; i < n && pos + i < total; i++) { const k = pos + i; t[2 * i] = k < L.length ? L[k] : 0; t[2 * i + 1] = k < R.length ? R[k] : 0; }
      return i;
    }};
    const filter = new ST.SimpleFilter(source, st), chunk = new Float32Array(8192 * 2), got = [];
    let n, frames = 0;
    while ((n = filter.extract(chunk, 8192)) > 0) { got.push(chunk.slice(0, n * 2)); frames += n; }
    const want = Math.min(frames, Math.ceil(L.length / sp) + Math.round(rate * 0.05)); // without the silence
    const out = actx.createBuffer(chans.length, want, rate), left = out.getChannelData(0), right = chans.length > 1 ? out.getChannelData(1) : null;
    let at = 0;
    for (const c of got) {
      for (let i = 0; i < c.length / 2 && at < want; i++, at++) { left[at] = c[2 * i]; if (right) right[at] = c[2 * i + 1]; }
    }
    return out;
  }
  // play plays one part, and says why not when it cannot ('' when it played).
  async function play(bytes, run) {
    let buffer;
    try { buffer = await new Promise((res, rej) => actx.decodeAudioData(bytes, res, rej)); } catch (e) {
      return 'This browser could not open the audio ElevenLabs read';
    }
    if (run.stopped) return '';
    try { buffer = await stretched(buffer); } catch (e) { /* at its own speed */ }
    if (run.stopped) return '';
    if (actx.state !== 'running') try { await actx.resume(); } catch (e) { /* said below */ }
    if (actx.state !== 'running') return 'This browser did not let the page play sound (' + actx.state + ')';
    return new Promise(res => {
      const src = actx.createBufferSource();
      src.buffer = buffer;
      const loud = actx.createGain(); // this device's volume
      loud.gain.value = DEV.volume;
      src.connect(loud);
      loud.connect(actx.destination);
      src.onended = () => res('');
      run.cancel = () => { try { src.stop(); } catch (e) { /* ended */ } res(''); };
      src.start();
    });
  }
  function readBrowser(run, text) {
    if (!canSpeak) { note('This browser cannot read aloud'); done(run); return; }
    const ps = parts(text, [200]); // a browser stops a long utterance midway
    let i = 0, started = false;
    const next = () => {
      if (run.stopped || reading !== run) return;
      if (i >= ps.length) { done(run); return; }
      const u = new SpeechSynthesisUtterance(ps[i++]);
      const mine = DEV.voice && speechSynthesis.getVoices().find(v => v.voiceURI === DEV.voice);
      if (mine) { u.voice = mine; u.lang = mine.lang; }
      u.rate = speed();
      u.volume = DEV.volume;
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
  function selection() { // the words selected, in a field or on the page
    const a = document.activeElement;
    if (isField(a) && a.selectionEnd > a.selectionStart) return {text: a.value.slice(a.selectionStart, a.selectionEnd)};
    const s = window.getSelection();
    if (!s || s.isCollapsed) return null;
    const text = s.toString();
    return text.trim() ? {text} : null;
  }
  // placePill shows Read aloud while words are selected, always in one place, where Stop reading
  // shows while it reads: by the selection, it would cover the menu an iPhone or iPad shows there,
  // above or below the words as room allows.
  let shown = '', selT = 0;
  function placePill() {
    const sel = selection();
    if (!sel) { pill.classList.remove('show'); shown = ''; return; }
    shown = sel.text;
    pill.classList.toggle('show', !reading);
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
  const writable = x => isField(x) && !x.disabled && !x.readOnly && x.type !== 'password' && !x.hasAttribute('data-nomic'); // data-nomic: a number, say
  // drawn is the field the page shows for f: f, or the one it drew in its place, which has its id,
  // while the page shows the same place; another thread's reply box has the same id.
  const drawn = (f, href) => f && !f.isConnected && f.id && location.href === href ? document.getElementById(f.id) || f : f;
  // placeMic puts the microphone in the field written in. While it records or writes down, it
  // stays on the screen whatever the page draws, so a tap always stops it.
  function placeMic() {
    const f = rec ? (rec.field = drawn(rec.field, rec.href)) : field;
    const on = !!f && f.isConnected;
    const show = canListen() && (rec || busy || on && document.activeElement === f);
    document.querySelectorAll('.vmic-pad').forEach(x => { if (x !== f || !show) x.classList.remove('vmic-pad'); });
    if (!show) { mic.classList.remove('show'); return; }
    let r = on ? f.getBoundingClientRect() : null;
    if (!r || !r.width || !r.height || r.bottom < 0 || r.top > innerHeight) {
      if (!rec && !busy) { mic.classList.remove('show'); return; }
      r = null; // the field is gone or out of view: the microphone waits at the corner
    }
    mic.classList.add('show');
    if (!r) { mic.style.left = (innerWidth - 44) + 'px'; mic.style.top = (innerHeight - 120) + 'px'; return; }
    f.classList.add('vmic-pad');
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
  setInterval(() => { if (rec || busy) soon(); }, 400); // while it records, the page may draw the field again

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
    const session = {field: f, href: at.href, stop() { this.cancelled = true; rec = null; busy = false; setMic(''); }};
    rec = session; busy = true; setMic('busy'); // a second tap while the microphone opens stops it
    if (reading) stop(); // it would hear itself
    audioMode('play-and-record');
    let stream;
    try { stream = await navigator.mediaDevices.getUserMedia({audio: true}); } catch (e) {
      if (rec === session) { rec = null; busy = false; setMic(''); }
      audioMode('auto');
      note('No microphone: ' + (e.name === 'NotAllowedError' ? 'the browser was not allowed to use it' : e.message || e.name));
      return;
    }
    if (session.cancelled || rec !== session) { stream.getTracks().forEach(t => t.stop()); audioMode('auto'); return; }
    busy = false;
    const type = ['audio/webm;codecs=opus', 'audio/webm', 'audio/mp4', 'audio/ogg;codecs=opus'].find(t => MediaRecorder.isTypeSupported && MediaRecorder.isTypeSupported(t)) || '';
    let mr;
    try { mr = new MediaRecorder(stream, type ? {mimeType: type} : undefined); } catch (e) {
      stream.getTracks().forEach(t => t.stop());
      audioMode('auto');
      rec = null; setMic('');
      note('This browser cannot record: ' + e.message);
      return;
    }
    const chunks = [];
    mr.ondataavailable = e => { if (e.data && e.data.size) chunks.push(e.data); };
    mr.onstop = async () => {
      stream.getTracks().forEach(t => t.stop());
      audioMode('auto');
      clearTimeout(rec && rec.limit);
      rec = null; busy = true; setMic('busy');
      const blob = new Blob(chunks, {type: mr.mimeType || type || 'audio/webm'});
      const ext = /mp4|aac|m4a/.test(blob.type) ? 'm4a' : /ogg/.test(blob.type) ? 'ogg' : 'webm';
      const body = new FormData();
      body.append('audio', blob, 'speech.' + ext);
      try {
        const r = await ask('/api/voice/listen', body);
        const j = await r.json().catch(() => ({}));
        if (!r.ok) throw new Error(r.status === 403 ? 'rota restarted: reload the page' : j.error || 'ElevenLabs did not write it down');
        if (j.text) put(at, j.text); else note('Nothing was heard');
      } catch (e) {
        await look();
        fell(e.message + (!elevenListens() && SR ? '. Tap the microphone again: the browser listens now.' : ''));
      }
      busy = false; setMic('');
    };
    mr.start(1000);
    rec = {field: f, href: at.href, stop: () => { if (mr.state !== 'inactive') mr.stop(); }, limit: setTimeout(() => { if (mr.state !== 'inactive') mr.stop(); }, 5 * 60e3)};
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
    r.onend = () => { clearTimeout(limit); audioMode('auto'); if (rec && rec.r === r) { rec = null; setMic(''); } };
    if (reading) stop();
    audioMode('play-and-record');
    try { r.start(); } catch (e) { audioMode('auto'); note('Dictation: ' + e.message); return; }
    rec = {field: f, href: at.href, r, stop: () => r.stop()};
    limit = setTimeout(() => { try { r.abort(); } catch (e) { /* ended */ } if (rec && rec.r === r) { rec = null; setMic(''); } }, 5 * 60e3); // a browser that stops telling
    setMic('rec');
  }
  // put writes what was said into the field it was said for, where its cursor is, as if typed:
  // the page saves a draft, grows the box and so on, as it does for what is typed. A field the page
  // drew again is the same one only while the page shows the same place: another thread's reply
  // box has the same id.
  function put(at, text) {
    if (!text) return;
    const f = drawn(at.field, at.href);
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
    device: DEV,
    setDevice,
    browserVoices,
    voiceList,
  };
  look();
})();

// gloss: a pull request's whole change, and each commit, with notes beside the code. No libraries.
(function () {
  'use strict';
  const $ = (s, el = document) => el.querySelector(s);
  const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  // a path breaks only between its folders, so a narrow screen shows src/\nproject-view.js, not src/project-\nview.js
  const pathHTML = p => p.split(/(?<=\/)/).map(seg => `<span class="seg">${esc(seg)}</span>`).join('');
  // code marks a line's tokens as the server lexed them (syntax.Highlight), each in a span of its
  // kind, with the plain text between as it is; the spans count code points
  const code = (text, spans) => {
    if (!spans) return esc(text);
    const cps = Array.from(text);
    let h = '', at = 0;
    for (const s of spans) {
      h += esc(cps.slice(at, s.s).join('')) + `<span class="hl-${s.k}">${esc(cps.slice(s.s, s.s + s.n).join(''))}</span>`;
      at = s.s + s.n;
    }
    return h + esc(cps.slice(at).join(''));
  };
  const main = $('#main'), side = $('#side'), tree = $('#tree'), filter = $('#filter'), statusEl = $('#status'), sendBtn = $('#send'), help = $('#help');
  const tools = $('#tools'), actBtn = $('#act'), whereBtn = $('#where'), menuBtn = $('#menubtn'), scrim = $('#scrim'), pick = $('#pick');
  // the phone layout: a narrow window, or a touch screen held sideways (must match app.css)
  const phone = matchMedia('(max-width: 800px), (pointer: coarse) and (max-height: 500px)');
  const store = {
    get: k => { try { return localStorage.getItem('gloss.' + k); } catch (_) { return null; } },
    set: (k, v) => { try { localStorage.setItem('gloss.' + k, v); } catch (_) { /* private mode: not remembered */ } },
  };

  // rv: the review from the server; view: 0 is the whole change, n is commit n; files: each diff
  // read, by key; items: what the cursor moves over, in page order; editor: the open note editor,
  // if any; fold: the folders folded in the files; side: the files shown, on a wide screen.
  // read: what the reviewer had read when the page opened, and what they folded since: what shows
  // folded; saved: what the server keeps; seen: what was on the screen since, for it to keep;
  // unfold: threads opened again; showRead: views whose read notes show one by one.
  const S = { rv: null, view: 0, files: {}, loading: {}, items: [], cur: 0, editor: null, key: '', chain: Promise.resolve(), modal: null,
    open: {}, expanded: {}, fold: {}, statusT: 0, busy: false, gen: 0, picking: false, pickCur: 0, side: store.get('side') !== '0',
    read: noneRead(), saved: noneRead(), seen: noneRead(), unfold: {}, showRead: {} };

  function status(msg, err) {
    statusEl.textContent = msg || '';
    statusEl.classList.toggle('error', !!err);
    clearTimeout(S.statusT);
    if (msg && !err) S.statusT = setTimeout(() => status(''), 5000);
  }

  async function api(path, body) {
    if (body !== undefined) S.gen++; // a change: a refresh that started before it is stale
    const opts = body === undefined ? {} :
      { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Gloss': '1' }, body: JSON.stringify(body) };
    let r;
    try { r = await fetch('api/' + path, opts); } catch (_) { throw Object.assign(new Error('no answer from the server'), { lost: true }); }
    if (body !== undefined) S.gen++; // and once more when it is done: a refresh that started meanwhile read the review before it
    let data;
    try { data = await r.json(); } catch (_) { throw new Error(r.status + ' ' + r.statusText); }
    if (!r.ok || data.error) throw new Error(data.error || r.statusText);
    return data;
  }

  // ---- what the reviewer has read: every note written by all, and a thread's notes written by
  // its time in threads; their own notes and drafts they have. Times are in milliseconds.
  function noneRead() { return { all: 0, threads: {} }; }
  const ms = s => (s && Date.parse(s)) || 0;
  function readOf(r) {
    const o = noneRead();
    if (!r) return o;
    o.all = ms(r.all);
    for (const [k, v] of Object.entries(r.threads || {})) o.threads[k] = ms(v);
    return o;
  }
  // a PR comment from the reviewer's account may be an agent's: not surely theirs
  const has = (r, t, n) => { const upto = Math.max(r.all, r.threads[t.id] || 0); return n.draft || isMine(n) && !n.pr_comment || upto > 0 && ms(n.created) <= upto; };
  const isNew = (t, n) => !has(S.read, t, n);
  const folded = t => !S.unfold[t.id] && t.notes.every(n => !n.draft && has(S.read, t, n));
  const latest = t => Math.max(0, ...t.notes.filter(n => !n.draft).map(n => ms(n.created)));
  function mark(r, t, at) { if (at > (r.threads[t.id] || 0)) r.threads[t.id] = at; }
  function newIn(ts) { let c = 0; for (const t of ts) for (const n of t.notes) if (isNew(t, n)) c++; return c; }
  const threadOf = el => { const th = el && el.closest('.thread'); return th ? threads().find(t => t.id === th.dataset.thread) : null; };

  // keep sends what was on the screen to the server, which keeps it for the next visit
  let keepT = 0;
  function keep(leaving) {
    clearTimeout(keepT);
    const sending = S.seen;
    if (!sending.all && !Object.keys(sending.threads).length) return;
    S.seen = noneRead();
    const body = { threads: {} };
    if (sending.all) body.all = new Date(sending.all).toISOString();
    for (const [k, v] of Object.entries(sending.threads)) body.threads[k] = new Date(v).toISOString();
    // keepalive: a request the page started goes through when the page is left meanwhile
    fetch('api/read', { method: 'POST', keepalive: true, headers: { 'Content-Type': 'application/json', 'X-Gloss': '1' }, body: JSON.stringify(body) })
      .then(r => { if (!r.ok) throw new Error(r.statusText); merge(S.saved, sending); })
      .catch(() => { merge(S.seen, sending); if (!leaving) keepT = setTimeout(keep, 10000); }); // sent again later, with the next ones
  }
  function merge(into, r) { into.all = Math.max(into.all, r.all); for (const [k, v] of Object.entries(r.threads)) mark(into, { id: k }, v); }
  // a note is read once most of it, or half the screen of it, was in view
  const looking = new IntersectionObserver(es => {
    for (const e of es) {
      if (!e.isIntersecting) continue;
      const root = e.rootBounds ? e.rootBounds.height : innerHeight;
      if (e.intersectionRatio < .5 && e.intersectionRect.height < root / 2) continue;
      const t = threadOf(e.target), n = t && t.notes.find(x => x.id === e.target.dataset.id);
      if (!n || has(S.saved, t, n) || has(S.seen, t, n)) continue;
      mark(S.seen, t, ms(n.created));
      clearTimeout(keepT);
      keepT = setTimeout(keep, 1500);
    }
  }, { root: main, threshold: [0, .1, .25, .5, .75, 1] });
  document.addEventListener('visibilitychange', () => { if (document.hidden) keep(true); });
  window.addEventListener('pagehide', () => keep(true));

  // ---- the review
  const commits = () => S.rv.commits || [];
  const threads = () => S.rv.threads || [];
  const threadsOf = sha => threads().filter(t => t.commit === sha);
  const commitNumber = sha => sha ? commits().findIndex(c => c.sha === sha) + 1 : 0; // 0: no such commit
  const isMine = n => !n.named && n.account === S.rv.viewer;
  const you = n => isMine(n) && !n.pr_comment; // a PR comment from the reviewer's account may be an agent's: it shows the account
  function drafts() { let n = 0; for (const t of threads()) for (const x of t.notes) if (x.draft) n++; return n; }
  function noteById(id) { for (const t of threads()) for (const n of t.notes) if (n.id === id) return n; return null; }
  function hasLine(f, line, old) {
    return f.hunks.some(h => h.lines.some(l => old ? l.old === line && l.k !== '+' : l.new === line && l.k !== '-'));
  }
  const short = sha => sha.slice(0, 7);
  const when = t => new Date(t).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' });

  // diffKey names view v's diff: the whole change's, from the base to the head, or a commit's own
  const diffKey = v => v === 0 ? `${S.rv.base}...${S.rv.head}` : commits()[v - 1].sha;
  const filesFor = v => S.files[diffKey(v)];

  // need reads view v's diff when it is not here yet, and draws it when it comes
  function need(v) {
    const key = diffKey(v);
    if (S.files[key] || S.loading[key]) return;
    S.loading[key] = true;
    api('diff/' + key + '?hl') // ?hl: a diff the browser kept from before the spans came would show plain
      .then(r => { S.files[key] = r.files || []; if (S.rv && diffKey(S.view) === key) render(); })
      .catch(e => status(e.message, true))
      .finally(() => { delete S.loading[key]; });
  }

  // ---- where notes show. A view's threads are placed in its diff: on a line, on a file when the
  // diff does not show the line, or at the top when the diff lacks the file or the note is on the
  // whole change or commit. The whole change places every note with a path where GitHub tracks it
  // to in the diff as it is now, whichever commit it was written on.
  const lineKey = (path, line, old) => `${path}\n${old ? '-' : '+'}${line}`;

  function place(v, files) {
    const p = { whole: [], files: {}, lines: {} };
    const put = (t, f, k) => {
      if (!t.path || !f) p.whole.push(t);
      else if (k) (p.lines[k] = p.lines[k] || []).push(t);
      else (p.files[t.path] = p.files[t.path] || []).push(t);
    };
    if (v === 0) {
      for (const t of threads()) {
        if (!t.path && t.commit && commitNumber(t.commit)) continue; // on a whole commit that is still there
        const f = t.path && files.find(f => f.path === t.path);
        put(t, f, f && wholeLine(t, f));
      }
    } else {
      for (const t of threadsOf(commits()[v - 1].sha)) {
        const f = t.path && files.find(f => f.path === t.path);
        put(t, f, f && t.line && hasLine(f, t.line, t.old) ? lineKey(t.path, t.line, t.old) : '');
      }
    }
    return p;
  }

  // wholeLine is the line of f, in the whole change's diff, that a thread shows on: where GitHub
  // tracks it, or its own line when it was written on the diff as it is now
  function wholeLine(t, f) {
    if (t.outdated || !t.line) return '';
    let line, old;
    if (t.head_line) { line = t.head_line; old = !!t.head_old; }
    else if ((!t.commit && t.head === S.rv.head) || (t.commit === S.rv.head && !t.old)) { line = t.line; old = !!t.old; }
    else return '';
    return hasLine(f, line, old) ? lineKey(t.path, line, old) : '';
  }

  // where says what a thread's place does not show by itself: in the whole change, the commit it
  // was written on; when loose, the line or file this diff does not show
  function where(t, loose) {
    const parts = [];
    if (S.view === 0 && t.commit) { const n = commitNumber(t.commit); parts.push(n ? `in commit ${n}` : 'in a commit no longer in the pull request'); }
    if (loose && t.path && !t.outdated) parts.push(t.line ? `line ${t.line}${t.old ? ' (old side)' : ''} of ${t.path}, which this diff does not show` : `on ${t.path}`);
    return parts.join(' · ');
  }

  // ---- markdown: the little that notes and descriptions use. Everything is escaped first, so
  // no HTML from GitHub reaches the page; links open only http and https addresses.
  function inline(s) {
    const codes = [];
    s = s.replace(/`([^`\n]+)`/g, (_, c) => { codes.push(c); return `\u0000${codes.length - 1}\u0000`; });
    s = esc(s);
    s = s.replace(/\[([^\]\n]+)\]\((https?:\/\/[^\s)]+)\)/g, (_, t, u) => `<a href="${u}" target="_blank" rel="noopener">${t}</a>`);
    s = s.replace(/(^|[\s(])(https?:\/\/[^\s<)]+)/g, (_, pre, u) => {
      let tail = '';
      while (/[.,;:!?]$/.test(u) && !/&[#\w]+;$/.test(u)) { tail = u.slice(-1) + tail; u = u.slice(0, -1); }
      return `${pre}<a href="${u}" target="_blank" rel="noopener">${u}</a>${tail}`;
    });
    s = s.replace(/\*\*([^*\n]+)\*\*/g, '<b>$1</b>').replace(/(^|[^*\w])\*([^*\n]+)\*(?=[^*\w]|$)/g, '$1<i>$2</i>');
    s = s.replace(/\n/g, '<br>');
    return s.replace(/\u0000(\d+)\u0000/g, (_, n) => `<code>${esc(codes[+n])}</code>`);
  }

  function md(src) {
    const out = [], lines = String(src ?? '').replace(/\r\n/g, '\n').split('\n');
    let para = [], list = null;
    const flush = () => {
      if (para.length) { out.push(`<p>${inline(para.join('\n'))}</p>`); para = []; }
      if (list) { out.push(`<${list.tag}>${list.items.map(x => `<li>${inline(x)}</li>`).join('')}</${list.tag}>`); list = null; }
    };
    for (let i = 0; i < lines.length; i++) {
      const l = lines[i];
      let m;
      if (/^\s*```/.test(l)) {
        flush();
        const code = [];
        while (++i < lines.length && !/^\s*```/.test(lines[i])) code.push(lines[i]);
        out.push(`<pre><code>${esc(code.join('\n'))}</code></pre>`);
      } else if (!l.trim()) flush();
      else if ((m = l.match(/^#{1,6}\s+(.*)/))) { flush(); out.push(`<p class="h">${inline(m[1])}</p>`); }
      else if ((m = l.match(/^\s*([-*+]|\d+[.)])\s+(.*)/))) {
        const tag = /\d/.test(m[1]) ? 'ol' : 'ul';
        if (para.length || (list && list.tag !== tag)) flush();
        if (!list) list = { tag, items: [] };
        list.items.push(m[2]);
      } else if (list && /^\s{2,}\S/.test(l)) list.items[list.items.length - 1] += '\n' + l.trim();
      else { if (list) flush(); para.push(l); }
    }
    flush();
    return out.join('');
  }

  // ---- drawing
  // desc is a pull request's description or a commit's message: open on a wide screen or when
  // short, and as the reader left it.
  function desc(text, label) {
    const open = S.open[S.view] ?? (!phone.matches || text.length < 280);
    return `<details class="desc" data-view="${S.view}"${open ? ' open' : ''}><summary>${label}</summary><div class="md">${md(text)}</div></details>`;
  }

  function noteHTML(n, t) {
    const who = n.named ? `<b>${esc(n.author)}</b> <span class="badge agent">agent</span>` : `<b>${you(n) ? 'you' : esc(n.author)}</b>`;
    const acts = n.draft
      ? '<a data-act="edit">edit</a><a data-act="delete">delete</a>'
      : '<a data-act="reply">reply</a>' + (t.notes.some(x => x.draft) ? '' : '<a data-act="fold" title="Fold it: you have read it (z)">fold</a>') +
        (n.url ? `<a href="${esc(n.url)}" target="_blank" rel="noopener">GitHub</a>` : '');
    const fold = !S.expanded[n.id] && (n.text.length > 700 || n.text.split('\n').length > 14); // long notes fold
    return `<div class="note item${n.draft ? ' draft' : ''}" data-kind="note" data-id="${esc(n.id)}">` +
      `<div class="by">${who}${n.draft ? ' <span class="badge draft">draft</span>' : ''}${isNew(t, n) ? ' <span class="badge new">new</span>' : ''}<span>${esc(when(n.created))}</span>` +
      `<span class="acts">${acts}</span></div><div class="text md${fold ? ' fold' : ''}">${md(n.text)}</div>` +
      (fold ? '<button class="more" data-act="more">Show more</button>' : '') + '</div>';
  }

  function threadHTML(t, where) {
    if (folded(t)) return foldHTML(t);
    const label = [where, t.outdated && 'changed by a later commit', t.resolved && 'resolved'].filter(Boolean).join(' · ');
    return `<div class="thread" data-thread="${esc(t.id)}">${label ? `<div class="where">${esc(label)}</div>` : ''}${t.notes.map(n => noteHTML(n, t)).join('')}</div>`;
  }

  // foldHTML is a thread the reviewer has read, on one line: who wrote it, when, and how it starts
  function foldHTML(t) {
    const n = t.notes[0], r = t.notes.length - 1;
    return `<div class="thread folded item" data-kind="fold" data-id="${esc(n.id)}" data-thread="${esc(t.id)}" title="Open it (z)">` +
      `<span class="by">▸ <b>${you(n) ? 'you' : esc(n.author)}</b> · ${esc(when(n.created))}</span><span class="gist">${esc(gist(n.text))}</span>` +
      (r ? `<span class="by">${r} repl${r === 1 ? 'y' : 'ies'}</span>` : '') + '</div>';
  }

  // gist is how a note starts: its first line with text, without Markdown's marks
  function gist(text) {
    for (let l of text.split('\n')) {
      l = l.replace(/\*\*|__|`/g, '').trim().replace(/^[#>\-* ]+/, '').trim();
      if (l) return l.replace(/\s+/g, ' ');
    }
    return '';
  }

  // the view: the whole change or a commit, laid out the same way: its title and description, the
  // notes on it, then its diff with the notes in place
  function renderView() {
    const v = S.view, rv = S.rv, files = filesFor(v);
    let h;
    if (v === 0) {
      h = `<div class="head item" data-kind="change"><h1>${esc(rv.title)}</h1>` +
        `<div class="meta">${esc(rv.pr)} · by ${esc(rv.author)} · ${esc(rv.state.toLowerCase())} · ${commits().length} commit${commits().length === 1 ? '' : 's'}` +
        (approved() ? ` · <span class="approved">approved by ${esc(rv.approval.by)}</span>` : '') + '</div>' +
        (rv.body ? desc(rv.body, 'Description') : '') + '</div>';
    } else {
      const c = commits()[v - 1];
      h = `<div class="head item" data-kind="commit"><h1>${esc(c.title)}</h1>` +
        `<div class="meta"><span>commit ${v} of ${commits().length} · ${esc(short(c.sha))} · ${esc(c.author)} · ${esc(when(c.date))}</span></div>` +
        (c.message ? desc(c.message, 'Message') : '') + '</div>';
    }
    if (!files) {
      need(v);
      return h + section(topThreads(v), false) + '<p class="loading">Loading the diff…</p>';
    }
    const p = place(v, files);
    h += section(p.whole, true) + renderFiles(files, p);
    if (!files.length) h += `<p class="empty">This ${v === 0 ? 'change' : 'commit'} changes no files.</p>`;
    if (v === 0 && rv.truncated) h += '<p class="empty">GitHub gave only the first 100 commits, threads or comments: some are missing.</p>';
    return h;
  }

  // topThreads are the view's notes that need no diff to place: on the whole change (and on
  // commits no longer in the pull request), or on the whole commit
  function topThreads(v) {
    if (v === 0) return threads().filter(t => !t.path && (!t.commit || !commitNumber(t.commit)));
    return threadsOf(commits()[v - 1].sha).filter(t => !t.path);
  }

  // section is the view's own notes; the read ones fold into one line
  function section(ts, loose) {
    const whole = S.view === 0, unread = newIn(threads());
    let h = `<div class="section">${whole ? 'Notes on the whole change' : 'Notes on this commit'}<span class="keys"> · <kbd>${whole ? 'C' : 'c'}</kbd> ${whole ? '' : 'on the title '}adds one</span>` +
      (unread ? '<button class="add" data-do="readAll" title="Fold every note: you have read them all (Z)">✓ All read</button>' : '') +
      '<button class="add" data-do="top">+ Note</button></div><div class="threads" data-anchor="top">';
    const open = ts.filter(t => !folded(t));
    if (ts.length - open.length >= 2 && !S.showRead[S.view]) {
      const n = ts.filter(folded).reduce((a, t) => a + t.notes.length, 0);
      h += `<div class="folds item" data-kind="folds" data-id="v${S.view}" title="Show them (z)">▸ ${n} notes you have read · show them</div>`;
      ts = open;
    }
    for (const t of ts) h += threadHTML(t, where(t, loose));
    return h + '</div>';
  }


  function renderFiles(files, p) {
    let h = '';
    for (const f of files) {
      let add = 0, del = 0;
      for (const hk of f.hunks) for (const l of hk.lines) { if (l.k === '+') add++; if (l.k === '-') del++; }
      const name = f.old_path ? `${pathHTML(f.old_path)} → ${pathHTML(f.path)}` : pathHTML(f.path);
      // --nd: the digits of the file's longest line number, so the number columns are only as wide as they must be
      const last = Math.max(0, ...f.hunks.map(hk => Math.max(hk.old_start + hk.old_count, hk.new_start + hk.new_count)));
      h += `<div class="file" data-path="${esc(f.path)}" style="--nd:${Math.max(2, String(last).length)}"><div class="fhead item" data-kind="file" data-path="${esc(f.path)}"><span class="path">${name}</span>` +
        `<span class="stat"><span class="st ${esc(f.status)}">${esc(f.status)} </span>+${add} −${del}</span></div>`;
      h += `<div class="threads" data-anchor="file">${(p.files[f.path] || []).map(t => threadHTML(t, where(t, true))).join('')}</div><div class="diff">`;
      if (f.binary) h += '<div class="row hunk"><span></span><span></span><span></span><span class="tx">binary file</span></div>';
      f.hunks.forEach((hk, hi) => {
        h += `<div class="row hunk item" data-kind="hunk" data-path="${esc(f.path)}" data-hunk="${hi + 1}"><span></span><span></span><span></span>` +
          `<span class="tx">@@ -${hk.old_start},${hk.old_count} +${hk.new_start},${hk.new_count} @@ ${esc(hk.section || '')}</span></div>`;
        for (const l of hk.lines) {
          const old = l.k === '-', ln = old ? l.old : l.new;
          const cls = l.k === '+' ? ' add' : old ? ' del' : '';
          h += `<div class="row item${cls}" data-kind="line" data-path="${esc(f.path)}" data-line="${ln}" data-old="${old ? 1 : 0}">` +
            `<span class="on">${l.old || ''}</span><span class="nn">${l.new || ''}</span><span>${l.k === ' ' ? '' : l.k}</span><span class="tx">${code(l.text, l.hl)}</span></div>`;
          const keys = l.k === ' ' ? [lineKey(f.path, l.new, false), lineKey(f.path, l.old, true)] : [lineKey(f.path, ln, old)];
          for (const k of keys) for (const t of p.lines[k] || []) h += threadHTML(t, where(t, false));
        }
      });
      h += '</div></div>';
    }
    return h;
  }

  // ---- the files, as GitHub's tree: a filter, folders that fold (a chain of single folders is
  // one row), each file with its mark and the notes in it
  const ICON = {
    dir: '<svg class="ic" viewBox="0 0 16 16" aria-hidden="true"><path d="M1.75 1A1.75 1.75 0 0 0 0 2.75v10.5C0 14.216.784 15 1.75 15h12.5A1.75 1.75 0 0 0 16 13.25v-8.5A1.75 1.75 0 0 0 14.25 3H7.5a.25.25 0 0 1-.2-.1l-.9-1.2C6.07 1.26 5.55 1 5 1H1.75Z"/></svg>',
    file: '<svg class="ic" viewBox="0 0 16 16" aria-hidden="true"><path d="M2 1.75C2 .784 2.784 0 3.75 0h6.586c.464 0 .909.184 1.237.513l2.914 2.914c.329.328.513.773.513 1.237v9.586A1.75 1.75 0 0 1 13.25 16h-9.5A1.75 1.75 0 0 1 2 14.25Zm1.75-.25a.25.25 0 0 0-.25.25v12.5c0 .138.112.25.25.25h9.5a.25.25 0 0 0 .25-.25V6h-2.75A1.75 1.75 0 0 1 9 4.25V1.5Zm6.75.062V4.25c0 .138.112.25.25.25h2.688l-.011-.013-2.914-2.914-.013-.011Z"/></svg>',
    note: '<svg class="ic" viewBox="0 0 16 16" aria-hidden="true"><path d="M1 2.75C1 1.784 1.784 1 2.75 1h10.5c.966 0 1.75.784 1.75 1.75v7.5A1.75 1.75 0 0 1 13.25 12H9.06l-2.573 2.573A1.458 1.458 0 0 1 4 13.543V12H2.75A1.75 1.75 0 0 1 1 10.25Zm1.75-.25a.25.25 0 0 0-.25.25v7.5c0 .138.112.25.25.25h2a.75.75 0 0 1 .75.75v2.19l2.72-2.72a.749.749 0 0 1 .53-.22h4.5a.25.25 0 0 0 .25-.25v-7.5a.25.25 0 0 0-.25-.25Z"/></svg>',
  };
  const MARK = { added: '+', deleted: '−', modified: '●', renamed: '→' };

  function renderSide() {
    const files = S.rv && filesFor(S.view);
    if (!files) { tree.innerHTML = `<p class="empty">${S.rv ? 'Loading…' : ''}</p>`; return; }
    const p = place(S.view, files), count = {};
    for (const path in p.files) count[path] = p.files[path].length;
    for (const k in p.lines) { const path = k.split('\n')[0]; count[path] = (count[path] || 0) + p.lines[k].length; }
    const q = filter.value.trim().toLowerCase();
    const shown = q ? files.filter(f => f.path.toLowerCase().includes(q) || (f.old_path || '').toLowerCase().includes(q)) : files;
    const root = { dirs: new Map(), files: [] };
    for (const f of shown) {
      let d = root;
      for (const s of f.path.split('/').slice(0, -1)) { if (!d.dirs.has(s)) d.dirs.set(s, { dirs: new Map(), files: [] }); d = d.dirs.get(s); }
      d.files.push(f);
    }
    const total = d => { let n = 0; for (const f of d.files) n += count[f.path] || 0; for (const x of d.dirs.values()) n += total(x); return n; };
    let h = '';
    const walk = (d, prefix, depth) => {
      for (const [name, sub] of [...d.dirs].sort((a, b) => a[0].localeCompare(b[0]))) {
        let label = name, node = sub, path = prefix + name;
        while (node.dirs.size === 1 && !node.files.length) { const [[n2, s2]] = node.dirs; label += '/' + n2; path += '/' + n2; node = s2; }
        const open = q ? true : S.fold[path] !== true, n = total(node);
        h += `<div class="dir${open ? ' open' : ''}" data-dir="${esc(path)}" style="--d:${depth}" role="treeitem" aria-expanded="${open}"><span class="caret"></span>${ICON.dir}` +
          `<span class="name">${esc(label)}</span>${!open && n ? `<span class="count">${ICON.note}${n}</span>` : ''}</div>`;
        if (open) walk(node, path + '/', depth + 1);
      }
      for (const f of d.files.sort((a, b) => a.path.localeCompare(b.path))) {
        const n = count[f.path] || 0;
        h += `<a class="f" href="#${S.view}" data-path="${esc(f.path)}" style="--d:${depth}" role="treeitem" title="${esc(f.path)} · ${esc(f.status)}">${ICON.file}` +
          `<span class="name">${esc(f.path.slice(prefix.length))}</span>${n ? `<span class="count">${ICON.note}${n}</span>` : ''}` +
          `<span class="mark ${esc(f.status)}" aria-label="${esc(f.status)}">${MARK[f.status] || '●'}</span></a>`;
      }
    };
    walk(root, '', 0);
    tree.innerHTML = h || `<p class="empty">${files.length ? 'No file matches.' : 'No files.'}</p>`;
    markFile();
  }

  // markFile lights the file the cursor is in
  function curPath() {
    const el = cur();
    if (!el) return '';
    const file = el.closest('.file');
    return el.dataset.path || (file ? file.dataset.path : '');
  }
  function markFile() {
    const p = curPath();
    let on = null;
    for (const a of tree.querySelectorAll('a.f')) { const is = a.dataset.path === p; a.classList.toggle('on', is); if (is) on = a; }
    if (on && (!phone.matches || document.body.classList.contains('menu'))) {
      const r = on.getBoundingClientRect(), t = tree.getBoundingClientRect();
      if (r.top < t.top || r.bottom > t.bottom) on.scrollIntoView({ block: 'nearest' });
    }
  }

  // toFile puts the cursor on the file's header, at the top of the page
  function toFile(path) {
    const i = S.items.findIndex(el => el.dataset.kind === 'file' && el.dataset.path === path);
    if (i >= 0) setCur(i, 'start');
  }

  // the files show on a wide screen when the reader left them showing; on a phone they are a
  // drawer, open only while in use
  function applySide() { document.body.classList.toggle('noside', !phone.matches && !S.side); }
  function toggleSide() {
    if (phone.matches) return menu(!document.body.classList.contains('menu'));
    S.side = !S.side;
    store.set('side', S.side ? '1' : '0');
    applySide();
    menuBtn.setAttribute('aria-expanded', S.side);
  }

  // ---- the picker: the whole change, or a commit, with the notes on each
  function renderPick() {
    const cnt = ts => (ts.length ? `<span class="count">${ts.length} note${ts.length === 1 ? '' : 's'}</span>` : '') +
      (newIn(ts) ? `<span class="count new">${newIn(ts)} new</span>` : '');
    let h = `<a class="pk${S.view === 0 ? ' on' : ''}" href="#0" data-view="0" role="option"><span class="t">The whole change</span>${cnt(threads().filter(t => !t.commit))}</a>`;
    commits().forEach((c, i) => {
      h += `<a class="pk${S.view === i + 1 ? ' on' : ''}" href="#${i + 1}" data-view="${i + 1}" role="option"><span class="n">${i + 1}</span><span class="sha">${esc(short(c.sha))}</span>` +
        `<span class="t">${esc(c.title)}</span>${cnt(threadsOf(c.sha))}</a>`;
    });
    pick.innerHTML = h;
  }
  function showPick(open) {
    if (open && !S.rv) return;
    S.picking = open;
    pick.hidden = !open;
    whereBtn.setAttribute('aria-expanded', open);
    document.body.classList.toggle('picking', open);
    scrim.hidden = !open && !document.body.classList.contains('menu');
    if (!open) { main.focus(); return; }
    renderPick();
    if (!phone.matches) { // under the button
      const r = whereBtn.getBoundingClientRect();
      pick.style.top = r.bottom + 4 + 'px';
      pick.style.left = Math.max(8, Math.min(r.left, innerWidth - pick.offsetWidth - 8)) + 'px';
    } else pick.style.top = pick.style.left = '';
    S.pickCur = S.view;
    markPick();
  }
  function markPick() {
    for (const a of pick.querySelectorAll('.pk')) a.classList.toggle('hl', +a.dataset.view === S.pickCur);
    const c = $('.pk.hl', pick);
    if (c) c.scrollIntoView({ block: 'nearest' });
  }

  const itemKey = el => el ? [el.dataset.kind, el.dataset.id, el.dataset.path, el.dataset.line, el.dataset.old, el.dataset.hunk].join('|') : '';

  function render(keep = true) {
    if (S.view > commits().length) { S.view = 0; keep = false; history.replaceState(null, '', '#0'); } // a force-push took commits away
    const key = keep ? itemKey(S.items[S.cur]) : '';
    main.innerHTML = renderView();
    S.items = [...main.querySelectorAll('.item')];
    looking.disconnect();
    for (const el of main.querySelectorAll('.note:not(.draft)')) looking.observe(el);
    const i = key ? S.items.findIndex(el => itemKey(el) === key) : -1;
    S.cur = Math.min(S.cur, S.items.length - 1);
    renderSide();
    setCur(i >= 0 ? i : keep ? S.cur : 0, keep ? 'none' : 'nearest');
    updateSend();
    updateTools();
    if (S.picking) { S.pickCur = Math.min(S.pickCur, commits().length); renderPick(); markPick(); } // a new head changed the commits
  }

  // primary is what the toolbar's main button does where the cursor is: a note there, a reply to
  // the note, or an edit of your draft.
  function primary() {
    const el = cur(), d = el ? el.dataset : {};
    if (d.kind === 'fold' || d.kind === 'folds') return { label: d.kind === 'fold' ? 'Open' : 'Show', run: fold };
    if (d.kind === 'note') {
      const n = noteById(d.id);
      return n && n.draft ? { label: 'Edit', run: edit } : { label: 'Reply', run: reply };
    }
    return { label: '+ Note', run: el ? compose : composeWhole };
  }

  function updateTools() {
    if (!S.rv) return;
    const n = commits().length;
    $('[data-do=prevCommit]', tools).disabled = S.view <= 0;
    $('[data-do=nextCommit]', tools).disabled = S.view >= n;
    const hunks = S.items.some(isHunk), notes = S.items.some(isNote);
    for (const b of tools.querySelectorAll('[data-do$=Hunk]')) b.disabled = !hunks;
    for (const b of tools.querySelectorAll('[data-do$=Note]')) b.disabled = !notes;
    actBtn.textContent = primary().label;
    $('.lbl', whereBtn).innerHTML = S.view === 0 ? '<span class="w">Whole change</span>'
      : `<span class="w">Commit ${S.view}/${n}</span><span class="ct"> · ${esc(commits()[S.view - 1].title)}</span>`;
  }

  // approved: a person approved the pull request
  const approved = () => !!(S.rv && S.rv.approval);
  const canApprove = () => !!(S.rv && S.rv.state === 'OPEN' && !approved());

  // the Send button sends the drafts; with none, it approves
  function updateSend() {
    const n = drafts();
    sendBtn.classList.remove('ready', 'approve', 'done');
    if (n) {
      sendBtn.textContent = `Send ${n}`; sendBtn.disabled = false; sendBtn.classList.add('ready');
      sendBtn.title = 'Send your drafts as one review (S); A sends them and approves';
    } else if (canApprove()) {
      sendBtn.textContent = 'Approve'; sendBtn.disabled = false; sendBtn.classList.add('approve');
      sendBtn.title = 'Approve: the team may merge it (A)';
    } else {
      sendBtn.textContent = approved() ? '✓ Approved' : 'Send'; sendBtn.disabled = true;
      sendBtn.classList.toggle('done', approved());
      sendBtn.title = approved() ? 'Approved' : 'No drafts to send';
    }
  }

  function setCur(i, how = 'nearest') {
    if (!S.items.length) return;
    i = Math.max(0, Math.min(S.items.length - 1, i));
    if (S.items[S.cur]) S.items[S.cur].classList.remove('cur');
    S.cur = i;
    const el = S.items[i];
    el.classList.add('cur');
    if (how !== 'none') el.scrollIntoView({ block: how });
    updateTools();
    markFile();
  }
  const cur = () => S.items[S.cur];

  async function go(v) {
    if (!S.rv) return;
    v = Math.max(0, Math.min(commits().length, v));
    if (S.editor && !(await closeEditor())) return;
    S.view = v;
    S.cur = 0;
    if (location.hash !== '#' + v) history.replaceState(null, '', '#' + v);
    render(false);
    main.scrollTop = 0;
  }

  // ---- folding: a thread the reviewer has read shows on one line; fold marks one read
  function fold() {
    const el = cur();
    if (!el) return;
    if (S.editor) return status('close the note you are writing first (Esc)');
    const d = el.dataset;
    if (d.kind === 'folds') { // the cursor goes onto the first of them
      S.showRead[S.view] = true;
      render();
      const i = S.items.findIndex(x => x.dataset.kind === 'fold' && x.closest('.threads[data-anchor=top]'));
      if (i >= 0) setCur(i);
      return;
    }
    if (d.kind === 'fold') {
      S.unfold[d.thread] = true;
      render();
      const i = S.items.findIndex(x => x.dataset.kind === 'note' && x.dataset.id === d.id);
      if (i >= 0) setCur(i);
      return;
    }
    const t = d.kind === 'note' && threadOf(el);
    if (!t) { if (threadOf(el.nextElementSibling)) status('z folds a note: move onto it first'); return; }
    if (t.notes.some(n => n.draft)) { status('a thread with your draft stays open'); return; }
    const first = S.items.findIndex(x => threadOf(x) === t), last = S.items.findLastIndex(x => threadOf(x) === t);
    const next = itemKey(S.items[last + 1]);
    delete S.unfold[t.id];
    const at = latest(t);
    mark(S.read, t, at);
    mark(S.seen, t, at);
    keep();
    render();
    let i = S.items.findIndex(x => x.dataset.kind === 'fold' && x.dataset.thread === t.id);
    if (i < 0) i = S.items.findIndex(x => itemKey(x) === next); // in the line of read ones: the cursor goes on to what came next
    setCur(i >= 0 ? i : first);
  }
  function readAll() {
    if (S.editor) return status('close the note you are writing first (Esc)');
    const at = Math.max(0, ...threads().map(latest));
    S.read.all = Math.max(S.read.all, at);
    S.seen.all = Math.max(S.seen.all, at);
    S.unfold = {};
    keep();
    render();
    status('you have read every note: they show folded');
  }

  // ---- moving
  const move = d => setCur(S.cur + d);
  function jump(pred, dir, what) {
    for (let i = S.cur + dir; i >= 0 && i < S.items.length; i += dir) if (pred(S.items[i])) return setCur(i, 'start');
    status(`no ${dir > 0 ? 'next' : 'previous'} ${what} in this ${S.view ? 'commit' : 'change'}`);
  }

  // syncCur puts the cursor on what is on screen after the reader scrolled it away, so the buttons
  // and keys go on from what they see.
  function syncCur() {
    const el = cur();
    if (!el || S.editor) return;
    const m = main.getBoundingClientRect(), top = m.top + 40, r = el.getBoundingClientRect();
    const stuck = el.dataset.kind === 'file' && el.parentElement.getBoundingClientRect().top < m.top - 5; // its header sticks as the file scrolls by
    if (!stuck && r.bottom > top && r.top < m.bottom - 10) return;
    let lo = 0, hi = S.items.length - 1; // items are in page order: the first one below top
    while (lo < hi) { const mid = (lo + hi) >> 1; if (S.items[mid].getBoundingClientRect().top < top) lo = mid + 1; else hi = mid; }
    if (S.items[lo].getBoundingClientRect().top < m.bottom) setCur(lo, 'none');
  }
  const isHunk = el => el.dataset.kind === 'hunk';
  const isFile = el => el.dataset.kind === 'file';
  const isNote = el => el.dataset.kind === 'note';
  function halfPage(dir) {
    if (!cur()) return;
    const target = cur().getBoundingClientRect().top + dir * main.clientHeight / 2;
    let i = S.cur;
    while (i + dir >= 0 && i + dir < S.items.length) {
      const top = S.items[i + dir].getBoundingClientRect().top;
      if (dir > 0 ? top > target : top < target) break;
      i += dir;
    }
    setCur(i === S.cur ? i + dir : i, 'center');
  }

  // ---- the editor: drafts save as they are typed
  function hint(ed, msg, err) {
    const el = $('.hint', ed.box);
    $('.state', el).textContent = msg;
    el.classList.toggle('error', !!err);
  }

  function openEditor(mode, o) {
    if (S.editor) { S.editor.ta.focus(); return; }
    const box = document.createElement('div');
    box.className = 'editor';
    box.innerHTML = '<textarea placeholder="Write a note. It is saved as a draft as you type; only you see it until you send."></textarea>' +
      '<div class="hint"><span class="state">a draft, saved as you type</span><span class="keys"><kbd>Esc</kbd> or <kbd>Ctrl</kbd>+<kbd>Enter</kbd> closes</span>' +
      '<button type="button" class="done primary">Done</button></div>';
    const ta = $('textarea', box);
    $('.done', box).addEventListener('click', () => closeEditor());
    const ed = { mode, at: o.at, note: o.note, id: mode === 'edit' ? o.note : null, box, ta, saved: '', timer: 0, failed: false, warned: false };
    if (mode === 'new') {
      const wrap = document.createElement('div');
      wrap.className = 'thread new';
      wrap.innerHTML = `<div class="where">New note on ${esc(o.label)}</div>`;
      wrap.append(box);
      o.insert(wrap);
    } else if (mode === 'reply') {
      o.thread.append(box);
    } else {
      ta.value = ed.saved = o.text;
      $('.text', o.el).hidden = true;
      o.el.append(box);
    }
    S.editor = ed;
    document.body.classList.add('editing');
    ta.addEventListener('input', () => {
      clearTimeout(ed.timer);
      ed.warned = false;
      ed.timer = setTimeout(() => save(ed), 800);
      hint(ed, 'typing…');
    });
    ta.addEventListener('keydown', e => {
      e.stopPropagation();
      if (e.key === 'Escape' || (e.key === 'Enter' && (e.ctrlKey || e.metaKey))) { e.preventDefault(); closeEditor(); }
    });
    ta.focus();
    box.scrollIntoView({ block: 'nearest' });
  }

  function save(ed) {
    clearTimeout(ed.timer);
    const text = ed.ta.value;
    S.chain = S.chain.then(async () => {
      if (text === ed.saved || !text.trim()) return; // an emptied draft is deleted when the editor closes
      hint(ed, 'saving…');
      try {
        const r = ed.id ? await api('edit', { note: ed.id, text })
          : ed.mode === 'reply' ? await api('reply', { note: ed.note, text })
            : await api('draft', { at: ed.at, text });
        if (r.id) ed.id = r.id;
        ed.saved = text;
        ed.failed = false;
        S.rv = r.review;
        hint(ed, 'saved as a draft: only you see it until you send');
        updateSend();
        renderSide();
      } catch (e) {
        ed.failed = true;
        hint(ed, 'not saved: ' + e.message, true);
      }
    });
    return S.chain;
  }

  // closeEditor saves and closes the editor, and says whether it closed: unsaved text keeps it
  // open once, so a failed save is never lost silently.
  async function closeEditor() {
    const ed = S.editor;
    if (!ed) return true;
    await save(ed);
    const text = ed.ta.value;
    if (text.trim() && text !== ed.saved && !ed.warned) {
      ed.warned = true;
      hint(ed, !phone.matches ? 'not saved. Esc again discards it; typing tries again' : 'not saved. Done again discards it; typing tries again', true);
      return false;
    }
    if (!text.trim() && ed.id) {
      try { S.rv = (await api('delete', { note: ed.id })).review; } catch (e) { status('the emptied draft stays: ' + e.message, true); }
    }
    S.editor = null;
    document.body.classList.remove('editing');
    render();
    if (ed.id && text.trim()) {
      const i = S.items.findIndex(el => el.dataset.id === ed.id);
      if (i >= 0) setCur(i);
    }
    main.focus();
    return true;
  }

  window.addEventListener('beforeunload', e => {
    if (S.busy) { e.preventDefault(); e.returnValue = ''; return; } // the browser asks first; the server finishes it either way
    const ed = S.editor;
    if (!ed || !ed.ta.value.trim() || ed.ta.value === ed.saved) return;
    const body = ed.id ? { note: ed.id, text: ed.ta.value } : ed.mode === 'reply' ? { note: ed.note, text: ed.ta.value } : { at: ed.at, text: ed.ta.value };
    fetch('api/' + (ed.id ? 'edit' : ed.mode === 'reply' ? 'reply' : 'draft'),
      { method: 'POST', keepalive: true, headers: { 'Content-Type': 'application/json', 'X-Gloss': '1' }, body: JSON.stringify(body) });
  });

  // after skips the threads under a line, so a new one goes below them
  function after(el) {
    let x = el;
    while (x.nextElementSibling && x.nextElementSibling.classList.contains('thread')) x = x.nextElementSibling;
    return x;
  }

  // compose writes a note where the cursor is: in the whole change's diff, or in the commit's own
  function compose() {
    const el = cur();
    if (!el) return;
    const d = el.dataset, sha = S.view ? commits()[S.view - 1].sha : undefined;
    const at = o => Object.assign(sha ? { commit: sha } : {}, o);
    switch (d.kind) {
      case 'line': return openEditor('new', {
        at: at({ file: d.path, line: +d.line, old: d.old === '1' }),
        label: `${d.path}:${d.old === '1' ? '−' : '+'}${d.line}`, insert: w => after(el).after(w) });
      case 'hunk': return openEditor('new', {
        at: at({ file: d.path, hunk: +d.hunk }), label: `hunk ${d.hunk} of ${d.path}`, insert: w => after(el).after(w) });
      case 'file': return openEditor('new', {
        at: at({ file: d.path }), label: d.path, insert: w => $('.threads', el.parentElement).append(w) });
      case 'commit': return composeTop();
      case 'note': case 'fold': return reply();
      case 'folds': return composeTop();
      default: return composeWhole();
    }
  }

  // composeTop writes a note on the view itself: the whole change, or the whole commit
  function composeTop() {
    if (S.view === 0) return composeWhole();
    const c = commits()[S.view - 1];
    openEditor('new', { at: { commit: c.sha }, label: `commit ${short(c.sha)}`, insert: w => $('.threads[data-anchor=top]', main).append(w) });
  }

  async function composeWhole() {
    if (S.view !== 0) await go(0);
    if (S.view !== 0) return;
    openEditor('new', { at: {}, label: 'the whole change', insert: w => $('.threads[data-anchor=top]', main).append(w) });
  }

  function reply() {
    const el = cur();
    if (!el) return;
    let th = el.closest('.thread');
    if (!th && el.nextElementSibling && el.nextElementSibling.classList.contains('thread')) th = el.nextElementSibling;
    if (!th) return status('no note here to reply to');
    if (th.classList.contains('folded')) { // open it, so the reply goes under its notes
      const id = th.dataset.thread;
      S.unfold[id] = true;
      render();
      th = [...main.querySelectorAll('.thread')].find(x => x.dataset.thread === id);
      const i = th ? S.items.indexOf(th.querySelector('.note.item')) : -1;
      if (i >= 0) setCur(i);
      if (!th) return;
    }
    openEditor('reply', { note: th.dataset.thread, thread: th });
  }

  function edit() {
    const el = cur();
    const n = el && el.dataset.kind === 'note' && noteById(el.dataset.id);
    if (!n || !n.draft) return status('only your drafts can be edited');
    openEditor('edit', { note: n.id, el, text: n.text });
  }

  async function del() {
    const el = cur();
    const n = el && el.dataset.kind === 'note' && noteById(el.dataset.id);
    if (!n || !n.draft) return status('only your drafts can be deleted');
    if (!(await ask('Delete this draft?'))) return;
    try { S.rv = (await api('delete', { note: n.id })).review; render(); status('draft deleted'); } catch (e) { status(e.message, true); }
  }

  async function send() {
    if (S.editor && !(await closeEditor())) return;
    const n = drafts();
    if (!n) return canApprove() ? approve() : status(approved() ? 'approved' : 'no drafts to send');
    const q = `Send your ${n} draft${n > 1 ? 's' : ''} as one review? The author is told.`;
    const how = canApprove()
      ? await ask(q + ' Send and approve also lets the team merge it.', 'Send', 'Send and approve')
      : await ask(q, 'Send');
    if (how) submit(how === 'also' ? 'approve' : 'send');
  }

  async function approve() {
    if (S.editor && !(await closeEditor())) return;
    if (!canApprove()) return status(approved() ? 'approved' : `it is ${S.rv.state.toLowerCase()}: nothing to approve`);
    const n = drafts();
    const q = (n ? `Send your ${n} draft${n > 1 ? 's' : ''} and approve this pull request?` : 'Approve this pull request?') +
      ' The team may merge it once nothing holds it back, such as a rollout or what it depends on.';
    if (await ask(q, n ? 'Send and approve' : 'Approve')) submit('approve');
  }

  // submit sends the drafts, or approves the pull request with them. A dialog nothing closes waits
  // for GitHub's answer, and another says how it went.
  async function submit(what) {
    const approving = what === 'approve', n = drafts();
    S.busy = true;
    waiting((approving ? 'Approving…' : 'Sending your notes…') + ' This takes a few seconds.');
    status(approving ? 'approving…' : 'sending…');
    let r, failed;
    try { r = await api(what, {}); } catch (e) { failed = e; }
    if (failed && failed.lost) r = await settled(approving, n);
    S.busy = false;
    if (!r) {
      const said = !failed.lost ? (approving ? 'Not approved: ' : 'Not sent: ') + failed.message
        : `No answer from the server for a minute, and GitHub does not show ${approving ? 'the approval' : 'your notes'} yet. It may still go through: reload in a minute to see.`;
      status(said, true);
      await refresh(true);
      await dialog(said, { kind: failed.lost ? 'warn' : 'fail' });
      return;
    }
    S.rv = r.review;
    render();
    const notes = `${r.sent} note${r.sent === 1 ? '' : 's'}`;
    let said = approving ? 'Approved' + (r.sent ? `, with ${notes}` : '') + '. The team may merge it once nothing holds it back.' : `Sent ${notes}.`;
    if (r.hook_error) said += ` But telling the team failed: ${r.hook_error}`;
    if (r.lost) said += ' The server\'s answer was lost on the way, so whether the team was told is not known.';
    status(said, !!(r.hook_error || r.lost));
    return dialog(said, { kind: r.hook_error || r.lost ? 'warn' : 'ok' });
  }

  // settled waits for GitHub to show what a change whose answer was lost did, as the server finishes
  // it all the same: the viewer's approval, or no drafts left after a send of n notes. It gives up
  // after a minute.
  async function settled(approving, n) {
    waiting('No answer from the server yet: checking GitHub…');
    for (let i = 0; i < 20; i++) {
      await new Promise(ok => setTimeout(ok, 3000));
      let rv;
      try { rv = (await api('review')).review; } catch (_) { continue; } // no answer still
      const done = approving
        ? rv.approval && rv.approval.by === rv.viewer
        : !(rv.threads || []).some(t => t.notes.some(x => x.draft));
      if (done) return { sent: n, review: rv, lost: true };
    }
    return null;
  }

  async function refresh(quiet) {
    if (S.editor || !S.rv) return;
    try {
      const gen = S.gen;
      const r = await api('review');
      if (gen !== S.gen) return; // a change came back meanwhile, with a newer review
      const changed = JSON.stringify(r.review) !== JSON.stringify(S.rv);
      S.rv = r.review;
      merge(S.saved, readOf(r.read)); // marked meanwhile in the console, or on another device
      if (changed) render();
      if (!quiet) status(changed ? 'reloaded' : 'up to date');
    } catch (e) { if (!quiet) status(e.message, true); }
  }

  // ask shows a question with Cancel and one or two answers, and says which was chosen: 'yes',
  // 'also' (the second answer) or false
  const ask = (text, yes = 'Yes', also = '') => dialog(text, { yes, also, no: 'Cancel' });

  // dialog shows text with up to three buttons and says which was pressed: 'yes', 'also', or
  // false for the third, Esc or a click beside it. kind marks how something went: 'ok', 'warn'
  // or 'fail'.
  function dialog(text, { yes = 'OK', also = '', no = '', kind = '' } = {}) {
    return new Promise(done => {
      show(text, kind);
      $('#yes').textContent = yes;
      $('#also').textContent = also;
      $('#also').hidden = !also;
      $('#no').textContent = no;
      $('#no').hidden = !no;
      $('#modal .hint').innerHTML = '<kbd>Enter</kbd> the green button · <kbd>Esc</kbd> ' + (no ? esc(no.toLowerCase()) : 'closes');
      S.modal = ok => { $('#modal').hidden = true; S.modal = null; main.focus(); done(ok); };
      $('#yes').focus();
    });
  }

  // waiting shows what is being sent to GitHub in a dialog that nothing closes, until a dialog
  // that says how it went takes its place
  function waiting(text) {
    show(text, 'wait');
    S.modal = () => {}; // keys and clicks wait too
  }

  function show(text, kind) {
    $('#modal').className = kind;
    $('#modaltext').textContent = text;
    $('#modal .btns').hidden = $('#modal .hint').hidden = kind === 'wait';
    $('#modal').hidden = false;
  }

  // ---- keys and clicks
  document.addEventListener('keydown', e => {
    if (S.busy) { e.preventDefault(); return; } // nothing to press while GitHub answers
    if (S.modal) {
      const onButton = e.target.closest && e.target.closest('#modal button');
      if ((e.key === 'Enter' || e.key === ' ') && onButton) { e.preventDefault(); onButton.click(); return; } // the focused button
      if (e.key === 'Tab') { // Tab stays in the dialog
        e.preventDefault();
        const bs = [...document.querySelectorAll('#modal button')].filter(b => !b.hidden), i = bs.indexOf(document.activeElement);
        bs[(i + (e.shiftKey ? -1 : 1) + bs.length) % bs.length].focus();
        return;
      }
      if (e.key === 'Enter' || e.key === 'y') S.modal('yes');
      else if (e.key === 'a' && !$('#also').hidden) S.modal('also');
      else if (e.key === 'Escape' || e.key === 'n' || e.key === 'q') S.modal(false);
      e.preventDefault();
      return;
    }
    if (e.target.closest && e.target.closest('textarea, input')) return;
    if (S.picking) { // before a focused button takes Enter: the picker was opened with its button
      const n = commits().length;
      if (e.key === 'j' || e.key === 'ArrowDown') { S.pickCur = Math.min(n, S.pickCur + 1); markPick(); }
      else if (e.key === 'k' || e.key === 'ArrowUp') { S.pickCur = Math.max(0, S.pickCur - 1); markPick(); }
      else if (e.key === 'Enter' || e.key === 'l') { const v = S.pickCur; showPick(false); go(v); }
      else if (e.key === 'Escape' || e.key === 'q' || e.key === 'v') showPick(false);
      e.preventDefault();
      return;
    }
    if ((e.key === 'Enter' || e.key === ' ') && e.target.closest && e.target.closest('button, a, summary')) return; // a focused button or link: the browser presses it
    if (!help.hidden) {
      if (e.key === 'Escape' || e.key === '?' || e.key === 'q') help.hidden = true;
      e.preventDefault();
      return;
    }
    if (e.metaKey || e.altKey) return;
    if (e.ctrlKey) {
      if (e.key === 'd' || e.key === 'u') { halfPage(e.key === 'd' ? 1 : -1); e.preventDefault(); }
      return;
    }
    if (e.key === 'Escape' && document.body.classList.contains('menu')) { menu(false); e.preventDefault(); return; }
    const prev = S.key;
    S.key = '';
    const k = e.key;
    const keys = {
      j: () => move(1), ArrowDown: () => move(1), k: () => move(-1), ArrowUp: () => move(-1),
      g: () => { if (prev === 'g') setCur(0); else S.key = 'g'; }, G: () => setCur(S.items.length - 1),
      ']': () => jump(isHunk, 1, 'hunk'), '[': () => jump(isHunk, -1, 'hunk'),
      '}': () => jump(isNote, 1, 'note'), '{': () => jump(isNote, -1, 'note'),
      '.': () => jump(isFile, 1, 'file'), ',': () => jump(isFile, -1, 'file'),
      J: () => go(S.view + 1), K: () => go(S.view - 1), o: () => go(0),
      v: () => showPick(true), f: toggleSide,
      c: compose, C: composeWhole, r: reply, e: edit, x: del, z: fold, Z: readAll,
      Enter: () => { const d = cur() && cur().dataset; if (d && (d.kind === 'fold' || d.kind === 'folds')) fold(); },
      d: () => { if (prev === 'd') del(); else S.key = 'd'; },
      S: send, A: approve, R: () => refresh(false), '?': () => { help.hidden = false; },
    };
    if (keys[k]) { e.preventDefault(); keys[k](); }
  });

  main.addEventListener('click', e => {
    if (e.target.closest('.editor')) return;
    const it = e.target.closest('.item');
    if (it) { const i = S.items.indexOf(it); if (i >= 0) setCur(i, 'none'); }
    const act = e.target.closest('[data-act]');
    if (act) {
      e.preventDefault();
      if (act.dataset.act === 'more') {
        const note = act.closest('.note');
        S.expanded[note.dataset.id] = true;
        $('.text', note).classList.remove('fold');
        act.remove();
        return;
      }
      ({ reply, edit, delete: del, fold })[act.dataset.act]();
      return;
    }
    if (it && (it.dataset.kind === 'fold' || it.dataset.kind === 'folds') && !e.target.closest('a')) { fold(); return; }
    if (e.target.closest('button[data-do=top]')) composeTop();
    if (e.target.closest('button[data-do=readAll]')) readAll();
  });
  $('#modal').addEventListener('click', e => { if (e.target.id === 'modal' && S.modal) S.modal(false); });
  $('#yes').addEventListener('click', () => S.modal && S.modal('yes'));
  $('#also').addEventListener('click', () => S.modal && S.modal('also'));
  $('#no').addEventListener('click', () => S.modal && S.modal(false));
  tools.addEventListener('click', e => {
    const b = e.target.closest('button[data-do]');
    if (!b || b.disabled || !S.rv) return;
    ({
      prevCommit: () => go(S.view - 1), nextCommit: () => go(S.view + 1),
      prevHunk: () => jump(isHunk, -1, 'hunk'), nextHunk: () => jump(isHunk, 1, 'hunk'),
      prevNote: () => jump(isNote, -1, 'note'), nextNote: () => jump(isNote, 1, 'note'),
      act: () => primary().run(),
    })[b.dataset.do]();
  });

  // the files: a drawer on a phone, a column on a wide screen
  function menu(open) {
    document.body.classList.toggle('menu', open); menuBtn.setAttribute('aria-expanded', open); scrim.hidden = !open && !S.picking;
    if (open) markFile(); // the file in view is in view here too
  }
  menuBtn.addEventListener('click', toggleSide);
  scrim.addEventListener('click', () => { menu(false); showPick(false); });
  tree.addEventListener('click', e => {
    const dir = e.target.closest('.dir');
    if (dir) { S.fold[dir.dataset.dir] = dir.classList.contains('open'); renderSide(); return; }
    const a = e.target.closest('a.f');
    if (!a) return;
    e.preventDefault();
    if (phone.matches) menu(false);
    toFile(a.dataset.path);
    main.focus();
  });
  filter.addEventListener('input', renderSide);
  filter.addEventListener('keydown', e => {
    if (e.key === 'Escape') { filter.value = ''; renderSide(); main.focus(); }
    if (e.key === 'Enter') { const a = $('#tree a.f'); if (a) { if (phone.matches) menu(false); toFile(a.dataset.path); main.focus(); } }
  });
  // the files column's width, dragged at its edge, is remembered
  const sidew = store.get('sidew');
  if (sidew) document.documentElement.style.setProperty('--sidew', sidew + 'px');
  $('#resize').addEventListener('pointerdown', e => {
    e.preventDefault();
    const rs = e.currentTarget;
    rs.setPointerCapture(e.pointerId);
    document.body.classList.add('resizing');
    let w = side.getBoundingClientRect().width;
    const move = ev => { w = Math.max(180, Math.min(innerWidth * .6, ev.clientX - side.getBoundingClientRect().left)); document.documentElement.style.setProperty('--sidew', w + 'px'); };
    const up = () => { rs.removeEventListener('pointermove', move); rs.removeEventListener('pointerup', up); document.body.classList.remove('resizing'); store.set('sidew', Math.round(w)); };
    rs.addEventListener('pointermove', move);
    rs.addEventListener('pointerup', up);
  });
  applySide();
  menuBtn.setAttribute('aria-expanded', !phone.matches && S.side);
  phone.addEventListener('change', () => { applySide(); menu(false); });

  // the picker
  whereBtn.addEventListener('click', () => showPick(!S.picking));
  pick.addEventListener('click', e => {
    const a = e.target.closest('.pk');
    if (!a) return;
    e.preventDefault();
    showPick(false);
    go(+a.dataset.view);
  });

  main.addEventListener('toggle', e => { if (e.target.matches('details.desc')) S.open[e.target.dataset.view] = e.target.open; }, true);
  let scrollT = 0;
  main.addEventListener('scroll', () => { clearTimeout(scrollT); scrollT = setTimeout(syncCur, 150); }, { passive: true });
  help.addEventListener('click', () => { help.hidden = true; });
  $('#helpbtn').addEventListener('click', () => { help.hidden = false; });
  sendBtn.addEventListener('click', send);
  window.addEventListener('hashchange', () => { const v = parseInt(location.hash.slice(1), 10); if (!isNaN(v) && v !== S.view) go(v); });
  setInterval(() => { if (!document.hidden) refresh(true); }, 30000);

  // ---- the chat of the app gloss is part of (rota's), when it has one: a button over the review
  // opens it, framed; it hears what the reviewer looks at: the pull request, the whole change or a
  // commit, and the file and line, the hunk, the file or the note under the cursor
  const chatBtn = $('#chatbtn'), chatBox = $('#chatbox'), chatFrame = $('#chatframe');
  if (chatFrame) {
    const tell = msg => { if (chatFrame.contentWindow) chatFrame.contentWindow.postMessage(msg, location.origin); };
    const where = () => {
      if (!S.rv) return null;
      const n = commits().length, el = cur(), d = el ? el.dataset : {};
      let about = `Reviewing pull request ${S.rv.pr} «${S.rv.title}»` +
        (S.view === 0 ? ', the whole change' : `, commit ${S.view} of ${n} «${commits()[S.view - 1].title}»`);
      if (d.kind === 'line') about += `, at ${d.path}:${d.line}${d.old === '1' ? ' (the old side)' : ''}`;
      else if (d.kind === 'hunk') about += `, at ${d.path}, hunk ${d.hunk}`;
      else if (d.kind === 'file') about += `, at ${d.path}`;
      else if (d.kind === 'note') {
        const t = threads().find(x => x.notes.some(m => m.id === d.id)), m = noteById(d.id);
        if (t && t.path) about += `, at ${t.path}${t.line ? ':' + t.line : ''}`;
        if (m) about += `, on ${m.author}'s note «${m.text.split('\n')[0].slice(0, 80)}»`;
      }
      return { about };
    };
    let told = '';
    const tellWhere = () => { const ctx = where(), k = JSON.stringify(ctx); if (k !== told) { told = k; tell({ type: 'rota-context', ctx }); } };
    const showChat = open => {
      chatBox.hidden = !open;
      chatBtn.setAttribute('aria-expanded', String(open));
      document.body.classList.toggle('chatting', open);
      if (open) tellWhere();
      tell({ type: 'rota-chat-open', open });
      if (open) chatFrame.focus(); else main.focus();
    };
    chatBtn.addEventListener('click', () => showChat(chatBox.hidden));
    addEventListener('message', e => {
      if (e.origin !== location.origin || e.source !== chatFrame.contentWindow) return;
      const d = e.data || {};
      if (d.type === 'rota-chat-close') showChat(false);
      else if (d.type === 'rota-chat-dot') $('.dot', chatBtn).hidden = !d.dot;
      else if (d.type === 'rota-chat-ready') { told = ''; tellWhere(); if (!chatBox.hidden) tell({ type: 'rota-chat-open', open: true }); } // loaded anew
    });
    setInterval(tellWhere, 600); // the cursor moves by keys, taps, scrolls and the files: what is under it now
  }

  (async function init() {
    try {
      const r = await api('review');
      S.rv = r.review;
      S.read = readOf(r.read); // without it, every note is new
      S.saved = readOf(r.read);
    } catch (e) { main.innerHTML = `<p class="empty">${esc(e.message)}</p>`; return; }
    const hash = S.rv.pr.lastIndexOf('#');
    $('#pr').innerHTML = `<span class="repo">${esc(S.rv.pr.slice(0, hash))}</span>${esc(S.rv.pr.slice(hash))}`;
    $('#pr').title = S.rv.pr;
    $('#pr').href = S.rv.url;
    $('#title').textContent = S.rv.title;
    const v = parseInt(location.hash.slice(1), 10);
    await go(isNaN(v) ? 0 : v);
    main.focus();
    const n = newIn(threads());
    if (n) status(`${n} new note${n === 1 ? '' : 's'} · } goes to the next`);
  })();
})();

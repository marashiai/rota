'use strict';
// rota: the team's mail. The app reads the human's mail from /api/mail, does what they do through
// /api/act, and reads the mail again whenever the team folder changes (/events). Every string
// that comes from the team is escaped here; a message's body comes as HTML the server made from
// its Markdown, escaped there.

const TOKEN = document.body.dataset.token;
const esc = s => String(s == null ? '' : s).replace(/[&<>"']/g, c => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'}[c]));

// ---------- icons (Feather, MIT) ----------
const P = d => `<svg viewBox="0 0 24 24" aria-hidden="true">${d}</svg>`;
const I = {
  menu: P('<line x1="3" y1="6" x2="21" y2="6"/><line x1="3" y1="12" x2="21" y2="12"/><line x1="3" y1="18" x2="21" y2="18"/>'),
  search: P('<circle cx="11" cy="11" r="7"/><line x1="21" y1="21" x2="16.65" y2="16.65"/>'),
  back: P('<line x1="19" y1="12" x2="5" y2="12"/><polyline points="12 19 5 12 12 5"/>'),
  x: P('<line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/>'),
  filter: P('<line x1="3" y1="6" x2="21" y2="6"/><line x1="7" y1="12" x2="17" y2="12"/><line x1="10" y1="18" x2="14" y2="18"/>'),
  archive: P('<polyline points="21 8 21 21 3 21 3 8"/><rect x="1" y="3" width="22" height="5"/><line x1="10" y1="12" x2="14" y2="12"/>'),
  clock: P('<circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/>'),
  star: P('<polygon points="12 2 15.09 8.26 22 9.27 17 14.14 18.18 21.02 12 17.77 5.82 21.02 7 14.14 2 9.27 8.91 8.26 12 2"/>'),
  mail: P('<path d="M4 4h16c1.1 0 2 .9 2 2v12c0 1.1-.9 2-2 2H4c-1.1 0-2-.9-2-2V6c0-1.1.9-2 2-2z"/><polyline points="22,6 12,13 2,6"/>'),
  more: P('<circle cx="12" cy="5" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="12" cy="19" r="1"/>'),
  pen: P('<path d="M12 20h9"/><path d="M16.5 3.5a2.121 2.121 0 0 1 3 3L7 19l-4 1 1-4L16.5 3.5z"/>'),
  inbox: P('<polyline points="22 12 16 12 14 15 10 15 8 12 2 12"/><path d="M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z"/>'),
  send: P('<line x1="22" y1="2" x2="11" y2="13"/><polygon points="22 2 15 22 11 13 2 9 22 2"/>'),
  gear: P('<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/>'),
  team: P('<path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M23 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/>'),
  pr: P('<circle cx="18" cy="18" r="3"/><circle cx="6" cy="6" r="3"/><path d="M13 6h3a2 2 0 0 1 2 2v7"/><line x1="6" y1="9" x2="6" y2="21"/>'),
  check: P('<polyline points="20 6 9 17 4 12"/>'),
  ext: P('<path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/><polyline points="15 3 21 3 21 9"/><line x1="10" y1="14" x2="21" y2="3"/>'),
  eye: P('<path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/>'),
  unread: P('<path d="M22 12v6a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h10"/><circle cx="19" cy="5" r="3"/>'),
  hist: P('<polyline points="1 4 1 10 7 10"/><path d="M3.51 15a9 9 0 1 0 2.13-9.36L1 10"/>'),
  wait: P('<path d="M6 2h12"/><path d="M6 22h12"/><path d="M17 2v4.5L12 12 7 6.5V2"/><path d="M7 22v-4.5L12 12l5 5.5V22"/>'),
  doc: P('<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/>'),
  play: P('<polygon points="6 3 20 12 6 21 6 3"/>'),
  chat: P('<path d="M21 11.5a8.38 8.38 0 0 1-.9 3.8 8.5 8.5 0 0 1-7.6 4.7 8.38 8.38 0 0 1-3.8-.9L3 21l1.9-5.7a8.38 8.38 0 0 1-.9-3.8 8.5 8.5 0 0 1 4.7-7.6 8.38 8.38 0 0 1 3.8-.9h.5a8.48 8.48 0 0 1 8 8v.5z"/>'),
  minus: P('<line x1="5" y1="12" x2="19" y2="12"/>'),
  pause: P('<rect x="6" y="4" width="4" height="16"/><rect x="14" y="4" width="4" height="16"/>'),
  stop: P('<rect x="5" y="5" width="14" height="14" rx="1"/>'),
  tag: P('<path d="M20.59 13.41l-7.17 7.17a2 2 0 0 1-2.83 0L2 12V2h10l8.59 8.59a2 2 0 0 1 0 2.82z"/><line x1="7" y1="7" x2="7.01" y2="7"/>'),
};

// ---------- the mail, and where the human is in it ----------
let M = null; // threads, members, repos, prefs, top: as /api/mail last said
const S = {view: 'inbox', tab: 'focused', open: null, drawer: false, q: null, f: {}, keepRead: new Set(), compose: null, sel: new Set(), reply: false, vals: {}, drafts: {}, focusSearch: false, autoOpen: true,
  aTab: 'messages', roleDraft: null, adv: null, edits: {}, sent: null, weights: Object.create(null), wbase: null, tc: {open: false, min: false}, ptab: 'mail', // weights: by an agent's name, which may be any // tc: the desktop's chat window; ptab: the phone's Mail or Chat tab
  repos: false, repoAdding: ''}; // repos: the team's repositories open in Settings; repoAdding: the one GitHub is asked about
const desk = () => matchMedia('(min-width:1000px)').matches;
const find = id => M && M.threads.find(t => t.id === id);
const member = n => M && M.members.find(m => m.name === n);
const repoOf = r => M && M.repos.find(x => x.repo === r);
const last = t => t.msgs[t.msgs.length - 1];
const plural = (n, w) => `${n} ${w}${n === 1 ? '' : 's'}`;
// peer: who the human's reply goes to
function lastFrom(t) { return t.msgs.length ? t.msgs[t.msgs.length - 1].from : ''; }
function peer(t) {
  if (t.pr && t.pr.author) return t.pr.author;
  if (t.kind === 'msg') return t.msgs[0].to; // the agent the human writes to
  for (let i = t.msgs.length - 1; i >= 0; i--) if (t.msgs[i].from !== 'you' && t.msgs[i].from !== 'team') return t.msgs[i].from;
  return '';
}

// when: the time today, the day this week, the date before
function when(iso) {
  const d = new Date(iso), now = new Date();
  if (d.toDateString() === now.toDateString()) return d.toLocaleTimeString([], {hour: '2-digit', minute: '2-digit', hour12: false});
  if (now - d < 6 * 864e5) return d.toLocaleDateString([], {weekday: 'short'});
  return d.toLocaleDateString([], {day: 'numeric', month: 'short', year: d.getFullYear() === now.getFullYear() ? undefined : 'numeric'});
}

// av: a face: an agent's colour and initial, its state as a ring or a dot (Rex's program status)
function av(name, size = 40, presence = false) {
  const m = member(name), team = name === 'team', you = name === 'you';
  const c = m ? m.color : team ? 'var(--accent)' : you ? '#7b7b86' : '#888';
  const letter = you ? 'Me' : team ? 'R' : esc((name || '?')[0].toUpperCase());
  const cls = (presence && m ? ' p-' + m.state : '') + (team ? ' sq' + (presence ? ' p-on' : '') : '');
  return `<span class="av${cls}" style="--c:${c};--s:${size}px${you ? ';font-size:' + Math.round(size * .34) + 'px' : ''}">${letter}</span>`;
}
const STATE = {working: 'working', needs: 'needs you', failed: 'failed', done: 'done', idle: ''};
const ORDER = {working: 0, needs: 1, failed: 2, done: 3, idle: 4};

// ---------- lists ----------
const live = t => !t.snooze;
const inbox = () => M.threads.filter(t => t.box === 'inbox' && live(t));
const byRank = (a, b) => a.rank - b.rank;
// split: Focused is what the human starred or moved there, then what matters most, up to the
// number they chose; Other is the rest of what needs them
function split() {
  const all = inbox().sort(byRank), n = M.prefs.focus;
  if (!M.prefs.split) return {focused: all, other: []};
  const head = all.filter(t => t.star || t.place === 'focused');
  const rest = all.filter(t => !head.includes(t) && t.place !== 'other');
  const focused = [...head, ...rest.slice(0, Math.max(0, n - head.length))];
  return {focused, other: all.filter(t => !focused.includes(t))};
}
const updates = () => M.threads.filter(t => t.box === 'updates' && live(t));
const unreadOf = l => l.filter(t => t.unread).length;

function listFor() {
  if (S.q !== null) return {rows: results()};
  const l = folder();
  return {...l, rows: l.rows.filter(passes)};
}
// folder: the rows of the folder shown, before the filters
function folder() {
  const v = S.view;
  if (v === 'inbox') { const s = split(); return {rows: S.tab === 'updates' ? updates() : S.tab === 'other' ? s.other : s.focused}; }
  if (v === 'reviews') return {rows: reviewGroups().flatMap(g => g.rows)}; // searching comes first, above
  if (v === 'waiting') return {title: 'Waiting', sub: 'An agent is on these for you. Each comes back to your inbox when it answers.', rows: M.threads.filter(t => t.box === 'waiting' && live(t))};
  if (v === 'starred') return {title: 'Starred', rows: M.threads.filter(t => t.star)};
  if (v === 'snoozed') return {title: 'Snoozed', rows: M.threads.filter(t => t.snooze).sort((a, b) => a.snooze.localeCompare(b.snooze))};
  if (v === 'sent') return {title: 'Sent', rows: M.threads.filter(t => t.msgs.some(m => m.from === 'you'))};
  if (v === 'all') return {title: 'All messages', rows: M.threads};
  if (v.startsWith('repo:')) { const r = v.slice(5); return {title: (repoOf(r) || {}).name || r, rows: M.threads.filter(t => t.repo === r)}; }
  return {rows: []};
}

// ---------- filters: what narrows any list of mail, and search, as its chips say ----------
const KINDS = [['ask', 'Questions'], ['do', 'To-dos'], ['review', 'Pull requests'], ['msg', 'Your messages'], ['update', 'Updates'], ['digest', 'Reports']];
const DATES = [['today', 'Today'], ['week', 'Past 7 days']];
const filtering = () => Object.values(S.f).some(Boolean);
const mailList = () => ['inbox', 'waiting', 'starred', 'snoozed', 'sent', 'all'].includes(S.view) || S.view.startsWith('repo:');
function passes(t) {
  const c = S.f;
  if (c.unread && !t.unread && !S.keepRead.has(t.id)) return false; // one read while it is listed stays until the filters change
  if (c.star && !t.star) return false;
  if (c.from && !t.msgs.some(m => m.from === c.from)) return false;
  if (c.kind && t.kind !== c.kind) return false;
  if (c.repo && t.repo !== c.repo) return false;
  if (c.date) { const d = new Date(); if (c.date === 'today') d.setHours(0, 0, 0, 0); else d.setDate(d.getDate() - 7); if (new Date(t.at) < d) return false; }
  return true;
}
function setFilter(k, v) { // a selection the filters hid is no longer acted on; the keyboard is back at the Filter button
  S.f[k] = v; S.keepRead.clear(); S.sel.clear(); render();
  const b = document.querySelector('[data-filters]'); if (b) b.focus({preventScroll: true});
}

// ---------- search: words, #numbers and operators; the filters' chips narrow it ----------
function parse(q) {
  const f = {text: []};
  for (const w of (q || '').toLowerCase().split(/\s+/).filter(Boolean)) {
    const m = w.match(/^(from|is|repo|in):(.+)$/);
    if (m) (f[m[1]] = f[m[1]] || []).push(m[2]); else f.text.push(w.replace(/^#/, '#'));
  }
  return f;
}
function hay(t) {
  return (t.subject + ' ' + (t.label || '') + ' ' + (t.ref || '') + ' ' + ((repoOf(t.repo) || {}).name || '') + ' ' +
    t.msgs.map(m => m.from + ' ' + (m.to || '') + ' ' + m.plain).join(' ')).toLowerCase();
}
function results() {
  const f = parse(S.q);
  return M.threads.filter(t => {
    const h = hay(t), people = t.msgs.map(m => m.from), is = f.is || [];
    if (!f.text.every(w => h.includes(w.startsWith('#') ? w : w))) return false;
    if (f.from && !f.from.some(x => people.includes(x))) return false;
    if (f.repo && !f.repo.some(r => (t.repo || '').toLowerCase().includes(r) || ((repoOf(t.repo) || {}).name || '') === r)) return false;
    if (is.includes('unread') && !t.unread) return false;
    if (is.includes('starred') && !t.star) return false;
    if (is.includes('waiting') && t.box !== 'waiting') return false;
    const kinds = {todo: 'do', question: 'ask', update: 'update', report: 'digest', pr: 'review'};
    for (const w of is) if (kinds[w] && t.kind !== kinds[w]) return false;
    if (is.includes('approved') && !(t.pr && t.pr.state === 'approved')) return false;
    if (is.includes('review') && !(t.pr && t.pr.state === 'review')) return false;
    return passes(t);
  });
}
function hl(s) {
  let out = esc(s);
  const words = S.q ? parse(S.q).text.filter(w => w.length > 1) : [];
  for (const w of words) out = out.replace(new RegExp('(' + esc(w).replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + ')', 'ig'), '<mark>$1</mark>');
  return out;
}
const searching = () => S.q !== null;
const searchEmpty = () => S.q === '' && !filtering();

// ---------- rows ----------
function people(t) {
  const seen = [];
  for (const m of t.msgs) { const n = m.from === 'you' ? 'me' : m.from === 'team' ? 'Team' : m.from; if (!seen.includes(n)) seen.push(n); }
  if (seen.length === 1 && seen[0] === 'me') return 'To: ' + esc(t.msgs[0].to || '');
  return esc(seen.join(', ')) + (t.msgs.length > 1 ? `<span class="cnt">${t.msgs.length}</span>` : '');
}
const TAG = {ask: ['Question', 'ask'], do: ['To do', 'do'], update: ['Update', 'update'], digest: ['Report', 'digest']};
const PRTAG = {review: ['Review', 'review'], waiting: ['Waiting', 'wait'], approved: ['Approved', 'ok'], open: ['In progress', 'wait']};
function kindTag(t) {
  const tag = t.pr ? PRTAG[t.pr.state] || PRTAG.open : TAG[t.kind];
  return tag ? `<span class="k k-${tag[1]}">${tag[0]}</span>` : ''; // a plain message needs none
}
function whyOf(t) {
  const w = (t.why || []).map(x => [x, 'amber']);
  if (t.pr && t.pr.answered) w.unshift(['Answered', 'blue']);
  if (t.star) w.unshift(['Starred', 'accent']);
  return w.slice(0, 2).map(([x, c]) => `<span class="why ${c}">${esc(x)}</span>`).join('');
}
// snippet: what the newest message says beyond the subject the row already shows
function snippet(t) {
  if (t.pr && t.pr.state === 'waiting') return esc(`${t.pr.author || 'An agent'} is working on your ${plural(t.pr.waiting || 1, 'note')}`);
  let p = last(t).plain.replace(/^[\w./-]+#\d+:\s*/, '');
  const s = t.subject.replace(/…$/, '');
  if (s && p.startsWith(s)) p = p.slice(s.length).replace(/^[\s.?!:;,]+/, '');
  return hl(p);
}
function row(t) {
  const picked = S.sel.has(t.id), from = t.msgs.length ? (last(t).from === 'you' ? (t.msgs[0].to || 'you') : last(t).from) : 'team';
  return `<div class="swipe" data-sw="${esc(t.id)}"><div class="sw-bg"><span>${I.archive}Archive</span><span>Snooze${I.clock}</span></div>
  <div class="row${t.unread ? ' unread' : ''}${S.open === t.id ? ' cur' : ''}${picked ? ' picked' : ''}" data-open="${esc(t.id)}">
    <button class="avbtn" data-pick="${esc(t.id)}" aria-label="Select">${picked ? `<span class="av picked" style="--s:40px">${I.check}</span>` : av(from)}</button>
    <div class="rtxt"><div class="l1"><span class="from">${people(t)}</span><span class="time">${when(t.at)}</span></div>
      <div class="subj">${hl(t.subject)}</div>
      <div class="l3"><span class="snip">${kindTag(t)}${whyOf(t)}${snippet(t)}</span><button class="starb${t.star ? ' on' : ''}" data-star="${esc(t.id)}" aria-label="${t.star ? 'Unstar' : 'Star'}">${I.star}</button></div></div>
  </div></div>`;
}

function emptyText() {
  if (searching()) return ['No messages match', filtering() ? 'Try fewer words, or fewer filters.' : 'Try fewer words.'];
  if (filtering()) return [S.f.unread && Object.values(S.f).filter(Boolean).length === 1 ? 'No unread messages here' : 'No messages here match the filters', ''];
  if (S.view === 'inbox' && S.tab === 'focused') { const o = split().other.length; return ['Nothing needs you now', o ? `${plural(o, 'smaller thing')} wait${o === 1 ? 's' : ''} in Other.` : 'The team has all it needs.']; }
  if (S.view === 'inbox' && S.tab === 'other') return ['Nothing else', 'What needs you is in Focused.'];
  if (S.view === 'inbox' && S.tab === 'updates') return ['No updates', 'The team tells you here what it did.'];
  if (S.view === 'waiting') return ['Nothing waits for an agent', 'What you send an agent waits here until it answers.'];
  return ['Nothing here', ''];
}
function listBody() {
  if (S.view === 'settings' && !searching()) return settingsPage();
  if (S.view === 'keys' && !searching()) return keysPage();
  if (!searching() && S.view === 'team') return teamPage();
  if (!searching() && S.view.startsWith('agent:')) return agentPage();
  if (!searching() && S.view.startsWith('run:')) return runPage();
  if (!searching() && S.view === 'shares') return sharesPage();
  if (S.view === 'reviews' && !searching()) return reviewsList();
  if (S.view === 'issues' && !searching()) return issuesList();
  const {title, sub, rows} = listFor();
  let h = searching() ? askTeam() : '';
  if (title) h += `<div class="ltitle"><span>${esc(title)}</span>${mailList() ? filterBtn() : ''}</div>`;
  if (mailList() || searching()) h += activeFilters();
  if (sub && rows.length) h += `<div class="lsub">${esc(sub)}</div>`;
  if (!rows.length) { const [a, b] = emptyText(); h += `<div class="empty"><b>${esc(a)}</b>${esc(b)}${filtering() && !searching() ? '<button class="btn" data-clearf>Clear filters</button>' : ''}</div>`; }
  h += rows.map(row).join('');
  const other = split().other.filter(passes).length, unread = unreadOf(rows);
  if (S.view === 'inbox' && S.tab === 'focused' && !searching() && other && rows.length) h += `<button class="more" data-tab="other">${other} more in Other</button>`;
  if (S.f.unread && unread) h += `<button class="more" data-allread>Mark ${unread === 1 ? 'it' : 'all ' + unread} read</button>`;
  return h;
}

// ---------- Reviews and Issues ----------
// What narrows each is kept on the server, the same on every device and in the console: a
// repository, labels (it keeps what carries every one; a pull request carries those of the open
// issues it is for too) and a mark, which only the console chooses. One button opens them, as in
// the mail; each that is on is a pill whose × takes it off.
const ORDERS = [['', 'Newest first'], ['oldest', 'Oldest first'], ['priority', 'By priority']];
const same = (a, b) => (a || '').toLowerCase() === (b || '').toLowerCase(); // GitHub's names ignore case
const kept = list => M.lists[list] || {};
const listed = list => list === 'reviews' ? M.threads.filter(t => t.pr) : issues || [];
const facts = (list, x) => list === 'reviews' ? {repo: x.repo, labels: x.pr.labels, marks: x.pr.marks} : x; // what a list narrows it by
const narrowed = list => { const f = kept(list); return !!(f.repo || f.mark || (f.labels || []).length); };
// keeps: whether what narrows the list lets x through, leaving out the repository when anyRepo
function keeps(list, x, anyRepo) {
  const f = kept(list), it = facts(list, x);
  return (anyRepo || !f.repo || same(it.repo, f.repo)) && (!f.mark || (it.marks || []).some(m => m.Emoji === f.mark)) &&
    (f.labels || []).every(l => (it.labels || []).some(x => same(x, l)));
}
// reviewGroups: the open pull requests kept, in the order to review them, by where each stands; one
// that waits for another sits under it, unless the list is narrowed
function reviewGroups() {
  const prs = listed('reviews').filter(t => keeps('reviews', t)).sort((a, b) => a.pr.order - b.pr.order);
  return [['review', 'Your review'], ['waiting', 'Waiting for an agent'], ['approved', 'Approved, waiting to merge'], ['open', 'The agents still work on these']]
    .map(([st, label]) => ({label, rows: prs.filter(t => t.pr.state === st)})).filter(g => g.rows.length);
}
function prow(t) {
  const p = t.pr, nest = p.depth && !narrowed('reviews'); // narrowed, what it waits for may be left out, as in the console
  const third = p.state === 'approved' ? (p.waits && p.waits.length ? `<span>after ${esc(p.waits.join(', '))}</span>` : '') :
    p.state === 'waiting' ? `<span>${esc(p.author || 'an agent')} is on your ${plural(p.waiting || 1, 'note')}</span>` : whyOf(t);
  return `<div class="prow${t.unread ? ' unread' : ''}${S.open === t.id ? ' cur' : ''}" data-open="${esc(t.id)}">${nest ? '<span class="nest"></span>' : ''}${av(p.author || 'team', 32)}
    <div class="rtxt"><div class="t1">${esc(t.subject)}</div><div class="t2"><span class="ref">${esc(t.label || t.ref)}</span>${p.add || p.del ? `<span><span class="add">+${p.add || 0}</span> <span class="del">−${p.del || 0}</span></span>` : ''}${third}</div></div></div>`;
}
// listHead: the list's title and its filter button, the filters on as pills, and how many it shows
function listHead(list, title, shown) {
  const all = listed(list).length, what = list === 'reviews' ? 'open pull request' : 'open issue';
  const order = list === 'issues' ? ', ' + orderOf().toLowerCase() : '';
  return `<div class="ltitle"><span>${title}</span>${listOpts(list).length ? listBtn(list) : ''}</div>${listPills(list)}` +
    (all && (list === 'issues' || shown !== all) ? `<div class="lsub">${shown === all ? plural(all, what) : `${shown} of ${plural(all, what)}`}${order}</div>` : '');
}
function listEmpty(list, none) {
  return narrowed(list) ? `<div class="empty"><b>None match the filters</b><button class="btn" data-clearlist="${list}">Clear filters</button></div>` : `<div class="empty"><b>${none}</b></div>`;
}
function reviewsList() {
  const groups = reviewGroups();
  let h = listHead('reviews', 'Reviews', groups.reduce((n, g) => n + g.rows.length, 0));
  for (const g of groups) h += `<div class="sec">${esc(g.label)}<span>${g.rows.length}</span></div>` + g.rows.map(t => prow(t)).join('');
  return groups.length ? h : h + listEmpty('reviews', 'No open pull requests');
}
let issues = null; // the open issues, read when the Issues folder opens
async function loadIssues() {
  try { const r = await fetch('/api/issues', {cache: 'no-store'}); if (r.ok) { issues = await r.json(); if (S.view === 'issues') render(); } } catch (e) { /* the folder says it */ }
}
const shownIssues = () => (issues || []).filter(i => keeps('issues', i));
function issuesList() {
  if (!issues) return `<div class="ltitle"><span>Issues</span></div><div class="empty"><b>Loading…</b></div>`;
  const rows = shownIssues(), h = listHead('issues', 'Issues', rows.length);
  if (!rows.length) return h + listEmpty('issues', 'No open issues');
  return h + rows.map(i => { const id = issueId(i);
    return `<div class="prow${S.open === id ? ' cur' : ''}" data-open="${esc(id)}"><div class="rtxt"><div class="t1">${(i.marks || []).map(m => `<span title="${esc(m.Text)}">${esc(m.Emoji)}</span> `).join('')}${esc(i.title)}</div>
      <div class="t2"><span class="ref">${esc(repoName(i.repo) + '#' + i.number)}</span>${i.created ? `<span>opened ${when(i.created)}</span>` : ''}${(i.labels || []).slice(0, 3).map(l => `<span>${esc(l)}</span>`).join('')}</div></div>
      ${(i.prs || []).filter(find).map(p => `<button class="sopt" data-open="${esc(p)}" title="${esc(find(p).subject)}">${I.pr}${esc(p.startsWith(`pr:${i.repo}#`) ? p.slice(p.indexOf('#')) : find(p).label)}</button>`).join('')}</div>`; }).join('');
} // a pull request of the issue's own repository is its number: the line keeps room for the title
// ---------- an issue, read in the reading pane ----------
// An issue opens as a message does: S.open is issue:<owner/name>#<n>. What it says and its comments
// are GitHub's, read each time it opens (/api/issue); until they come, the list's line shows, or what
// was read the last time.
const issueId = i => `issue:${i.repo}#${i.number}`;
const isIssue = id => typeof id === 'string' && id.startsWith('issue:');
const ISS = {got: new Map(), asked: new Set(), err: new Map(), expand: null}; // by id: what GitHub said, what is being asked, why it did not come; expand: the issue whose earlier comments are open
// issueOf: the list's line for an issue, or what its id names when the list does not have it
function issueOf(id) {
  const l = (issues || []).find(i => issueId(i) === id);
  if (l) return l;
  const m = /^issue:([^/#]+\/[^/#]+)#(\d+)$/.exec(id || '');
  return m ? {repo: m[1], number: +m[2], title: '', url: `https://github.com/${m[1]}/issues/${m[2]}`} : null;
}
async function loadIssue(id) {
  const i = issueOf(id);
  if (!i || ISS.asked.has(id)) return;
  ISS.asked.add(id);
  try {
    const r = await fetch(`/api/issue?repo=${encodeURIComponent(i.repo)}&n=${i.number}`, {cache: 'no-store'});
    if (r.ok) { ISS.got.set(id, await r.json()); ISS.err.delete(id); } else ISS.err.set(id, (await r.text()).trim() || r.statusText);
  } catch (e) { ISS.err.set(id, 'The team’s pages did not answer: ' + (e.message || e)); }
  ISS.asked.delete(id);
  if (S.open === id) render();
}
const REASON = {NOT_PLANNED: 'Closed as not planned', COMPLETED: 'Closed as completed', DUPLICATE: 'Closed as a duplicate'};
function issueTools(id, phone) {
  const i = issueOf(id);
  return `${phone ? `<button class="ib" data-close aria-label="Back">${I.back}</button>` : ''}<span class="sp"></span>${i ? `<a class="ib" href="${esc(i.url)}" target="_blank" rel="noopener" title="Open on GitHub" aria-label="Open on GitHub">${I.ext}</a>` : ''}`;
}
// issueMsg: what the issue says, or a comment on it, as a message of a thread shows
function issueMsg(who, at, html, sub) {
  return `<div class="msg"><div class="mh">${av(who || '?', 40)}<div class="who"><b>${esc(who || 'someone')}</b>${sub ? `<span>${esc(sub)}</span>` : ''}</div><span class="at">${when(at)}</span></div><div class="mb">${html || '<p class="small">No description.</p>'}</div></div>`;
}
function issuePane(id) {
  const i = issueOf(id), g = ISS.got.get(id), err = ISS.err.get(id);
  if (!i) return `<div class="empty"><b>No issue selected</b></div>`;
  const r = repoOf(i.repo), labels = g ? g.labels || [] : i.labels || [];
  let h = `<div class="th"><h1>${esc((g && g.title) || i.title || repoName(i.repo) + '#' + i.number)}</h1><div class="chips">${g && g.state !== 'OPEN' ? `<span class="chip rs-merged">${esc(REASON[g.reason] || 'Closed')}</span>` : ''}${r ? `<span class="chip"><i style="--c:${r.color}"></i>${esc(r.name)}</span>` : ''}<span class="chip ref">${esc(repoName(i.repo) + '#' + i.number)}</span>${labels.map(l => `<span class="chip">${esc(l)}</span>`).join('')}</div>`;
  if (g) {
    h += issueMsg(g.author, g.created, g.html, 'opened it');
    const n = g.comments.length;
    g.comments.forEach((c, k) => { // the newest open; the earlier ones a line each, until one is opened
      if (k === n - 1 || ISS.expand === id) { h += issueMsg(c.author, c.at, c.html); return; }
      const line = c.html.replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ').trim(); // the server's escaped text, without its tags
      h += `<div class="msg c" data-expand="${esc(id)}"><div class="mh">${av(c.author || '?', 36)}<div class="who"><b>${esc(c.author || 'someone')}</b><span>${line}</span></div><span class="at">${when(c.at)}</span></div></div>`;
    });
  } else h += `<div class="msg">${err ? `<p class="warn">${esc(err)}</p>` : '<p class="small">Reading it on GitHub…</p>'}</div>`;
  const prs = (i.prs || []).map(find).filter(Boolean);
  if (prs.length) h += `<div class="gh2">${prs.length === 1 ? 'Pull request' : 'Pull requests'} for it</div>` + prs.map(t => `<button class="prc" data-open="${esc(t.id)}"><span class="ic">${I.pr}</span><span class="tx"><b>${esc(t.label || t.ref)}${t.pr.author ? ' · by ' + esc(t.pr.author) : ''}</b><span class="meta"><span>${esc(t.subject)}</span></span></span></button>`).join('');
  return h + `<div class="acts"><a class="btn" href="${esc(i.url)}" target="_blank" rel="noopener">${I.ext}Open on GitHub</a></div></div>`;
}
// paneOf: the reading pane's tools and what it shows, for a message or an issue
const paneOf = (id, phone) => isIssue(id) ? [issueTools(id, phone), issuePane(id)] : [threadTools(id, phone), threadPane(id)];
// openIt opens a message or an issue in the reading pane; an issue is read again from GitHub
function openIt(id) {
  S.open = id; S.reply = false; S.wrong = null;
  if (isIssue(id)) { ISS.err.delete(id); loadIssue(id); }
  render();
}
const orderOf = () => (ORDERS.find(o => o[0] === (kept('issues').order || '')) || ORDERS[0])[1];
const repoName = r => (repoOf(r) || {}).name || r;
function listBtn(list) {
  const f = kept(list), n = (f.repo ? 1 : 0) + (f.mark ? 1 : 0) + (f.labels || []).length, what = list === 'issues' ? 'Sort and filter' : 'Filter';
  return `<button class="ib fbtn${n ? ' on' : ''}" data-filters="${list}" title="${what}" aria-label="${what}${n ? `, ${n} on` : ''}">${I.filter}</button>`;
}
function listPills(list) {
  const f = kept(list), on = [];
  if (f.repo) on.push(['repo', '', repoName(f.repo), '']);
  if (f.mark) on.push(['mark', '', markName(list, f.mark), '']);
  for (const l of f.labels || []) on.push(['drop', l, l, I.tag]); // a label has its tag: creator the label is not shop-creator the repository
  if (!on.length) return '';
  return `<div class="factive">${on.map(([k, v, text, ic]) => `<button class="fpill" data-unlist="${list}" data-lk="${k}" data-lv="${esc(v)}" aria-label="Take off ${esc(text)}">${ic}${esc(text)}${I.x}</button>`).join('')}${on.length > 1 ? `<button class="fclear" data-clearlist="${list}">Clear</button>` : ''}</div>`;
}
function markName(list, e) {
  for (const x of listed(list)) for (const m of facts(list, x).marks || []) if (m.Emoji === e) return `${e} ${m.Name}`;
  return e;
}
// labelChoices: the labels of what the list shows but those chosen, each with how many carry it:
// as many as the list keeps when it is chosen too
function labelChoices(list) {
  const chosen = kept(list).labels || [], n = new Map();
  for (const x of listed(list)) if (keeps(list, x)) for (const l of facts(list, x).labels || []) {
    if (chosen.some(c => same(c, l))) continue;
    const k = l.toLowerCase(), e = n.get(k); if (e) e[1]++; else n.set(k, [l, 1]);
  }
  return [...n.values()].sort((a, b) => a[0].localeCompare(b[0], undefined, {sensitivity: 'base', numeric: true}));
}
// filterAt: the filter button a menu is for, the mail's ('') or a list's, where it opens on a computer
const filterAt = list => document.querySelector(`[data-filters="${list}"]`);
// listOpts: what the list's button offers
function listOpts(list) {
  const f = kept(list), chosen = f.labels || [], opts = [];
  if (list === 'issues') opts.push(['Sort', orderOf(), () => sheet('Sort', ORDERS.map(([v, l]) => [l, v === (f.order || '') ? '✓' : '', () => setList('issues', {order: v})]), filterAt(list))]);
  if (M.repos.length > 1 || f.repo) opts.push(['Repository', f.repo ? repoName(f.repo) : 'Any', () => chooseRepo(list)]);
  if (chosen.length || labelChoices(list).length) opts.push(['Label', chosen.length ? chosen[0] + (chosen.length > 1 ? ` +${chosen.length - 1}` : '') : 'Any', () => chooseLabel(list)]);
  if (narrowed(list)) opts.push(['Clear filters', '', () => setList(list, {clear: 1})]);
  return opts;
}
function chooseRepo(list) {
  const f = kept(list), base = listed(list).filter(x => keeps(list, x, true)), n = r => base.filter(x => same(facts(list, x).repo, r)).length;
  sheet('Repository', [['Any repository', f.repo ? String(base.length) : '✓', () => setList(list, {repo: ''})],
    ...M.repos.filter(r => n(r.repo) || same(r.repo, f.repo)).map(r => [r.name, same(r.repo, f.repo) ? '✓' : String(n(r.repo)), () => setList(list, {repo: r.repo})])], filterAt(list));
}
function chooseLabel(list) {
  const chosen = kept(list).labels || [];
  sheet(chosen.length ? 'Label: it shows those with every ticked one' : 'Label', [...chosen.map(l => [l, '✓', () => setList(list, {drop: l})]),
    ...labelChoices(list).map(([l, n]) => [l, String(n), () => setList(list, {add: l})])], filterAt(list));
}
// setList changes what narrows Reviews or Issues: here at once, then on the server, which keeps it
// for every device and the console. It sends only what changed, so what they chose meanwhile stays.
async function setList(list, change) {
  const f = {...kept(list)};
  let labels = [...(f.labels || [])];
  if ('repo' in change) f.repo = change.repo;
  if ('mark' in change) f.mark = change.mark;
  if (change.add && !labels.some(l => same(l, change.add))) labels.push(change.add);
  if (change.drop) labels = labels.filter(l => !same(l, change.drop));
  if (change.clear) { f.repo = f.mark = ''; labels = []; }
  if ('order' in change) f.order = change.order;
  M.lists[list] = {...f, labels}; S.sel.clear(); render();
  const b = document.querySelector(`[data-filters="${list}"]`); if (b) b.focus({preventScroll: true}); // the keyboard is back at the button
  if (await post({action: 'list', id: list, ...change}) !== null && 'order' in change) loadIssues(); // the order is the server's
}

// ---------- a thread ----------
function prCard(t) {
  const p = t.pr, r = repoOf(t.repo);
  const size = p.add || p.del ? `<span><span class="add">+${p.add || 0}</span> <span class="del">−${p.del || 0}</span></span>` : '';
  return `<a class="prc" href="${esc(t.review)}"><span class="ic">${I.pr}</span><span class="tx"><b>${esc(t.label || t.ref)}${p.author ? ' · by ' + esc(p.author) : ''}</b>
    <span class="meta">${p.files ? `<span>${plural(p.files, 'file')}</span>` : ''}${size}${p.draft ? '<span>Draft</span>' : ''}${p.waits && p.waits.length ? `<span>after ${esc(p.waits.join(', '))}</span>` : ''}</span></span></a>`;
}
function statusChip(t) {
  if (t.kind === 'msg') { // from the thread, not its box: the human may have archived it
    const to = peer(t), answered = lastFrom(t) !== 'you';
    const l = answered ? `${to} answered` : !t.seen ? `Waiting for ${to} to read it` : `${to} read it ${when(t.seen)}`;
    return `<span class="chip rs-${answered ? 'review' : 'waiting'}">${esc(l)}</span>`;
  }
  if (!t.pr) return kindTag(t).replace('class="k', 'class="chip k');
  const p = t.pr, l = {review: 'Your review', waiting: `Waiting for ${p.author || 'an agent'}`, approved: 'Approved · merges once nothing holds it back', open: 'The agents still work on it'}[p.state];
  return `<span class="chip rs-${esc(p.state)}">${esc(l)}</span>`;
}
function threadPane(id) {
  const t = find(id);
  if (!t) return `<div class="empty"><b>No message selected</b>Choose one from the list.</div>`;
  const n = t.msgs.length, r = repoOf(t.repo);
  let h = `<div class="th"><h1>${esc(t.subject)}</h1><div class="chips">${statusChip(t)}${r ? `<span class="chip"><i style="--c:${r.color}"></i>${esc(r.name)}</span>` : ''}${t.label ? `<span class="chip ref">${esc(t.label)}</span>` : ''}${(t.why || []).map(w => `<span class="chip why amber">${esc(w)}</span>`).join('')}</div>`;
  t.msgs.forEach((m, i) => {
    const open = i === n - 1 || t.expand;
    const who = m.from === 'you' ? 'You' : m.from === 'team' ? 'Team' : m.from;
    if (!open) { h += `<div class="msg c" data-expand="${esc(t.id)}"><div class="mh">${av(m.from, 36)}<div class="who"><b>${esc(who)}</b><span>${esc(m.plain)}</span></div><span class="at">${when(m.at)}</span></div></div>`; return; }
    const mm = member(m.from), sub = [m.from === 'you' ? `to ${m.to || peer(t)}` : '', mm && i === n - 1 ? STATE[mm.state] : ''].filter(Boolean).join(' · ');
    h += `<div class="msg"><div class="mh">${av(m.from, 40, true)}<div class="who"><b>${esc(who)}</b>${sub ? `<span>${esc(sub)}</span>` : ''}</div><span class="at">${when(m.at)}</span></div><div class="mb">${m.html}`;
    if (m.quotes) h += `<div class="quotes">${m.quotes.map(q => `<div class="quote">${q.where ? `<span class="at2">${esc(q.where)}</span>` : ''}${q.answer ? `<div class="mine">${q.note}</div><div class="an">${I.check}<div>${q.answer}</div></div>` : q.note}</div>`).join('')}</div>`;
    if (m.steps) h += `<div class="steps">${m.steps.map((s, k) => { const on = (t.ticks || []).includes(k);
      return `<button class="step" data-tick="${esc(t.id)}" data-k="${k}"${t.qid ? '' : ' disabled'}><span class="box${on ? ' on' : ''}">${on ? I.check : ''}</span><span>${s}</span></button>`; }).join('')}</div>`;
    if (m.options && t.qid) h += `<div class="smart">${m.options.map(o => `<button class="sr${o === m.recommend ? ' rec' : ''}" data-ans="${esc(t.id)}" data-v="${esc(o)}">${esc(o)}${o === m.recommend ? '<span class="r">RECOMMENDED</span>' : ''}</button>`).join('')}</div>`;
    if (m.steps && t.qid) h += `<div class="acts"><button class="btn p" data-done="${esc(t.id)}">${I.check}Mark done</button><button class="btn" data-wrong="${esc(t.id)}">Something went wrong</button></div>`;
    if (i === n - 1) {
      if (t.pr) h += prCard(t) + `<div class="acts"><a class="btn p" href="${esc(t.review)}">${I.eye}${t.pr.answered ? 'Review the changes' : 'Review'}</a>${['review', 'waiting'].includes(t.pr.state) ? `<button class="btn" data-approve="${esc(t.id)}">${I.check}Approve</button>` : ''}</div>`;
      if (m.run) h += `<div class="acts"><a class="btn" href="${esc(m.run)}">${I.doc}View the run</a></div>`;
    }
    h += `</div></div>`;
  });
  h += `</div>`;
  const to = peer(t), ask = !!t.qid;
  if (to && t.kind !== 'digest') {
    const label = S.wrong === t.id ? `What went wrong, and at which step?` : ask ? `Answer ${to}` : `Reply to ${to}`;
    h += `<div class="replybar">${S.reply ? `<textarea id="rtext" placeholder="${esc(label)}">${esc(S.drafts[t.id] || '')}</textarea><div class="row2"><button class="btn" data-replyclose>Cancel</button><button class="btn p" data-send="${esc(t.id)}">${I.send}${ask ? 'Answer' : 'Send'}</button></div>` : `<button class="fake" data-replyopen>${av(to, 24)}${esc(label)}…</button>`}</div>`;
  }
  return h;
}
function threadTools(id, phone) {
  const t = find(id);
  if (!t) return phone ? `<button class="ib" data-close aria-label="Back">${I.back}</button>` : '';
  return `${phone ? `<button class="ib" data-close aria-label="Back">${I.back}</button>` : ''}<span class="sp"></span>
    <button class="ib" data-archive="${esc(id)}" title="${t.box === 'done' ? 'Move to Inbox' : 'Archive (e)'}">${I.archive}</button>
    <button class="ib" data-snooze="${esc(id)}" title="Snooze (b)">${I.clock}</button>
    <button class="ib" data-unread="${esc(id)}" title="Mark unread">${I.unread}</button>
    <button class="ib starb${t.star ? ' on' : ''}" data-star="${esc(id)}" title="Star (s)">${I.star}</button>
    <button class="ib" data-moremenu="${esc(id)}" title="More">${I.more}</button>`;
}

// ---------- pages in the app ----------
function settingsPage() {
  const p = M.prefs;
  if (push.state === 'unknown') pushLook(); // however Settings was opened
  return `<div class="ltitle">Settings</div><div class="setp">
  <div class="gh2">Inbox</div><div class="card">
    <div class="fld"><span>Focused and Other</span><button class="tog${p.split ? '' : ' off'}" data-pref="split" aria-label="Focused and Other"></button></div>
    <div class="fld"><span>Focused shows up to</span><span class="mini">${[3, 5, 7, 10].map(n => `<button class="${p.focus === n ? 'on' : ''}" data-pref="focus" data-v="${n}">${n}</button>`).join('')}</span></div>
    <div class="fld"><span>After you answer or archive</span><span class="mini">${[['next', 'Next'], ['list', 'List']].map(([v, l]) => `<button class="${p.after === v ? 'on' : ''}" data-pref="after" data-v="${v}">${l}</button>`).join('')}</span></div></div>
  <div class="gh2">Notifications</div><div class="card">
    <div class="fld"><span>Notify me about</span><span class="mini">${[['focused', 'Focused'], ['all', 'All'], ['off', 'None']].map(([v, l]) => `<button class="${p.notify === v ? 'on' : ''}" data-pref="notify" data-v="${v}">${l}</button>`).join('')}</span></div>
    <div class="fld" style="display:block"><div>This device</div><div class="small">${pushText()}</div>${pushButtons()}</div></div>
  ${teamSettings()}
  <div class="gh2">More</div><div class="card">
    <button class="fld" data-view="keys"><span>Keyboard shortcuts</span><span class="v">?</span></button>
    <button class="fld" data-adv=""><span>Advanced</span><span class="v">${TEAM.settings ? TEAM.settings.fields.length + ' settings' : ''}</span></button></div>
  ${S.adv !== null && TEAM.settings ? advancedForm(TEAM.settings.fields) : ''}
  ${TEAM.settings ? `<p class="small" style="text-align:center;margin-top:16px">rota ${esc(TEAM.settings.version)}</p>` : ''}</div>`;
}
// ---------- the team, each agent, each run ----------
// Each reads its own endpoint when shown, and again when the team folder changes; the mail's
// threads with an agent come from the mail.
const TEAM = {team: null, agent: null, run: null, error: '', saves: 0, asked: 0, shown: 0}; // saves: how many the human made; a reading from before one is stale; asked, shown: readings, in order
const teamView = () => S.view === 'team' || S.view === 'shares' || S.view.startsWith('agent:') || S.view.startsWith('run:');
const pageView = () => teamView() || S.view === 'settings'; // read from its own endpoint, and kept in the address
const editing = () => S.roleDraft !== null || Object.keys(S.edits).length > 0 || Object.keys(S.weights).length > 0;
// typing: the human writes a message, a reply, a role or a setting; the page is not drawn again under them
const typing = () => { const a = document.activeElement; return !!(a && ['rtext', 'ctext', 'cabout', 'repoin'].includes(a.id) && a.value) || editing(); };
const cap = s => s.charAt(0).toUpperCase() + s.slice(1);
async function loadPage(force) { // force: drawn even while the human edits, after they tapped something
  const v = S.view;
  const url = v === 'team' ? '/api/team' : v === 'settings' ? '/api/settings' : v === 'shares' ? '/api/shares' : v.startsWith('agent:') ? '/api/agent?name=' + encodeURIComponent(v.slice(6)) : v.startsWith('run:') ? '/api/run?id=' + encodeURIComponent(v.slice(4)) : '';
  if (!url) return;
  const saves = TEAM.saves, n = ++TEAM.asked;
  try {
    const r = await fetch(url, {cache: 'no-store'});
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
    const data = await r.json();
    if (S.view !== v || saves !== TEAM.saves || n < TEAM.shown) return; // the human went elsewhere, or saved: a reading after it follows; or a newer one is shown
    TEAM.shown = n;
    TEAM[v === 'team' ? 'team' : v === 'settings' ? 'settings' : v === 'shares' ? 'shares' : v.startsWith('agent:') ? 'agent' : 'run'] = data;
    TEAM.error = '';
  } catch (e) { if (S.view === v) TEAM.error = e.message || String(e); }
  if (S.view === v && (force || !typing())) render();
}
function teamPage() {
  const T = TEAM.team;
  if (!T) return `<div class="ltitle">Team</div>` + loadingText('The team');
  const ctl = !T.organizer ? `<button class="btn" data-teamact="start">${I.play}Start</button>` : T.paused ? `<button class="btn" data-teamact="resume">${I.play}Resume</button>` : `<button class="btn" data-teamact="pause">${I.pause}Pause</button>`;
  let h = `<div class="ltitle">Team</div><div class="tpad"><div class="card tcard"><div class="trow"><div class="tx"><b>${esc(T.title)}</b>${T.line ? `<div class="small">${esc(T.line)}</div>` : ''}</div><button class="btn" data-chat>${I.chat}Chat</button>${ctl}</div>`;
  if (T.budget) h += `<div class="meter"><i style="width:${+T.fill || 0}%"></i></div><div class="small">${esc(T.budget)}${T.free ? ' · ' + esc(T.free) : ''}</div>`;
  if (T.usage) h += `<div class="small">Claude: ${esc(T.usage)}</div>`;
  h += `<button class="link" data-view="shares">Rebalance the agents' shares…</button>`;
  h += `</div>${(T.warnings || []).map(w => `<p class="warn">${esc(w)}</p>`).join('')}</div>`;
  h += [...T.agents].sort((a, b) => ORDER[a.state] - ORDER[b.state]).map(a => `<button class="row" data-view="agent:${esc(a.name)}">${av(a.name, 40, true)}<div class="rtxt">
    <div class="l1"><span class="from">${esc(a.name)} ${STATE[a.state] ? `<span class="state st-${esc(a.state)}">${esc(STATE[a.state])}</span>` : ''}</span></div>
    <div class="snip">${esc(a.line || '')}${a.schedule ? ' · ' + esc(a.schedule) : ''}</div>${a.why ? `<div class="wait">${esc(a.why)}</div>` : ''}</div></button>`).join('');
  return h;
}
function loadingText(what) {
  return TEAM.error ? `<div class="empty"><b>${esc(what)} did not load</b>${esc(TEAM.error)}</div>` : `<div class="empty"><b>Loading…</b></div>`;
}
function agentPage() {
  const name = S.view.slice(6), A = TEAM.agent && TEAM.agent.name === name ? TEAM.agent : null;
  let h = `<div class="prof"><button class="lnk" data-view="team">${I.back}Team</button>`;
  if (!A) return h + `</div>` + loadingText(name);
  const working = A.state === 'working', n = esc(name), online = CHAT.here.includes(name); // online: in the chat, as the chat routes it
  h += `<div class="top">${av(name, 64, true)}<div><h2>${n}</h2><div class="ln">${STATE[A.state] ? `<span class="state st-${esc(A.state)}">${esc(STATE[A.state])}</span> ` : ''}${esc(A.line || '')}</div>${A.why ? `<div class="wait">${esc(A.why)}</div>` : ''}${!online && CHAT.agents.length ? `<div class="pline"><i></i>Offline: a message waits for its next run</div>` : ''}</div></div>
    <div class="acts">${online ? `<button class="btn p" data-chat="@${n} ">${I.chat}Chat now</button>` : ''}<button class="btn${online ? '' : ' p'}" data-composeto="${n}">${I.pen}Message</button>${working
      ? `<button class="btn" data-teamact="cancel" data-arg="${n}">${I.stop}Stop</button>` : `<button class="btn" data-teamact="run" data-arg="${n}">${I.play}Run now</button>`}${A.paused
      ? `<button class="btn" data-teamact="resume" data-arg="${n}">${I.play}Resume</button>` : `<button class="btn" data-teamact="pause" data-arg="${n}">${I.pause}Pause</button>`}</div>
    ${working && A.live ? `<div class="live">$ ${esc(A.live)}</div>` : ''}
    <div class="seg" role="tablist">${[['messages', 'Messages'], ['runs', 'Runs'], ['role', 'Role'], ['settings', 'Settings']].map(([v, l]) => `<button role="tab" class="${S.aTab === v ? 'on' : ''}" data-atab="${v}">${l}</button>`).join('')}</div></div>`;
  if (S.aTab === 'messages') {
    const ts = M.threads.filter(t => t.msgs.some(m => m.from === name || m.to === name)).sort((a, b) => b.at.localeCompare(a.at));
    h += ts.length ? ts.map(row).join('') : `<div class="empty"><b>No messages with ${n} yet</b></div>`;
  }
  if (S.aTab === 'runs') {
    h += (A.reports || []).map(r => `<a class="row run2" href="${esc(r.url)}"><div class="rtxt"><div class="l1"><span class="from">Report: ${esc(r.name)}</span><span class="time">${esc(when(r.at))}</span></div></div></a>`).join('');
    h += (A.runList || []).length ? A.runList.map(runRow).join('') : `<div class="empty"><b>No runs yet</b></div>`;
  }
  if (S.aTab === 'role') h += roleTab(A);
  if (S.aTab === 'settings') h += agentSettings(A);
  return h;
}
// sharesPage: a proposal of new weights from how each agent used the pool, to change and apply
function sharesPage() {
  const P = TEAM.shares;
  let h = `<div class="prof"><button class="lnk" data-view="team">${I.back}Team</button>`;
  if (!P) return h + `</div>` + loadingText('The proposal');
  h += `<h2 class="rh">Rebalance the shares</h2><div class="small">Proposed from what each agent's shifts used since ${esc(when(P.since))}, with a quarter to spare. Change any weight, then Apply.</div>${P.note ? `<p class="warn">${esc(P.note)}</p>` : ''}</div>`;
  if (!P.agents.length) return h + `<div class="empty"><b>No agent is enabled</b></div>`;
  h += `<div class="tpad"><div class="card">` + P.agents.map(a => `<div class="fld shr"><div class="tx"><b>${esc(a.name)}</b>
    <div class="small">Now ${+a.weight} (${(+a.share).toFixed(1)}%)${a.why ? '' : ` · ${plural(+a.shifts, 'shift')}${a.need ? ', ' + esc(a.need) : ''}`}</div>${a.why ? `<div class="small">${esc(cap(a.why))}</div>` : ''}</div>
    <div class="shw"><input id="w-${esc(a.name)}" data-weight="${esc(a.name)}" inputmode="decimal" aria-label="${esc(a.name)}'s weight" value="${esc(Object.hasOwn(S.weights, a.name) ? S.weights[a.name] : a.proposed)}"><span class="small" data-share="${esc(a.name)}">${esc(shareOf(a.name))}</span></div></div>`).join('') +
    `</div></div>`;
  return h;
}
// sharesChanged: the agents whose share the weights shown change, as the page rounds a share, or whom
// they stop or start
const sharesChanged = () => TEAM.shares.agents.filter(a => shareOf(a.name) !== (+a.share).toFixed(1) + '%' || !weightOf(a) !== !+a.weight).length;
// weightOf: an agent's weight as the page shows it; shareOf: its part of the pool by them all
const weightOf = a => Math.max(0, +(Object.hasOwn(S.weights, a.name) ? S.weights[a.name] : a.proposed) || 0);
function shareOf(name) {
  const P = TEAM.shares, sum = P.agents.reduce((s, a) => s + weightOf(a), 0), a = P.agents.find(x => x.name === name);
  return sum > 0 && a ? (100 * weightOf(a) / sum).toFixed(1) + '%' : '';
}
function sharesShown() { document.querySelectorAll('[data-share]').forEach(el => { el.textContent = shareOf(el.dataset.share); }); }
// importance is an agent's priority as High (1), Normal (2) or Low (3 and more): it goes ahead of those below
function importance(f) { const p = +(f.effective || 100); return p <= 1 ? 1 : p === 2 ? 2 : 3; }
function runStatus(r) {
  return {ok: 'Finished', running: 'Working', pool: 'Stopped: the team pool ran out', hours: 'Stopped: its hours ended', interrupted: 'Interrupted'}[r.status] || cap(r.status);
}
// runRow: what the run said, or how it ended when it said nothing
function runRow(r) {
  const odd = r.status !== 'ok', chip = odd && r.title ? `<span class="state st-${r.status === 'running' ? 'working' : 'failed'}">${esc(runStatus(r))}</span> ` : '';
  return `<button class="row run2" data-view="run:${esc(r.id)}"><div class="rtxt"><div class="l1"><span class="from">${esc(r.title || runStatus(r))}</span><span class="time">${esc(when(r.at))}</span></div>
    <div class="snip">${chip}${esc(r.text || (odd ? r.reason || '' : ''))}</div></div></button>`;
}
function roleTab(A) {
  if (S.roleDraft !== null) return `<div class="prof"><textarea id="roletext" class="roleed" rows="18" aria-label="${esc(A.name)}'s role">${esc(S.roleDraft)}</textarea>
    <div class="acts"><button class="btn" data-cancelrole>Close</button></div><div class="small">Its next run works under the new role.</div></div>`;
  return `<div class="prof"><div class="acts"><button class="btn" data-editrole>${I.pen}Edit the role</button></div><div class="mb role">${A.roleHTML}</div></div>`;
}
function agentSettings(A) {
  const f = k => A.fields.find(x => x.key === k) || {value: '', effective: '', options: []};
  const on = f('enabled').effective === 'true', model = f('model');
  const models = ['', ...(model.options || []).filter(Boolean)];
  if (model.value && !models.includes(model.value)) models.push(model.value);
  let h = `<div class="prof"><div class="card">
    <div class="fld"><span>On</span><button class="tog${on ? '' : ' off'}" data-aset="enabled" data-v="${on ? 'false' : 'true'}" aria-label="On"></button></div>
    <button class="fld" data-adv="Schedule"><span>Runs</span><span class="v">${esc(A.schedule)}</span></button>
    <div class="fld"><span>Importance</span><span class="mini">${[[1, 'High'], [2, 'Normal'], [3, 'Low']].map(([v, l]) => `<button class="${importance(f('priority')) === v ? 'on' : ''}" data-aset="priority" data-v="${v}">${l}</button>`).join('')}</span></div>
    <div class="fld fmodel"><span>Model</span><span class="mini">${models.map(m => `<button class="${(model.value || '') === m ? 'on' : ''}" data-aset="model" data-v="${esc(m)}">${esc(m ? cap(m) : 'Default')}</button>`).join('')}</span></div></div>
    <div class="card" style="margin-top:12px"><button class="fld" data-adv=""><span>Advanced</span><span class="v">${A.fields.length} settings</span></button></div>`;
  if (S.adv !== null) h += advancedForm(A.fields);
  return h + `</div>`;
}
// saved: a setting as the server has it, as far as the page knows: what is on its way there, or what
// the page read (a switch: what applies)
const saved = f => S.sent && S.sent.view === S.view && S.sent.vals && f.key in S.sent.vals ? S.sent.vals[f.key] : f.kind === 'check' ? f.effective : f.value;
// advancedForm edits every setting, by section: an empty field takes the default, which it shows
function advancedForm(fields) {
  let h = `<div class="adv">`, sec = '';
  for (const f of fields) {
    if (f.section !== sec) { if (sec) h += `</div>`; sec = f.section; h += `<div class="gh2" id="sec-${esc(sec.replace(/\W+/g, '-'))}">${esc(sec)}</div><div class="card">`; }
    const id = 'f-' + f.key, v = f.key in S.edits ? S.edits[f.key] : saved(f);
    let input;
    if (f.kind === 'check') {
      const on = (f.key in S.edits ? S.edits[f.key] : saved(f)) === 'true'; // the file may say TRUE or 1: effective says true
      input = `<button class="tog${on ? '' : ' off'}" data-fcheck="${esc(f.key)}" data-v="${on ? 'false' : 'true'}" aria-label="${esc(f.label)}"></button>`;
    } else if (f.kind === 'choice' && !f.free) {
      input = `<select id="${id}" data-field="${esc(f.key)}">${['', ...(f.options || []).filter(o => o !== '')].map(o => `<option value="${esc(o)}"${o === v ? ' selected' : ''}>${esc(o || 'Default (' + (f.effective || 'none') + ')')}</option>`).join('')}</select>`;
    } else {
      const list = f.free && (f.options || []).some(Boolean);
      input = `<input id="${id}" data-field="${esc(f.key)}" value="${esc(v)}" placeholder="${esc(f.effective)}"${f.kind === 'number' ? ' inputmode="decimal"' : ''}${list ? ` list="dl-${esc(f.key)}"` : ''} autocomplete="off" spellcheck="false">` +
        (list ? `<datalist id="dl-${esc(f.key)}">${f.options.filter(Boolean).map(o => `<option value="${esc(o)}">`).join('')}</datalist>` : '');
    }
    h += `<div class="fld afld${f.key in S.edits ? ' edited' : ''}"><label for="${id}">${esc(f.label)}</label>${input}${f.help ? `<div class="small">${esc(f.help)}</div>` : ''}</div>`;
  }
  if (sec) h += `</div>`;
  return h + `<div class="acts"><button class="btn" data-closeadv>Close</button></div>
    <div class="small">An empty field takes the default it shows.</div></div>`;
}
function runPage() {
  const id = S.view.slice(4), R = TEAM.run && TEAM.run.id === id ? TEAM.run : null, agent = id.slice(16);
  let h = `<div class="prof"><button class="lnk" data-view="agent:${esc(agent)}">${I.back}${esc(agent)}</button>`;
  if (!R) return h + `</div>` + loadingText('The run');
  const facts = [runStatus(R), R.turns ? plural(R.turns, 'turn') : '', R.minutes ? R.minutes + ' min' : '', R.pct ? R.pct + ' of the team pool' : ''].filter(Boolean).join(' · ');
  const at = new Date(R.at).toLocaleString([], {weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit', hour12: false});
  h += `<h2 class="rh">${esc(R.title || agent + '’s run')}</h2><div class="small">${esc(at)} · ${esc(facts)}</div>${R.reason ? `<div class="small">${esc(R.reason)}</div>` : ''}</div>`;
  if (R.report) h += `<div class="prof"><div class="mb report">${R.report}</div></div>`;
  if (R.events) h += `<div class="prof"><details class="trans"${R.report ? '' : ' open'}><summary>The whole transcript</summary><div class="events">${R.events}</div>${R.running ? '<p class="ok">The run is still going…</p>' : ''}</details></div>`;
  else h += `<div class="prof small">Its transcript is no longer kept.</div>`;
  return h;
}
async function teamPost(data, after, undo) {
  TEAM.saves++;
  const said = await post(data);
  TEAM.saves++; // a reading started while it was sent may be from before it
  if (said === null) { loadPage(); return null; }
  if (said) toast(M.members.some(m => said.startsWith(m.name + ' ')) ? said : cap(said), undo); // an agent's name stays as it is
  if (after) after();
  await loadPage(true); // what they tapped shows, as what they typed stays
  return said;
}

// ---------- what waits for Save ----------
// A setting changed under Advanced, a role edited, and new shares wait for Save (Apply, for the shares),
// and a bar says so as soon as anything does, wherever the page is scrolled: on a phone above Mail and
// Chat, on a computer at the foot of the page. A switch or a choice outside Advanced applies at once.
// pending: what the page shown has that is not saved, or null; mine: the human changed it, and leaving
// the page drops it (new shares may be only the proposal)
function pending() {
  const v = S.view, n = Object.keys(S.edits).length, A = TEAM.agent;
  if (n && (v === 'settings' || v.startsWith('agent:'))) return {act: 'adv', text: plural(n, 'unsaved change'), save: 'Save', mine: true};
  const role = S.sent && S.sent.view === v && 'role' in S.sent ? S.sent.role : A && A.role; // as the server has it, as far as the page knows
  if (S.roleDraft !== null && A && v === 'agent:' + A.name && S.roleDraft !== role) return {act: 'role', text: 'Unsaved changes', save: 'Save', mine: true};
  if (v === 'shares' && TEAM.shares) {
    const c = sharesChanged();
    if (c) return {act: 'shares', text: plural(c, 'new share'), save: 'Apply', mine: Object.keys(S.weights).some(k => !S.wbase || +S.weights[k] !== +S.wbase[k])};
  }
  return null;
}
const unsaved = () => { const p = pending(); return !!(p && p.mine); };
// leaveOK: whether the human may leave what they changed: nothing is unsaved, or they say so
const leaveOK = (q = 'Leave without saving your changes?') => !unsaved() || confirm(q);
// barShown: what the bar says, when the page is shown: not under a search or the phone's Chat
const barShown = () => searching() || !desk() && S.ptab === 'chat' ? null : pending();
let barWas = ''; // what the bar showed when it was drawn: it slides in only as it appears
function barInner() {
  const p = barShown();
  if (!p) return '';
  return `<span class="sbt"><i class="sbd"></i>${esc(p.text)}</span>${p.mine ? `<button class="btn" data-discard${S.saving ? ' disabled' : ''}>Discard</button>` : ''}` +
    `<button class="btn p" data-save${S.saving ? ' disabled' : ''}>${S.saving ? `<span class="spin"></span>${p.save === 'Apply' ? 'Applying' : 'Saving'}…` : p.save}</button>`;
}
// saveBar: the bar's place in the page drawn; paintBar fills it as what waits changes, as it is typed
function saveBar() {
  const inner = barInner(), cls = inner && !barWas ? ' in' : '';
  barWas = inner; document.body.classList.toggle('unsaved', !!inner);
  return `<div class="savebar${cls}" id="savebar" role="region" aria-label="Unsaved changes" aria-live="polite"${inner ? '' : ' hidden'}>${inner}</div>`;
}
function paintBar() {
  const el = document.getElementById('savebar'), inner = barInner();
  if (!el || inner === barWas) return;
  if (inner && !barWas) { el.classList.remove('in'); void el.offsetWidth; el.classList.add('in'); }
  el.innerHTML = inner; el.hidden = !inner; barWas = inner;
  document.body.classList.toggle('unsaved', !!inner);
}
// saveNow saves what the bar says waits; it says so until the page shows it saved
async function saveNow() {
  const p = pending(), from = S.view;
  if (!p || S.saving) return;
  if (p.act === 'role' && !S.roleDraft.trim()) { toast('The role is empty'); return; }
  const w = Object.create(null); // the shares: the weights as the page shows them, not as a later reading has them
  if (p.act === 'shares') {
    document.querySelectorAll('[data-weight]').forEach(el => { w[el.dataset.weight] = Number(el.value); });
    if (Object.values(w).some(v => !Number.isFinite(v) || v < 0)) { toast('A weight is a number, 0 or more'); return; }
  }
  S.saving = true; paintBar();
  try {
    if (p.act === 'adv') {
      const sent = {...S.edits}, agent = from.startsWith('agent:'); // what is typed meanwhile stays: typed back to what was, it is a change
      S.sent = {view: from, vals: sent};
      const said = await teamPost({action: agent ? 'agent-set' : 'team-set', arg: agent ? TEAM.agent.name : '', text: JSON.stringify(sent)}, () => {
        if (S.view !== from) return;
        for (const f of ((agent ? TEAM.agent : TEAM.settings) || {}).fields || []) if (f.key in sent) { f.value = sent[f.key]; if (f.kind === 'check') f.effective = sent[f.key]; } // saved, before the page reads it back
        for (const k in sent) if (S.edits[k] === sent[k]) delete S.edits[k];
      });
      if (said === null && S.view === from && S.adv !== null) { // not saved: what was set back to what was sent still waits, unless it is what the server has
        for (const k in sent) if (!(k in S.edits)) S.edits[k] = sent[k];
        S.sent = null;
        for (const k of Object.keys(S.edits)) { const f = advField(k); if (f && S.edits[k] === saved(f)) delete S.edits[k]; }
      }
    } else if (p.act === 'role') {
      const sent = S.roleDraft;
      S.sent = {view: from, role: sent};
      await teamPost({action: 'role', arg: TEAM.agent.name, text: sent}, () => { if (S.view === from && S.roleDraft === sent) S.roleDraft = null; });
    } else { // applied, the proposal is done with: back to the team, unless weights were typed meanwhile
      const typed = JSON.stringify(S.weights);
      await teamPost({action: 'shares-apply', text: JSON.stringify(w)}, () => {
        if (S.view !== from) return;
        if (JSON.stringify(S.weights) !== typed) { S.wbase = Object.assign(Object.create(null), w); return; } // typed meanwhile: they wait, compared with what was applied
        S.weights = Object.create(null); S.wbase = null; S.view = 'team'; window.scrollTo(0, 0);
      });
    }
  } finally { S.saving = false; S.sent = null; }
  render();
}
// discardNow drops what the human changed; the toast's Undo brings it back while the page is the same
function discardNow() {
  const p = pending(), was = {aTab: S.aTab, adv: S.adv, edits: {...S.edits}, roleDraft: S.roleDraft, weights: Object.assign(Object.create(null), S.weights), wbase: S.wbase}, view = S.view;
  if (!p || !p.mine || S.saving) return;
  if (p.act === 'adv') S.edits = {}; else if (p.act === 'role') S.roleDraft = null; else { S.weights = Object.create(null); S.wbase = null; }
  render();
  toast('Changes discarded', () => { if (S.view === view && !unsaved()) Object.assign(S, was); }); // not over what was changed since
}
// teamSettings: the team's budget, its repositories, and whether the human's phone opens the pages
function teamSettings() {
  const T = TEAM.settings;
  if (!T) return `<div class="gh2">Team</div><div class="card"><div class="fld"><span class="small">${TEAM.error ? esc(TEAM.error) : 'Loading…'}</span></div></div>`;
  const phone = T.web === 'tailscale';
  return `<div class="gh2">Team</div><div class="card">
    <button class="fld fbud" data-adv="The team pool"><div class="trow"><span>Budget</span><b>${esc(T.teamPct)}% of your Claude plan</b></div>
      ${T.budget ? `<div class="meter"><i style="width:${+T.fill || 0}%"></i></div><div class="small">${esc(T.budget)} · ${T.pacing === 'daily' ? 'a seventh of the week each day' : 'by the week'}</div>` : ''}</button>
    <button class="fld" data-repos aria-expanded="${S.repos}"><span>Repositories</span><span class="v">${T.repos.length}</span></button>
    ${S.repos ? reposEditor(T) : ''}
    <div class="fld"><span>Open on my phone${phone && T.phone ? `<div class="small">${esc(T.phone)}</div>` : ''}</span><button class="tog${phone ? '' : ' off'}" data-webmode="${phone ? 'local' : 'tailscale'}" aria-label="Open on my phone"></button></div></div>`;
}
// reposEditor: the team's repositories, each a link to it on GitHub with an × that removes it, a field
// that adds one, and the agents with a list of their own, which the team's does not reach. Each change
// applies at once, a removal with Undo.
function reposEditor(T) {
  const adding = S.repoAdding, others = T.own.map(o => o.name);
  let h = `<div class="repos">`;
  h += T.repos.length ? T.repos.map(r => `<div class="rrow"><a href="https://github.com/${esc(r)}" target="_blank" rel="noopener"><span class="dotc" style="--c:${esc((repoOf(r) || {}).color || 'var(--ink3)')}"></span><span>${esc(r)}</span></a><button class="ib" data-repodel="${esc(r)}" aria-label="Remove ${esc(r)}" title="Remove">${I.x}</button></div>`).join('')
    : `<div class="small">None yet.</div>`;
  h += `<div class="radd"><input id="repoin" value="${esc(S.vals.repoin || '')}" placeholder="owner/name" aria-label="Add a repository: owner/name, or its link on GitHub" inputmode="url" enterkeyhint="done" autocapitalize="off" autocomplete="off" autocorrect="off" spellcheck="false">
    <button class="btn" data-repoadd${adding ? ' disabled' : ''}>${adding ? '<span class="spin"></span>Checking…' : 'Add'}</button></div>`;
  h += `<div class="small">${others.length ? `Every agent works on these but ${esc(andList(others))}, which ${others.length > 1 ? 'have lists' : 'has a list'} of ${others.length > 1 ? 'their' : 'its'} own:` : 'Every agent works on these. One given a list of its own in its Settings works on that instead.'}</div>`;
  h += T.own.map(o => `<div class="rown"><button class="lnk" data-view="agent:${esc(o.name)}">${esc(o.name)}</button><button class="btn" data-repoteam="${esc(o.name)}">Use the team’s</button><span class="small">${o.on ? '' : 'Off · '}${esc(o.repos.length ? o.repos.join(', ') : 'No repository')}</span></div>`).join('');
  return h + `</div>`;
}
const andList = a => a.length < 2 ? a.join('') : a.slice(0, -1).join(', ') + ' and ' + a[a.length - 1];
// repoAdd asks the server to add what the field says; GitHub says whether it exists, so it takes a moment
async function repoAdd() {
  const v = (S.vals.repoin || '').trim();
  if (!v || S.repoAdding) return;
  S.repoAdding = v; render();
  try { await teamPost({action: 'repo-add', arg: v}, () => { if ((S.vals.repoin || '').trim() === v) S.vals.repoin = ''; }); } // what was typed meanwhile stays
  finally { S.repoAdding = ''; render(); }
}
// calmUntil: a row just went, and what was below it moved up under the pointer: a double click's second
// click would land there, on the next ×, on Add or on Open on my phone, so clicks wait out the moment
let calmUntil = 0;
// repoDel removes one at once, and Undo puts it back where it was
function repoDel(repo) {
  const at = TEAM.settings.repos.indexOf(repo);
  TEAM.settings.repos = TEAM.settings.repos.filter(r => r !== repo); calmUntil = performance.now() + 500; render();
  teamPost({action: 'repo-remove', arg: repo}, null, () => {
    const T = TEAM.settings; // as read since
    if (T && !T.repos.includes(repo)) T.repos.splice(Math.min(at, T.repos.length), 0, repo);
    teamPost({action: 'repo-add', arg: repo, text: String(at)});
  });
}
// repoTeam has an agent work on the team's repositories: its own list goes, with Undo while it had one
function repoTeam(name) {
  const o = TEAM.settings.own.find(x => x.name === name);
  if (!o) return;
  const was = o.repos.join(', ');
  TEAM.settings.own = TEAM.settings.own.filter(x => x !== o); calmUntil = performance.now() + 500; render();
  teamPost({action: 'agent-set', arg: name, text: JSON.stringify({repos: ''})}, null, was ? () => {
    const T = TEAM.settings;
    if (T && !T.own.some(x => x.name === name)) T.own.push(o);
    teamPost({action: 'agent-set', arg: name, text: JSON.stringify({repos: was})});
  } : null);
}
// ---------- the team chat: one, as a call: the Team always in it, an agent while it works ----------
// An agent is online, in the chat, while it works: its turn; offline, what the human writes it goes
// to its mail, and it reads it at its next run. The chat says who is which, and why.
// It reads /api/chat whenever the team folder changes; what the human writes goes to /api/act.
const CHAT = {msgs: [], busy: '', here: [], agents: [], team: true, tag: null, asked: 0, shown: 0, local: 0, applying: {}}; // asked, shown: readings, in order: an older one is not shown over a newer; applying: cards tapped, until the server says how they stand
async function loadChat() {
  const n = ++CHAT.asked;
  try {
    const r = await fetch('/api/chat', {cache: 'no-cache', headers: CHAT.tag ? {'If-None-Match': CHAT.tag} : {}});
    if (r.status === 304 || !r.ok || n < CHAT.shown) return;
    const tag = r.headers.get('ETag');
    const data = await r.json();
    if (n < CHAT.shown) return;
    CHAT.shown = n;
    const pres = () => JSON.stringify(CHAT.agents.map(p => [p.name, p.online]));
    const mine = CHAT.msgs.filter(m => m.local), was = pres(); // mine: sent, not yet in what the server says
    Object.assign(CHAT, data, {tag});
    CHAT.agents = CHAT.agents || []; CHAT.here = CHAT.here || [];
    if (S.view.startsWith('agent:') && pres() !== was && !typing()) render(); // its page says whether it is online
    const recent = CHAT.msgs.slice(-10).filter(x => x.from === 'you');
    CHAT.msgs = CHAT.msgs.concat(mine.filter(m => !recent.some(x => x.html.includes(esc(m.text)))));
    if (chatShown() && (!typing() || document.activeElement.id === 'chatin')) render(); // a new line shows at the end, unless the human reads further up
  } catch (e) { /* the next change reads it again */ }
}
const chatShown = () => desk() ? S.tc.open && !S.tc.min : S.ptab === 'chat';
function chatToEnd() { const b = document.querySelector('[data-cbody]'); if (b) b.scrollTop = b.scrollHeight; }
function hereText() {
  const w = CHAT.here;
  return w.length ? `${w.join(', ')} ${w.length > 1 ? 'are' : 'is'} online` : 'No agent is online';
}
// presenceRow: every agent, online (at work: it answers here) or offline (not its turn: what you write
// it goes to its mail); a tap names it in what you write
function presenceRow() {
  if (!CHAT.agents.length) return '';
  return `<div class="cwp" role="list" aria-label="Agents in the chat">${[...CHAT.agents].sort((a, b) => b.online - a.online).map(p => `<button role="listitem" class="pres${p.online ? ' on' : ''}" data-cto="${esc(p.name)}" aria-label="${esc(p.name)}, ${p.online ? 'online' : 'offline'}" title="${esc(p.online ? `${p.name} is online: it answers here` : `${p.name} is offline, ${p.note}`)}"><i></i>${esc(p.name)}</button>`).join('')}</div>`;
}
// changeCard: what the Team proposes, which the human applies with a tap and undoes where that can be done
function changeCard(m) {
  const c = m.change, set = Object.entries(c.set || {});
  let what = c.say ? `<b>${esc(c.say)}</b>` : '';
  if (c.new) what += `<div class="cq">${esc(c.role)}</div>${set.length ? '<div class="small">' + set.map(([k, v]) => esc(k + ': ' + v)).join(' · ') + '</div>' : ''}`;
  else if (c.replace) what += `<div class="cq"><s>${esc(c.replace)}</s></div><div class="cq">${esc(c.with || '')}</div>`;
  else if (c.do === 'role') what += `<div class="cq">${esc(c.role)}</div>`;
  else what += set.map(([k, v]) => `<div>${c.was && c.was[k] !== undefined ? `<s>${esc(c.was[k] || 'default')}</s> → ` : ''}<b>${esc(v || 'default')}</b></div>`).join('');
  if (c.text) what += `<div class="cq">${esc(c.text)}</div>`;
  if (c.lines) what += `<ul class="cl">${c.lines.map(l => `<li>${esc(l)}</li>`).join('')}</ul>`;
  const sent = {answer: 'Sent', message: 'Sent', 'review-note': 'Sent', approve: 'Approved', comment: 'Sent', close: 'Closed', reopen: 'Reopened', issue: 'Opened', merge: 'Merged', github: 'Done'}[c.do] || 'Applied';
  const go = {answer: 'Send', message: 'Send', 'review-note': 'Send', approve: 'Approve', comment: 'Send', merge: 'Merge', close: 'Close', reopen: 'Reopen', issue: 'Open it', github: 'Do it'}[c.do] || (c.new ? 'Create it' : 'Apply');
  const doing = {answer: 'Sending', message: 'Sending', 'review-note': 'Sending', approve: 'Approving', comment: 'Sending', merge: 'Merging', close: 'Closing', reopen: 'Reopening', issue: 'Opening it', start: 'Starting', github: 'Doing it'}[c.do] || (c.new ? 'Creating it' : 'Applying');
  const busy = c.state === 'working' || CHAT.applying[m.id] && (!c.state || c.state === 'failed');
  // what its work did, while it works and once it is done: a line each, the one under way last
  const done = (c.done || []).map(l => `<li class="${/…$/.test(l) ? (c.state === 'working' ? 'now' : 'cut') : / is not merged: /.test(l) ? 'no' : 'yes'}">${esc(l)}</li>`).join('');
  const acts = busy ? ((c.done || []).some(l => /…$/.test(l)) ? '' : `<div class="cbusy" role="status"><span class="spin"></span>${doing}…</div>`) // a line under way says it
    : c.state === 'applied' ? `<div class="ok">${I.check}${sent}${c.once ? '' : ` <button class="link" data-cundo="${esc(m.id)}">Undo</button>`}</div>`
    : c.state === 'partly' ? `<div class="cpart">Merged in part: see above</div>`
    : c.state === 'stopped' ? `<div class="small">Stopped when rota did: GitHub shows what was merged.</div>`
    : c.state && c.state !== 'failed' ? `<div class="small">${esc(c.state === 'undone' ? 'Undone' : 'Cancelled')}</div>`
    : `<div class="acts"><button class="btn p" data-capply="${esc(m.id)}">${c.state === 'failed' ? 'Try again' : go}</button><button class="btn" data-cno="${esc(m.id)}">Cancel</button></div>`;
  return `<div class="chg"><div class="f">${esc(c.title || '')}</div>${what}${c.why ? `<div class="small">${esc(c.why)}</div>` : ''}${done ? `<ul class="cd">${done}</ul>` : ''}${acts}</div>`;
}
// applyCard: a card tapped shows at once that it is being done, until the server says how it stands
async function applyCard(id) {
  if (CHAT.applying[id]) return;
  CHAT.applying[id] = true;
  render();
  const said = await post({action: 'chat-apply', arg: id});
  if (said) toast(said);
  CHAT.tag = null;
  await loadChat();
  delete CHAT.applying[id];
  render();
}
// chatBody: the conversation, each run of one sender's lines under its name, as a group chat shows them
function chatBody() {
  if (!CHAT.msgs.length) return `<div class="sys">${CHAT.team ? 'Ask the Team what anyone is doing, or tell it what to change. ' : ''}Write @ and an agent’s name to reach it: online, it answers here; offline, it reads it at its next run.</div>`;
  let h = '', prev = null;
  for (const m of CHAT.msgs) {
    if (!m.from) { prev = null; h += `<div class="sys">${m.html}</div>`; continue; }
    if (m.from === 'you') { prev = 'you'; h += `<div class="bub me">${m.to ? `<span class="small">@${esc(m.to)}</span> ` : ''}${m.html}</div>`; continue; }
    const first = prev !== m.from; prev = m.from;
    h += `<div class="grp${first ? '' : ' cont'}">${first ? av(m.from, 26) : '<span class="avsp"></span>'}<div class="col">${first ? `<div class="nm">${m.from === 'team' ? 'Team' : esc(m.from)} · ${esc(when(m.at))}</div>` : ''}${m.html ? `<div class="bub mb">${m.html}</div>` : ''}${m.change ? changeCard(m) : ''}</div></div>`;
  }
  if (CHAT.busy === 'team') h += `<div class="grp">${av('team', 26)}<div class="col"><div class="nm">Team</div><div class="typing"><i></i><i></i><i></i></div></div></div>`;
  return h;
}
function chatChips() {
  if (CHAT.msgs.some(m => m.from === 'you')) return '';
  const chips = ['What is everyone doing?', 'Who needs me, and why?', 'What did the team do today?'];
  if (CHAT.here[0]) chips.push(`@${CHAT.here[0]} what are you doing?`);
  return `<div class="cwc">${chips.map(c => `<button data-csay="${esc(c)}">${esc(c)}</button>`).join('')}</div>`;
}
// mentions: after @, the agents to name, the online ones first, each online or offline; once one is
// named, where what is written goes: to it here, or to its mail
function mentionsHTML() {
  const text = S.vals.chat || '', m = text.match(/(?:^|\s)@([A-Za-z0-9_.-]*)$/);
  if (m) {
    const p = m[1].toLowerCase();
    return CHAT.agents.filter(x => x.name.toLowerCase().startsWith(p)).slice(0, 6).map(x => `<button data-mention="${esc(x.name)}">${av(x.name, 18)}@${esc(x.name)} <span class="${x.online ? 'pon' : 'small'}">${x.online ? 'online' : 'offline · ' + esc(x.note)}</span></button>`).join('');
  }
  const to = text.match(/(?:^|\s)@([A-Za-z0-9_][A-Za-z0-9_.-]*)/), a = to && CHAT.agents.find(x => x.name === to[1]); // as the server reads it: the name as it is
  if (!a) return '';
  return a.online ? `<div class="cwhint on">${esc(a.name)} is online: it answers here.</div>`
    : `<div class="cwhint">${esc(a.name)} is offline, ${esc(a.note)}. It reads this then.</div>`;
}
function chatPane(tab) {
  const w = CHAT.here;
  return `<div class="cwh"${tab ? '' : ' data-cmin'}>${av('team', 30, true)}<div class="tt"><b>Team</b><span class="here">${w.length ? `<span class="ppl">${w.map(n => av(n, 18)).join('')}</span>` : ''}${esc(hereText())}</span></div>
    ${tab ? `<button class="ib" data-view="team" title="Everyone on the team">${I.team}</button>` : `<button class="ib" data-cmin title="${S.tc.min ? 'Open' : 'Minimize'}">${I.minus}</button><button class="ib" data-cclose title="Close">${I.x}</button>`}</div>
    ${presenceRow()}<div class="cwb" data-cbody>${chatBody()}</div>${chatChips()}<div class="cwc" id="mentions">${mentionsHTML()}</div>
    <div class="cwi"><textarea id="chatin" rows="1" placeholder="${CHAT.team ? 'Message the Team, or @ an agent' : '@ an agent'}" aria-label="Message the team">${esc(S.vals.chat || '')}</textarea><button class="ib" data-csend aria-label="Send">${I.send}</button></div>`;
}
// openChat: the dock on the desktop, the Chat tab on the phone; what was typed comes along
function openChat(prefill) {
  if (prefill !== undefined) S.vals.chat = prefill;
  if (desk()) S.tc = {open: true, min: false}; else { S.ptab = 'chat'; S.open = null; }
  if (searching()) { S.q = null; S.f = {}; S.keepRead.clear(); } // as search's own X: its filters were the search's
  S.drawer = false; render(); chatToEnd();
  const el = document.getElementById('chatin');
  if (el && desk()) { el.focus(); el.setSelectionRange(el.value.length, el.value.length); }
}
async function chatSend(text) {
  text = (text || '').trim();
  if (!text) return;
  S.vals.chat = '';
  const id = 'local' + (++CHAT.local), msg = {id, local: true, text, from: 'you', html: `<p>${esc(text)}</p>`, at: new Date().toISOString()};
  CHAT.msgs.push(msg);
  if (!/(?:^|\s)@(?!team\b)[A-Za-z0-9_]/i.test(text) && CHAT.team) CHAT.busy = 'team';
  render(); chatToEnd();
  const said = await post({action: 'chat', text});
  CHAT.msgs = CHAT.msgs.filter(m => m.id !== id); // the server's copy takes its place
  if (said === null) { if (!S.vals.chat) S.vals.chat = text; CHAT.busy = ''; render(); return; } // a newer draft stays
  CHAT.tag = null; loadChat();
}
// ---------- notifications on this device ----------
// push: where this device stands: unknown until asked, insecure (open the HTTPS address), unsupported
// (on an iPhone: from the Home Screen), on or off
const push = {state: 'unknown', reg: null, info: null, looking: false};
function pushText() {
  const https = push.info && push.info.https;
  return {unknown: 'Looking…', insecure: https ? `Open <a class="link" href="${esc(https)}/">${esc(https)}</a> to turn them on: a phone allows notifications only over HTTPS.` : 'Notifications need these pages over HTTPS.',
    unsupported: 'On an iPhone: add these pages to your Home Screen (Share, Add to Home Screen), open them from there, and turn notifications on here.',
    on: 'On: this device is told when an agent needs you.', off: 'Off for this device.', blocked: 'Blocked for these pages in the browser’s settings.',
    unavailable: 'Not available: ' + esc((push.info && push.info.error) || 'rota has no key to sign them with')}[push.state] || esc(push.state);
}
function pushButtons() {
  if (push.state === 'off') return `<div class="acts"><button class="btn p" data-push="on">Turn on</button></div>`;
  if (push.state === 'on') return `<div class="acts"><button class="btn" data-push="test">Send a test</button><button class="btn" data-push="off">Turn off</button></div>`;
  return '';
}
async function pushLook() {
  if (push.looking) return;
  push.looking = true;
  try {
    push.info = await (await fetch('/api/push', {cache: 'no-store'})).json();
    if (!window.isSecureContext) push.state = 'insecure';
    else if (!('serviceWorker' in navigator) || !('PushManager' in window)) push.state = 'unsupported';
    else if (!push.info.key) push.state = 'unavailable';
    else {
      push.reg = push.reg || await navigator.serviceWorker.register('/sw.js');
      push.state = (await push.reg.pushManager.getSubscription()) ? 'on' : 'off';
    }
  } catch (e) { push.state = 'Notifications: ' + (e.message || e); }
  push.looking = false;
  if (S.view === 'settings') render();
}
async function pushPost(path, data) {
  const r = await fetch(path, {method: 'POST', body: new URLSearchParams({...data, token: TOKEN})});
  if (r.status === 403) { setTimeout(() => location.reload(), 900); throw new Error('rota restarted: reloading…'); } // the page's token is old
  if (!r.ok) throw new Error((await r.text()) || r.statusText);
  return r;
}
async function pushDo(act) {
  try {
    if (act === 'on') {
      if (await Notification.requestPermission() !== 'granted') { push.state = 'blocked'; render(); return; }
      const key = push.info.key.replace(/-/g, '+').replace(/_/g, '/');
      const bytes = Uint8Array.from(atob(key + '==='.slice((key.length + 3) % 4)), c => c.charCodeAt(0));
      const sub = await push.reg.pushManager.subscribe({userVisibleOnly: true, applicationServerKey: bytes});
      try { await pushPost('/push/subscribe', {endpoint: sub.endpoint, device: navigator.userAgent}); } catch (e) { await sub.unsubscribe(); throw e; } // on only when rota has it
      push.state = 'on';
    } else if (act === 'off') {
      const sub = await push.reg.pushManager.getSubscription();
      if (sub) { await pushPost('/push/unsubscribe', {endpoint: sub.endpoint}); await sub.unsubscribe(); }
      push.state = 'off';
    } else {
      toast(await (await pushPost('/push/test', {})).text());
    }
  } catch (e) { toast('Notifications: ' + (e.message || e)); }
  render();
}

const KEYS = [['j / k', 'Next / previous message or issue'], ['e', 'Archive'], ['s', 'Star'], ['b', 'Snooze'], ['r', 'Reply or answer'], ['c', 'New message'], ['/', 'Search'], ['Esc', 'Back'], ['⌘ S', 'Save changes'], ['← → ↑ ↓', 'Move the divider or the chat’s edge the keyboard is on'], ['?', 'These shortcuts']];
function keysPage() { return `<div class="ltitle">Keyboard shortcuts</div><div class="keys">${KEYS.map(([k, l]) => `<span class="kbd">${k}</span><span>${l}</span>`).join('')}</div>`; }

// ---------- search ----------
// the filters: one button opens them; only those that are on show, each a pill that takes it off
const filterWords = {from: v => v === 'team' ? 'From Team' : 'From ' + v, kind: v => (KINDS.find(k => k[0] === v) || [v, v])[1],
  repo: v => (repoOf(v) || {}).name || v, date: v => (DATES.find(x => x[0] === v) || [v, v])[1], unread: () => 'Unread', star: () => 'Starred'};
function filterBtn() {
  const n = Object.values(S.f).filter(Boolean).length;
  return `<button class="ib fbtn${n ? ' on' : ''}" data-filters title="Filter" aria-label="Filter${n ? `, ${n} on` : ''}">${I.filter}</button>`;
}
function activeFilters() {
  if (!filtering()) return '';
  const on = ['unread', 'star', 'from', 'kind', 'repo', 'date'].filter(k => S.f[k]);
  return `<div class="factive">${on.map(k => `<button class="fpill" data-unfilter="${k}" aria-label="Take off ${esc(filterWords[k](S.f[k]))}">${esc(filterWords[k](S.f[k]))}${I.x}</button>`).join('')}${on.length > 1 ? '<button class="fclear" data-clearf>Clear</button>' : ''}</div>`;
}
function filterSheet() {
  const c = S.f, mark = x => x ? '✓' : '';
  const opts = [['Unread', mark(c.unread), () => setFilter('unread', !c.unread)], ['Starred', mark(c.star), () => setFilter('star', !c.star)],
    ['From', c.from ? filterWords.from(c.from).slice(5) : 'Anyone', () => chooseFilter('from')], ['Type', c.kind ? filterWords.kind(c.kind) : 'Any', () => chooseFilter('kind')]];
  if ((M.repos || []).length > 1) opts.push(['Repository', c.repo ? filterWords.repo(c.repo) : 'Any', () => chooseFilter('repo')]);
  opts.push(['Date', c.date ? filterWords.date(c.date) : 'Any time', () => chooseFilter('date')]);
  if (filtering()) opts.push(['Clear filters', '', () => { S.f = {}; S.keepRead.clear(); S.sel.clear(); render(); }]);
  sheet('Filter', opts, filterAt(''));
}
function chooseFilter(k) {
  const any = l => [l, '', () => setFilter(k, null)];
  const [title, opts] = k === 'from' ? ['From', [any('Anyone'), ...M.members.map(m => [m.name, STATE[m.state], () => setFilter('from', m.name)]), ['Team', 'the daily reports, and rota', () => setFilter('from', 'team')]]] :
    k === 'kind' ? ['Type', [any('Any type'), ...KINDS.map(([v, l]) => [l, '', () => setFilter('kind', v)])]] :
    k === 'repo' ? ['Repository', [any('Any repository'), ...M.repos.map(r => [r.name, r.repo, () => setFilter('repo', r.repo)])]] :
    ['Date', [any('Any time'), ...DATES.map(([v, l]) => [l, '', () => setFilter('date', v)])]];
  sheet(title, opts, filterAt(''));
}
// askTeam: the question typed in search, for the Team in the chat
function askTeam() {
  const q = (S.q || '').trim();
  return q && CHAT.team ? `<button class="askteam" data-askteam>${av('team', 32, true)}<span>Ask the team: “${esc(q)}”</span></button>` : '';
}
function searchStart() {
  return `<div class="sgrp"><div class="lab">Team</div><div class="people">${M.members.map(m => `<button data-from="${esc(m.name)}">${av(m.name, 44, true)}${esc(m.name)}</button>`).join('')}</div>
    <div class="lab">Try</div><p class="small" style="margin:0">Words, <b>#673</b>, an agent’s name, or <b>from:developer is:unread is:review is:todo repo:creator</b>.</p></div>`;
}

// ---------- frames ----------
const PAGES = [['/reports/', 'Daily reports']]; // the documents the team writes for you, kept as pages
function nav() {
  const folders = [['inbox', 'Inbox', I.inbox, unreadOf(inbox()) || ''], ['reviews', 'Reviews', I.pr, M.threads.filter(t => t.pr && t.pr.state === 'review').length || ''],
    ['waiting', 'Waiting', I.wait, M.threads.filter(t => t.box === 'waiting' && live(t)).length || ''],
    ['starred', 'Starred', I.star, ''], ['snoozed', 'Snoozed', I.clock, M.threads.filter(t => t.snooze).length || ''], ['sent', 'Sent', I.send, ''], ['all', 'All messages', I.mail, ''], ['issues', 'Issues', I.doc, '']];
  let h = folders.map(([v, l, ic, c]) => `<button class="nv${S.view === v && !searching() ? ' on' : ''}" data-view="${v}">${ic}${l}<span class="c">${c}</span></button>`).join('');
  h += `<button class="nh nhb${S.view === 'team' ? ' on' : ''}" data-view="team">Team</button>` + [...M.members].sort((a, b) => ORDER[a.state] - ORDER[b.state]).map(m =>
    `<button class="nv${S.view === 'agent:' + m.name ? ' on' : ''}" data-view="agent:${esc(m.name)}" title="${esc(m.line)}">${av(m.name, 24, true)}${esc(m.name)}<span class="c dim">${esc(STATE[m.state] || '')}</span></button>`).join('');
  if ((M.repos || []).length) h += `<div class="nh">Repositories</div>` + M.repos.map(r => `<button class="nv${S.view === 'repo:' + r.repo ? ' on' : ''}" data-view="repo:${esc(r.repo)}"><span class="dotc" style="--c:${r.color}"></span>${esc(r.name)}</button>`).join('');
  h += `<div class="nsep"></div>` + PAGES.map(([href, l]) => `<a class="nv" href="${href}">${I.doc}${l}</a>`).join('');
  h += `<button class="nv${S.view === 'settings' ? ' on' : ''}" data-view="settings">${I.gear}Settings</button>`;
  return h;
}
function tabsHTML(cls) {
  const s = split(), u = unreadOf(updates()), f = filtering(); // with filters, each tab counts what they let through
  if (f) [s.focused, s.other] = [s.focused.filter(passes), s.other.filter(passes)];
  const n3 = f ? updates().filter(passes).length : u;
  return `<div class="tabrow tabrow-${cls}"><div class="${cls}" role="tablist">${[['focused', 'Focused', s.focused.length, 0], ['other', 'Other', s.other.length, 0], ['updates', 'Updates', n3, !f]].filter(x => M.prefs.split || x[0] !== 'other')
    .map(([v, l, n, isNew]) => `<button role="tab" class="${S.tab === v ? 'on' : ''}" data-tab="${v}">${v === 'focused' && !M.prefs.split ? 'Inbox' : l}${n ? `<span class="n${isNew ? ' new' : ''}">${n}</span>` : ''}</button>`).join('')}</div>${filterBtn()}</div>`;
}
function statusMini() {
  const w = M.members.find(m => m.state === 'working');
  return w ? av(w.name, 32, true) : av('team', 32, true);
}
function phoneHTML() {
  const sel = S.sel.size;
  let h = `<div class="app phone">`;
  if (sel) h += `<div class="selbar"><button class="ib" data-clearsel aria-label="Clear">${I.x}</button><b>${sel} selected</b><button class="ib" data-bulk="archive" aria-label="Archive">${I.archive}</button><button class="ib" data-bulk="snooze" aria-label="Snooze">${I.clock}</button><button class="ib" data-bulk="read" aria-label="Mark read">${I.mail}</button></div>`;
  else if (searching()) h += `<div class="pbar"><div class="spill"><button class="ib" data-endsearch aria-label="Back">${I.back}</button><input id="q" value="${esc(S.q || '')}" placeholder="Search messages" autocomplete="off" enterkeyhint="search">${S.q ? `<button class="ib" data-clearq aria-label="Clear">${I.x}</button>` : ''}${filterBtn()}</div></div>`;
  else h += `<div class="pbar"><div class="spill"><button class="ib" data-drawer aria-label="Menu">${I.menu}</button><span class="ph" data-search>Search messages</span><button class="ib" data-view="team" aria-label="${esc(M.top.State)}" style="width:44px">${statusMini()}</button></div></div>`;
  const chatTab = S.ptab === 'chat' && !searching();
  if (!searching() && S.view === 'inbox' && !chatTab) h += tabsHTML('tabs3');
  h += `<div class="list">${searching() && searchEmpty() ? searchStart() : chatTab ? '' : listBody()}</div>`;
  if (chatTab) h += `<div class="cfull tab">${chatPane(true)}</div>`;
  if (!searching() && !sel && !pageView() && S.view !== 'keys' && !chatTab) h += `<button class="fab" data-compose>${I.pen}Message</button>`; // only over the mail: an agent's page has its own, and it would cover a setting
  if (!searching() && !sel) h += `<div class="pfoot">${saveBar()}<nav class="bnav">${[['mail', 'Mail', I.mail], ['chat', 'Chat', I.chat]].map(([v, l, ic]) => `<button class="${S.ptab === v ? 'on' : ''}" data-ptab="${v}"><span class="pill">${ic}</span>${l}${v === 'chat' && CHAT.busy ? '<span class="dot"></span>' : ''}</button>`).join('')}</nav></div>`;
  else { barWas = ''; document.body.classList.remove('unsaved'); } // no bar under a search or a selection
  h += `</div>`;
  if (S.open) { const [tools, body] = paneOf(S.open, true); h += `<div class="over${S.open !== shownOpen ? ' slide' : ''}" id="over"><div class="tbar">${tools}</div>${body}</div>`; }
  h += `<div class="scrim${S.drawer ? ' open' : ''}" data-closedrawer></div><nav class="drawer${S.drawer ? ' open' : ''}" aria-label="Folders"><div class="brand"><img class="mk" src="/mark.png" alt="">rota</div>${nav()}</nav>`;
  return h;
}
function deskHTML() {
  const wide = !searching() && (['settings', 'keys'].includes(S.view) || S.view.startsWith('run:')); // search results open in the reading pane
  const [tools, body] = S.open ? paneOf(S.open, false) : [];
  const rp = S.open ? `<div class="tbar">${tools}</div>${body}` : S.view === 'issues' && !searching() ? `<div class="empty"><b>No issue selected</b>Choose one to read it here, or press <span class="kbd">j</span>.</div>` : `<div class="empty"><b>No message selected</b>Choose one, or press <span class="kbd">j</span>.</div>`;
  const sel = S.sel.size;
  return `<div class="desk">
   <div class="gbar"><div class="brand"><img class="mk" src="/mark.png" alt="">rota</div>
     <div class="gsearch${searching() ? ' on' : ''}"><div class="box2"><span class="ib">${I.search}</span><input id="dq" value="${esc(S.q || '')}" placeholder="Search messages, agents and #numbers" autocomplete="off">${searching() ? `${filterBtn()}<button class="ib" data-endsearch aria-label="Close search">${I.x}</button>` : ''}</div>
       ${searching() && searchEmpty() ? `<div class="gdrop">${searchStart()}</div>` : ''}</div>
     <div class="gright"><button class="gstat" data-view="team" title="${esc(M.top.Title)}">${statusMini()}<span>${esc(M.top.State)}</span></button>${M.top.Budget ? `<button class="gbud" data-view="team" title="The team's budget">${esc(M.top.Budget)}<b><i style="width:${M.top.Fill}%"></i></b></button>` : ''}<button class="ib${S.tc.open ? ' on' : ''}" data-chat title="Team chat">${I.chat}</button><button class="ib" data-view="settings" title="Settings">${I.gear}</button></div></div>
   <div class="cols${wide ? ' full' : ''}">
     <nav class="side" aria-label="Folders"><button class="compose-btn" data-compose>${I.pen}New message</button>${nav()}</nav>
     <section class="lp">${S.view === 'inbox' && !searching() ? `<div class="lhead">${tabsHTML('ltabs')}${sel ? `<div class="ltool"><button class="ib" data-clearsel aria-label="Clear">${I.x}</button><b>${sel} selected</b><span class="sp"></span><button class="ib" data-bulk="archive" title="Archive">${I.archive}</button><button class="ib" data-bulk="snooze" title="Snooze">${I.clock}</button><button class="ib" data-bulk="read" title="Mark read">${I.mail}</button></div>` : ''}</div>` : ''}${listBody()}${saveBar()}</section>
     ${wide ? '' : `<section class="rp">${rp}</section>`}${divider('side')}${wide ? '' : divider('list')}
   </div>${S.tc.open ? `<div class="dock"><div class="cw${S.tc.min ? ' min' : ''}">${S.tc.min ? '' : chatGrips()}${chatPane(false)}</div></div>` : ''}</div>`;
}
// ---------- the computer's panes and the chat: as wide as the human drags them, and the chat as tall ----------
// The divider between two panes drags, and so do the chat's left and top edges and the corner between
// them, as the chat sits in the window's corner. With the keyboard on a divider or an edge, the arrows
// move it (with Shift, further) and Home and End take it to its ends; a double click puts it back. The
// sizes are this browser's, kept in its storage, and the page lays them out within the window: the
// reading pane keeps READ, and the chat stays below the top bar.
const READ = 400; // px the reading pane keeps at least, as the style sheet's --sw and --lw say
const PANE = { // max: as the style sheet's --sw and --lw, and .cw's max-width and max-height, say
  side: {def: 256, min: 180, max: () => Math.min(420, innerWidth - PANE.list.min - READ), el: '.desk .side', label: 'Width of the folders'},
  list: {def: 440, min: 300, max: () => innerWidth - paneNow('side') - READ, el: '.desk .lp', label: 'Width of the list'},
  chatw: {def: 360, min: 320, max: () => innerWidth - 48, el: '.dock .cw:not(.min)', label: 'Width of the chat', keys: {ArrowLeft: 1, ArrowRight: -1}},
  chath: {def: 520, min: 320, max: () => innerHeight - 80, el: '.dock .cw:not(.min)', tall: true, label: 'Height of the chat', keys: {ArrowUp: 1, ArrowDown: -1}},
};
let W = {}, rz = null; // W: the sizes chosen, in px, by PANE's keys; rz: a divider or an edge being dragged
try { const w = JSON.parse(localStorage.getItem('rota.panes')); if (w && typeof w === 'object') for (const k in PANE) if (+w[k] > 0) W[k] = Math.max(PANE[k].min, +w[k]); } catch (e) { /* none kept, or no storage: the usual sizes */ }
const paneEl = k => document.querySelector(PANE[k].el);
const paneNow = k => { const el = paneEl(k), r = el && el.getBoundingClientRect(); return r ? Math.round(PANE[k].tall ? r.height : r.width) : W[k] || PANE[k].def; };
const paneMax = k => Math.max(PANE[k].min, PANE[k].max());
function divider(k) {
  const p = PANE[k], c = k.startsWith('chat') ? 'crz' : 'rz';
  return `<div class="${c} ${c}-${k}${rz && rz.k === k ? ' on' : ''}" id="rz-${k}" data-rz="${k}" role="separator" aria-orientation="${p.tall ? 'horizontal' : 'vertical'}" tabindex="0" aria-label="${p.label}" aria-valuemin="${p.min}" aria-valuemax="${Math.round(paneMax(k))}" title="Drag to change the ${p.tall ? 'height' : 'width'}; double-click to reset it"></div>`;
}
// chatGrips: the chat's corner, which drags both its edges, then its left and its top edge
const chatGrips = () => `<div class="crz crz-c${rz && rz.k === 'chat' ? ' on' : ''}" data-rz="chat" aria-hidden="true" title="Drag to change the size; double-click to reset it"></div>${divider('chatw')}${divider('chath')}`;
// paintPanes lays the panes and the chat out at the sizes chosen, and tells each divider and edge where it stands
function paintPanes() {
  const s = document.documentElement.style;
  for (const k in PANE) if (W[k]) s.setProperty('--pane-' + k, W[k] + 'px'); else s.removeProperty('--pane-' + k);
  for (const d of document.querySelectorAll('[role=separator][data-rz]')) { d.setAttribute('aria-valuenow', paneNow(d.dataset.rz)); d.setAttribute('aria-valuemax', Math.round(paneMax(d.dataset.rz))); }
}
function setPane(k, px) { W[k] = Math.round(Math.max(PANE[k].min, Math.min(paneMax(k), px))); paintPanes(); }
function keepPanes() { try { localStorage.setItem('rota.panes', JSON.stringify(W)); } catch (e) { /* this browser keeps them until the page reloads */ } }
document.addEventListener('pointerdown', e => {
  const d = e.target.closest && e.target.closest('[data-rz]');
  if (!d || e.button !== 0 || !desk()) return;
  e.preventDefault(); // the mouse selects no text on its way
  const k = d.dataset.rz, r = paneEl(k === 'chat' ? 'chatw' : k).getBoundingClientRect();
  rz = {k, id: e.pointerId, x: e.clientX, y: e.clientY, w: r.width, h: r.height}; // the chat grows from its corner: its right and bottom stay
  document.body.style.setProperty('--rzc', getComputedStyle(d).cursor); // read before body.resizing gives everything that cursor
  d.classList.add('on'); document.body.classList.add('resizing');
  try { d.setPointerCapture(e.pointerId); } catch (_) { /* the pointer is gone already: its next move ends the drag */ } // its release is heard outside the window too
});
addEventListener('pointermove', e => { // on the window: the page may be drawn anew under the mouse, and the divider with it
  if (!rz || e.pointerId !== rz.id) return;
  if (e.pointerType === 'mouse' && !(e.buttons & 1)) return dragEnd(e); // the button was let go where the page did not hear it
  const dx = e.clientX - rz.x, dy = e.clientY - rz.y;
  if (!rz.k.startsWith('chat')) return setPane(rz.k, rz.w + dx);
  if (rz.k !== 'chath') setPane('chatw', rz.w - dx);
  if (rz.k !== 'chatw') setPane('chath', rz.h - dy);
});
function dragEnd(e) {
  if (!rz || e.pointerId !== rz.id) return;
  rz = null; document.body.classList.remove('resizing'); document.body.style.removeProperty('--rzc');
  for (const d of document.querySelectorAll('[data-rz].on')) d.classList.remove('on');
  keepPanes();
}
addEventListener('pointerup', dragEnd); addEventListener('pointercancel', dragEnd);
document.addEventListener('dblclick', e => {
  const d = e.target.closest && e.target.closest('[data-rz]');
  if (!d) return;
  for (const k of d.dataset.rz === 'chat' ? ['chatw', 'chath'] : [d.dataset.rz]) delete W[k];
  paintPanes(); keepPanes();
});
// paneKey moves the divider or the chat's edge the keyboard is on; it says whether the key was its own
function paneKey(e) {
  const a = document.activeElement, k = a && a.dataset && a.dataset.rz, p = k && PANE[k];
  const step = p && (p.keys || {ArrowLeft: -1, ArrowRight: 1})[e.key];
  if (!p || !step && e.key !== 'Home' && e.key !== 'End') return false;
  e.preventDefault();
  setPane(k, e.key === 'Home' ? p.min : e.key === 'End' ? paneMax(k) : paneNow(k) + step * (e.shiftKey ? 64 : 16));
  keepPanes();
  return true;
}
function composeHTML() {
  const c = S.compose;
  if (!c) return '';
  return `<div class="compose open" id="compose" role="dialog" aria-label="New message"><div class="ch"><button class="ib" data-closecompose aria-label="Close">${I.x}</button><b>New message</b><button class="btn p" data-sendcompose>${I.send}Send</button></div>
    <div class="cf"><label>To</label>${c.to ? `<button class="tochip" data-to="">${av(c.to, 24)}${esc(c.to)} ×</button>` : `<span class="small">Choose an agent below</span>`}</div>
    ${c.to ? '' : `<div class="sugg">${M.members.map(m => `<button data-to="${esc(m.name)}">${av(m.name, 24, true)}${esc(m.name)}</button>`).join('')}</div>`}
    <div class="cf"><label>About</label><input id="cabout" placeholder="#number (optional)" value="${esc(S.vals.cabout || '')}"></div>
    <textarea id="ctext" placeholder="What should ${esc(c.to || 'the agent')} do or know? It reads this at its next step.">${esc(S.vals.ctext || '')}</textarea></div>`;
}

let shownOpen = null; // the thread the phone showed last: it slides in only when it opens
const FOLDERS = ['inbox', 'reviews', 'waiting', 'starred', 'snoozed', 'sent', 'all', 'issues']; // the mail's
// route opens the page the address names: #team, #settings, #agent:<name>, #run:<id>
function route() {
  let h = '';
  try { h = decodeURIComponent(location.hash.slice(1)); } catch (e) { return; } // not an address of the app's
  if (FOLDERS.includes(h)) { // a folder of the mail, from an old page's link
    if (!leaveOK()) { writeStep(false, placeNow(), shown); return; }
    history.replaceState(null, '', location.pathname); S.view = h; S.open = null; S.ptab = 'mail'; S.q = null; S.f = {}; S.keepRead.clear(); S.sel.clear();
    S.roleDraft = null; S.adv = null; S.edits = {}; S.weights = Object.create(null);
    if (h === 'issues') loadIssues();
    render(); return;
  }
  const page = h === 'team' || h === 'settings' || h === 'shares' || /^agent:[A-Za-z0-9_.-]+$/.test(h) || /^run:\d{4}-\d\d-\d\d\/\d{4}-[A-Za-z0-9_.-]+$/.test(h);
  if (!page && h === '' && pageView()) h = 'inbox'; // Back from a page to the mail
  else if (!page || h === S.view) return;
  if (!leaveOK()) { writeStep(false, placeNow(), shown); return; }
  if (h.startsWith('agent:') && !S.view.startsWith('run:')) S.aTab = 'messages';
  S.view = h; S.open = null; S.q = null; S.roleDraft = null; S.adv = null; S.edits = {}; S.weights = Object.create(null); TEAM.error = '';
  loadPage(); render();
}
// ---------- the browser's history ----------
// Each folder, page and message the human opens is a step in the browser's history, so the browser's
// own back and forward go through them: on an iPhone a swipe from the screen's left edge, also in the
// app opened from the Home Screen; on a computer its back button. A page's step has it in the address,
// so a reload or a link opens it again. Another message opened while one is, as j and k do, takes the
// open one's place in its step, so back goes to the list. Going where the step came from, as a
// message's back arrow does, is the browser's back, so its forward comes back. The browser loads a step
// anew once it no longer keeps the page, as after a few minutes on a review, and the start shows the
// step's folder and message again.
const placeNow = () => ({view: S.view, tab: S.tab, open: S.open || null});
const samePlace = (a, b) => !!a && !!b && a.view === b.view && a.tab === b.tab && (a.open || null) === (b.open || null);
const stepOf = st => st && typeof st.view === 'string' ? st : null; // what the app wrote in a step, or null
const pageAt = v => v === 'team' || v === 'shares' || v === 'settings' || v.startsWith('agent:') || v.startsWith('run:');
let shown = null, placeT = 0, backing = false; // shown: the place last drawn; placeT: its step's change, waiting, as the browser allows only so many at once; backing: the app went back itself
// writeStep writes a place in a new step, or in the one shown; from: the place of the step before it
function writeStep(push, p, from) {
  const st = {view: p.view, tab: p.tab, open: p.open || null, from: from ? {view: from.view, tab: from.tab, open: from.open || null} : null};
  try { history[push ? 'pushState' : 'replaceState'](st, '', pageAt(p.view) ? '#' + p.view : location.pathname + location.search); } catch (e) { /* the browser allows only so many at once */ }
}
// flushPlace writes the place shown in its step now, as before the page is left
function flushPlace() {
  clearTimeout(placeT); placeT = 0;
  const cur = stepOf(history.state);
  if (shown && !samePlace(cur, shown)) writeStep(false, shown, cur && cur.from);
}
document.addEventListener('click', e => { if (e.target.closest && e.target.closest('a[href]')) flushPlace(); }, true); // a link may leave the page
// keepPlace, after each drawing: a folder, a page or a message opened is a new step; going where the
// step came from is the browser's back; anything else changes the step, a little later
function keepPlace() {
  const want = placeNow(), was = shown, cur = stepOf(history.state);
  if (samePlace(was, want)) return;
  shown = want;
  if (!was) { clearTimeout(placeT); placeT = 0; writeStep(false, want, samePlace(cur, want) ? cur.from : null); return; } // the start: the step it came to
  if (!cur) { writeStep(false, want, was); return; } // a step the browser made for an address typed in, after the one shown
  const keep = () => { if (!placeT) return; clearTimeout(placeT); placeT = 0; if (!samePlace(cur, was)) writeStep(false, was, cur.from); }; // the step left keeps what it showed
  if (samePlace(cur.from, want)) { keep(); backing = true; history.back(); return; }
  if (was.view !== want.view || !was.open && want.open) { keep(); writeStep(true, want, was); return; }
  if (!placeT) placeT = setTimeout(flushPlace, 400);
}
// showStep shows the step the browser went to
function showStep(p) {
  if (p.view !== S.view) setView(p.view);
  S.tab = p.tab || S.tab; S.open = p.open || null; S.reply = false; S.wrong = null; S.autoOpen = false; S.drawer = false; S.ptab = 'mail';
  shownOpen = S.open; // shown at once, with no slide: the browser's back or forward is the move
  shown = placeNow();
  render();
}
// backTo goes back when the step came from the view, as Escape does from the keys or the settings
function backTo(view) {
  const f = (stepOf(history.state) || {}).from;
  if (!f || f.view !== view) return false;
  history.back(); return true;
}
window.addEventListener('popstate', e => {
  clearTimeout(placeT); placeT = 0;
  const p = stepOf(e.state);
  if (backing) { backing = false; shown = placeNow(); if (p && !samePlace(p, shown)) writeStep(false, shown, p.from); return; } // the app went back itself: the step shows what the app does
  if (!p) return; // a step the app did not write, as an address typed in: route() reads it
  if (!M) { S.view = p.view; S.tab = p.tab || S.tab; S.open = p.open || null; S.autoOpen = false; return; } // drawn once the mail is read
  if (p.view !== S.view && !leaveOK()) { writeStep(true, shown, p); return; } // what is unsaved stays, if the human says so
  showStep(p);
});
// a page the browser kept and shows again, as by its forward after the app went back to a step the
// page was loaded anew for, shows the step it is on
window.addEventListener('pageshow', e => {
  if (!e.persisted) return;
  backing = false; clearTimeout(placeT); placeT = 0;
  const p = stepOf(history.state);
  if (M && p && !samePlace(p, shown)) showStep(p);
});
window.addEventListener('hashchange', route);
function render() {
  if (!M) return;
  if (S.autoOpen && desk() && !S.open && S.view === 'inbox' && !searching()) { const l = listFor().rows; if (l[0]) S.open = l[0].id; }
  S.autoOpen = false;
  const t = S.open && find(S.open);
  if (S.open && !t && !issueOf(S.open)) S.open = null;
  if (isIssue(S.open) && !ISS.got.has(S.open) && !ISS.err.has(S.open)) loadIssue(S.open); // opened by a link or a reload
  if (t && t.unread && !document.hidden) { t.unread = false; if (S.f.unread) S.keepRead.add(t.id); post({action: 'read', id: t.id}); } // seen, not merely open in a hidden tab
  const fid = document.activeElement && document.activeElement.id, keepQ = ['q', 'dq'].includes(fid);
  const ae = document.activeElement, caret = ae && typeof ae.selectionStart === 'number' ? [ae.selectionStart, ae.selectionEnd, ae.scrollTop] : null; // where the human types
  const fkey = document.activeElement && document.activeElement.dataset ? document.activeElement.dataset.filters : undefined; // a filter button the keyboard is on, which the page draws anew
  const keep = {};
  for (const sel of ['.lp', '.rp', '#over', '.side']) { const el = document.querySelector(sel); if (el) keep[sel] = el.scrollTop; }
  const winY = window.scrollY, sameOpen = S.open === shownOpen;
  const cb = document.querySelector('[data-cbody]'), chatTop = cb && cb.scrollTop, chatEnd = cb && cb.scrollHeight - cb.scrollTop - cb.clientHeight < 32; // the chat: read at its end, which new lines move, or further up
  document.getElementById('root').innerHTML = (desk() ? deskHTML() : phoneHTML()) + composeHTML();
  for (const sel in keep) { const el = document.querySelector(sel); if (el && (sel !== '.rp' && sel !== '#over' || sameOpen)) el.scrollTop = keep[sel]; }
  const nb = document.querySelector('[data-cbody]'); if (nb) nb.scrollTop = !cb || chatEnd ? nb.scrollHeight : chatTop; // one that appears anew opens at its end
  if (!desk() && !S.open) window.scrollTo(0, winY);
  if (keepQ || S.focusSearch) { const q = document.getElementById(desk() ? 'dq' : 'q'); if (q) { q.focus(); q.setSelectionRange(q.value.length, q.value.length); } S.focusSearch = false; }
  else if (fid && /^(rtext|ctext|cabout|repoin|roletext|chatin|f-[a-z_]+|w-[\w.-]+|rz-(?:side|list|chatw|chath))$/.test(fid)) { const el = document.getElementById(fid); if (el) { el.focus({preventScroll: true}); if (caret && el.setSelectionRange && el.type !== 'number') { el.setSelectionRange(Math.min(caret[0], el.value.length), Math.min(caret[1], el.value.length)); el.scrollTop = caret[2]; } } }
  else if (fkey !== undefined) { const b = filterAt(fkey); if (b) b.focus({preventScroll: true}); }
  bindSwipe();
  if (desk()) paintPanes();
  shownOpen = S.open;
  keepPlace();
  const n = unreadOf(inbox());
  document.title = n ? `(${n}) rota` : 'rota';
}

// ---------- talking to the server ----------
// load reads the mail again: at most every few seconds when the team folder keeps changing, as it
// does while a shift runs; the server answers 304 when the mail is the same
let lastLoad = 0, loadTimer = null, lastTag = null;
function reload() {
  const wait = lastLoad + 4000 - Date.now();
  if (wait <= 0) { load(); return; }
  if (!loadTimer) loadTimer = setTimeout(() => { loadTimer = null; load(); }, wait);
}
async function load() {
  lastLoad = Date.now();
  try {
    const r = await fetch('/api/mail', {cache: 'no-cache'});
    if (!r.ok) throw new Error(await r.text());
    const tag = r.headers.get('ETag'); // the browser answers a 304 with the mail it kept: nothing to draw again
    if (pageView()) loadPage(); // the team's state changes when the mail does not
    loadChat();
    if (tag && tag === lastTag && M) return;
    lastTag = tag;
    const busy = typing();
    M = await r.json();
    if (S.ask) { // opened from a notification: the newest open question
      S.ask = false;
      const q = M.threads.filter(t => t.qid).sort((a, b) => b.at.localeCompare(a.at))[0];
      if (q) { S.open = q.id; S.autoOpen = false; }
      history.replaceState(null, '', '/');
    }
    if (!busy || !S.loaded) render();
    else { S.due = true; lastTag = null; } // drawn once the human stops typing, whatever the next tag
    S.loaded = true;
  } catch (e) {
    if (!M) document.getElementById('root').innerHTML = `<div class="empty"><b>The team’s mail did not load</b>${esc(e.message || e)}</div>`;
  }
}
// post does one thing on the server: it returns what the server said, or null when that failed,
// after it told the human why and read the mail again
async function post(data) {
  try {
    const r = await fetch('/api/act', {method: 'POST', body: new URLSearchParams({...data, token: TOKEN})});
    const j = await r.json().catch(() => ({error: r.statusText}));
    if (r.ok) return j.said || '';
    if (r.status === 403) { // rota restarted, as it does when a new one is installed: the page's token is old
      toast('rota restarted: reloading…');
      if (S.open) location.hash = 'open=' + encodeURIComponent(S.open);
      setTimeout(() => location.reload(), 900);
      return null;
    }
    toast(j.error || 'That did not work');
  } catch (e) {
    toast('The team’s pages did not answer: ' + (e.message || e));
  }
  lastTag = null; // what the server has wins over what this page assumed
  load();
  return null;
}

// ---------- toast, sheet ----------
let tt;
function toast(msg, undo, label = 'Undo') {
  const el = document.getElementById('toast');
  el.innerHTML = `<span>${esc(msg)}</span>${undo ? `<button id="undo">${esc(label)}</button>` : ''}`;
  el.classList.add('open');
  if (undo) document.getElementById('undo').onclick = () => { undo(); el.classList.remove('open'); render(); };
  clearTimeout(tt); tt = setTimeout(() => el.classList.remove('open'), 5000);
}
// sheet opens a menu: on a phone from the bottom; on a computer, given the button it is for, as a
// dropdown at that button, as menus open there
function sheet(title, opts, at) {
  const sh = document.getElementById('sheet'), sc = document.getElementById('sscrim');
  sh.innerHTML = `<div class="card"><div class="sh">${esc(title)}</div>${opts.map(([l, sub], k) => `<button class="opt" data-sh="${k}">${esc(l)}<span>${esc(sub || '')}</span></button>`).join('')}</div><button class="cancel" data-sh="x">Cancel</button>`;
  const back = document.activeElement; // the keyboard goes back there, if it is still on the page
  const pop = !!(at && at.isConnected && desk());
  if (sh.classList.contains('pop') !== pop) { sh.style.transition = 'none'; sh.classList.toggle('pop', pop); sh.offsetHeight; sh.style.transition = ''; } // it does not fly across the page to its new place
  sc.classList.toggle('pop', pop);
  for (const k of ['left', 'width', 'top', 'bottom', '--mh']) sh.style.removeProperty(k);
  if (pop) { // below the button, its right edge at the button's, or above it when there is more room there
    const r = at.getBoundingClientRect(), w = Math.min(340, innerWidth - 16), below = innerHeight - r.bottom - 16, above = r.top - 16, down = below >= 260 || below >= above;
    Object.assign(sh.style, {left: Math.max(8, Math.min(r.right - w, innerWidth - w - 8)) + 'px', width: w + 'px', top: down ? r.bottom + 6 + 'px' : 'auto', bottom: down ? 'auto' : innerHeight - r.top + 6 + 'px'});
    sh.style.setProperty('--mh', Math.max(160, down ? below : above) + 'px');
  }
  sh.classList.add('open'); sc.classList.add('open');
  const fkey = back && back.dataset ? back.dataset.filters : undefined; // a filter button the page may draw anew meanwhile
  const close = () => {
    sh.classList.remove('open'); sc.classList.remove('open');
    const to = back && back.isConnected ? back : fkey !== undefined ? filterAt(fkey) : null;
    if (to && !sh.contains(to)) to.focus({preventScroll: true});
  };
  sc.onclick = close;
  sh.onclick = e => { const b = e.target.closest('[data-sh]'); if (!b) return; close(); if (b.dataset.sh !== 'x') opts[+b.dataset.sh][2](); };
  const first = sh.querySelector('[data-sh]'); if (first) first.focus({preventScroll: true}); // the keyboard is in the menu
}
function at(days, hour) { const d = new Date(); d.setDate(d.getDate() + days); d.setHours(hour, 0, 0, 0); return d; }
function snoozeSheet(ts) {
  const now = new Date(), later = now.getHours() < 17 ? at(0, 18) : new Date(now.getTime() + 3 * 3600e3);
  const monday = at((8 - now.getDay()) % 7 || 7, 8);
  const fmt = d => d.toLocaleString([], {weekday: 'short', hour: '2-digit', minute: '2-digit', hour12: false});
  sheet('Snooze until', [['Later today', fmt(later), () => snooze(ts, later)], ['Tomorrow', fmt(at(1, 8)), () => snooze(ts, at(1, 8))], ['Next week', fmt(monday), () => snooze(ts, monday)]]);
}

// ---------- what the human does ----------
function nextAfter(id) {
  const rows = listFor().rows, i = rows.findIndex(t => t.id === id);
  return rows[i + 1] || rows[i - 1] || null;
}
// leave: after an answer or an archive, the desktop opens the next one, as the human likes
function leave(id) {
  if (S.open !== id) return;
  const n = desk() && M.prefs.after === 'next' ? nextAfter(id) : null;
  S.open = n ? n.id : null; S.reply = false;
}
function archive(ts) {
  const back = ts.filter(t => t.box === 'done');
  if (back.length === ts.length && ts.length) { ts.forEach(t => { t.box = 'inbox'; post({action: 'unarchive', id: t.id}); }); render(); toast('Moved to Inbox'); return; }
  const before = ts.map(t => [t, t.box]);
  ts.forEach(t => leave(t.id));
  ts.forEach(t => { t.box = 'done'; post({action: 'archive', id: t.id}); });
  S.sel.clear(); render();
  toast(ts.length > 1 ? `${ts.length} archived` : 'Archived', () => before.forEach(([t, b]) => { t.box = b; post({action: 'unarchive', id: t.id}); }));
}
function snooze(ts, until) {
  ts.forEach(t => leave(t.id));
  ts.forEach(t => { t.snooze = until.toISOString(); post({action: 'snooze', id: t.id, arg: until.toISOString().replace(/\.\d+Z$/, 'Z')}); });
  S.sel.clear(); render();
  toast(`Snoozed until ${until.toLocaleString([], {weekday: 'short', hour: '2-digit', minute: '2-digit', hour12: false})}`, () => ts.forEach(t => { delete t.snooze; post({action: 'unsnooze', id: t.id}); }));
}
async function send(t, text) {
  const ask = !!t.qid, to = peer(t);
  if (ask) {
    if (await post({action: 'answer', id: t.id, text}) === null) return;
    leave(t.id); t.box = 'done'; t.qid = '';
    t.msgs.push({from: 'you', to, at: new Date().toISOString(), html: `<p>${esc(text)}</p>`, plain: text});
    toast(t.kind === 'do' && !S.wrong ? `Done: ${to} is told` : `Answered ${to}`);
  } else if (t.kind === 'msg') { // the thread goes on: the agent answers this one too
    if (await post({action: 'note', arg: to, thread: t.id, text}) === null) return;
    leave(t.id); t.box = 'waiting'; delete t.seen;
    t.msgs.push({from: 'you', to, at: new Date().toISOString(), html: `<p>${esc(text)}</p>`, plain: text});
    toast(`Sent to ${to}. It comes back to your inbox when ${to} answers.`);
  } else if (t.pr && t.pr.state !== 'approved') {
    if (await post({action: 'review-note', id: t.id, text}) === null) return;
    leave(t.id); t.box = 'waiting'; t.pr.state = 'waiting';
    toast(`Sent to ${to}. It comes back to your inbox when ${to} answers.`);
  } else {
    if (await post({action: 'note', arg: to, about: t.ref || '', text}) === null) return;
    lastTag = null; load(); // the message is a thread of its own, under Waiting
    toast(`Sent to ${to}. Its answer comes to your inbox.`);
  }
  S.reply = false; S.wrong = null; delete S.drafts[t.id];
  render();
}
async function approve(t) {
  if (await post({action: 'approve', id: t.id}) === null) return;
  leave(t.id); t.box = 'done'; t.pr.state = 'approved'; render();
  toast('Approved: it merges once nothing holds it back');
}
async function dismiss(t) {
  if (await post({action: 'dismiss', id: t.id}) === null) return;
  leave(t.id); t.box = 'done'; t.qid = ''; render(); toast(`Dismissed: ${peer(t)} is told`);
}
function setPref(k, v) {
  const p = {...M.prefs, [k]: v};
  M.prefs = p; render();
  post({action: 'prefs', arg: JSON.stringify(p)});
}

document.addEventListener('click', e => {
  const b = e.target.closest('button,[data-open],[data-search],[data-closedrawer],[data-expand],[data-cmin]');
  if (!b || !M) return;
  if (performance.now() < calmUntil) return; // the second click of a double click, on what moved up
  const d = b.dataset;
  if (d.pick) { e.stopPropagation(); S.sel.has(d.pick) ? S.sel.delete(d.pick) : S.sel.add(d.pick); render(); return; }
  if (d.star) { e.stopPropagation(); const t = find(d.star); t.star = !t.star; post({action: t.star ? 'star' : 'unstar', id: t.id}); render(); if (t.star) toast('Starred: it stays in Focused'); return; }
  if (d.open) { S.drawer = false; openIt(d.open); const o = document.getElementById('over'); if (o) o.scrollTop = 0; return; }
  if (d.view) { if (leaveOK()) goView(d.view); return; }
  if (d.atab) { if (!leaveOK()) return; S.aTab = d.atab; S.roleDraft = null; S.adv = null; S.edits = {}; render(); return; }
  if (d.teamact) {
    const act = d.teamact, who = d.arg || '';
    if (act === 'cancel') { sheet(`Stop ${who}’s run? It is killed and recorded as stopped.`, [['Stop it', '', () => teamPost({action: 'cancel', arg: who})]]); return; }
    teamPost({action: act, arg: who}); return;
  }
  if (d.composeto) { S.compose = {to: d.composeto}; render(); const c = document.getElementById('ctext'); if (c) c.focus(); return; }
  if (d.editrole !== undefined) { S.roleDraft = TEAM.agent.role; render(); const r = document.getElementById('roletext'); if (r) r.focus(); return; }
  if (d.cancelrole !== undefined) { if (!leaveOK('Close without saving your changes?')) return; S.roleDraft = null; render(); return; }
  if (d.save !== undefined) { saveNow(); return; }
  if (d.discard !== undefined) { discardNow(); return; }
  if (d.aset && b.classList.contains('on')) return; // already so: an agent at priority 50 stays there
  if (d.aset) { teamPost({action: 'agent-set', arg: TEAM.agent.name, text: JSON.stringify({[d.aset]: d.v})}); return; }
  if (d.adv !== undefined) {
    S.adv = d.adv; render();
    const el = document.getElementById(d.adv ? 'sec-' + d.adv.replace(/\W+/g, '-') : 'sec-none') || document.querySelector('.adv');
    if (el) el.scrollIntoView({block: 'start'});
    return;
  }
  if (d.closeadv !== undefined) { if (!leaveOK('Close without saving your changes?')) return; S.adv = null; S.edits = {}; render(); return; }
  if (d.chat !== undefined) { if (desk() && S.tc.open && !d.chat) { S.tc.open = false; render(); } else openChat(d.chat || undefined); return; }
  if (d.ptab) { S.ptab = d.ptab; S.open = null; S.drawer = false; render(); if (d.ptab === 'chat') chatToEnd(); window.scrollTo(0, 0); return; }
  if (d.cmin !== undefined) { e.stopPropagation(); S.tc.min = !S.tc.min; render(); if (!S.tc.min) chatToEnd(); return; }
  if (d.cclose !== undefined) { e.stopPropagation(); S.tc.open = false; render(); return; }
  if (d.csend !== undefined) { chatSend((document.getElementById('chatin') || {}).value); return; }
  if (d.csay) { chatSend(d.csay); return; }
  if (d.askteam !== undefined) { const q = S.q; S.q = null; S.f = {}; openChat(''); chatSend(q); return; }
  if (d.cto) { S.vals.chat = '@' + d.cto + ' ' + (S.vals.chat || '').replace(/^@[A-Za-z0-9_.-]+\s*/, ''); render(); const el = document.getElementById('chatin'); if (el) { el.focus(); el.setSelectionRange(el.value.length, el.value.length); } return; }
  if (d.mention) { S.vals.chat = (S.vals.chat || '').replace(/@[A-Za-z0-9_.-]*$/, '@' + d.mention + ' '); render(); const el = document.getElementById('chatin'); if (el) { el.focus(); el.setSelectionRange(el.value.length, el.value.length); } return; }
  if (d.capply) { applyCard(d.capply); return; }
  if (d.cno) { post({action: 'chat-cancel', arg: d.cno}).then(() => { CHAT.tag = null; loadChat(); }); return; }
  if (d.cundo) { post({action: 'chat-undo', arg: d.cundo}).then(said => { if (said !== null) { if (said) toast(said); CHAT.tag = null; loadChat(); } }); return; }
  if (d.repos !== undefined) { S.repos = !S.repos; render(); return; }
  if (d.repoadd !== undefined) { repoAdd(); return; }
  if (d.repodel) { repoDel(d.repodel); return; }
  if (d.repoteam) { repoTeam(d.repoteam); return; }
  if (d.webmode) {
    const on = d.webmode === 'tailscale', save = () => teamPost({action: 'team-set', text: JSON.stringify({web: d.webmode})});
    if (on) save(); else sheet('Stop opening the pages on your phone? Only this Mac opens them then.', [['Stop', '', save]]);
    return;
  }
  if (d.fcheck) { const f = advField(d.fcheck); if (f && d.v === saved(f)) delete S.edits[d.fcheck]; else S.edits[d.fcheck] = d.v; render(); return; }
  if (d.tab) { S.view = 'inbox'; S.tab = d.tab; S.open = null; S.autoOpen = true; render(); window.scrollTo(0, 0); return; }
  if (d.drawer !== undefined) { S.drawer = true; render(); return; }
  if (d.closedrawer !== undefined) { S.drawer = false; render(); return; }
  if (d.search !== undefined) { S.q = ''; S.f = {}; S.focusSearch = true; render(); return; }
  if (d.endsearch !== undefined) { S.q = null; S.f = {}; render(); return; }
  if (d.clearq !== undefined) { S.q = ''; S.focusSearch = true; render(); return; }
  if (d.from) { S.f.from = d.from; if (S.q === null) S.q = ''; render(); return; }
  if (d.filters !== undefined) { if (d.filters) sheet(d.filters === 'issues' ? 'Sort and filter' : 'Filter', listOpts(d.filters), b); else filterSheet(); return; }
  if (d.unlist) { setList(d.unlist, d.lk === 'drop' ? {drop: d.lv} : {[d.lk]: ''}); return; }
  if (d.clearlist) { setList(d.clearlist, {clear: 1}); return; }
  if (d.unfilter) { setFilter(d.unfilter, null); return; }
  if (d.close !== undefined) { S.open = null; S.reply = false; render(); return; }
  if (d.expand) { if (isIssue(d.expand)) ISS.expand = d.expand; else find(d.expand).expand = true; render(); return; }
  if (d.archive) { archive([find(d.archive)]); return; }
  if (d.snooze) { snoozeSheet([find(d.snooze)]); return; }
  if (d.unread) { const t = find(d.unread); leave(t.id); t.unread = true; post({action: 'unread', id: t.id}); if (!desk()) S.open = null; render(); toast('Marked unread'); return; }
  if (d.moremenu) {
    const t = find(d.moremenu), inF = split().focused.includes(t), opts = [];
    if (t.box === 'inbox') opts.push([inF ? 'Move to Other' : 'Move to Focused', '', () => {
      const was = t.place, starred = t.star;
      t.place = inF ? 'other' : 'focused';
      post({action: 'place', id: t.id, arg: t.place});
      if (inF && starred) { t.star = false; post({action: 'unstar', id: t.id}); } // a star keeps it in Focused
      leave(t.id); render();
      toast(inF ? 'Moved to Other' : 'Moved to Focused', () => { t.place = was; post({action: 'place', id: t.id, arg: was || ''}); if (inF && starred) { t.star = true; post({action: 'star', id: t.id}); } });
    }]);
    if (t.kind === 'msg' && lastFrom(t) === 'you' && !t.seen) opts.push(['Take it back', `${peer(t)} has not read it yet`, async () => {
      if (await post({action: 'unsend', id: t.id}) === null) return;
      leave(t.id); load(); toast('Taken back');
    }]);
    if (t.qid) opts.push(['Dismiss without an answer', `${peer(t)} is told, and does not ask again`, () => dismiss(t)]);
    if (t.url) opts.push(['Open on GitHub', t.label || '', () => window.open(t.url, '_blank', 'noopener')]);
    if (t.pr && ['review', 'waiting'].includes(t.pr.state)) opts.push(['Approve', 'the team may merge it', () => approve(t)]);
    if (t.review) opts.push(['Review', 'the whole change and each commit', () => { flushPlace(); location.href = t.review; }]);
    if (opts.length) sheet(t.subject, opts);
    return;
  }
  if (d.ans) { send(find(d.ans), d.v); return; }
  if (d.approve) { const t = find(d.approve); sheet(`Approve ${t.label || 'it'}? The team may merge it once nothing holds it back.`, [['Approve', 'with any notes you drafted in the review', () => approve(t)]]); return; }
  if (d.tick) { // says what the step is to be: two windows ticking it agree
    const t = find(d.tick), k = +d.k, on = !(t.ticks || []).includes(k);
    t.ticks = on ? [...(t.ticks || []), k] : t.ticks.filter(x => x !== k);
    post({action: 'tick', id: t.id, arg: `${k}:${on ? 'on' : 'off'}`}); render(); return;
  }
  if (d.done) { send(find(d.done), 'Done: all the steps are done.'); return; }
  if (d.wrong) { S.wrong = d.wrong; S.reply = true; render(); const r = document.getElementById('rtext'); if (r) r.focus(); return; }
  if (d.replyopen !== undefined) { S.reply = true; render(); const r = document.getElementById('rtext'); if (r) r.focus(); return; }
  if (d.replyclose !== undefined) { S.reply = false; render(); return; }
  if (d.send) { const v = ((document.getElementById('rtext') || {}).value || '').trim(); if (!v) { toast('Write something first'); return; } send(find(d.send), v); return; }
  if (d.compose !== undefined) { S.compose = {to: ''}; render(); return; }
  if (d.to !== undefined) { S.compose.to = d.to; render(); return; }
  if (d.closecompose !== undefined) { S.compose = null; S.vals.ctext = S.vals.cabout = ''; render(); return; }
  if (d.sendcompose !== undefined) {
    const c = S.compose, text = (S.vals.ctext || '').trim();
    if (!c.to) { toast('Choose who it is for'); return; }
    if (!text) { toast('Write something first'); return; }
    post({action: 'note', arg: c.to, about: S.vals.cabout || '', text}).then(said => { if (said === null) return; S.compose = null; S.vals.ctext = S.vals.cabout = ''; render(); lastTag = null; load(); toast(`Sent to ${c.to}. It answers in this thread, under Waiting until it reads it.`); });
    return;
  }
  if (d.clearsel !== undefined) { S.sel.clear(); render(); return; }
  if (d.clearf !== undefined) { S.f = {}; S.keepRead.clear(); S.sel.clear(); render(); return; }
  if (d.allread !== undefined) { // what the list shows, which the mail read meanwhile may not be
    const ts = [...document.querySelectorAll('.row.unread[data-open]')].map(r => find(r.dataset.open)).filter(t => t && t.unread);
    ts.forEach(t => { t.unread = false; post({action: 'read', id: t.id}); });
    S.keepRead.clear(); render(); toast(ts.length === 1 ? 'Marked read' : `${ts.length} marked read`);
    return;
  }
  if (d.bulk) {
    const ts = [...S.sel].map(find).filter(Boolean);
    if (d.bulk === 'archive') archive(ts);
    else if (d.bulk === 'snooze') snoozeSheet(ts);
    else { ts.forEach(t => { t.unread = false; post({action: 'read', id: t.id}); }); S.sel.clear(); render(); toast('Marked read'); }
    return;
  }
  if (d.push) { pushDo(d.push); return; }
  if (d.pref) { const k = d.pref; setPref(k, k === 'split' ? !M.prefs.split : k === 'focus' ? +d.v : d.v); return; }
});
document.addEventListener('input', e => {
  const id = e.target.id;
  if (id === 'q' || id === 'dq') { S.q = e.target.value; render(); return; }
  if (id === 'roletext') { S.roleDraft = e.target.value; paintBar(); return; }
  if (id === 'chatin') { // it grows with what is typed, to five lines
    S.vals.chat = e.target.value; e.target.style.height = 'auto'; e.target.style.height = Math.min(e.target.scrollHeight, 120) + 'px';
    const m = document.getElementById('mentions'); if (m) m.innerHTML = mentionsHTML(); return;
  }
  if (e.target.dataset.field) { fieldInput(e.target); return; }
  if (e.target.dataset.weight) {
    if (!Object.keys(S.weights).length && TEAM.shares) { // the first: the page keeps every weight it shows, whatever a later reading proposes
      S.wbase = Object.create(null);
      for (const a of TEAM.shares.agents) S.wbase[a.name] = String(a.proposed);
      Object.assign(S.weights, S.wbase);
    }
    S.weights[e.target.dataset.weight] = e.target.value; sharesShown(); paintBar(); return;
  }
  if (id === 'rtext' && S.open) S.drafts[S.open] = e.target.value;
  else if (/^(ctext|cabout|repoin)$/.test(id)) S.vals[id] = e.target.value;
});
// advField: a setting of the page shown, the agent's or the team's
const advField = k => ((S.view.startsWith('agent:') ? TEAM.agent : TEAM.settings) || {fields: []}).fields.find(x => x.key === k);
// fieldInput keeps what the human typed in a setting, unless it is what the file has; its row and the
// bar say it waits for Save
function fieldInput(el) {
  const k = el.dataset.field, f = advField(k);
  if (f && el.value === saved(f)) delete S.edits[k]; else S.edits[k] = el.value;
  const row = el.closest('.afld'); if (row) row.classList.toggle('edited', k in S.edits);
  paintBar();
}
document.addEventListener('change', e => { if (e.target.dataset.field && e.target.tagName === 'SELECT') fieldInput(e.target); });
document.addEventListener('focusin', e => { if (e.target.id === 'dq' && S.q === null) { S.q = ''; S.f = {}; render(); } });

// keyboard, as Gmail's
document.addEventListener('keydown', e => {
  if (M && (e.metaKey || e.ctrlKey) && !e.altKey && !e.shiftKey && e.key.toLowerCase() === 's' && barShown()) { e.preventDefault(); saveNow(); return; } // as the bar's Save
  if (!M || e.metaKey || e.ctrlKey || e.altKey) return;
  if (paneKey(e)) return;
  if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && e.keyCode !== 229 && document.activeElement.id === 'chatin') { e.preventDefault(); chatSend(document.activeElement.value); return; } // Enter that ends an IME's word only ends it
  if (e.key === 'Enter' && !e.isComposing && e.keyCode !== 229 && document.activeElement.id === 'repoin') { e.preventDefault(); repoAdd(); return; }
  const typing = ['INPUT', 'TEXTAREA'].includes(document.activeElement.tagName);
  if (e.key === 'Escape') {
    if (document.getElementById('sheet').classList.contains('open')) { document.getElementById('sscrim').click(); return; }
    if (S.compose) S.compose = null; else if (searching()) { S.q = null; S.f = {}; document.activeElement.blur(); } else if (S.reply) S.reply = false; else if (S.open && !desk()) S.open = null; else if (S.view === 'keys' || S.view === 'settings' && !unsaved()) { if (backTo('inbox')) return; S.view = 'inbox'; }
    else if (S.view.startsWith('run:')) { S.view = 'agent:' + S.view.slice(20); loadPage(); }
    render(); return;
  }
  const sh = document.getElementById('sheet');
  if (sh.classList.contains('open')) { // a menu is open: the keys are its own, and Tab or an arrow moves in it
    const step = e.key === 'Tab' ? (e.shiftKey ? -1 : 1) : e.key === 'ArrowDown' ? 1 : e.key === 'ArrowUp' ? -1 : 0;
    if (step) { const bs = [...sh.querySelectorAll('button')].filter(b => b.getClientRects().length), k = bs.indexOf(document.activeElement); e.preventDefault(); bs[(k + step + bs.length) % bs.length].focus(); } // a dropdown has no Cancel to stop at
    return;
  }
  if (typing) return;
  const ids = S.view === 'issues' && !searching() ? shownIssues().map(issueId) : listFor().rows.map(t => t.id), i = ids.indexOf(S.open);
  const go = k => { const id = ids[Math.max(0, Math.min(ids.length - 1, i + k))]; if (id) openIt(id); };
  const t = S.open && find(S.open);
  const keys = {j: () => go(1), k: () => go(-1), e: () => t && archive([t]), s: () => { if (t) { t.star = !t.star; post({action: t.star ? 'star' : 'unstar', id: t.id}); render(); } },
    b: () => t && snoozeSheet([t]), r: () => { if (t && peer(t)) { S.reply = true; render(); const r = document.getElementById('rtext'); if (r) r.focus(); } },
    c: () => { S.compose = {to: ''}; render(); }, '/': () => { e.preventDefault(); S.q = ''; S.focusSearch = true; render(); }, '?': () => { if (!leaveOK()) return; S.view = 'keys'; S.q = null; S.f = {}; S.roleDraft = null; S.adv = null; S.edits = {}; S.weights = Object.create(null); render(); }};
  if (keys[e.key]) { keys[e.key](); }
});

// swipe on the phone: right archives, left snoozes
function bindSwipe() {
  if (desk()) return;
  document.querySelectorAll('.app.phone .swipe').forEach(w => {
    const r = w.querySelector('.row'), bg = w.querySelector('.sw-bg');
    let x0 = 0, y0 = 0, dx = 0, on = false, locked = false;
    r.addEventListener('pointerdown', e => { x0 = e.clientX; y0 = e.clientY; dx = 0; locked = false; on = true; });
    r.addEventListener('pointermove', e => {
      if (!on) return;
      const mx = e.clientX - x0, my = e.clientY - y0;
      if (!locked) { if (Math.abs(mx) < 8) return; if (Math.abs(my) > Math.abs(mx)) { on = false; return; } locked = true; r.classList.add('drag'); r.setPointerCapture(e.pointerId); }
      dx = mx; r.style.transform = `translateX(${dx}px)`; bg.classList.toggle('snz', dx < 0);
    });
    const end = () => {
      if (!on) return; on = false; r.classList.remove('drag');
      if (!locked) return;
      const t = find(w.dataset.sw);
      if (dx > 90) { r.style.transform = 'translateX(110%)'; setTimeout(() => archive([t]), 160); }
      else if (dx < -90) { r.style.transform = ''; snoozeSheet([t]); }
      else r.style.transform = '';
      r.addEventListener('click', ev => ev.stopPropagation(), {once: true, capture: true});
    };
    r.addEventListener('pointerup', end); r.addEventListener('pointercancel', end);
  });
}

// setView opens a folder or a page, to be drawn; what was unsaved, the human chose to leave
function setView(v) {
  if (v.startsWith('agent:') && !S.view.startsWith('run:') && S.view !== v) S.aTab = 'messages'; // back from a run: its tab stays
  S.view = v; S.drawer = false; S.q = null; S.f = {}; S.keepRead.clear(); S.open = null; S.sel.clear(); S.autoOpen = true; S.roleDraft = null; S.adv = null; S.edits = {}; S.weights = Object.create(null); TEAM.error = '';
  S.ptab = 'mail'; // on the phone, a page is under Mail
  if (v === 'issues') loadIssues();
  if (pageView()) loadPage();
}
// goView opens a folder or a page, as the menu does
function goView(v) { setView(v); render(); window.scrollTo(0, 0); }

// ---------- start: deep links, live updates ----------
for (const [k, v] of new URLSearchParams(location.hash.slice(1))) {
  if (k === 'open') S.open = v;
  else if (k === 'view') S.view = v;
  else if (k === 'tab') S.tab = v;
  else if (k === 'q') S.q = v;
}
if (location.hash === '#questions') S.ask = true; // from a notification: the question it is about
const step = stepOf(history.state); // the page loaded anew for a step of the browser's history, as by its back or a reload
let atPage = ''; try { atPage = decodeURIComponent(location.hash.slice(1)); } catch (e) { /* not an address of the app's */ }
if (step && (atPage ? atPage === step.view && pageAt(step.view) : FOLDERS.includes(step.view) || step.view.startsWith('repo:'))) { // its folder, or the page its address says, with its tab and message
  S.view = step.view; S.tab = step.tab || S.tab; S.open = step.open || null; S.autoOpen = false; shownOpen = S.open;
  if (S.view === 'issues') loadIssues();
}
addEventListener('resize', () => render());
addEventListener('beforeunload', e => { if (unsaved()) { e.preventDefault(); e.returnValue = ''; } }); // closing or reloading the page asks first too
route(); // a link or a reload of #team, #settings, #agent:<name> or #run:<id>
load();
const events = new EventSource('/events');
events.onmessage = reload;
events.onopen = () => { if (S.loaded) reload(); };
document.addEventListener('visibilitychange', () => {
  if (document.hidden || !M) return;
  if (S.due) { S.due = false; lastTag = null; load(); } else render(); // what is open is seen now
});

package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Organize is rota start, the whole scheduler: one foreground process. Every look it
// rereads the team folder, starts the shift of whoever is due, and writes its snapshot for
// the front ends; then it waits for the next tick, the next due time or a request to run
// now. Hosts join it over the network. When ctx ends, the running shift is stopped, here or
// on its host, with everything it started.
func Organize(ctx context.Context, root, conf string) error {
	t, err := Open(root, conf)
	if err != nil {
		return err
	}
	release, err := claimOrganizer(t.Root)
	if err != nil {
		return err
	}
	defer release()
	pidFile := t.Path(lockFile + ".pid")
	if os.WriteFile(pidFile, []byte(fmt.Sprint(os.Getpid())), 0o644) == nil {
		defer os.Remove(pidFile)
	}
	os.Remove(t.cancelFile()) // a cancel asked for while no organizer ran is not for the next shift
	t.askToResume()           // a shift the last organizer could not record as interrupted, unless paused
	o := &organizer{Config: t.Config, wake: make(chan time.Time, 1),
		monitor: monitor{Config: t.Config, started: time.Now(), lastShift: map[string]time.Time{}}}
	var self *selfWatch
	if exe, err := os.Executable(); err == nil {
		self = watchSelf(exe)
	}
	o.self = self
	go o.watch(ctx)
	o.hub = newHub(t.Config)
	go o.followListen(ctx, root, conf) // hosts join where the settings say, as they change
	kicked, warned := false, ""
	for {
		o.updateSelf(ctx, self)    // between shifts: a new rota takes this process's place
		stamp := o.settingsStamp() // before they are read: a change while they are, is a change
		t, err := Open(root, conf)
		if err == nil && strings.Join(t.Warnings, "\n") != warned { // a file changed: say so once
			warned = strings.Join(t.Warnings, "\n")
			for _, w := range t.Warnings {
				logf("warn: %s", w)
			}
		}
		if err == nil {
			o.syncLabels(ctx, t)
		}
		switch {
		case err != nil:
			logf("rota: %v", err) // fix the file; the next look reads it again
		case t.Paused() && !kicked && len(t.askedByHand()) == 0:
			o.look(ctx, t, true, true)
			logf("paused")
		default:
			o.look(ctx, t, t.Paused() && !kicked, false) // paused: only the agents the human asks for run
		}
		if ctx.Err() != nil {
			o.write()
			fmt.Println("rota: stopped")
			return nil
		}
		if t == nil {
			t = &Team{Config: o.Config}
		}
		kicked = false
		wait := time.Duration(t.TickMinutes * float64(time.Minute))
		switch {
		case o.tookShift():
			wait = 3 * time.Second // a shift just ran: serve whoever else is due first
		case o.held():
			wait = min(wait, holdMinutes*time.Minute) // on hold: start soon after Claude answers again
		}
		next := o.wakeBefore(time.Now(), time.Now().Add(wait))
		pruneLogs(t.Config)
		if wait > 3*time.Second {
			logf("next look at %s", next.Format("15:04"))
		}
		// Wait by the wall clock in short slices: a Go timer stops while the machine sleeps, so
		// after a night asleep the look would otherwise come a whole tick after waking.
		edited := stamp
		for waiting := true; waiting && time.Until(next) > 0; {
			select {
			case <-ctx.Done():
				fmt.Println("rota: stopped")
				return nil
			case at := <-o.wake:
				if o.answered(at) {
					continue // a shift already answered this request
				}
				// agents the human asked for are served by name; a bare request asks for the next
				// shift: while paused, only the human wakes the organizer up
				kicked, waiting = len(t.askedByHand()) == 0, false
				logf("run now: %s", askedText(t.Config))
			case <-time.After(min(waitSlice, time.Until(next))):
				if t.settingsStamp() != edited {
					waiting = false // a setting changed, the team's or an agent's: look again with it now
				}
				if pending, _ := self.check(time.Now()); pending {
					waiting = false // a new rota is installed: restart with it now, between shifts
				}
			}
		}
	}
}

// labelSync says whether the organizer keeps the team's labels on GitHub; tests turn it off.
var labelSync = true

// syncLabels creates or corrects the team's labels in the background, when the organizer starts
// and whenever the labels or repositories the team needs change; after a failure, at the next
// look.
func (o *organizer) syncLabels(ctx context.Context, t *Team) {
	key := t.labelsKey()
	o.mu.Lock()
	if !labelSync || o.labeling || o.labeled == key {
		o.mu.Unlock()
		return
	}
	o.labeling = true
	o.mu.Unlock()
	go func() {
		changed, err := t.SyncLabels(ctx)
		if len(changed) > 0 {
			logf("labels: %s", strings.Join(changed, "; "))
		}
		if err != nil {
			logf("warn: %v", err)
		}
		o.mu.Lock()
		if err == nil {
			o.labeled = key
		}
		o.labeling = false
		o.mu.Unlock()
	}()
}

// organizerLock is held by the machine's one organizer, whatever its team folder.
var organizerLock = func() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "rota", "organizer.lock")
}()

// claimOrganizer makes this process the machine's organizer until release, or says which
// process already is.
func claimOrganizer(root string) (release func(), err error) {
	os.MkdirAll(filepath.Dir(organizerLock), 0o755)
	f, err := os.OpenFile(organizerLock, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		who, _ := io.ReadAll(f)
		f.Close()
		return nil, fmt.Errorf("an organizer already runs on this machine (%s); a machine has one", or(strings.TrimSpace(string(who)), "it is starting"))
	}
	f.Truncate(0)
	f.WriteAt([]byte(fmt.Sprintf("pid %d, team folder %s", os.Getpid(), root)), 0)
	return func() { f.Truncate(0); f.Close() }, nil // closing lets the lock go
}

func modTime(path string) time.Time {
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

// look updates the snapshot and, unless statusOnly, starts the shift of whoever is due:
// with only, just the agents asked to run now. The last reading of Claude's usage, however
// old, tells whether a shift is due; only then is Claude asked for a fresh one.
func (o *organizer) look(ctx context.Context, t *Team, only, statusOnly bool) {
	t.openWork() // GitHub's list stays fresh, so the human hears of agents' answers with nothing open
	var hosts []HostState
	if o.hub != nil {
		var users []string
		for _, a := range t.Agents {
			if a.Enabled && a.GhUser != "" && !contains(users, a.GhUser) {
				users = append(users, a.GhUser)
			}
		}
		o.hub.setNeeds(users)
		hosts = o.hub.hosts()
	}
	u := t.lastUsage()
	s := t.schedule(time.Now(), u, hosts)
	s.only = only
	var err error
	// asked only when a shift is due, when Claude's week has reset since (the budget week moves),
	// or every usageStale, for the status
	if !statusOnly && usageEnabled(t.Config) && (u == nil || s.pick() != nil || time.Since(u.Read) > usageStale || !u.ResetsAt.IsZero() && time.Now().After(u.ResetsAt)) {
		if fresh, ferr := fetchUsage(t.Config, usageReuse, 0); ferr != nil { // refused: not asked again until it may be
			err = ferr
			logf("on hold: Claude's usage cannot be read: %v", err)
		} else {
			u = fresh
		}
		s = t.schedule(time.Now(), u, hosts) // a failed reading keeps the last one for the status
		s.only = only
	}
	st := t.status(s)
	st.Listen = o.addr()
	if err != nil {
		st.UsageError = err.Error()
	}
	o.update(st)
	if statusOnly || err != nil {
		return
	}
	a := s.pick()
	if a == nil {
		logf("nothing to run; see: rota status")
		return
	}
	logf("shift: %s", a.Name)
	sctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	o.setCancel(cancel)
	defer o.setCancel(nil)
	if err := t.shift(sctx, s, a, u, o.hub, &o.monitor, false); err != nil && ctx.Err() == nil {
		logf("rota: %v", err)
	}
}

func logf(format string, args ...any) {
	fmt.Printf("%s %s\n", time.Now().Format("15:04"), fmt.Sprintf(format, args...))
}

// askedText names the agents waiting to run now, for the organizer's output.
func askedText(c *Config) string {
	var names []string
	for n := range c.Asked() {
		names = append(names, n)
	}
	if len(names) == 0 {
		return "the next shift"
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

type organizer struct {
	*Config
	monitor
	self     *selfWatch // a newly installed rota, which takes this one's place
	hub      *hub
	wake     chan time.Time // a request to run now, with the time it was made
	labeled  string         // the team labels last made right on GitHub (labelsKey)
	labeling bool
}

func (o *organizer) addr() string {
	if o.hub == nil {
		return ""
	}
	return o.hub.address()
}

// followListen keeps the hosts' listener where listen and listen_port say, from the organizer's
// start and within seconds of a change: hosts that joined stay joined, as their connections
// outlive the listener. One that cannot bind is tried again every minute.
func (o *organizer) followListen(ctx context.Context, root, conf string) {
	var ln net.Listener
	at, edited, failed, first := "", time.Time{}, time.Time{}, true
	for {
		m := modTime(o.Path(o.File))
		retry := ln == nil && !failed.IsZero() && time.Since(failed) > time.Minute
		if first || retry || !m.Equal(edited) {
			c, err := LoadConfig(root, conf)
			if err == nil && modTime(o.Path(o.File)).Equal(m) { // a file that cannot be read, or changed while it was, changes nothing yet
				edited = m
				if want := strings.ToLower(strings.TrimSpace(c.Listen)) + " " + strconv.Itoa(c.ListenPort); first || retry || want != at {
					ln, failed = o.relisten(ctx, ln, c, first), time.Time{}
					if ln == nil && !strings.EqualFold(c.Listen, "off") {
						failed = time.Now()
					}
					at, first = want, false
				}
			}
		}
		select {
		case <-ctx.Done():
			return // serve closes the listener
		case <-time.After(waitSlice):
		}
	}
}

// relisten closes the hosts' listener, if there is one, and listens as c says: nil when listen is
// off, or the address cannot be bound.
func (o *organizer) relisten(ctx context.Context, ln net.Listener, c *Config, first bool) net.Listener {
	if ln != nil {
		ln.Close()
	}
	if strings.EqualFold(c.Listen, "off") {
		o.hub.setAddr("")
		if !first {
			logf("hosts can no longer join: listen is off")
		}
		return nil
	}
	l, err := o.hub.listenOn(c.Listen, c.ListenPort)
	if err != nil {
		o.hub.setAddr("")
		logf("warn: hosts cannot join: %v", err)
		return nil
	}
	go o.hub.serve(ctx, l)
	logf("organizer listening on %s; a host joins with: rota join <this machine> <code>, code from: rota pair", o.hub.address())
	return l
}

// settingsStamp changes when a setting does: rota.conf, or an agent's file.
func (c *Config) settingsStamp() string {
	files, _ := filepath.Glob(filepath.Join(c.Path(agentsDir), "*.md"))
	var b strings.Builder
	for _, p := range append([]string{c.Path(c.File)}, files...) {
		if fi, err := os.Stat(p); err == nil {
			fmt.Fprintf(&b, "%s %d %d\n", p, fi.Size(), fi.ModTime().UnixNano())
		}
	}
	return b.String()
}

// monitor is the organizer's live record: the snapshot it writes to the state file, the
// running shift, and how to stop it.
type monitor struct {
	*Config
	mu        sync.Mutex
	st        Status
	running   *Running
	ranShift  bool
	nextTick  time.Time
	cancel    context.CancelCauseFunc // the running shift's, with why it stops
	begun     bool                    // its claude began: it has a session to resume
	open      map[string]bool         // its tool calls that have not answered, by id: true for one that only waits for the human's answer
	newSince  time.Time               // when a new rota was ready while the shift ran
	updating  bool                    // the shift was stopped for it
	lastWrite time.Time
	started   time.Time
	lastShift map[string]time.Time // agent: when its latest shift started
}

func (m *monitor) update(st Status) {
	m.mu.Lock()
	m.st = st
	m.mu.Unlock()
	m.write()
}

func (m *monitor) write() {
	m.mu.Lock()
	st := m.st
	st.Time, st.Pid, st.StartedAt, st.Running, st.NextTick = time.Now(), os.Getpid(), m.started, m.running, m.nextTick
	m.lastWrite = time.Now()
	m.mu.Unlock()
	st.Paused = (&Team{Config: m.Config}).Paused()
	b, _ := json.MarshalIndent(st, "", "  ")
	os.WriteFile(m.Path(stateFile), b, 0o644)
}

func (m *monitor) shiftStarted(agent, host string) {
	m.mu.Lock()
	m.running = &Running{Agent: agent, Host: host, Since: time.Now()}
	m.lastShift[agent] = time.Now()
	m.mu.Unlock()
	m.write()
}

// shiftLine records what the running shift is doing; the snapshot is written at most every
// two seconds for it.
func (m *monitor) shiftLine(live string) {
	m.mu.Lock()
	if m.running != nil {
		m.running.Live = live
	}
	due := time.Since(m.lastWrite) > 2*time.Second
	m.mu.Unlock()
	if due {
		m.write()
	}
}

func (m *monitor) shiftEnded(started bool) {
	m.mu.Lock()
	m.running, m.ranShift = nil, m.ranShift || started
	m.mu.Unlock()
	m.write()
}

// tookShift says whether a shift ran since the last call, so the organizer looks again at
// once for others that are due.
func (m *monitor) tookShift() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := m.ranShift
	m.ranShift = false
	return v
}

// held says whether the last look found Claude's usage unreadable, so no shift could start.
func (m *monitor) held() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st.UsageError != ""
}

// answered says whether a run-now request made at t needs nothing more: no agent is still
// asked for by name, and a shift has started since, so a bare request was served.
func (o *organizer) answered(t time.Time) bool { return len(o.Asked()) == 0 && o.shiftSince(t) }

// shiftSince says whether any shift started after t.
func (m *monitor) shiftSince(t time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, at := range m.lastShift {
		if at.After(t) {
			return true
		}
	}
	return false
}

// wakeBefore brings the next look forward to the earliest moment an agent can start, so a
// shift begins when its window or due time opens, not up to a tick later; it is the
// organizer's next look.
func (m *monitor) wakeBefore(now, next time.Time) time.Time {
	m.mu.Lock()
	for _, a := range m.st.Agents {
		if at := a.NextRun.Add(time.Second); a.Enabled && at.After(now) && at.Before(next) {
			next = at
		}
	}
	m.nextTick = next
	m.mu.Unlock()
	m.write()
	return next
}

func (m *monitor) setCancel(f context.CancelCauseFunc) {
	m.mu.Lock()
	m.cancel, m.begun, m.open, m.newSince, m.updating = f, false, map[string]bool{}, time.Time{}, false
	m.mu.Unlock()
}

// waitOnly is a command that only waits for the human's answer: rota wait, its output cut short.
var waitOnly = regexp.MustCompile(`^rota wait \S+(\s+2>&1)?(\s*\|\s*tail(\s+-n)?\s+-?\d+)?\s*$`)

// step follows the running shift: whether its claude began, and which of its tool calls are open,
// by their ids, each marked when it only waits for the human's answer.
func (m *monitor) step(e Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.open == nil {
		m.open = map[string]bool{}
	}
	switch e.Kind {
	case "start":
		m.begun = true
	case "tool":
		id := e.ID
		if id == "" { // a call without an id is matched by the next answer without one
			id = fmt.Sprintf("?%d", len(m.open))
		}
		m.open[id] = e.Tool == "Bash" && waitOnly.MatchString(strings.TrimSpace(e.Arg))
	case "output":
		if e.ID != "" {
			delete(m.open, e.ID) // one answering a call not seen leaves the others open
			return
		}
		for id := range m.open {
			if strings.HasPrefix(id, "?") {
				delete(m.open, id)
				break
			}
		}
	}
}

// updateGrace is how long a new rota waits for the running shift's tool calls to answer before it
// stops the shift anyway; tests shorten it.
var updateGrace = 15 * time.Minute

// takeUpdate stops the running shift for a newly installed rota, between its tool calls or while
// it only waits for the human's answer, or once updateGrace has passed: it resumes where it
// stopped in the new rota, which the organizer runs as soon as the shift is recorded.
func (o *organizer) takeUpdate(now time.Time) {
	if _, ready := o.self.check(now); !ready {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock() // the line handler sees no new step between the look and the stop
	if o.cancel == nil || o.updating {
		return
	}
	if o.newSince.IsZero() {
		o.newSince = now
	}
	rest := o.begun // a shift still preparing has no session yet to resume
	for _, waits := range o.open {
		rest = rest && waits
	}
	if !rest && now.Sub(o.newSince) < updateGrace {
		return
	}
	o.updating = true
	who := "the"
	if o.running != nil {
		who = o.running.Agent + "'s"
	}
	logf("a new rota is installed: %s shift stops here and resumes in it", who)
	o.cancel(errUpdating)
}

// watch polls the control files every two seconds and writes a heartbeat every 30, so the
// snapshot is fresh within seconds of the machine waking up.
func (o *organizer) watch(ctx context.Context) {
	beat := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
		if time.Since(beat) >= 30*time.Second {
			beat = time.Now()
			o.write()
		}
		if who, err := os.ReadFile(o.cancelFile()); err == nil && os.Remove(o.cancelFile()) == nil {
			o.mu.Lock()
			stop, running := o.cancel, o.running
			o.mu.Unlock()
			// a request names the agent whose shift it stops: one that started since is not it
			if stop != nil && (len(who) == 0 || running != nil && running.Agent == strings.TrimSpace(string(who))) {
				logf("shift cancelled by the operator")
				stop(errOperatorCancel)
			}
		}
		o.takeUpdate(time.Now()) // a new rota: the running shift stops between steps and resumes in it
		if _, err := os.Stat(o.kickFile()); err == nil {
			at := time.Now()
			if st, err := os.Stat(o.kickFile()); err == nil {
				at = st.ModTime()
			}
			os.Remove(o.kickFile())
			if time.Since(at) > time.Hour {
				logf("ignoring a run-now request from %s", at.Format("Mon 15:04"))
				continue
			}
			select {
			case o.wake <- at:
			default: // a wake-up is already waiting; the requests themselves are on disk
			}
		}
	}
}

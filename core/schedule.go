package core

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// The schedule decides who may start a shift now. An agent may when it is enabled, its host
// is ready, it is due (its interval since its last run, or retry backoff after a failed
// attempt, or asked to run now), it is inside its hours, and the team pool has room. Of
// those, the one that has waited longest starts, since it came due or was asked for: a queue
// without one, where an agent waits once, however often it is asked for, and goes to the back
// when its shift starts. Agents that began waiting at once go by priority; an agent in hours
// it owns goes ahead of them, and the human's Run now goes first.
//
// The team pool is the only limit on what the agents use: team_pct of each day's seventh of
// Claude's weekly window (or of the week, with pacing weekly). Each enabled agent has a fair
// share of it, weight / sum of weights, and higher-priority agents get theirs first. An agent
// the human asks to run now goes ahead of them all. What an agent cannot use before the pool
// fills up again is free for those whose share is used (free.go).

type schedule struct {
	*Team
	now        time.Time
	usage      *Usage // what Claude reports now; without it the pool cannot be enforced
	week       time.Time
	hosts      map[string]HostState
	asked      map[string]bool // asked to run now
	askedAt    map[string]time.Time
	byHand     map[string]bool // of those, the ones the human asked for: they go first
	paused     map[string]bool // the agents the human paused
	only       bool            // only those may run: the team is paused
	pool, used float64         // the team pool and what was used of it in the period, in percent of the weekly window
	quota      map[string]float64
	usedBy     map[string]float64
	again      map[string]bool // can still start a shift before the period ends
	uses       map[string]*shiftUse
	canUse     map[string]float64 // what each agent can still use of its share before the period ends
	canWhy     map[string]string
	free       float64 // what no agent can use before the period ends: for those whose share is used
	period     string  // "today" or "this week"
	stats      map[string]Stats
}

// WeekGuessed says the budget week should follow Claude's window but, without usage data,
// starts on Monday.
func (c *Config) WeekGuessed(u *Usage) bool {
	return c.Week.Claude && c.Usage != "off" && (u == nil || u.ResetsAt.IsZero())
}

// Stats is one agent's record.
type Stats struct {
	Pct         float64 // weekly-window percent used this week
	Period      float64 // weekly-window percent used in the pacing period
	Runs        int     // this week: shifts that count as runs, and failed series
	Last        time.Time
	Failed      bool   // the last run was a failed series
	Reason      string // the organizer's verdict on the last attempt
	Attempts    int    // attempts of the series in progress
	LastAttempt time.Time
}

func (t *Team) schedule(now time.Time, u *Usage, hosts []HostState) *schedule {
	s := &schedule{Team: t, now: now.In(t.Zone.Location), usage: u, hosts: map[string]HostState{}, asked: t.Asked(), askedAt: t.askedAt(), byHand: t.askedByHand(), paused: t.PausedAgents(),
		quota: map[string]float64{}, usedBy: map[string]float64{}, again: map[string]bool{}, stats: map[string]Stats{},
		canUse: map[string]float64{}, canWhy: map[string]string{}}
	for _, h := range hosts {
		s.hosts[h.Name] = h
	}
	s.week = weekStart(t.Config, s.now, u)
	s.pool, s.period = t.TeamPct/7, "today"
	if t.Pacing == "weekly" {
		s.pool, s.period = t.TeamPct, "this week"
	}
	from := s.periodStart()
	for _, r := range t.Runs {
		if !r.Time.Before(from) {
			s.used += r.PctUsed
			s.usedBy[r.Agent] += r.PctUsed
		}
	}
	var sum float64
	for _, a := range t.Agents {
		if a.Enabled {
			sum += a.Weight
		}
	}
	for _, a := range t.Agents {
		if a.Enabled && sum > 0 {
			s.quota[a.Name] = s.pool * a.Weight / sum
			next := s.due(a)
			if next.Before(s.now) {
				next = s.now
			}
			next = s.startIn(a, next)
			s.again[a.Name] = !next.IsZero() && next.Before(s.periodEnd())
		}
	}
	s.uses = t.shiftUses(s.now)
	var keep float64
	for _, a := range t.Agents {
		if _, ok := s.quota[a.Name]; ok {
			s.canUse[a.Name], s.canWhy[a.Name] = s.canUseOf(a)
			keep += s.canUse[a.Name]
		}
	}
	s.free = max(0, s.pool-s.used-keep)
	return s
}

// weekStart is when the budget week began: Claude's rolling window when week_start is
// claude and usage data says where it is, else the configured weekday (Monday by default).
func weekStart(c *Config, now time.Time, u *Usage) time.Time {
	if c.Week.Claude && u != nil && !u.ResetsAt.IsZero() {
		return u.ResetsAt.Add(-7 * 24 * time.Hour)
	}
	day := c.Week.Day
	if c.Week.Claude {
		day = time.Monday
	}
	t := now.In(c.Zone.Location)
	back := (int(t.Weekday()) - int(day) + 7) % 7
	return time.Date(t.Year(), t.Month(), t.Day()-back, 0, 0, 0, 0, c.Zone.Location)
}

func (s *schedule) periodStart() time.Time {
	if s.Pacing == "weekly" {
		return s.week
	}
	return time.Date(s.now.Year(), s.now.Month(), s.now.Day(), 0, 0, 0, 0, s.Zone.Location)
}

// periodEnd is when the pool fills up again.
func (s *schedule) periodEnd() time.Time {
	if s.Pacing == "weekly" {
		return s.week.AddDate(0, 0, 7)
	}
	return s.periodStart().AddDate(0, 0, 1)
}

// stat reads an agent's history. A shift that counts as a run ends a series; any other
// outcome is an attempt, retried after retry_minutes (doubling) while the organizer judged
// a retry worthwhile and attempts are left, and otherwise one failed run. What an attempt
// used counts against the pool all the same.
func (s *schedule) stat(a *Agent) Stats {
	if st, ok := s.stats[a.Name]; ok {
		return st
	}
	var st Stats
	from := s.periodStart()
	for _, r := range sortRunsByTime(s.Runs) {
		if r.Agent != a.Name {
			continue
		}
		if !r.Time.Before(s.week) {
			st.Pct += r.PctUsed
		}
		if !r.Time.Before(from) {
			st.Period += r.PctUsed
		}
		if r.Status == "interrupted" && r.Session != "" && r.Retry == "yes" {
			continue // the organizer's stop: the shift resumes, and its end counts
		}
		if !r.IsRun() {
			st.Attempts++
			st.LastAttempt = r.Time
			if r.Retry != "no" && st.Attempts < s.MaxAttempts {
				continue
			}
		}
		st.Last, st.Failed, st.Reason, st.Attempts = r.Time, !r.IsRun(), r.Reason, 0
		if !r.Time.Before(s.week) {
			st.Runs++
		}
	}
	s.stats[a.Name] = st
	return st
}

// retryAt is when a series in progress may try again: retry_minutes after the last attempt,
// doubling with every attempt.
func (s *schedule) retryAt(st Stats) time.Time {
	wait := time.Duration(s.RetryMinutes * float64(time.Minute))
	for i := 1; i < st.Attempts; i++ {
		wait *= 2
	}
	return st.LastAttempt.Add(wait)
}

// due is when the agent's next shift or retry is due, before hours; zero when it never ran.
func (s *schedule) due(a *Agent) time.Time {
	st := s.stat(a)
	if st.Attempts > 0 {
		return s.retryAt(st)
	}
	if st.Last.IsZero() {
		return time.Time{}
	}
	if a.UntilDone && !st.Failed { // it runs when asked: by its own last shift, a teammate's, or the human; after a failed series, every applies
		if s.asked[a.Name] {
			return s.now
		}
		return s.now.AddDate(1, 0, 0)
	}
	return st.Last.Add(time.Duration(a.Every))
}

// idle says an until-done agent has nothing to do until something brings it work.
func (s *schedule) idle(a *Agent) bool {
	st := s.stat(a)
	return a.UntilDone && !s.asked[a.Name] && st.Attempts == 0 && !st.Last.IsZero() && !st.Failed
}

// lastQuarter is the end of a window an agent that owns its hours starts no shift in: so short
// a shift would hardly get past its brief.
const lastQuarter = 15 * time.Minute

// windows are the agent's hours; without any, the whole day.
func windows(a *Agent) Hours {
	if len(a.Hours) == 0 {
		return Hours{{0, 24 * 60}}
	}
	return a.Hours
}

const day, week = 24 * 60, 7 * 24 * 60

// weekSpans are the stretches of the week the agent's hours cover, in minutes from Sunday
// 00:00: each window on each of its days, one past midnight running into the next day.
func weekSpans(a *Agent) [][2]int {
	var spans [][2]int
	for d := time.Sunday; d <= time.Saturday; d++ {
		if !a.Days.has(d) {
			continue
		}
		for _, w := range windows(a) {
			n := w.End - w.Start
			if n <= 0 {
				n += day
			}
			spans = append(spans, [2]int{int(d)*day + w.Start, int(d)*day + w.Start + n})
		}
	}
	return spans
}

// ownedOverlap is the first of owners, enabled agents that own their hours, whose hours
// overlap a's, and where, such as "fri 07:00-08:00"; nil when a owns no hours or they
// overlap none. An hour of the week has one owner.
func ownedOverlap(a *Agent, owners []*Agent) (*Agent, string) {
	if !a.Enabled || !a.OwnHours {
		return nil, ""
	}
	for _, b := range owners {
		if b.Name == a.Name || !b.Enabled || !b.OwnHours {
			continue
		}
		for _, x := range weekSpans(a) {
			for _, y := range weekSpans(b) {
				for _, shift := range []int{-week, 0, week} { // a span past Saturday midnight wraps to Sunday
					if from, to := max(x[0], y[0]+shift), min(x[1], y[1]+shift); from < to {
						d := strings.ToLower(time.Weekday(from / day % 7).String()[:3])
						return b, d + " " + Window{from % day, (to-1)%day + 1}.String()
					}
				}
			}
		}
	}
	return nil, ""
}

// windowEnd is the end of the agent's window that t falls in, on one of its days; ok is false
// when t falls in none.
func (s *schedule) windowEnd(a *Agent, t time.Time) (end time.Time, ok bool) {
	t = t.In(s.Zone.Location)
	m := t.Hour()*60 + t.Minute()
	for _, w := range windows(a) {
		day := t.Day()
		if w.End <= w.Start && m < w.End { // past midnight: the window began the day before
			day--
		}
		start := time.Date(t.Year(), t.Month(), day, w.Start/60, w.Start%60, 0, 0, s.Zone.Location)
		if w.contains(m) && a.Days.has(start.Weekday()) {
			if w.End <= w.Start {
				day++
			}
			return time.Date(t.Year(), t.Month(), day, w.End/60, w.End%60, 0, 0, s.Zone.Location), true
		}
	}
	return time.Time{}, false
}

// inHours says whether a shift of the agent may start at t.
func (s *schedule) inHours(a *Agent, t time.Time) bool {
	end, ok := s.windowEnd(a, t)
	return ok && (!a.OwnHours || end.Sub(t) >= lastQuarter)
}

// ownedEnd is when the shift starting now must stop: the end of its window, for an agent that
// owns its hours and starts inside one.
func (s *schedule) ownedEnd(a *Agent) (time.Time, bool) {
	if !a.OwnHours {
		return time.Time{}, false
	}
	return s.windowEnd(a, s.now)
}

// startIn is t when a shift may start then, otherwise the start of the agent's next window.
func (s *schedule) startIn(a *Agent, t time.Time) time.Time {
	if t.IsZero() || s.inHours(a, t) {
		return t
	}
	t = t.In(s.Zone.Location)
	var best time.Time
	for _, w := range windows(a) {
		for d := 0; d < 8; d++ { // the next eight days hold a window of every weekday
			at := time.Date(t.Year(), t.Month(), t.Day()+d, w.Start/60, w.Start%60, 0, 0, s.Zone.Location)
			if at.After(t) && a.Days.has(at.Weekday()) && (best.IsZero() || at.Before(best)) {
				best = at
			}
		}
	}
	return best
}

// held is the part of the pool kept for agents of higher priority than a: what they can still
// use of their fair share before the period ends, as long as they can still run in it or were
// asked to, and are not paused. Nothing is held from an agent the human asked for.
func (s *schedule) held(a *Agent) float64 {
	if s.byHand[a.Name] {
		return 0
	}
	var h float64
	for _, b := range s.Agents {
		q, ok := s.quota[b.Name]
		if ok && b.Name != a.Name && b.Priority < a.Priority && q > s.usedBy[b.Name] && (s.again[b.Name] || s.asked[b.Name]) && !s.paused[b.Name] {
			h += s.canUse[b.Name]
		}
	}
	return h
}

// poolHold says why the pool keeps a from starting a shift now, or "" when it may.
func (s *schedule) poolHold(a *Agent) string {
	switch q, h := s.quota[a.Name], s.held(a); {
	case s.pool <= 0:
		return "the team pool is empty: team_pct is 0"
	case q <= 0:
		return "no share of the team pool: its weight is 0"
	case s.used >= s.pool:
		return fmt.Sprintf("team pool, %.1f%% used %s", s.OfPool(s.used), s.period)
	case s.usedBy[a.Name] >= q && q > 0 && s.borrows(a):
		return "" // on what is free
	case s.usedBy[a.Name] >= q && s.free >= 0.005 && q > 0:
		return fmt.Sprintf("fair share, %.1f of %.1f%% of the pool used %s; the %.1f%% the others cannot use is less than one of its shifts, about %.1f%%", s.OfPool(s.usedBy[a.Name]), s.OfPool(q), s.period, s.OfPool(s.free), s.OfPool(s.expected(a)))
	case s.usedBy[a.Name] >= q:
		return fmt.Sprintf("fair share, %.1f of %.1f%% of the pool used %s", s.OfPool(s.usedBy[a.Name]), s.OfPool(q), s.period)
	case s.used+h >= s.pool:
		return fmt.Sprintf("the rest of the pool is held for higher priority: %.1f%% used and %.1f%% held %s", s.OfPool(s.used), s.OfPool(h), s.period)
	}
	return ""
}

// hostReady says whether the agent's host (or, for any, one host) is online and has what
// the agent needs.
func (s *schedule) hostReady(a *Agent) (bool, string) {
	check := func(h HostState) string {
		if !h.Online {
			return "offline"
		}
		if m := append(append([]string{}, h.Problems...), missingGh(h.Caps, []string{a.GhUser})...); len(m) > 0 {
			return "lacks " + strings.Join(m, ", ")
		}
		return ""
	}
	if a.Host != "any" {
		h, known := s.hosts[a.Host]
		if !known {
			return false, "host " + a.Host + " never paired"
		}
		if why := check(h); why != "" {
			return false, "host " + a.Host + " " + why
		}
		return true, ""
	}
	var names, reasons []string
	for name := range s.hosts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		why := check(s.hosts[name])
		if why == "" {
			return true, ""
		}
		reasons = append(reasons, name+" "+why)
	}
	if len(names) == 0 {
		return false, "no host joined"
	}
	return false, "no host ready: " + strings.Join(reasons, "; ")
}

// eligible says whether the agent may start a shift now, and if not, why: the reason given
// is the longest wait.
func (s *schedule) eligible(a *Agent) (bool, string) {
	if !a.Enabled {
		return false, "disabled"
	}
	if s.paused[a.Name] && !s.byHand[a.Name] {
		return false, "paused"
	}
	if a.Host != "" {
		if ok, why := s.hostReady(a); !ok {
			return false, why
		}
	}
	if s.idle(a) {
		return false, "waiting for new work"
	}
	st := s.stat(a)
	poolWhy := "" // a pool hold lasts at least until the period ends
	if s.usage != nil {
		poolWhy = s.poolHold(a)
	} else if s.Usage != "off" {
		poolWhy = "the team pool needs Claude usage data"
	}
	waits := func(at time.Time, skipped bool) bool {
		return s.now.Before(at) && !skipped && (poolWhy == "" || !at.Before(s.periodEnd()))
	}
	asked, byHand := s.asked[a.Name], s.byHand[a.Name] // a request skips the interval; the human's the retry wait and the hours too
	switch due := s.startIn(a, s.due(a)); {
	case st.Attempts > 0 && waits(due, byHand):
		return false, fmt.Sprintf("retry %d/%d at %s", st.Attempts+1, s.MaxAttempts, due.Local().Format("Mon 15:04"))
	case st.Attempts == 0 && !st.Last.IsZero() && waits(due, asked) && st.Failed:
		return false, "failed, due " + due.Local().Format("Mon 15:04")
	case st.Attempts == 0 && !st.Last.IsZero() && waits(due, asked):
		return false, "due " + due.Local().Format("Mon 15:04")
	}
	if next := s.startIn(a, s.now); !s.inHours(a, s.now) && waits(next, byHand) && !s.resumesNow(a) {
		return false, "outside hours, next " + next.Local().Format("Mon 15:04")
	}
	if poolWhy != "" {
		return false, poolWhy
	}
	if b := s.waitsFor(a); b != "" {
		return false, "waits for " + b + ": its shift goes first"
	}
	free := ""
	if q := s.quota[a.Name]; s.usage != nil && q > 0 && s.usedBy[a.Name] >= q {
		free = fmt.Sprintf(", on the free part: its fair share is used, and the others cannot use %.1f%% of the pool before %s", s.OfPool(s.free), s.periodEnd().Local().Format("Mon 15:04"))
	}
	if _, ok := s.toResume(a.Name); ok && s.asked[a.Name] { // a running shift keeps its session too, unasked
		return true, "ready to resume its interrupted shift" + free
	}
	if st.Attempts > 0 {
		return true, fmt.Sprintf("ready, retry %d/%d", st.Attempts+1, s.MaxAttempts) + free
	}
	return true, "ready" + free
}

// resumesNow says the agent's interrupted shift may resume now, outside its hours: it began in
// them, and would still be running had the organizer not stopped it. A window the agent owns
// ends its shift all the same.
func (s *schedule) resumesNow(a *Agent) bool {
	r, ok := s.toResume(a.Name)
	return ok && s.asked[a.Name] && !a.OwnHours && s.now.Before(r.Began.Add(time.Duration(a.RunMinutes)*time.Minute))
}

// waitsFor is the first of the agents a waits for (wait_for) that would start a shift now, as pick
// picks, so a holds back until it has had it; "" when none would. One that waits for a, itself or
// through others, is not waited for: the two would hold each other back forever.
func (s *schedule) waitsFor(a *Agent) string {
	for _, name := range a.WaitFor {
		b := s.Agent(name)
		if b == nil || b.Name == a.Name || s.waitsOn(b, a.Name, map[string]bool{}) {
			continue
		}
		if ok, _ := s.eligible(b); ok && (!s.only || s.byHand[b.Name]) {
			return b.Name
		}
	}
	return ""
}

// waitsOn says whether b waits for the named agent, itself or through those it waits for.
func (s *schedule) waitsOn(b *Agent, name string, seen map[string]bool) bool {
	if seen[b.Name] {
		return false
	}
	seen[b.Name] = true
	for _, n := range b.WaitFor {
		if n == name {
			return true
		}
		if c := s.Agent(n); c != nil && s.waitsOn(c, name, seen) {
			return true
		}
	}
	return false
}

// nextRun is the earliest moment the agent could start again, or a note when that is not
// a time: "next tick", "disabled", "when host linux1 joins".
func (s *schedule) nextRun(a *Agent) (time.Time, string) {
	if !a.Enabled {
		return time.Time{}, "disabled"
	}
	if s.paused[a.Name] && !s.byHand[a.Name] {
		return time.Time{}, "when resumed"
	}
	if a.Host != "" {
		if ok, why := s.hostReady(a); !ok {
			return time.Time{}, "when " + strings.TrimPrefix(why, "no host ready: ")
		}
	}
	if s.idle(a) {
		return time.Time{}, "when there is new work"
	}
	if b := s.waitsFor(a); b != "" {
		return time.Time{}, "after " + b + "'s shift"
	}
	var at time.Time
	if !s.byHand[a.Name] && (!s.asked[a.Name] || s.stat(a).Attempts > 0) { // as eligible skips them
		if due := s.due(a); due.After(s.now) {
			at = due
		}
	}
	switch {
	case s.usage != nil && s.pool <= 0:
		return time.Time{}, "when team_pct is more than 0"
	case s.usage != nil && s.quota[a.Name] <= 0:
		return time.Time{}, "when it has a share of the team pool: its weight is 0"
	case s.usage != nil && s.poolHold(a) != "" && s.periodEnd().After(at):
		at = s.periodEnd()
	case s.usage == nil && s.Usage != "off":
		return time.Time{}, "when Claude usage data is back"
	}
	if !s.byHand[a.Name] && !s.resumesNow(a) {
		if at.IsZero() && !s.inHours(a, s.now) {
			at = s.startIn(a, s.now)
		} else {
			at = s.startIn(a, at)
		}
	}
	if at.IsZero() {
		return time.Time{}, "next tick"
	}
	return at, ""
}

// pick is the agent whose shift starts now, or nil. While the team is paused, only one the
// human asked for may start.
func (s *schedule) pick() *Agent {
	var ready []*Agent
	for _, a := range s.Agents { // in priority order: those that began waiting at once keep it
		if ok, _ := s.eligible(a); ok && (!s.only || s.byHand[a.Name]) {
			ready = append(ready, a)
		}
	}
	owns := func(a *Agent) bool { return a.OwnHours && s.inHours(a, s.now) }
	sort.SliceStable(ready, func(i, j int) bool { return s.since(ready[i]).Before(s.since(ready[j])) })
	sort.SliceStable(ready, func(i, j int) bool { return owns(ready[i]) && !owns(ready[j]) })
	sort.SliceStable(ready, func(i, j int) bool { return s.byHand[ready[i].Name] && !s.byHand[ready[j].Name] })
	if len(ready) == 0 {
		return nil
	}
	return ready[0] // the human's requests first, then an agent in hours it owns, then the one that waited longest
}

// since is when the agent began waiting: when it came due (never, for one that never ran), or
// when it was asked for, if that was earlier. Its shift moves both on: a new last run, and its
// request answered. The human's requests go in the order the human made them.
func (s *schedule) since(a *Agent) time.Time {
	if s.byHand[a.Name] {
		return s.askedAt[a.Name]
	}
	at := s.due(a)
	if r, ok := s.askedAt[a.Name]; ok && r.Before(at) {
		at = r
	}
	return at
}

// shareText is the agent's fair share, as its brief says it.
func (s *schedule) shareText(a *Agent) string {
	q, ok := s.quota[a.Name]
	switch {
	case !ok:
		return "none"
	case s.Pacing == "weekly":
		return fmt.Sprintf("%.1f%% of the team pool, %.2f%% of the subscription's weekly window, this week", s.OfPool(q), q)
	}
	return fmt.Sprintf("%.1f%% of the team pool, %.2f%% of the subscription's weekly window, per day", s.OfPool(q), q)
}

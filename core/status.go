package core

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// Status is the team at one moment: what rota status prints, what the organizer keeps in
// its state file, and what the console, the web pages and the menu bar show.
type Status struct {
	Time       time.Time     `json:"time"`
	Pid        int           `json:"pid,omitempty"` // the organizer's; 0 when it is not running
	StartedAt  time.Time     `json:"started_at,omitempty"`
	Paused     bool          `json:"paused"`
	Listen     string        `json:"listen,omitempty"`
	Usage      *Usage        `json:"usage,omitempty"`
	UsageError string        `json:"usage_error,omitempty"`
	Pool       Pool          `json:"pool"`
	WeekSince  time.Time     `json:"week_since"`
	Agents     []AgentStatus `json:"agents"` // the running agent first, then as the team lists them
	Hosts      []HostState   `json:"hosts"`
	Next       string        `json:"next,omitempty"`
	NextTick   time.Time     `json:"next_tick,omitempty"` // the organizer's next look
	Running    *Running      `json:"running,omitempty"`
	Stale      bool          `json:"-"` // the organizer is alive but has not written for a while
}

type Pool struct {
	Used     float64   `json:"used"`
	Quota    float64   `json:"quota"`
	Period   string    `json:"period"`
	Enforced bool      `json:"enforced"`
	TeamPct  float64   `json:"team_pct"`
	Pacing   string    `json:"pacing"`
	Free     float64   `json:"free"`  // what no agent can use before it fills up again: for those whose share is used
	Fills    time.Time `json:"fills"` // when it fills up again
}

type AgentStatus struct {
	Name       string    `json:"name"`
	Host       string    `json:"host,omitempty"`
	Model      string    `json:"model,omitempty"`
	Effort     string    `json:"effort,omitempty"`
	Enabled    bool      `json:"enabled"`
	Paused     bool      `json:"paused,omitempty"` // by the human, until resumed
	Priority   int       `json:"priority"`
	Weight     float64   `json:"weight"`
	Runs       int       `json:"runs"`       // this week
	PeriodPct  float64   `json:"period_pct"` // weekly-window percent used in the pacing period
	QuotaPct   float64   `json:"quota_pct"`  // its fair share of the team pool, same unit
	CanUse     float64   `json:"can_use"`    // what it can still use of its share before the pool fills up again
	CanUseWhy  string    `json:"can_use_why,omitempty"`
	WeekPct    float64   `json:"week_pct"`
	LastRun    time.Time `json:"last_run,omitempty"`
	LastStatus string    `json:"last_status,omitempty"`
	State      string    `json:"state"`
	NextRun    time.Time `json:"next_run,omitempty"`
	NextNote   string    `json:"next_note,omitempty"` // when the next run is not a time
	Disk       int64     `json:"disk,omitempty"`      // its work folder in bytes, as measured after its last shift
	DiskAt     time.Time `json:"disk_at,omitempty"`   // when; zero: never measured
	DiskCap    int       `json:"disk_cap"`            // GB; 0: no cap
}

// DiskText is its work folder against its cap: "12.3 / 100 GB", "– / 100 GB" before it was
// measured, "12.3 GB" without a cap.
func (a AgentStatus) DiskText() string {
	held := "–"
	if !a.DiskAt.IsZero() {
		held = fmt.Sprintf("%.1f", float64(a.Disk)/1e9)
	}
	if a.DiskCap == 0 {
		if a.DiskAt.IsZero() {
			return "–"
		}
		return held + " GB"
	}
	return fmt.Sprintf("%s / %d GB", held, a.DiskCap)
}

// OverCap says whether its work folder held more than its cap when last measured.
func (a AgentStatus) OverCap() bool { return overCap(a.Disk, a.DiskCap) }

type Running struct {
	Agent string    `json:"agent"`
	Host  string    `json:"host"`
	Since time.Time `json:"since"`
	Live  string    `json:"live,omitempty"` // what it is doing: its last tool call
}

// Status is the team now, as the front ends show it: the schedule computed from the files,
// with the running organizer's own state merged in (its process, the running shift, its
// next look, the hosts it sees). Claude's usage comes from the organizer's snapshot while it
// runs, else from Claude, a reading up to a minute old reused.
func (t *Team) Status(now time.Time) Status {
	live := t.Live()
	var u *Usage
	uerr := ""
	switch {
	case live != nil:
		u, uerr = live.Usage, live.UsageError
	case usageEnabled(t.Config):
		var err error
		if u, err = fetchUsage(t.Config, 60, 0); err != nil {
			u, uerr = t.lastUsage(), err.Error() // the last reading still shows, with why there is no newer one
		}
	}
	hosts := knownHosts(t.Config)
	if live != nil {
		hosts = live.Hosts
	}
	s := t.schedule(now, u, hosts)
	s.only = t.Paused() // as the organizer picks
	st := t.status(s)
	st.UsageError = uerr
	if live != nil {
		st.Pid, st.StartedAt, st.Running, st.NextTick, st.Stale, st.Listen = live.Pid, live.StartedAt, live.Running, live.NextTick, live.Stale, live.Listen
	}
	if r := st.Running; r != nil { // the agent at work leads the list; the others keep their order
		if i := slices.IndexFunc(st.Agents, func(a AgentStatus) bool { return a.Name == r.Agent }); i > 0 {
			a := st.Agents[i]
			st.Agents = slices.Insert(slices.Delete(st.Agents, i, i+1), 0, a)
		}
	}
	return st
}

// heldText is what the front ends say while no shift can start: usage is on and Claude's
// usage cannot be read.
const heldText = "no shift starts until Claude's usage can be read; wait for it, or set usage to off"

// Held says why the running organizer starts no shift although agents may be due; "" when
// nothing holds the team, or it is paused or silent, which say so themselves. UsageError
// says what failed.
func (st Status) Held() string {
	if st.Pid == 0 || st.Stale || st.Paused || st.UsageError == "" {
		return ""
	}
	return heldText
}

// RunAsk is what the human's request to run an agent now does, as the organizer decides it.
type RunAsk struct {
	Why   string // why the agent cannot start: "" when it may
	Next  string // then, when it can start, as people read it
	After string // when it may: the agent whose shift starts, or runs, first; "" when it starts at once
}

// RunWait is what the human's request to run the named agent now does, by the given status.
func (t *Team) RunWait(name string, st Status) RunAsk { return t.RunWaits(st)[name] }

// RunWaits is RunWait for every agent, by one look at the team, each as if the human asked for
// it alone: whether it may start, as eligible says, and whether it starts first, as pick picks,
// one shift at a time; while the organizer is on hold, nothing starts.
func (t *Team) RunWaits(st Status) map[string]RunAsk {
	s := t.schedule(time.Now(), st.Usage, st.Hosts)
	s.only = st.Paused // as the organizer picks
	out := map[string]RunAsk{}
	for _, a := range t.Agents {
		if st.Pid != 0 && st.UsageError != "" { // as the organizer's look: a reading that fails starts nothing, paused or not
			out[a.Name] = RunAsk{Why: "the organizer is on hold: " + heldText, Next: "once Claude's usage can be read"}
			continue
		}
		asked, byHand := s.asked[a.Name], s.byHand[a.Name]
		when, had := s.askedAt[a.Name]
		s.asked[a.Name], s.byHand[a.Name] = true, true
		if !byHand {
			s.askedAt[a.Name] = s.now // the human's request is timed when it is made: after those made before
		}
		if ok, why := s.eligible(a); !ok {
			at, note := s.nextRun(a)
			out[a.Name] = RunAsk{Why: why, Next: AgentStatus{NextRun: at, NextNote: note}.NextText(st.NextTick)}
		} else if st.Running != nil {
			out[a.Name] = RunAsk{After: st.Running.Agent}
		} else if p := s.pick(); p != nil && p.Name != a.Name { // a request the human made before goes first
			out[a.Name] = RunAsk{After: p.Name}
		} else {
			out[a.Name] = RunAsk{}
		}
		s.asked[a.Name], s.byHand[a.Name] = asked, byHand
		if had {
			s.askedAt[a.Name] = when
		} else {
			delete(s.askedAt, a.Name)
		}
	}
	return out
}

// WhyWaits says, for each agent that does not work now, why it does not run now, in a sentence
// for the pages to show under its line, by the organizer's own reasons and the given status, the
// team as it is now. It names none where its line says it: off, paused, waiting for new work or
// for its interval, or for the retry of a run that failed; nor while the team is not running or
// its organizer is silent, which the team's header says.
func (t *Team) WhyWaits(st Status) map[string]string {
	at := func(tm time.Time) string { return tm.Local().Format("Mon 15:04") }
	askedAt, byHand := t.askedAt(), t.askedByHand()
	next := st.Next // who starts next: after the running shift, not the running agent again
	if st.Running != nil {
		next = t.nextAfter(st)
	}
	out := map[string]string{}
	for _, a := range st.Agents {
		if st.Pid == 0 || st.Stale || st.Running != nil && st.Running.Agent == a.Name || !a.Enabled || a.Paused && !byHand[a.Name] {
			continue
		}
		why, s := "", a.State
		switch {
		case strings.HasPrefix(s, "ready"):
			switch {
			case st.UsageError != "": // the organizer starts nothing then, paused or not
				why = "Ready, but nothing starts until Claude's usage can be read."
			case st.Paused && !byHand[a.Name]:
				why = "Ready, but the team is paused: it starts once you resume the team, or with Run now."
			case st.Running != nil && (next == "" || next == a.Name):
				why = "Ready: it starts when " + st.Running.Agent + "'s shift ends, as one shift runs at a time."
			case st.Running != nil:
				why = "Ready: when " + st.Running.Agent + "'s shift ends, " + next + " starts first, as one shift runs at a time."
			case next != "" && next != a.Name:
				why = "Ready: " + next + " starts first, as one shift runs at a time."
			case st.NextTick.After(time.Now().Add(time.Minute)) && askedAt[a.Name].IsZero(): // a request wakes the organizer at once
				why = "Ready: it starts at the organizer's next look, " + st.NextTick.Local().Format("15:04") + "."
			default:
				why = "Starting now."
			}
		case strings.HasPrefix(s, "outside hours"):
			why = "Outside its hours."
			if h := t.hoursOf(a.Name); h != "" {
				why = "Outside its hours: it may start only " + h + "."
			}
		case strings.HasPrefix(s, "team pool,"):
			why = fmt.Sprintf("The team pool is used up: %.1f%% of it %s. It fills again %s.", st.Pool.Of(st.Pool.Used), st.Pool.Period, at(st.Pool.Fills))
		case strings.HasPrefix(s, "fair share,"):
			why = fmt.Sprintf("Its share of the team pool is used up: %.1f of its %.1f%% %s. It runs again when the pool fills, %s, or sooner with a larger weight.",
				st.Pool.Of(a.PeriodPct), st.Pool.Of(a.QuotaPct), st.Pool.Period, at(st.Pool.Fills))
		case strings.HasPrefix(s, "the rest of the pool is held"):
			why = "The rest of the team pool is kept for higher-priority agents that still have work. It starts once they are done, or when the pool fills again, " + at(st.Pool.Fills) + "."
		case strings.HasPrefix(s, "no share of the team pool"):
			why = "It has no share of the team pool: its weight is 0."
		case strings.HasPrefix(s, "the team pool is empty"):
			why = "The team pool is empty: the team's budget is 0%."
		case s == "the team pool needs Claude usage data":
			why = "It waits for Claude's usage to be read."
		case strings.HasPrefix(s, "host ") || strings.HasPrefix(s, "no host"):
			why = "Its host is not ready: " + s + "."
		}
		if when, ok := askedAt[a.Name]; ok { // a run asked for that has not started: what holds it
			who := "A teammate"
			if byHand[a.Name] {
				who = "You"
			}
			if why == "" {
				why = "It waits: " + s + "."
			}
			why = who + " asked it to run at " + when.Local().Format("15:04") + ". " + why
		}
		if why != "" {
			out[a.Name] = why
		}
	}
	return out
}

// nextAfter is the agent that starts when the running shift ends, if the team stays as it is: the
// organizer's pick without the running agent; "" for none.
func (t *Team) nextAfter(st Status) string {
	s := t.schedule(time.Now(), st.Usage, st.Hosts)
	s.only = st.Paused // as the organizer picks
	if r := st.Running; r != nil {
		s.paused[r.Agent], s.byHand[r.Agent] = true, false
	}
	if a := s.pick(); a != nil {
		return a.Name
	}
	return ""
}

// hoursOf is when the agent may start, as its settings say: its hours, and its days if any; ""
// for any time.
func (t *Team) hoursOf(name string) string {
	a := t.Agent(name)
	if a == nil {
		return ""
	}
	get := func(key string) string {
		if f, ok := AgentField(key); ok {
			return strings.TrimSpace(f.Get(a))
		}
		return ""
	}
	h := get("hours")
	if h == "any" {
		h = ""
	}
	if d := get("days"); d != "" {
		h = strings.TrimSpace(h + " on " + d)
	}
	return h
}

func (t *Team) status(s *schedule) Status {
	st := Status{Time: time.Now(), Paused: t.Paused(), WeekSince: s.week, Usage: s.usage,
		Pool: Pool{Used: s.used, Quota: s.pool, Period: s.period, Enforced: s.usage != nil, TeamPct: t.TeamPct, Pacing: t.Pacing, Free: s.free, Fills: s.periodEnd()}}
	for name, h := range s.hosts {
		h.Name = name
		st.Hosts = append(st.Hosts, h)
	}
	sort.Slice(st.Hosts, func(i, j int) bool { return st.Hosts[i].Name < st.Hosts[j].Name })
	for _, a := range t.Agents {
		x := s.stat(a)
		as := AgentStatus{Name: a.Name, Host: a.Host, Model: a.Model, Effort: a.Effort, Enabled: a.Enabled, Paused: s.paused[a.Name],
			Priority: a.Priority, Weight: a.Weight, Runs: x.Runs, PeriodPct: x.Period, QuotaPct: s.quota[a.Name], WeekPct: x.Pct, LastRun: x.Last,
			CanUse: s.canUse[a.Name], CanUseWhy: s.canWhy[a.Name], DiskCap: a.DiskGB}
		if d, ok := t.DiskOf(a.Name); ok {
			as.Disk, as.DiskAt = d.Bytes, d.At
		}
		for _, r := range t.Runs {
			if r.Agent == a.Name && !r.Time.Before(x.Last) {
				as.LastStatus = r.Status
			}
		}
		if s.usage == nil { // the pool is not enforced: nothing is freed
			as.CanUse, as.CanUseWhy = max(0, as.QuotaPct-as.PeriodPct), ""
		}
		_, as.State = s.eligible(a)
		as.NextRun, as.NextNote = s.nextRun(a)
		st.Agents = append(st.Agents, as)
	}
	if a := s.pick(); a != nil {
		st.Next = a.Name
	}
	return st
}

// Organizer says how the organizer is: not running, silent, on hold, paused or running.
func (st Status) Organizer() string {
	switch {
	case st.Pid == 0:
		return "not running"
	case st.Stale:
		return fmt.Sprintf("running (pid %d) but silent since %s; was the machine asleep?", st.Pid, st.Time.Local().Format("Mon 15:04"))
	case st.Paused:
		return "paused"
	case st.Held() != "":
		return "on hold: " + st.Held()
	}
	return "running since " + st.StartedAt.Local().Format("Mon 15:04")
}

// NextText is when the agent runs next, as people read it.
func (a AgentStatus) NextText(tick time.Time) string {
	switch {
	case !a.NextRun.IsZero():
		return a.NextRun.Local().Format("Mon 15:04")
	case a.NextNote == "next tick" && !tick.IsZero():
		return "next tick " + tick.Local().Format("15:04")
	}
	return a.NextNote
}

// ModelEffort is "opus/xhigh", with "default" for what is not set.
func (a AgentStatus) ModelEffort() string {
	or := func(s string) string {
		if s == "" {
			return "default"
		}
		return s
	}
	return or(a.Model) + "/" + or(a.Effort)
}

func (h HostState) String() string {
	switch m := append(append([]string{}, h.Problems...), h.MissingGh...); {
	case h.Online && len(m) > 0:
		return h.Name + " online, missing " + strings.Join(m, ", ")
	case h.Online && h.Busy:
		return h.Name + " online, running a shift"
	case h.Online:
		return h.Name + " online, ready"
	}
	return h.Name + " offline, last seen " + h.LastSeen.Local().Format("Mon 15:04")
}

// FreeText says what of the pool no agent can use before it fills up again; "" when none is.
func (p Pool) FreeText() string {
	if !p.Enforced || p.Free < 0.005 {
		return ""
	}
	return fmt.Sprintf("%.1f%% of the pool that the agents cannot use before %s: an agent whose fair share is used runs on it", p.Of(p.Free), p.Fills.Local().Format("Mon 15:04"))
}

// Frees is what of its fair share the agent cannot use before the pool fills up again: it is
// free for those whose share is used.
func (a AgentStatus) Frees() float64 { return max(0, a.QuotaPct-a.PeriodPct-a.CanUse) }

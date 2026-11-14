package tray

// The menu's words: short, as a menu bar reads, from the same state the inbox shows.

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"rota/core"
)

// when is a time as the menu says it: "15:04" today, "tomorrow 15:04", "yesterday 15:04",
// "Mon 15:04" within the week around today, "2 Jan 15:04" beyond.
func when(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	day := func(d time.Time) string { return d.Format("2006-01-02") }
	switch {
	case day(t) == day(now):
		return t.Format("15:04")
	case day(t) == day(now.AddDate(0, 0, 1)):
		return "tomorrow " + t.Format("15:04")
	case day(t) == day(now.AddDate(0, 0, -1)):
		return "yesterday " + t.Format("15:04")
	case t.After(now.AddDate(0, 0, -6)) && t.Before(now.AddDate(0, 0, 6)):
		return t.Format("Mon 15:04")
	}
	return t.Format("2 Jan 15:04")
}

// minutes is how long something has run: "just started", "12 min", "1 h 5 min".
func minutes(d time.Duration) string {
	m := int(d.Minutes())
	switch {
	case m < 1:
		return "just started"
	case m < 60:
		return fmt.Sprintf("%d min", m)
	}
	return fmt.Sprintf("%d h %d min", m/60, m%60)
}

// look is the dot an agent shows: the state the inbox gives it, or off, an empty ring, when it
// is turned off, paused, or the team is not running.
func look(m core.Member, enabled, paused bool, live *core.Status) string {
	switch {
	case !enabled || paused || live == nil:
		return "off"
	case m.State == "working" || m.State == "needs" || m.State == "failed" || m.State == "done":
		return m.State
	}
	return "idle"
}

// doing is what an agent's line says after its name: what it does now, or when it runs next.
// "" when there is nothing to say: the team is not running, which the menu's first line says.
func doing(m core.Member, as core.AgentStatus, enabled, paused bool, live *core.Status, now time.Time) string {
	switch {
	case !enabled:
		return "off"
	case paused:
		return "paused"
	case live == nil:
		return ""
	case m.State == "working" && live.Running != nil:
		return "working · " + minutes(now.Sub(live.Running.Since))
	case m.State == "needs":
		return lowerFirst(m.Line) // "asks you 2 things"
	case m.State == "failed" && !as.NextRun.IsZero():
		return "failed · next " + when(as.NextRun, now)
	case m.State == "failed":
		return "failed"
	case m.State == "done":
		return "finished " + when(as.LastRun, now)
	case strings.HasPrefix(as.State, "ready"):
		return "ready to run"
	case !as.NextRun.IsZero() && !as.NextRun.After(now):
		return "due since " + when(as.NextRun, now)
	case !as.NextRun.IsZero():
		return "next " + when(as.NextRun, now)
	case as.NextNote == "next tick" && !live.NextTick.IsZero():
		return "next " + when(live.NextTick, now)
	}
	return as.NextNote
}

// agentTitle is an agent's line in the menu: "maintainer — next 09:07".
func agentTitle(name, doing string) string {
	if doing == "" {
		return name
	}
	return name + " — " + doing
}

// status is the menu's first line: what the team does now.
func status(live *core.Status, problem string, now time.Time) string {
	switch {
	case problem != "":
		return problem
	case live == nil:
		return "Not running"
	case live.Running != nil:
		s := live.Running.Agent + " is working · " + minutes(now.Sub(live.Running.Since))
		if h := live.Running.Host; h != "" && h != "local" {
			s += " on " + h
		}
		return s
	case live.Held() != "":
		return "On hold: " + live.Held()
	case live.Paused:
		return "Paused: no new shift starts"
	}
	var next core.AgentStatus
	for _, a := range live.Agents {
		if a.Enabled && !a.Paused && !a.NextRun.IsZero() && (next.NextRun.IsZero() || a.NextRun.Before(next.NextRun)) {
			next = a
		}
	}
	if next.Name == "" {
		return "Running"
	}
	if !next.NextRun.After(now) {
		return "Next: " + next.Name + " · due now"
	}
	return "Next: " + next.Name + " · " + when(next.NextRun, now)
}

// budget is the menu's second line: how much of the team's budget is used. "" while the team is
// not running.
func budget(live *core.Status) string {
	switch {
	case live == nil:
		return ""
	case !live.Pool.Enforced:
		return "Team budget: not enforced without Claude's usage data"
	}
	return fmt.Sprintf("Team budget: %.0f%% used %s", live.Pool.Of(live.Pool.Used), live.Pool.Period)
}

// barTitle is the text beside the icon in the menu bar: the questions waiting, then who works or
// why nothing does; nothing at all while the team just waits for its next shift.
func barTitle(questions int, live *core.Status) string {
	s := ""
	if questions > 0 {
		s = fmt.Sprintf(" ?%d", questions)
	}
	switch {
	case live == nil:
		s += " off"
	case live.Running != nil:
		s += " " + live.Running.Agent
	case live.Held() != "":
		s += " on hold"
	case live.Paused:
		s += " paused"
	}
	return s
}

// share is an agent's use of the team's budget, "" when it has no share.
func share(as core.AgentStatus, live *core.Status) string {
	if live == nil || as.QuotaPct <= 0 {
		return ""
	}
	return fmt.Sprintf("Used %.1f%% of its %.1f%% share %s", live.Pool.Of(as.PeriodPct), live.Pool.Of(as.QuotaPct), live.Pool.Period)
}

// lastRun is an agent's last run: "Last run 07:07 · ok", "" before its first.
func lastRun(as core.AgentStatus, now time.Time) string {
	if as.LastRun.IsZero() {
		return ""
	}
	s := "Last run " + when(as.LastRun, now)
	if as.LastStatus != "" {
		s += " · " + as.LastStatus
	}
	return s
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToLower(r)) + s[n:]
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

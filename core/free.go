package core

import (
	"fmt"
	"math"
	"time"
)

// What an agent cannot use before the pool fills up again is free: an agent whose fair share is
// used may run on it, so no agent starves while another's share goes unused. The shares
// themselves never change. At every look the organizer estimates what each agent can still
// use before the period ends: the shifts its every and hours still allow, each as much as its
// shifts used lately with a quarter to spare, and never more than what is left of its share.
// The shifts it may still run are as many as its schedule allows, or as it ran lately at the
// same pace, whichever is more. Every estimate errs toward the agent whose share it is: an agent
// that works until done, runs after other agents' shifts, has too few shifts to judge, or runs as
// often as it is asked keeps all of what is left; a paused one frees it all. The team pool stays
// the only hard limit.

const freeFloor = 0.1 // percent of the weekly window a shift counts at least: Claude measures whole percents

// shiftUse is what an agent's shifts used in the last balanceDays.
type shiftUse struct {
	pct        float64 // percent of the weekly window, estimated from its tokens
	shifts     int     // the shifts that ran; failed attempts count in pct only
	first      time.Time
	ranLongAgo bool // it ran before the history: the whole history counts
}

// perShift is what one of its shifts used.
func (u *shiftUse) perShift() float64 { return u.pct / float64(max(1, u.shifts)) }

// shiftUses reads the last balanceDays of spend.csv. Claude reports whole percents, so one
// shift usually measures 0 or 1: what a shift used is its tokens' part of what all agents on
// the same model used, each kind weighted as Claude's prices weigh them. A shift that recorded
// no tokens (one stopped before its result) keeps what it measured: shared by tokens, it would
// go to the others.
func (t *Team) shiftUses(now time.Time) map[string]*shiftUse {
	since := now.AddDate(0, 0, -balanceDays)
	uses, tokens, own := map[string]*shiftUse{}, map[string]float64{}, map[string]float64{}
	groupPct, groupTok := map[string]float64{}, map[string]float64{}
	for _, r := range t.Runs {
		a := t.Agent(r.Agent)
		if a == nil || r.Time.After(now) {
			continue
		}
		u := uses[a.Name]
		if u == nil {
			u = &shiftUse{}
			uses[a.Name] = u
		}
		if r.Time.Before(since) {
			u.ranLongAgo = true
			continue
		}
		tok := float64(r.In) + 5*float64(r.Out) + 1.25*float64(r.CacheWrite) + 0.1*float64(r.CacheRead)
		u.pct = u.pct + r.PctUsed
		if tok == 0 {
			own[a.Name] += r.PctUsed
		} else {
			tokens[a.Name] = tokens[a.Name] + tok
			groupPct[a.Model], groupTok[a.Model] = groupPct[a.Model]+r.PctUsed, groupTok[a.Model]+tok
		}
		if r.IsRun() { // what failed attempts used counts, spread over the shifts that ran
			u.shifts++
		}
		if u.first.IsZero() || r.Time.Before(u.first) {
			u.first = r.Time
		}
	}
	for name, u := range uses {
		if a := t.Agent(name); a != nil && groupTok[a.Model] > 0 {
			u.pct = groupPct[a.Model]*tokens[name]/groupTok[a.Model] + own[name]
		}
	}
	return uses
}

// shiftsLeft is how many shifts the agent's every and hours still allow before the period ends,
// counting one now when it is due or asked for; -1 when there is no telling: it runs when asked,
// after other agents' shifts, or as often as it may.
func (s *schedule) shiftsLeft(a *Agent) int {
	every := time.Duration(a.Every)
	if a.UntilDone || len(a.After) > 0 || every <= 0 {
		return -1
	}
	at := s.due(a)
	if s.asked[a.Name] || at.Before(s.now) {
		at = s.now
	}
	end, n := s.periodEnd(), 0
	for at = s.startIn(a, at); !at.IsZero() && at.Before(end) && n < 7*24*60; at = s.startIn(a, at.Add(every)) {
		n++ // at most one a minute: the organizer looks no more often
	}
	return n
}

// expected is what one of the agent's shifts is expected to use, with a quarter to spare.
func (s *schedule) expected(a *Agent) float64 {
	u := s.uses[a.Name]
	if u == nil {
		return freeFloor
	}
	return max(freeFloor, u.perShift()*balanceMargin)
}

// canUseOf is what the agent can still use of its share before the period ends, and why it
// keeps what it keeps.
func (s *schedule) canUseOf(a *Agent) (float64, string) {
	left := max(0, s.quota[a.Name]-s.usedBy[a.Name])
	u, n, end := s.uses[a.Name], s.shiftsLeft(a), s.periodEnd()
	switch {
	case left == 0:
		return 0, ""
	case s.paused[a.Name] && !s.byHand[a.Name]:
		return 0, "paused"
	case a.UntilDone:
		return left, "it works until done"
	case len(a.After) > 0:
		return left, "it runs after other agents' shifts"
	case n < 0:
		return left, "it runs as often as it may"
	case u == nil || u.shifts < balanceShifts:
		return left, "too few shifts to judge"
	}
	days := float64(balanceDays)
	if !u.ranLongAgo {
		days = s.now.Sub(u.first).Hours() / 24
	}
	pace := float64(u.shifts) / max(1, days) * end.Sub(s.now).Hours() / 24 // as many as it ran lately
	shifts := math.Ceil(max(float64(n), pace))
	if shifts == 0 {
		return 0, "no shift before " + end.Local().Format("Mon 15:04")
	}
	return min(left, shifts*s.expected(a)), fmt.Sprintf("%s before %s, about %.1f%% of the pool each", plural(int(shifts), "more shift"), end.Local().Format("Mon 15:04"), s.OfPool(s.expected(a)))
}

// borrows says whether an agent whose fair share is used may run on what is free: its shift is
// expected to fit in what no other agent can use before the period ends.
func (s *schedule) borrows(a *Agent) bool {
	return s.usage != nil && s.free >= s.expected(a)
}

package core

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// A rebalance proposes each enabled agent's weight from how it used the pool lately, for the
// human to change and then apply. An agent's need is what one of its shifts uses times the
// shifts it runs a week: as many as it ran, or as its every and hours allow, whichever is more.
// An until-done agent works for as long as it has share, so it takes what the others leave.
// An agent with too few shifts to judge keeps its share.
//
// Claude reports whole percents, so one shift usually measures 0 or 1. What a shift used is
// therefore its tokens' part of what all agents on the same model used, weighting each kind of
// token as Claude's prices do: output five times input, writing the cache a quarter more than
// input, reading it a tenth. That estimate moves a little with every shift that lands, for every
// agent at once, so a share that is about what the agent needs is kept as it is: a proposal
// made twice says the same, and one that is applied and made again says the same too. For that
// the proposal never depends on the weights it replaces, other than to keep a share.

const (
	balanceDays   = 14   // the history a proposal looks at
	balanceShifts = 3    // an agent with fewer shifts in it keeps its share
	balanceMargin = 1.25 // a need gets a quarter more, so that a busy week does not hold the agent
	balanceFloor  = 1.0  // no proposed share below 1% of the weekly window a week: the step Claude measures in
	balanceBand   = 0.15 // a share within this part of what the agent needs is kept: the estimate's noise
)

// Proposal is a rebalance of the enabled agents' weights. A proposed weight is the agent's
// share of the pool in percent, so the weights add up to 100.
type Proposal struct {
	Pool      float64   // the team pool, in percent of the weekly window a week
	Since     time.Time // the history it looked at
	MinShifts int       // an agent with fewer shifts in it keeps its share
	Agents    []Balance // the enabled agents, in the team's order
	Note      string    // about the proposal as a whole, when needed
}

// Balance is one agent's part of a proposal.
type Balance struct {
	Name     string
	Weight   float64 // its weight now
	Share    float64 // its share of the pool now, in percent
	Proposed float64 // the weight proposed
	Shifts   int     // its shifts in the history
	PerShift float64 // percent of the weekly window one shift used, estimated
	PerWeek  float64 // shifts a week: as many as it ran, or as its schedule allows
	Need     float64 // percent of the weekly window a week: PerShift × PerWeek
	Rest     bool    // it works until done, so it takes what the others leave
	Why      string
}

// Rebalance proposes weights from the last two weeks of spend.csv.
func (t *Team) Rebalance(now time.Time) Proposal {
	p := Proposal{Pool: t.TeamPct, Since: now.AddDate(0, 0, -balanceDays), MinShifts: balanceShifts}
	uses := t.shiftUses(now)

	var sumW float64
	for _, a := range t.Agents {
		if a.Enabled {
			sumW += a.Weight
		}
	}
	var kept, fixed, openW float64 // percent of the weekly window a week, but openW: weight
	var open []int
	alloc := map[int]float64{}
	for _, a := range t.Agents {
		if !a.Enabled {
			continue
		}
		b := Balance{Name: a.Name, Weight: a.Weight}
		if sumW > 0 {
			b.Share = 100 * a.Weight / sumW
		}
		i := len(p.Agents)
		u := uses[a.Name]
		if u == nil || u.shifts < balanceShifts {
			n := 0
			if u != nil {
				n = u.shifts
			}
			b.Shifts = n
			b.Why = fmt.Sprintf("%s in the last %d days: too few to judge, so it keeps its share", plural(n, "shift"), balanceDays)
			kept += b.Share * p.Pool / 100
			p.Agents = append(p.Agents, b)
			continue
		}
		est := u.pct
		days := now.Sub(u.first).Hours() / 24
		if u.ranLongAgo {
			days = balanceDays
		}
		days = max(1, days)
		b.Shifts = u.shifts
		b.PerShift = est / float64(u.shifts)
		ran := float64(u.shifts) * 7 / days
		b.PerWeek = max(ran, t.scheduledPerWeek(a, now))
		b.Need = b.PerShift * b.PerWeek
		seen := fmt.Sprintf("%s in %s, about %.2f%% of the pool each", plural(u.shifts, "shift"), daysText(days), p.Of(b.PerShift))
		switch {
		case a.UntilDone:
			b.Why = fmt.Sprintf("%s, %.1f%% a week; it works until done, so it takes what the others leave", seen, p.Of(est*7/days))
			open, openW, b.Rest = append(open, i), openW+a.Weight, true
		default:
			want := max(balanceFloor, b.Need*balanceMargin)
			b.Why = fmt.Sprintf("%s; at %.0f a week it needs %.1f%% a week, %.1f%% with a quarter to spare", seen, b.PerWeek, p.Of(b.Need), p.Of(b.Need*balanceMargin))
			if b.Need*balanceMargin < balanceFloor {
				b.Why = fmt.Sprintf("%s; at %.0f a week it needs %.2f%% a week, and gets the least: %.1f%%, 1%% of the weekly window, the step Claude measures in", seen, b.PerWeek, p.Of(b.Need), p.Of(balanceFloor))
			}
			if has := b.Share * p.Pool / 100; math.Abs(want-has) <= max(balanceFloor/2, balanceBand*want) {
				want = has // about what it has: a proposal made again says the same
				b.Why = fmt.Sprintf("%s; at %.0f a week it needs %.1f%% a week, about its share now, so it keeps it", seen, b.PerWeek, p.Of(b.Need*balanceMargin))
			}
			alloc[i] = want
			fixed += want
		}
		p.Agents = append(p.Agents, b)
	}
	if len(p.Agents) == 0 {
		return p
	}

	avail := p.Pool - kept
	switch {
	case p.Pool <= 0 || len(open) == 0 && fixed == 0:
		for i := range p.Agents {
			p.Agents[i].Proposed = keptWeight(p.Agents[i])
		}
		if p.Pool <= 0 {
			p.Note = "team_pct is 0: there is no pool to divide"
		} else {
			p.Note = "no agent has enough shifts to judge yet: each keeps its share"
		}
		return p
	case fixed >= avail:
		total := fixed           // the until-done agents count what they used, and all are cut alike
		for _, i := range open { // by use, not by weight: applied and made again, the proposal is the same
			alloc[i] = max(balanceFloor, p.Agents[i].Need)
			total += alloc[i]
		}
		for i := range alloc {
			alloc[i] *= avail / total
		}
		p.Note = fmt.Sprintf("the agents need %.0f%% of the pool a week, more than the pool: each is cut alike; raise team_pct, or lower a weight", p.Of(fixed+kept))
	case len(open) > 0:
		for _, i := range open { // by their weights: how the human divides them now
			if openW > 0 {
				alloc[i] = (avail - fixed) * p.Agents[i].Weight / openW
			} else {
				alloc[i] = (avail - fixed) / float64(len(open))
			}
		}
	default: // nobody takes the rest: the agents judged share it, in proportion to their needs
		for i := range alloc {
			alloc[i] *= avail / fixed
		}
		p.Note = "no agent works until done, so what the others do not need is spread over them all"
	}
	for i := range p.Agents {
		w, ok := alloc[i]
		if !ok {
			p.Agents[i].Proposed = keptWeight(p.Agents[i])
			continue
		}
		p.Agents[i].Proposed = max(0.1, round1(100*w/p.Pool)) // a weight of 0 would stop the agent
	}
	return p
}

// scheduledPerWeek is how many shifts the agent's every and hours allow in a week; 0 for an
// until-done agent, which runs when asked.
func (t *Team) scheduledPerWeek(a *Agent, now time.Time) float64 {
	every := time.Duration(a.Every)
	if a.UntilDone || every <= 0 {
		return 0
	}
	s := &schedule{Team: t}
	l := now.In(t.Zone.Location)
	start := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, t.Zone.Location)
	end := start.AddDate(0, 0, 7)
	n := 0
	for at := s.startIn(a, start); !at.IsZero() && at.Before(end) && n < 7*24*60; at = s.startIn(a, at.Add(every)) {
		n++ // at most one a minute: the organizer looks no more often
	}
	return float64(n)
}

// SetWeights writes new weights into the agents' files, after checking them all: nothing is
// written when one is wrong, or when the enabled agents' weights would add up to 0.
func (t *Team) SetWeights(weights map[string]float64) error {
	for name, w := range weights {
		if _, err := t.agent(name); err != nil {
			return fmt.Errorf("%v; nothing was saved", err)
		}
		if w < 0 || math.IsNaN(w) || math.IsInf(w, 0) {
			return fmt.Errorf("%s: a weight is a number of 0 or more; nothing was saved", name)
		}
	}
	var sum float64
	for _, a := range t.Agents {
		if w, ok := weights[a.Name]; a.Enabled && ok {
			sum += w
		} else if a.Enabled {
			sum += a.Weight
		}
	}
	if sum == 0 && len(weights) > 0 {
		return fmt.Errorf("the enabled agents' weights would add up to 0: no agent could run; nothing was saved")
	}
	var done []string
	for _, a := range t.Agents {
		w, ok := weights[a.Name]
		if !ok || w == a.Weight {
			continue
		}
		if err := t.SetAgent(a.Name, map[string]string{"weight": strconv.FormatFloat(w, 'f', -1, 64)}); err != nil {
			if len(done) == 0 {
				return fmt.Errorf("%s: %v; nothing was saved", a.Name, err)
			}
			return fmt.Errorf("%s: %v; only these were saved: %s", a.Name, err, strings.Join(done, ", "))
		}
		done = append(done, a.Name)
	}
	return nil
}

// Of is pct of the weekly window as a percent of the pool, as the proposal shows it.
func (p Proposal) Of(pct float64) float64 { return ofPool(pct, p.Pool) }

// keptWeight is the weight of an agent that keeps its share: never 0, which would stop it.
func keptWeight(b Balance) float64 {
	if b.Weight == 0 {
		return 0 // it had none: it keeps none
	}
	return max(0.1, round1(b.Share))
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func daysText(d float64) string {
	if d < 10 {
		return fmt.Sprintf("%.1f days", d)
	}
	return fmt.Sprintf("%.0f days", d)
}

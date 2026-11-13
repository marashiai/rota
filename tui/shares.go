package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The Spend tab also proposes new shares from how the agents used the pool: B opens the
// proposal in place of the shifts, Enter changes a weight, A applies them all, Esc drops it.
// The Agents tab's B comes here.

// openBalance proposes new shares from how the agents used the pool, to change and apply.
func (s *spendPage) openBalance(c *console) {
	pr := c.t.Rebalance(time.Now())
	if len(pr.Agents) == 0 {
		c.say("no agent is enabled: there is nothing to rebalance", true)
		return
	}
	s.balance, s.bform = &pr, form{}
}

func (s *spendPage) balanceRows(c *console) []row {
	pr := s.balance
	var sum float64
	for _, b := range pr.Agents {
		sum += b.Proposed
	}
	var rows []row
	for _, b := range pr.Agents {
		share := 0.0
		if sum > 0 {
			share = 100 * b.Proposed / sum
		}
		value := fmt.Sprintf("%-6g → %-6s %5.1f%% → %5.1f%% of the pool", b.Weight, sty(bold, fmt.Sprint(b.Proposed)), b.Share, share)
		rows = append(rows, row{section: "Weight now → proposed · share of the pool now → then", key: b.Name, label: b.Name, value: value, set: true, help: b.Why})
	}
	return rows
}

func (s *spendPage) drawBalance(c *console, f *frame, a rect) {
	p, pr := c.pal, s.balance
	r := rect{margin - 1, a.y, a.w - margin, a.h}
	head := []string{sty(p.Head(), "Rebalance the shares") + sty(p.Muted(), "  from each agent's shifts since "+pr.Since.Local().Format("Mon 2 Jan"))}
	text := fmt.Sprintf("Each need is what one shift used times the shifts it runs a week (as many as it ran, or as its every and hours allow), with a quarter to spare. "+
		"An agent that works until done takes what the others leave; one with fewer than %d shifts, or a share about what it needs, keeps its share. "+
		"The proposed weights add up to 100: each is the agent's percent of the pool. ⏎ changes one, A applies them all, Esc drops the proposal.", pr.MinShifts)
	head = append(head, wrap([]span{{text, p.Muted()}}, r.w-3, "", "")...)
	if pr.Note != "" {
		head = append(head, sty(p.Warn(), "▲ "+pr.Note))
	}
	c.drawForm(f, r, &s.bform, head, s.balanceRows(c), nil, true, s.balanceWrite(c))
}

func (s *spendPage) balanceKey(c *console, k key) bool {
	switch {
	case k.name == "esc" || k.name == "left" || k.r == 'h':
		s.balance = nil
		c.say("proposal dropped: no weight changed", false)
	case k.r == 'A':
		var said []string
		for _, b := range s.balance.Agents {
			said = append(said, fmt.Sprintf("%s %g", b.Name, b.Proposed))
		}
		c.ask("Apply these weights? "+strings.Join(said, ", "), func() { s.applyBalance(c, said) })
	case k.r == 'B':
		s.openBalance(c)
	default:
		return c.formKey(&s.bform, s.balanceRows(c), k, s.balanceWrite(c))
	}
	return true
}

// balanceWrite asks for a new weight of the agent on a row of the proposal.
func (s *spendPage) balanceWrite(c *console) func(row, string) {
	return func(r row, _ string) {
		for i := range s.balance.Agents {
			b := &s.balance.Agents[i]
			if b.Name != r.key {
				continue
			}
			c.prompt(b.Name+"'s weight: ", fmt.Sprint(b.Proposed), func(s string) {
				w, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
				if err != nil || w < 0 {
					c.say("a weight is a number of 0 or more", true)
					return
				}
				b.Proposed = w
			})
		}
	}
}

func (s *spendPage) applyBalance(c *console, said []string) {
	weights := map[string]float64{}
	for _, b := range s.balance.Agents {
		weights[b.Name] = b.Proposed
	}
	if c.sayErr(c.t.SetWeights(weights)) {
		return
	}
	s.balance = nil
	c.reread()
	c.say("weights applied: "+strings.Join(said, ", ")+" · the organizer uses them within seconds", false)
}

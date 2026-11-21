package core

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// The plan is the order the team works on its open issues in: an agent, the planner, gives it with
// rota work-order, most important first, each with why it comes there. It replaces the last one;
// none clears it. The Issues tab lists the issues in that order unless the human chose another,
// each with its why, those it leaves out after them, newest first; a role reads it as {plan}.

// workOrder is the order an agent gave last, most important first.
type workOrder struct {
	By     string        `json:"by"`
	Time   time.Time     `json:"time"`
	Issues []reviewEntry `json:"issues"`
}

// Plan is the order an agent gave last of the open issues, as GitHub last listed them: one closed
// since, or never open, is left out.
type Plan struct {
	By     string    // the agent that gave it; "" when none has
	Time   time.Time // when
	Issues []PlannedIssue
}

// PlannedIssue is an open issue in the plan.
type PlannedIssue struct {
	Repo   string
	Number int
	Title  string
	Why    string
}

// Ref is owner/name#n.
func (p PlannedIssue) Ref() string { return p.Repo + "#" + strconv.Itoa(p.Number) }

// orderEntries reads an order's entries, "<repository>#<n> <why>" each, most first. It returns those
// GitHub did not last list as what wanted says, which the order keeps but no list shows.
func (t *Team) orderEntries(entries []string, what string, wanted func(it openItem) bool) ([]reviewEntry, []string, error) {
	open := t.openWork()
	var out []reviewEntry
	var unknown []string
	seen := map[string]bool{}
	for _, e := range entries {
		ref, why, _ := strings.Cut(strings.TrimSpace(e), " ")
		repo, n, err := t.parseRef(ref, "")
		if err != nil {
			return nil, nil, fmt.Errorf("%w: give each %s as \"<repository>#<number> <why>\"", err, what)
		}
		if why = strings.TrimSpace(why); why == "" {
			return nil, nil, fmt.Errorf("%s: say why it comes there, after its number", ref)
		}
		full := repo + "#" + strconv.Itoa(n)
		if seen[full] {
			return nil, nil, fmt.Errorf("%s is in the order twice", ref)
		}
		seen[full] = true
		if items, listed := open.Repos[repo]; listed {
			if it, ok := items[n]; !ok || !wanted(it) {
				unknown = append(unknown, ref)
			}
		}
		out = append(out, reviewEntry{full, why})
	}
	return out, unknown, nil
}

// SetWorkOrder replaces the order in which the team works on the open issues with agent's: each
// entry is "<repository>#<n> <why>", most important first. It returns those GitHub did not last
// list as open issues, which the plan leaves out.
func (t *Team) SetWorkOrder(agent string, entries []string) ([]string, error) {
	issues, unknown, err := t.orderEntries(entries, "issue", func(it openItem) bool { return !it.PR })
	if err != nil {
		return nil, err
	}
	b, _ := json.MarshalIndent(workOrder{By: agent, Time: time.Now(), Issues: issues}, "", "  ")
	return unknown, writeWhole(t.Path(planFile), append(b, '\n'))
}

// Plan is the order an agent gave last, of the issues GitHub last listed as open.
func (t *Team) Plan() Plan {
	var o workOrder
	if b, err := os.ReadFile(t.Path(planFile)); err == nil {
		json.Unmarshal(b, &o)
	}
	p := Plan{By: o.By, Time: o.Time}
	open := t.openWork()
	for _, e := range o.Issues {
		repo, num, _ := strings.Cut(e.Ref, "#")
		n, _ := strconv.Atoi(num)
		it, ok := open.Repos[repo][n]
		if !ok || it.PR {
			continue
		}
		p.Issues = append(p.Issues, PlannedIssue{Repo: repo, Number: n, Title: it.Title, Why: e.Why})
	}
	return p
}

// planText is the plan as a role reads it, {plan}: the open issues in the order the agent gave,
// each with its why.
func (t *Team) planText() string {
	p := t.Plan()
	if p.By == "" {
		return "none: no agent has ordered the open issues yet (rota work-order)"
	}
	given := p.By + " gave at " + p.Time.Local().Format("Mon 2 Jan 15:04")
	if len(p.Issues) == 0 {
		return "none: every issue in the order " + given + " is closed"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "the open issues in the order %s, most important first:", given)
	for i, it := range p.Issues {
		fmt.Fprintf(&b, "\n%d. %s «%s»: %s", i+1, it.Ref(), it.Title, it.Why)
	}
	return b.String()
}

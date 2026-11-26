package core

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The plan is the order the team works on its open issues in: an agent, the planner, gives it with
// rota work-order, most important first, each with why it comes there, and the open issues the
// team does not take now after them (--out), most important first too, each with why it waits.
// It replaces the last one; none clears it. The Issues tab lists the issues in that order unless
// the human chose another, each with its why, then those the plan names neither way, newest first;
// a role reads it as {plan}.

// workOrder is the order an agent gave last, most important first.
type workOrder struct {
	By     string        `json:"by"`
	Time   time.Time     `json:"time"`
	Issues []reviewEntry `json:"issues"`
	Out    []reviewEntry `json:"out,omitempty"` // the open issues not taken now, most important first, each with why
}

// Plan is the order an agent gave last of the open issues, as GitHub last listed them: one closed
// since, or never open, is left out.
type Plan struct {
	By     string    // the agent that gave it; "" when none has
	Time   time.Time // when
	Issues []PlannedIssue
	Out    []PlannedIssue // the open issues it leaves out for now, most important first, each with why
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
// entry is "<repository>#<n> <why>", most important first, and each of out one the team does not
// take now, with why. It returns those GitHub did not last list as open issues, which the plan
// does not show.
func (t *Team) SetWorkOrder(agent string, entries, out []string) ([]string, error) {
	isIssue := func(it openItem) bool { return !it.PR }
	issues, unknown, err := t.orderEntries(entries, "issue", isIssue)
	if err != nil {
		return nil, err
	}
	left, unknownOut, err := t.orderEntries(out, "issue", isIssue)
	if err != nil {
		return nil, err
	}
	for _, e := range left {
		if slices.ContainsFunc(issues, func(i reviewEntry) bool { return i.Ref == e.Ref }) {
			return nil, fmt.Errorf("%s is both in the order and left out of it", e.Ref)
		}
	}
	b, _ := json.MarshalIndent(workOrder{By: agent, Time: time.Now(), Issues: issues, Out: left}, "", "  ")
	return append(unknown, unknownOut...), writeWhole(t.Path(planFile), append(b, '\n'))
}

// Plan is the order an agent gave last, of the issues GitHub last listed as open.
func (t *Team) Plan() Plan {
	var o workOrder
	if b, err := os.ReadFile(t.Path(planFile)); err == nil {
		json.Unmarshal(b, &o)
	}
	open := t.openWork()
	stillOpen := func(entries []reviewEntry) (out []PlannedIssue) {
		for _, e := range entries {
			repo, num, _ := strings.Cut(e.Ref, "#")
			n, _ := strconv.Atoi(num)
			if it, ok := open.Repos[repo][n]; ok && !it.PR {
				out = append(out, PlannedIssue{Repo: repo, Number: n, Title: it.Title, Why: e.Why})
			}
		}
		return out
	}
	return Plan{By: o.By, Time: o.Time, Issues: stillOpen(o.Issues), Out: stillOpen(o.Out)}
}

// planText is the plan as a role reads it, {plan}: the open issues in the order the agent gave,
// each with its why, then those it leaves out for now, each with why.
func (t *Team) planText() string {
	p := t.Plan()
	if p.By == "" {
		return "none: no agent has ordered the open issues yet (rota work-order)"
	}
	given := p.By + " gave at " + p.Time.Local().Format("Mon 2 Jan 15:04")
	var b strings.Builder
	if len(p.Issues) == 0 {
		b.WriteString("none: every issue in the order " + given + " is closed")
	} else {
		fmt.Fprintf(&b, "the open issues in the order %s, most important first:", given)
		for i, it := range p.Issues {
			fmt.Fprintf(&b, "\n%d. %s «%s»: %s", i+1, it.Ref(), it.Title, it.Why)
		}
	}
	if len(p.Out) > 0 {
		b.WriteString("\n\nLeft out of the order for now, not to be taken, most important first:")
		for _, it := range p.Out {
			fmt.Fprintf(&b, "\n- %s «%s»: %s", it.Ref(), it.Title, it.Why)
		}
	}
	return b.String()
}

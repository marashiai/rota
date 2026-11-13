package core

import (
	"context"
	"fmt"
	"strings"
)

// The team's GitHub labels: one name, color and description in every repository of the team.
// rota has four kinds of its own, rota, LeaveLabel, ApprovedLabel and rota:<agent>; the rest are the team's,
// declared in the labels setting. The organizer creates or corrects them when it starts and whenever the
// team's agents, labels or repositories change; the agents only put them on issues and pull
// requests, and may not create or edit them.

// Label is one of the team's labels.
type Label struct {
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
	Emoji       string `json:"-"` // what marks a pull request that carries it in the Reviews tab; "" for nothing
}

// LeaveLabel is the human's word that the team leaves an issue or pull request alone: the brief
// tells every agent not to work on, comment on or label one that carries it, and rota merge does
// not merge a pull request that carries it.
const LeaveLabel = "rota:leave-alone"

// ApprovedLabel is the human's approval of a pull request, which rota puts on it when they
// approve it in a review: the agents whose role merges merge it once nothing holds it back. It is
// GitHub's record of the approval, however it got there: rota keeps none of its own.
const ApprovedLabel = "rota:approved"

// builtinLabels counts the labels rota has whatever the team: rota, LeaveLabel, ApprovedLabel.
const builtinLabels = 3

// Labels are the team's labels: rota, LeaveLabel, ApprovedLabel, one per agent, then the labels
// setting's.
func (t *Team) Labels() []Label {
	ls := []Label{{"rota", "5319E7", "Work for the AI team", ""}, {LeaveLabel, "BFBFBF", "The human's word: no agent works on it, comments on it, labels it or merges it", "✋"},
		{ApprovedLabel, "2DA44E", "The human approved it: the team merges it once nothing holds it back", "✅"}}
	for _, a := range t.Agents {
		ls = append(ls, Label{"rota:" + a.Name, "D4C5F9", "Filed by the " + a.Name + " agent", ""})
	}
	for _, l := range t.Config.Labels {
		if findLabel(ls, l.Name) == nil { // rota's own come first
			ls = append(ls, l)
		}
	}
	return ls
}

// labelsText lists the team's own labels for a shift's brief, one per line: those of the labels
// setting that are not rota's.
func (t *Team) labelsText() string {
	var lines []string
	for _, l := range t.Labels()[builtinLabels+len(t.Agents):] {
		lines = append(lines, strings.TrimSuffix("  - "+l.Name+": "+l.Description, ": "))
	}
	return orNone(strings.Join(lines, "\n"))
}

// labelsKey changes whenever the labels the team needs, or the repositories they go in, change.
func (t *Team) labelsKey() string {
	var b strings.Builder
	for _, l := range t.Labels() {
		b.WriteString(l.Name + " " + l.Color + " " + l.Description + "\n")
	}
	return b.String() + strings.Join(t.teamRepos(), " ")
}

// SyncLabels creates or corrects the team's labels in every repository of the team, and says
// what it changed.
func (t *Team) SyncLabels(ctx context.Context) ([]string, error) {
	var changed, failed []string
	for _, repo := range t.teamRepos() {
		var have []Label
		if err := t.gh(ctx, &have, "label", "list", "-R", repo, "--limit", "500", "--json", "name,color,description"); err != nil {
			failed = append(failed, repo+": "+err.Error())
			continue
		}
		for _, l := range t.Labels() {
			var args []string
			switch h := findLabel(have, l.Name); {
			case h == nil:
				args = []string{"label", "create", l.Name, "-R", repo, "--color", l.Color, "--description", l.Description}
			case h.Name != l.Name || !strings.EqualFold(h.Color, l.Color) || h.Description != l.Description:
				args = []string{"label", "edit", h.Name, "-R", repo, "--name", l.Name, "--color", l.Color, "--description", l.Description}
			default:
				continue
			}
			if err := t.gh(ctx, nil, args...); err != nil {
				failed = append(failed, repo+" "+l.Name+": "+err.Error())
				continue
			}
			changed = append(changed, args[1]+" "+repo+" "+l.Name)
		}
	}
	if len(failed) > 0 {
		return changed, fmt.Errorf("labels: %s", strings.Join(failed, "; "))
	}
	return changed, nil
}

func findLabel(ls []Label, name string) *Label {
	for i := range ls {
		if strings.EqualFold(ls[i].Name, name) {
			return &ls[i]
		}
	}
	return nil
}

package core

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Triggers are what an agent starts a shift on besides its interval: on in its file. Most happen
// on GitHub, in the repositories the agent works on, where the organizer looks every few minutes
// (watch.go); note is a teammate's note to it. Each event that matches reaches the agent as a note
// from github, which its brief lists under the pull request or issue, and asks for its shift as a
// request does: it skips the interval, and keeps to the agent's hours, its days and its share of
// the pool. An event that happens while the agent's own shift runs starts no shift of it: it may
// be that shift's own doing. It reaches that shift at its next step, if the shift still runs, or
// else waits for its next shift.
type Triggers []string

// triggerKinds are the triggers there are, and what each means. The two labeled ones take a label
// after a colon, * matching any text, as issue-labeled:priority:*; without one, any label.
var triggerKinds = []struct{ Name, What string }{
	{"issue-opened", "an issue is opened"},
	{"issue-labeled", "an issue gets a label"},
	{"issue-commented", "an issue gets a comment"},
	{"pr-opened", "a pull request is opened"},
	{"pr-ready", "a pull request is ready for review: opened as one, or no longer a draft"},
	{"pr-labeled", "a pull request gets a label"},
	{"pr-commented", "a pull request gets a comment or a review"},
	{"pr-approved", "a pull request is approved, on GitHub or by the human in rota"},
	{"pr-merged", "a pull request is merged"},
	{"pr-conflict", "a pull request no longer merges cleanly into its base"},
	{"pr-checks-failed", "a pull request's checks fail"},
	{"base-moved", "the branch an open pull request merges into gets new commits"},
	{"note", "a teammate leaves it a note (not one that reacts to notes itself: the two would take turns forever)"},
}

// noteTrigger is the trigger for a teammate's note; githubFrom, who an event's note is from.
const (
	noteTrigger = "note"
	githubFrom  = "github"
)

func (ts *Triggers) UnmarshalText(b []byte) error {
	*ts = nil
	for _, p := range strings.Split(string(b), ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		kind, label, labeled := strings.Cut(p, ":")
		kind = strings.ToLower(kind)
		if !slices.ContainsFunc(triggerKinds, func(k struct{ Name, What string }) bool { return k.Name == kind }) {
			*ts = nil
			return fmt.Errorf("no trigger %q: use %s", p, triggerNames())
		}
		if labeled {
			if kind != "issue-labeled" && kind != "pr-labeled" {
				*ts = nil
				return fmt.Errorf("%s takes no label: only issue-labeled and pr-labeled do", kind)
			}
			if strings.TrimSpace(label) == "" {
				*ts = nil
				return fmt.Errorf("%q: name a label after the colon, * matching any text, as issue-labeled:priority:*", p)
			}
			p = kind + ":" + strings.TrimSpace(label)
		} else {
			p = kind
		}
		if !slices.Contains(*ts, p) {
			*ts = append(*ts, p)
		}
	}
	return nil
}

func (ts Triggers) MarshalText() ([]byte, error) { return []byte(strings.Join(ts, ", ")), nil }

func triggerNames() string {
	names := make([]string, len(triggerKinds))
	for i, k := range triggerKinds {
		names[i] = k.Name
	}
	return strings.Join(names, ", ")
}

// has says whether the agent starts on kind, as for note.
func (ts Triggers) has(kind string) bool { return slices.Contains(ts, kind) }

// onGitHub says whether any trigger is an event on GitHub.
func (ts Triggers) onGitHub() bool {
	return slices.ContainsFunc(ts, func(t string) bool { return t != noteTrigger })
}

// match is the trigger e matches, if any.
func (ts Triggers) match(e RepoEvent) (string, bool) {
	for _, t := range ts {
		kind, label, labeled := strings.Cut(t, ":")
		if kind != e.Kind {
			continue
		}
		if !labeled {
			return t, true
		}
		if labelGlob(label).MatchString(e.Label) {
			return t, true
		}
	}
	return "", false
}

// labelGlob is a labeled trigger's label as a pattern: * matches any text, a slash too, and case
// does not matter.
func labelGlob(label string) *regexp.Regexp {
	return regexp.MustCompile("(?i)^" + strings.ReplaceAll(regexp.QuoteMeta(label), `\*`, ".*") + "$")
}

// RepoEvent is one thing that happened on GitHub to an issue or a pull request, as a trigger
// names it.
type RepoEvent struct {
	Kind   string    `json:"kind"`
	Repo   string    `json:"repo"` // owner/name
	Number int       `json:"number"`
	Title  string    `json:"title,omitempty"`
	Label  string    `json:"label,omitempty"` // issue-labeled, pr-labeled: the label it got
	At     time.Time `json:"at"`              // when it happened, as GitHub says; else when the organizer saw it
}

// Ref is the issue or pull request, as owner/name#n.
func (e RepoEvent) Ref() string { return e.Repo + "#" + strconv.Itoa(e.Number) }

// what says what happened, as the agent's note says it.
func (e RepoEvent) what() string {
	switch e.Kind {
	case "issue-opened":
		return "the issue was opened"
	case "issue-labeled":
		return "the issue got the label " + e.Label
	case "issue-commented":
		return "the issue got a comment"
	case "pr-opened":
		return "the pull request was opened"
	case "pr-ready":
		return "the pull request is ready for review"
	case "pr-labeled":
		return "the pull request got the label " + e.Label
	case "pr-commented":
		return "the pull request got a comment or a review"
	case "pr-approved":
		return "the pull request was approved"
	case "pr-merged":
		return "the pull request was merged"
	case "pr-conflict":
		return "the pull request no longer merges cleanly into its base"
	case "pr-checks-failed":
		return "the pull request's checks failed"
	case "base-moved":
		return "the branch the pull request merges into got new commits"
	}
	return e.Kind
}

// noteText is the event as the agent's note says it, with the trigger it matched.
func (e RepoEvent) noteText(trigger string) string {
	s := e.what()
	if e.Title != "" {
		s = "«" + e.Title + "»: " + s
	}
	return s + " (" + trigger + ", one of your triggers)"
}

// eventSlack is how long before an agent's shift began and after it ended an event on GitHub still
// counts as happening during it: GitHub's clock and this machine's differ a little, a shift's
// record leaves out the minutes rota spends before and after Claude, and what changed shows on
// GitHub only once GitHub has worked it out.
const eventSlack = 5 * time.Minute

// during says whether at falls in one of the agent's shifts: the one running now, or one in its
// record.
func (t *Team) during(a *Agent, at time.Time, live *Status) bool {
	if live != nil && live.Running != nil && live.Running.Agent == a.Name && !at.Before(live.Running.Since.Add(-eventSlack)) {
		return true
	}
	for _, r := range t.Runs {
		if r.Agent == a.Name && !at.Before(r.Time.Add(-eventSlack)) && !at.After(r.Time.Add(time.Duration(r.Seconds)*time.Second+eventSlack)) {
			return true
		}
	}
	return false
}

// onEvents brings the events to the enabled agents whose triggers they match, in the repositories
// each works on: a note from github per event, and a request for the agent's shift, unless the
// agent is at work or each event happened during its own shift. It says what reached whom, a line
// each.
func (t *Team) onEvents(events []RepoEvent, now time.Time) (said []string, err error) {
	if len(events) == 0 {
		return nil, nil
	}
	live, asked := t.Live(), false
	for _, a := range t.Agents {
		if !a.Enabled || !a.On.onGitHub() {
			continue
		}
		running := live != nil && live.Running != nil && live.Running.Agent == a.Name
		ask := false
		for _, e := range events {
			if !slices.Contains(a.Repos, e.Repo) {
				continue
			}
			trigger, ok := a.On.match(e)
			if !ok {
				continue
			}
			if nerr := t.addNote(a.Name, e.Ref(), githubFrom, e.noteText(trigger), now); nerr != nil {
				err = errors.Join(err, nerr)
				continue
			}
			own := t.during(a, e.At, live)
			ask = ask || !running && !own
			how := "asked for"
			switch {
			case running:
				how = "at work: it reaches its shift"
			case own:
				how = "during its own shift: no new shift"
			}
			said = append(said, fmt.Sprintf("%s %s → %s (%s)", e.Kind+labelPart(e), e.Ref(), a.Name, how))
		}
		if ask {
			if aerr := t.askFor(a.Name); aerr != nil {
				err = errors.Join(err, aerr)
			} else {
				asked = true
			}
		}
	}
	if asked && !t.Paused() { // paused, a wake-up is the human's Run now: the request waits for the team to go on
		err = errors.Join(err, t.kick())
	}
	return said, err
}

func labelPart(e RepoEvent) string {
	if e.Label == "" {
		return ""
	}
	return ":" + e.Label
}

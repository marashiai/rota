package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// What an agent asks the human: a line "@human: <question>" in its final message, or rota ask
// during its shift. It waits in questions/ until the human answers, from the console, the pages
// or the menu bar; the answer goes to the agent's inbox, and to its shift if that is waiting
// for it (rota wait). The human may dismiss it unanswered instead, and the agent is told; the
// agent may withdraw it when it no longer needs an answer ("@human withdraw <id>" in its final
// message, or rota withdraw). Closed questions are kept in questions/closed/.

// Question is one question, open or closed. One asked with rota ask may say more than its text:
// a few words to show it by, the answers to choose from and the one the agent recommends, the
// steps the human takes (a to-do: the human says when they are done), how much it matters, and
// the pull request or issue it is about.
type Question struct {
	ID         string    `json:"-"` // its file's name in questions/
	Agent      string    `json:"agent"`
	Asked      time.Time `json:"asked"`
	Text       string    `json:"text"`
	Subject    string    `json:"subject,omitempty"`
	Options    []string  `json:"options,omitempty"`
	Recommend  string    `json:"recommend,omitempty"`  // one of Options
	Steps      []string  `json:"steps,omitempty"`      // Markdown, one line each
	Importance string    `json:"importance,omitempty"` // high or low; "" is normal
	About      string    `json:"about,omitempty"`      // owner/name#n
	Answer     string    `json:"answer,omitempty"`
	Answered   time.Time `json:"answered,omitempty"`
	Dismissed  time.Time `json:"dismissed,omitempty"` // closed unanswered by the human
	Withdrawn  time.Time `json:"withdrawn,omitempty"` // closed unanswered by the agent
}

// ErrDismissed is what waiting for an answer the human dismissed brings.
var ErrDismissed = errors.New("the human dismissed the question without an answer")

// Questions are the open questions, oldest first.
func (c *Config) Questions() []Question {
	files, _ := filepath.Glob(filepath.Join(c.Path(questionsDir), "*.json"))
	var qs []Question
	for _, f := range files {
		var q Question
		if b, err := os.ReadFile(f); err == nil && json.Unmarshal(b, &q) == nil {
			q.ID = strings.TrimSuffix(filepath.Base(f), ".json")
			qs = append(qs, q)
		}
	}
	sort.SliceStable(qs, func(i, j int) bool { return qs[i].Asked.Before(qs[j].Asked) })
	return qs
}

// Ask records a question from an agent and returns its id; one it already has open is not
// asked twice.
func (c *Config) Ask(agent, text string) (string, error) { return c.ask(agent, text, time.Now()) }

func (c *Config) ask(agent, text string, now time.Time) (string, error) {
	return c.askQuestion(Question{Agent: agent, Text: text, Asked: now})
}

// Limits of what one question says.
const (
	subjectMax = 120
	optionsMax = 6
	optionMax  = 80
	stepsMax   = 25
	stepMax    = 1000
)

// AskWith records a question from an agent that says more than its text (Question): its subject,
// options and the one it recommends, steps, importance, and what it is about, as "#n" in the
// agent's only repository or "<repository>#<n>".
func (t *Team) AskWith(q Question) (string, error) {
	q.Asked = time.Now()
	if strings.TrimSpace(q.About) != "" {
		in := ""
		if a := t.Agent(q.Agent); a != nil && len(a.Repos) == 1 {
			in = a.Repos[0]
		}
		repo, n, err := t.parseRef(q.About, in)
		if err != nil {
			return "", fmt.Errorf("--about: %w", err)
		}
		q.About = repo + "#" + strconv.Itoa(n)
	}
	return t.askQuestion(q)
}

func (c *Config) askQuestion(q Question) (string, error) {
	clean := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	q.Text, q.Subject, q.Recommend, q.Importance = clean(q.Text), clean(q.Subject), clean(q.Recommend), strings.ToLower(clean(q.Importance))
	for i := range q.Options {
		q.Options[i] = clean(q.Options[i])
	}
	for i := range q.Steps { // a step's commands keep their spaces: only its ends are trimmed
		q.Steps[i] = strings.TrimSpace(q.Steps[i])
	}
	switch {
	case q.Agent == "" || q.Text == "":
		return "", fmt.Errorf("a question needs an agent and a text")
	case len(q.Subject) > subjectMax:
		return "", fmt.Errorf("the subject is a few words, at most %d characters", subjectMax)
	case len(q.Options) > optionsMax || len(q.Options) == 1:
		return "", fmt.Errorf("give two to %d options, or none", optionsMax)
	case len(q.Steps) > stepsMax:
		return "", fmt.Errorf("at most %d steps", stepsMax)
	case q.Importance != "" && q.Importance != "high" && q.Importance != "normal" && q.Importance != "low":
		return "", fmt.Errorf("importance is high, normal or low")
	case q.Recommend != "" && !slices.Contains(q.Options, q.Recommend):
		return "", fmt.Errorf("the recommended answer must be one of the options, word for word")
	}
	if q.Importance == "normal" {
		q.Importance = ""
	}
	for _, o := range q.Options {
		if o == "" || len(o) > optionMax {
			return "", fmt.Errorf("each option is a short answer, at most %d characters", optionMax)
		}
	}
	for _, s := range q.Steps {
		if s == "" || len(s) > stepMax || strings.ContainsAny(s, "\r\n") {
			return "", fmt.Errorf("each step is one line, at most %d characters: give a step of several lines as steps of one", stepMax)
		}
	}
	for _, o := range c.Questions() { // the same question about something else is another question
		if o.Agent == q.Agent && o.Text == q.Text && o.About == q.About {
			return o.ID, nil
		}
	}
	if q.Asked.IsZero() {
		q.Asked = time.Now()
	}
	id := q.Asked.Format("20060102-150405") + "-" + q.Agent + "-" + randHex(2)
	if err := writeQuestion(filepath.Join(c.Path(questionsDir), id+".json"), q); err != nil {
		return "", err
	}
	if len(c.PushSubscriptions()) > 0 && c.Notifies(q.Importance) { // the human's phone is told; a failed push is no failed question
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := c.PushAll(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "warn:", err)
		}
	}
	return id, nil
}

// writeQuestion writes a question's file whole: a reader never sees half of it.
func writeQuestion(path string, q Question) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, _ := json.Marshal(q)
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// closeQuestion closes an open question, after check approves it, and records how. The file
// moves to questions/closed/ first, so only one answer, dismissal or withdrawal closes it.
func (c *Config) closeQuestion(id string, check func(Question) error, how func(*Question)) (Question, error) {
	gone := fmt.Errorf("that question is no longer open")
	var q Question
	if filepath.Base(id) != id {
		return q, gone
	}
	path, done := filepath.Join(c.Path(questionsDir), id+".json"), filepath.Join(c.Path(questionsDir), closedDir, id+".json")
	if b, err := os.ReadFile(path); err != nil || json.Unmarshal(b, &q) != nil {
		return q, gone
	}
	if err := check(q); err != nil {
		return q, err
	}
	if os.MkdirAll(filepath.Dir(done), 0o755) != nil || os.Rename(path, done) != nil {
		return q, gone
	}
	how(&q)
	if err := writeQuestion(done, q); err != nil {
		os.Rename(done, path) // still open
		return q, err
	}
	return q, nil
}

// aboutRef is the pull request or issue a question names first, owner/name#n, or "": what is
// said back about the question goes with its notes.
func aboutRef(text string) string {
	if m := fullRef.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

// ref is the pull request or issue the question is about: the one it says, else the first its
// text names in full; "" for none.
func (q Question) ref() string {
	if q.About != "" {
		return q.About
	}
	return aboutRef(q.Text)
}

// Answer sends the human's answer to the agent that asked, for its running shift or its next
// one, and closes the question. An empty answer changes nothing.
func (t *Team) Answer(id, answer string) error {
	answer = strings.Join(strings.Fields(answer), " ")
	if answer == "" {
		return nil
	}
	now := time.Now()
	q, err := t.closeQuestion(id, func(Question) error { return nil }, func(q *Question) { q.Answer, q.Answered = answer, now })
	if err != nil {
		return err
	}
	if err := t.addNote(q.Agent, q.ref(), "human", fmt.Sprintf("answering your question \"%s\": %s", q.Text, answer), now); err != nil {
		return err
	}
	if !t.Steerable(q.Agent) { // its running shift gets the answer
		t.wakeForHuman(q.Agent)
	}
	return nil
}

// Dismiss closes a question unanswered, as the human decided. The agent finds that in its
// inbox, or its running shift gets it; nothing wakes it for that.
func (t *Team) Dismiss(id string) error {
	now := time.Now()
	q, err := t.closeQuestion(id, func(Question) error { return nil }, func(q *Question) { q.Dismissed = now })
	if err != nil {
		return err
	}
	return t.addNote(q.Agent, q.ref(), "human",
		fmt.Sprintf("dismissed your question \"%s\" without an answer: do not ask it again unless something about it changes", q.Text), now)
}

// Withdraw closes one of the agent's own questions that no longer needs an answer.
func (c *Config) Withdraw(agent, id string) error {
	_, err := c.closeQuestion(id, func(q Question) error {
		if q.Agent != agent {
			return fmt.Errorf("question %s is %s's, not yours", id, q.Agent)
		}
		return nil
	}, func(q *Question) { q.Withdrawn = time.Now() })
	return err
}

// WaitAnswer waits for the answer to a question, at most max, no longer than answer_minutes
// after it was asked, and not past end, when the shift must have its work put away: the answer,
// or "" and whether waiting longer can still bring one. A question closed unanswered is an
// error: ErrDismissed when the human dismissed it.
func (c *Config) WaitAnswer(id string, max time.Duration, end time.Time) (string, bool, error) {
	if filepath.Base(id) != id {
		return "", false, fmt.Errorf("no question %s", id)
	}
	for start := time.Now(); ; time.Sleep(2 * time.Second) {
		var q Question
		found := false
		for _, p := range []string{filepath.Join(c.Path(questionsDir), closedDir, id+".json"), filepath.Join(c.Path(questionsDir), id+".json")} {
			if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &q) == nil {
				found = true
				break
			}
		}
		switch {
		case !found:
			return "", false, fmt.Errorf("no question %s", id)
		case q.Answer != "":
			return q.Answer, false, nil
		case !q.Dismissed.IsZero():
			return "", false, ErrDismissed
		case !q.Withdrawn.IsZero():
			return "", false, fmt.Errorf("question %s was withdrawn", id)
		case time.Since(q.Asked) >= time.Duration(c.AnswerMinutes*float64(time.Minute)) || !end.IsZero() && time.Now().After(end):
			return "", false, nil
		case time.Since(start) >= max:
			return "", true, nil
		}
	}
}

// wakeFor asks for an until-done agent's shift: something brought it work. Asked while its
// shift runs, it runs once more after it. It is asked as a shift ends, and the organizer looks
// then anyway.
func (t *Team) wakeFor(agent string) {
	if a := t.Agent(agent); a != nil && a.UntilDone && a.Enabled {
		t.askFor(agent)
	}
}

// wakeForHuman asks for the agent's shift because the human answered it or left it a note: a
// scheduled agent too, before its interval is up, as the human expects; within its hours, and
// not while the team is paused: then the organizer is not woken, since a wake-up while paused
// is the human's Run now.
func (t *Team) wakeForHuman(agent string) {
	if a := t.Agent(agent); a != nil && a.Enabled && t.askFor(agent) == nil && !t.Paused() {
		t.kick()
	}
}

// questionsFor lists an agent's open questions for its brief, with their ids, so it does not
// ask them again and can withdraw them.
func (c *Config) questionsFor(agent string) string {
	var lines []string
	for _, q := range c.Questions() {
		if q.Agent == agent {
			lines = append(lines, "- "+q.Asked.In(c.Zone.Location).Format("2006-01-02")+", id "+q.ID+": "+q.Text)
		}
	}
	return orNone(strings.Join(lines, "\n"))
}

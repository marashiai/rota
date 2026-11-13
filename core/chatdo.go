package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// What the Team proposes in the chat is anything within rota's scope, done for the human once they
// apply it: rota itself, its agents, and their work and what they did. Settings, roles, runs and
// new agents; an answer to what an agent asked, or its dismissal, and a to-do's steps ticked; a
// message to an agent, or one taken back; a pull request approved, a note sent on it, or merged;
// a pull request or an issue of the team's repositories closed or reopened, labelled, commented
// on, a new issue, or any other change of their work as a gh command; the mail filed; and how the
// inbox works. Nothing outside the scope: each kind checks that what it changes is the team's, so
// the rest of the machine, other repositories and a repository's settings are out of its reach.
// Each kind of change checks a proposal and says in words what it does, does it once applied, and
// takes it back where that can be done: an answer, a run, a message taken back, an approval, a
// merge, a comment or a gh command stays.

// chatKind is one kind of change. check checks a proposal and fills in what its card shows. apply
// does it, keeps in line what undo needs and says what it did; the takeBack it returns undoes it
// when the chat cannot record it (nil: nothing can). undo takes it back; nil: applied, it stays.
type chatKind struct {
	check func(t *Team, ch *ChatChange) error
	apply func(t *Team, ch ChatChange, line *ChatMsg) (said string, takeBack func() error, err error)
	undo  func(t *Team, ch ChatChange, line ChatMsg) (string, error)
}

var chatKinds = map[string]chatKind{
	"set":         {(*Team).checkSettings, (*Team).applySettings, (*Team).undoSettings},
	"new":         {(*Team).checkNewAgent, (*Team).applyNewAgent, (*Team).undoNewAgent},
	"pause":       {(*Team).checkPause, (*Team).applyPause, (*Team).undoPause},
	"resume":      {(*Team).checkPause, (*Team).applyPause, (*Team).undoPause},
	"run":         {(*Team).checkRun, (*Team).applyRun, nil},
	"cancel":      {(*Team).checkRun, (*Team).applyCancel, nil},
	"start":       {(*Team).checkStart, (*Team).applyStart, nil},
	"role":        {(*Team).checkRole, (*Team).applyRole, (*Team).undoRole},
	"brief":       {(*Team).checkBrief, (*Team).applyBrief, (*Team).undoBrief},
	"answer":      {(*Team).checkAnswer, (*Team).applyAnswer, nil},
	"dismiss":     {(*Team).checkAnswer, (*Team).applyAnswer, nil},
	"tick":        {(*Team).checkTick, (*Team).applyTick, (*Team).undoTick},
	"untick":      {(*Team).checkTick, (*Team).applyTick, (*Team).undoTick},
	"message":     {(*Team).checkMessage, (*Team).applyMessage, (*Team).undoMessage},
	"unsend":      {(*Team).checkUnsend, (*Team).applyUnsend, nil},
	"approve":     {(*Team).checkPR, (*Team).applyPR, nil},
	"review-note": {(*Team).checkPR, (*Team).applyPR, nil},
	"merge":       {(*Team).checkMerge, nil, nil}, // chatWork merges
	"close":       {(*Team).checkState, (*Team).applyState, (*Team).undoState},
	"reopen":      {(*Team).checkState, (*Team).applyState, (*Team).undoState},
	"label":       {(*Team).checkLabels, (*Team).applyLabels, (*Team).undoLabels},
	"unlabel":     {(*Team).checkLabels, (*Team).applyLabels, (*Team).undoLabels},
	"comment":     {(*Team).checkComment, (*Team).applyComment, nil},
	"issue":       {(*Team).checkIssue, (*Team).applyIssue, (*Team).undoIssue},
	"github":      {(*Team).checkGitHub, (*Team).applyGitHub, nil},
	"inbox":       {(*Team).checkInbox, (*Team).applyInbox, (*Team).undoInbox},
}

func init() {
	for do := range markActs {
		chatKinds[do] = chatKind{(*Team).checkMarks, (*Team).applyMarks, (*Team).undoMarks}
	}
}

// kind is which kind of change ch is: "new" for a new agent, "set" for settings, else what it does.
func (ch ChatChange) kind() string {
	switch {
	case ch.New:
		return "new"
	case ch.Do == "":
		return "set"
	}
	return ch.Do
}

// ---- settings, new agents, pauses and runs ----

func (t *Team) checkSettings(ch *ChatChange) error {
	switch {
	case ch.Agent != "" && t.Agent(ch.Agent) == nil:
		return fmt.Errorf("no agent is called %s", ch.Agent)
	case len(ch.Set) == 0:
		return fmt.Errorf("propose what to do: a setting, a run, an answer, a message")
	}
	now := t.Settings()
	if ch.Agent != "" {
		now = t.AgentSettings(ch.Agent)
	}
	ch.Was = map[string]string{}
	for k, v := range ch.Set {
		v = strings.TrimSpace(v)
		ch.Set[k] = v
		if strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("%s: a value is one line", k)
		}
		if ch.Agent == "" {
			if _, ok := ConfigField(k); !ok {
				return fmt.Errorf("the team has no setting %s", k)
			}
			if err := checkSetting(k, v); err != nil {
				return fmt.Errorf("%s: %v", k, err)
			}
		} else {
			f, ok := AgentField(k)
			if !ok {
				return fmt.Errorf("an agent has no setting %s", k)
			}
			if v != "" {
				if err := f.Set(&Agent{}, v); err != nil {
					return fmt.Errorf("%s: %v", k, err)
				}
			}
		}
		ch.Was[k] = now[k]
	}
	ch.Title = or(ch.Agent, "The team") + " · " + strings.Join(sortedKeys(ch.Set), ", ")
	return nil
}

func (t *Team) applySettings(ch ChatChange, line *ChatMsg) (string, func() error, error) {
	if ch.Agent == "" {
		was, absent, err := swapConf(t.Path(t.File), ch.Set)
		if err != nil {
			return "", nil, err
		}
		line.Undo, line.Absent = was, absent
		return "Applied: " + changeText(ch), func() error { return restoreConf(t.Path(t.File), was, absent, nil) }, nil
	}
	was, absent, err := t.swapAgent(ch.Agent, ch.Set)
	if err != nil {
		return "", nil, err
	}
	line.Undo, line.Absent = was, absent
	return "Applied: " + changeText(ch), func() error { return t.restoreAgent(ch.Agent, was, absent, nil) }, nil
}

func (t *Team) undoSettings(ch ChatChange, line ChatMsg) (string, error) {
	if ch.Agent == "" {
		return "Undone: " + changeText(ch), restoreConf(t.Path(t.File), line.Undo, line.Absent, ch.Set)
	}
	return "Undone: " + changeText(ch), t.restoreAgent(ch.Agent, line.Undo, line.Absent, ch.Set)
}

func (t *Team) checkNewAgent(ch *ChatChange) error {
	if err := t.newAgentOK(ch.Agent, ch.Role, ch.Set); err != nil {
		return err
	}
	ch.Title = "New agent · " + ch.Agent
	return nil
}

func (t *Team) applyNewAgent(ch ChatChange, line *ChatMsg) (string, func() error, error) {
	if err := t.newAgentOK(ch.Agent, ch.Role, ch.Set); err != nil {
		return "", nil, err
	}
	file := newAgentFile(ch.Role, ch.Set)
	if err := writeWhole(t.AgentFile(ch.Agent), []byte(file)); err != nil {
		return "", nil, err
	}
	line.Undo["file"] = file
	return "Added " + ch.Agent + ".", func() error { return os.Remove(t.AgentFile(ch.Agent)) }, nil
}

// undoNewAgent removes a new agent's file, unless it was saved since or the agent began work.
func (t *Team) undoNewAgent(ch ChatChange, line ChatMsg) (string, error) {
	path := t.AgentFile(ch.Agent)
	err := changeLocked(path, func() error {
		b, _ := os.ReadFile(path)
		switch {
		case string(b) != line.Undo["file"]:
			return fmt.Errorf("%s's file changed since it was added; remove it by hand if it is to go", ch.Agent)
		case t.began(ch.Agent):
			return fmt.Errorf("%s has begun work already; pause it, or remove it by hand", ch.Agent)
		}
		return os.Remove(path)
	})
	return ch.Agent + " is removed again.", err
}

func (t *Team) checkPause(ch *ChatChange) error {
	if ch.Agent != "" && t.Agent(ch.Agent) == nil {
		return fmt.Errorf("no agent is called %s", ch.Agent)
	}
	ch.Title, ch.Say = or(ch.Agent, "The team"), map[string]string{"pause": "Pause", "resume": "Resume"}[ch.Do]
	return nil
}

func (t *Team) applyPause(ch ChatChange, line *ChatMsg) (string, func() error, error) {
	paused := t.isPaused(ch.Agent)
	line.Undo["paused"] = strconv.FormatBool(paused) // undo puts it back as it was
	if err := t.setPaused(ch.Agent, ch.Do == "pause"); err != nil {
		return "", nil, err
	}
	return pausedText(ch.Agent, ch.Do == "pause"), func() error { return t.setPaused(ch.Agent, paused) }, nil
}

func (t *Team) undoPause(ch ChatChange, line ChatMsg) (string, error) {
	if now := t.isPaused(ch.Agent); now != (ch.Do == "pause") {
		return "", fmt.Errorf("it was %s since; undo would overwrite that", map[bool]string{true: "paused", false: "resumed"}[now])
	}
	was := line.Undo["paused"] == "true"
	return pausedText(ch.Agent, was), t.setPaused(ch.Agent, was)
}

// isPaused says whether an agent is paused; "" is the whole team.
func (t *Team) isPaused(agent string) bool {
	if agent == "" {
		return t.Paused()
	}
	return t.PausedAgents()[agent]
}

// checkRun checks a run, or a stop of the running one: of an agent.
func (t *Team) checkRun(ch *ChatChange) error {
	switch {
	case ch.Agent == "":
		return fmt.Errorf("which agent %s?", map[string]string{"run": "runs", "cancel": "stops"}[ch.Do])
	case t.Agent(ch.Agent) == nil:
		return fmt.Errorf("no agent is called %s", ch.Agent)
	}
	ch.Title, ch.Say = ch.Agent, map[string]string{"run": "Run now", "cancel": "Stop its run"}[ch.Do]
	if ch.Do == "run" { // what the tap does, as the organizer would decide it now
		switch r := t.RunWait(ch.Agent, t.Status(time.Now())); {
		case r.Why != "":
			ch.Lines = []string{"It cannot start now: " + r.Why + ". Its next run: " + r.Next + "."}
		case r.After != "":
			ch.Lines = []string{"It starts after " + r.After + "'s shift, if it still may then."}
		}
	}
	return nil
}

func (t *Team) applyRun(ch ChatChange, line *ChatMsg) (string, func() error, error) {
	asked := t.Asked()[ch.Agent]
	said, err := t.AskToRun(ch.Agent, t.Status(time.Now()))
	if err != nil {
		return "", nil, err
	}
	return said, func() error {
		if !asked {
			t.answered(ch.Agent)
		}
		return nil
	}, nil
}

func (t *Team) applyCancel(ch ChatChange, line *ChatMsg) (string, func() error, error) {
	if err := t.CancelShift(ch.Agent); err != nil {
		return "", nil, err
	}
	return "Stopping " + ch.Agent + "'s run.", nil, nil
}

// selfExe is rota's own program, which starts the organizer; tests replace it.
var selfExe = os.Executable

func (t *Team) checkStart(ch *ChatChange) error {
	ch.Title, ch.Say = "The organizer", "Start it"
	return nil
}

func (t *Team) applyStart(ch ChatChange, line *ChatMsg) (string, func() error, error) {
	if t.Live() != nil {
		return "", nil, fmt.Errorf("the organizer is running already")
	}
	exe, err := selfExe()
	if err != nil {
		return "", nil, err
	}
	if _, err := t.StartOrganizer(exe, true); err != nil {
		return "", nil, err
	}
	return "The organizer is starting; what it says is in rota.log.", nil, nil
}

// roleMax is the longest role the Team may propose.
const roleMax = 50_000

func (t *Team) checkRole(ch *ChatChange) error {
	switch {
	case t.Agent(ch.Agent) == nil:
		return fmt.Errorf("whose role? no agent is called %q", ch.Agent)
	case ch.Replace != "" && ch.Role != "":
		return fmt.Errorf("give its whole new role, or a passage of it to replace, not both")
	case ch.Replace == "" && ch.Role == "":
		return fmt.Errorf("the role is empty")
	case len(ch.Role) > roleMax:
		return fmt.Errorf("a role holds at most %d characters", roleMax)
	}
	ch.Title, ch.Say = ch.Agent+" · role", "Its new role"
	if ch.Replace != "" {
		b, err := os.ReadFile(t.AgentFile(ch.Agent))
		if err != nil {
			return err
		}
		_, body, err := splitFront(string(b))
		if err != nil {
			return err
		}
		if _, err := roleEdit(*ch, strings.TrimSpace(body)); err != nil {
			return err
		}
		ch.Say = "Change this passage of its role"
	}
	return nil
}

// roleEdit is the role a card makes of an agent's role: its whole new role, or the role with one
// passage replaced.
func roleEdit(ch ChatChange, role string) (string, error) {
	if ch.Replace == "" {
		return ch.Role, nil
	}
	out, err := replaced(role, ch.Replace, ch.With)
	if out = strings.TrimSpace(out); err == nil && out == "" {
		err = fmt.Errorf("the role would be empty")
	}
	return out, err
}

func (t *Team) applyRole(ch ChatChange, line *ChatMsg) (string, func() error, error) {
	path := t.AgentFile(ch.Agent)
	var old string
	err := changeLocked(path, func() error {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		head, body, err := splitFront(string(b))
		if err != nil {
			return err
		}
		old = strings.TrimSpace(body)
		role, err := roleEdit(ch, old)
		if err != nil {
			return err
		}
		return writeWhole(path, []byte("---\n"+head+"\n---\n"+role+"\n"))
	})
	if err != nil {
		return "", nil, err
	}
	line.Undo["role"] = old
	return "Saved: " + ch.Agent + "'s next run works under its new role.", func() error { _, err := t.undoRole(ch, *line); return err }, nil
}

// undoRole puts the role back as it was, unless it changed since.
func (t *Team) undoRole(ch ChatChange, line ChatMsg) (string, error) {
	applied, _ := roleEdit(ch, line.Undo["role"])
	_, err := swapRole(t.AgentFile(ch.Agent), line.Undo["role"], applied)
	return "Undone: " + ch.Agent + "'s role is as it was.", err
}

// passageMax is the longest passage the Team may write into the brief or a role.
const passageMax = 20_000

// replaced is text with its one passage old replaced by with, or why it cannot be.
func replaced(text, old, with string) (string, error) {
	switch n := strings.Count(text, old); {
	case old == "":
		return "", fmt.Errorf("say which passage to replace, word for word")
	case n == 0:
		return "", fmt.Errorf("the passage to replace is not there word for word")
	case n > 1:
		return "", fmt.Errorf("the passage to replace is there %d times: give more of it", n)
	case len(with) > passageMax:
		return "", fmt.Errorf("what replaces it holds at most %d characters", passageMax)
	}
	return strings.Replace(text, old, with, 1), nil
}

// placeholder is what rota fills in in the brief: {agent}, {questions}...
var placeholder = regexp.MustCompile(`\{[a-z_]+\}`)

// briefEdit is the agents' brief with a card's passage replaced; its placeholders stay.
func briefEdit(ch ChatChange, brief string) (string, error) {
	out, err := replaced(brief, ch.Replace, ch.With)
	if err != nil {
		return "", err
	}
	before, after := placeholder.FindAllString(brief, -1), placeholder.FindAllString(out, -1)
	slices.Sort(before)
	slices.Sort(after)
	if !slices.Equal(before, after) {
		return "", fmt.Errorf("the brief's placeholders in braces, which rota fills in, stay as they are")
	}
	return out, nil
}

// briefText is the agents' brief as it is now: the team's brief.md, else rota's built-in one.
func (t *Team) briefText() (string, error) {
	b, err := os.ReadFile(t.Path(briefFile))
	if os.IsNotExist(err) {
		return DefaultBrief, nil
	}
	return string(b), err
}

func (t *Team) checkBrief(ch *ChatChange) error {
	brief, err := t.briefText()
	if err != nil {
		return err
	}
	if _, err := briefEdit(*ch, brief); err != nil {
		return err
	}
	ch.Title, ch.Say = "The agents' brief: what each is told at the start of every run", "Change this passage"
	return nil
}

func (t *Team) applyBrief(ch ChatChange, line *ChatMsg) (string, func() error, error) {
	path := t.Path(briefFile)
	var before string
	var built bool // the built-in brief: the team had no brief.md
	err := changeLocked(path, func() error {
		_, err := os.Stat(path)
		built = os.IsNotExist(err)
		brief, err := t.briefText()
		if err != nil {
			return err
		}
		after, err := briefEdit(ch, brief)
		if err != nil {
			return err
		}
		before = brief
		return writeWhole(path, []byte(after))
	})
	if err != nil {
		return "", nil, err
	}
	line.Undo["brief"] = before
	if built {
		line.Undo["built"] = "true"
	}
	return "Saved: each agent's next run is told so.", func() error { _, err := t.undoBrief(ch, *line); return err }, nil
}

// undoBrief puts the brief back as it was, unless it changed since.
func (t *Team) undoBrief(ch ChatChange, line ChatMsg) (string, error) {
	path, before := t.Path(briefFile), line.Undo["brief"]
	applied, _ := briefEdit(ch, before)
	err := changeLocked(path, func() error {
		if b, err := os.ReadFile(path); err != nil || string(b) != applied {
			return fmt.Errorf("the brief has changed since; undo would overwrite that")
		}
		if line.Undo["built"] == "true" {
			return os.Remove(path) // the built-in brief again, as rota brings it
		}
		return writeWhole(path, []byte(before))
	})
	return "Undone: the agents' brief is as it was.", err
}

// swapRole writes role into an agent's file and returns the one it replaced; with expect, only
// while the role is that one.
func swapRole(path, role, expect string) (string, error) {
	var old string
	err := changeLocked(path, func() error {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		head, body, err := splitFront(string(b))
		if err != nil {
			return err
		}
		if old = strings.TrimSpace(body); expect != "" && old != strings.TrimSpace(expect) {
			return fmt.Errorf("the role has changed since; undo would overwrite that")
		}
		return writeWhole(path, []byte("---\n"+head+"\n---\n"+strings.TrimSpace(role)+"\n"))
	})
	return old, err
}

// ---- what agents asked ----

// openQuestion is the open question a thread of the mail is: q:<id>.
func (t *Team) openQuestion(thread string) (Question, error) {
	id, ok := strings.CutPrefix(thread, "q:")
	if !ok || id == "" {
		return Question{}, fmt.Errorf("name the question by its thread, q:<id>")
	}
	for _, q := range t.Questions() {
		if q.ID == id {
			return q, nil
		}
	}
	return Question{}, fmt.Errorf("%s is no longer open", thread)
}

// questionTitle is how a card names a question: its agent, its subject, and what it is about.
func (t *Team) questionTitle(q Question) string {
	th := t.questionThread(q, t.repoLabels())
	return strings.Join(slices.DeleteFunc([]string{q.Agent, th.Label, th.Subject}, func(s string) bool { return s == "" }), " · ")
}

func (t *Team) checkAnswer(ch *ChatChange) error {
	q, err := t.openQuestion(ch.Thread)
	if err != nil {
		return err
	}
	if ch.Do == "answer" {
		switch {
		case ch.Text == "":
			return fmt.Errorf("the answer is empty")
		case len(ch.Text) > chatMax:
			return fmt.Errorf("an answer holds at most %d characters", chatMax)
		}
		ch.Text = choice(q.Options, ch.Text)
	}
	ch.Agent, ch.Title = q.Agent, t.questionTitle(q)
	ch.Say = map[string]string{"answer": "Answer it", "dismiss": "Dismiss it, unanswered"}[ch.Do]
	return nil
}

// choice is the option an answer names, as "B" names "B: accept it as a known risk", or the
// answer as it is.
func choice(options []string, answer string) string {
	var named []string
	for _, o := range options {
		label, _, ok := strings.Cut(o, ":")
		switch {
		case strings.EqualFold(o, answer):
			return o
		case ok && strings.EqualFold(strings.TrimSpace(label), strings.TrimSuffix(answer, ".")):
			named = append(named, o)
		}
	}
	if len(named) == 1 {
		return named[0]
	}
	return answer
}

func (t *Team) applyAnswer(ch ChatChange, line *ChatMsg) (string, func() error, error) {
	qid, said := strings.TrimPrefix(ch.Thread, "q:"), "Answered "+ch.Agent+"."
	var err error
	if ch.Do == "dismiss" {
		said, err = ch.Agent+"'s question is dismissed; it is told.", t.Dismiss(qid)
	} else {
		err = t.Answer(qid, ch.Text)
	}
	if err != nil {
		return "", nil, err
	}
	t.MarkMail(ch.Thread, "read", "", time.Now()) // as an answer in the mail
	return said, nil, nil
}

func (t *Team) checkTick(ch *ChatChange) error {
	q, err := t.openQuestion(ch.Thread)
	if err != nil {
		return err
	}
	switch {
	case len(q.Steps) == 0:
		return fmt.Errorf("%s is a question, not a to-do with steps", ch.Thread)
	case ch.Step < 1 || ch.Step > len(q.Steps):
		return fmt.Errorf("which step? the to-do has %d, from 1", len(q.Steps))
	}
	ch.Agent, ch.Title, ch.Lines = q.Agent, t.questionTitle(q), []string{cut(q.Steps[ch.Step-1], 300)}
	ch.Say = fmt.Sprintf("%s step %d", map[string]string{"tick": "Tick", "untick": "Untick"}[ch.Do], ch.Step)
	return nil
}

func (t *Team) applyTick(ch ChatChange, line *ChatMsg) (string, func() error, error) {
	if q, err := t.openQuestion(ch.Thread); err != nil || ch.Step > len(q.Steps) { // done since: its steps are done
		return "", nil, fmt.Errorf("%s is no longer open", ch.Thread)
	}
	on := ch.Do == "tick"
	was, err := t.markMail(ch.Thread, "tick", fmt.Sprintf("%d:%s", ch.Step-1, map[bool]string{true: "on", false: "off"}[on]), time.Now())
	if err != nil {
		return "", nil, err
	}
	line.Undo["ticked"] = strconv.FormatBool(slices.Contains(was.Ticks, ch.Step-1))
	said := fmt.Sprintf("%s step %d.", map[bool]string{true: "Ticked", false: "Unticked"}[on], ch.Step)
	return said, func() error { _, err := t.undoTick(ch, *line); return err }, nil
}

// undoTick puts a step back as it was, unless it was ticked or unticked since.
func (t *Team) undoTick(ch ChatChange, line ChatMsg) (string, error) {
	was, k := line.Undo["ticked"] == "true", ch.Step-1
	var changed bool
	err := t.changeMail(time.Now(), func(s *mailState) {
		m := s.Marks[ch.Thread]
		if changed = slices.Contains(m.Ticks, k) != (ch.Do == "tick"); changed {
			return
		}
		if m.Ticks = slices.DeleteFunc(m.Ticks, func(i int) bool { return i == k }); was {
			m.Ticks = append(m.Ticks, k)
			slices.Sort(m.Ticks)
		}
		s.Marks[ch.Thread] = m
	})
	if err == nil && changed {
		err = fmt.Errorf("step %d was changed since; undo would overwrite that", ch.Step)
	}
	return fmt.Sprintf("Undone: step %d is %s again.", ch.Step, map[bool]string{true: "ticked", false: "not ticked"}[was]), err
}

// ---- messages and pull requests ----

func (t *Team) checkMessage(ch *ChatChange) error {
	a := t.Agent(ch.Agent)
	switch {
	case a == nil:
		return fmt.Errorf("to whom? no agent is called %q", ch.Agent)
	case ch.Text == "":
		return fmt.Errorf("the message is empty")
	case len(ch.Text) > chatMax:
		return fmt.Errorf("a message holds at most %d characters", chatMax)
	}
	ref, err := t.NoteRef(ch.Agent, ch.About)
	if err != nil {
		return err
	}
	if m := fullRefOnly.FindStringSubmatch(ref); ref != "" && (m == nil || !slices.Contains(a.Repos, m[1])) {
		return fmt.Errorf("%s is not a pull request or issue of %s's repositories", ref, ch.Agent)
	}
	ch.About, ch.Title, ch.Say = ref, "To "+ch.Agent, "Send this message"
	if _, label := refLabel(ref, t.repoLabels()); label != "" {
		ch.Title += " · " + label
	}
	return nil
}

func (t *Team) applyMessage(ch ChatChange, line *ChatMsg) (string, func() error, error) {
	thread, err := t.SendMessage(ch.Agent, ch.About, ch.Text, "")
	if err != nil {
		return "", nil, err
	}
	line.Undo["thread"] = thread
	return "Sent to " + ch.Agent + ": its answer comes to your inbox.", func() error { return t.unsend(thread, thread) }, nil
}

// undoMessage takes the message back while its agent has not read it.
func (t *Team) undoMessage(ch ChatChange, line ChatMsg) (string, error) {
	thread := line.Undo["thread"]
	return "Taken back: " + ch.Agent + " does not get it.", t.unsend(thread, thread)
}

func (t *Team) checkUnsend(ch *ChatChange) error {
	id, ok := strings.CutPrefix(ch.Thread, "m:")
	for _, m := range t.MessageThreads() {
		if !ok || m.ID != id {
			continue
		}
		last := m.Said[len(m.Said)-1]
		if !m.Gone.IsZero() || last.From != "you" || !last.Read.IsZero() {
			return fmt.Errorf("%s has read that message already, or it is taken back", m.To)
		}
		ch.Agent, ch.Title, ch.Say, ch.Lines, ch.Note = m.To, "To "+m.To+" · "+subjectOf(m.Said[0].Text), "Take your message back", []string{cut(last.Text, 300)}, last.Note
		return nil
	}
	return fmt.Errorf("no message of yours is %s", ch.Thread)
}

func (t *Team) applyUnsend(ch ChatChange, line *ChatMsg) (string, func() error, error) {
	if err := t.unsend(strings.TrimPrefix(ch.Thread, "m:"), ch.Note); err != nil {
		return "", nil, err
	}
	return "Taken back: " + ch.Agent + " does not get it.", nil, nil
}

// prThread is the thread of a pull request in the mail, pr:<owner/name#n>, with its repository
// and number.
func (t *Team) prThread(thread string) (Thread, string, int, error) {
	ref, ok := strings.CutPrefix(thread, "pr:")
	repo, num, ok2 := strings.Cut(ref, "#")
	n, err := strconv.Atoi(num)
	if !ok || !ok2 || err != nil || n <= 0 {
		return Thread{}, "", 0, fmt.Errorf("name the pull request by its thread, pr:<owner/name#n>")
	}
	for _, th := range t.Mail(time.Now()).Threads {
		if th.ID == thread && th.PR != nil {
			return th, repo, n, nil
		}
	}
	return Thread{}, "", 0, fmt.Errorf("%s is not a pull request the team has open", ref)
}

func (t *Team) checkPR(ch *ChatChange) error {
	th, _, _, err := t.prThread(ch.Thread)
	if err != nil {
		return err
	}
	switch {
	case ch.Do == "approve" && th.PR.State == "approved":
		return fmt.Errorf("%s is approved already", or(th.Label, th.Ref))
	case ch.Do == "review-note" && ch.Text == "":
		return fmt.Errorf("the note is empty")
	case len(ch.Text) > githubTextMax:
		return fmt.Errorf("a note holds at most %d characters", githubTextMax)
	}
	ch.Agent, ch.Title, ch.Say = th.PR.Author, or(th.Label, th.Ref)+" · "+th.Subject, "Approve it on GitHub"
	ch.Lines = []string{"Notes you left on it as drafts in its review go with it, as from the review."}
	if ch.Do == "review-note" {
		ch.Say = "Send this note on GitHub"
		if th.PR.Author != "" {
			ch.Say += ", for " + th.PR.Author
		}
	}
	return nil
}

func (t *Team) applyPR(ch ChatChange, line *ChatMsg) (string, func() error, error) {
	_, repo, n, err := t.prThread(ch.Thread)
	if err != nil {
		return "", nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second) // within the pages' write deadline
	defer cancel()
	ref := repo + "#" + strconv.Itoa(n)
	said, warn := "Approved "+ref+".", ""
	if ch.Do == "approve" {
		warn, err = t.ApprovePR(ctx, repo, n)
	} else {
		said = "Sent your note on " + ref + "."
		warn, err = t.NotePR(ctx, repo, n, ch.Text)
	}
	if err != nil {
		return "", nil, err
	}
	if warn != "" { // GitHub has it; telling the agents did not work
		said = strings.TrimSuffix(said, ".") + ", but " + warn + "."
	}
	return said, nil, nil
}

// ---- the team's work on GitHub: merged, closed, labelled, commented on, a new issue ----

// githubTextMax is the most GitHub takes in a comment or a description.
const githubTextMax = 65536

// humanMerges lets one merge card merge at a time, so two never rebase in the same clone at once.
var humanMerges sync.Mutex

// mergeItem is a pull request a merge card merges: its thread, and GitHub's last list of it.
type mergeItem struct {
	th      Thread
	repo    string
	n       int
	it      openItem
	thread  string
	pending bool
}

func (t *Team) checkMerge(ch *ChatChange) error {
	if ch.Thread != "" {
		ch.Threads, ch.Thread = append([]string{ch.Thread}, ch.Threads...), ""
	}
	var ids []string
	for _, id := range ch.Threads {
		if id = strings.TrimSpace(id); !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return fmt.Errorf("which pull requests? name each by its thread, pr:<owner/name#n>")
	}
	open := t.openWork()
	var prs []*mergeItem
	for _, id := range ids {
		th, repo, n, err := t.prThread(id)
		if err != nil {
			return err
		}
		prs = append(prs, &mergeItem{th: th, repo: repo, n: n, it: open.Repos[repo][n], thread: id, pending: true})
	}
	in := func(repo string, n int) bool {
		return slices.ContainsFunc(prs, func(p *mergeItem) bool { return strings.EqualFold(p.repo, repo) && p.n == n })
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var lines []string
	for _, p := range prs {
		name := or(p.th.Label, p.th.Ref)
		nums := slices.Sorted(maps.Keys(open.Repos[p.repo]))
		for _, un := range nums {
			if u := open.Repos[p.repo][un]; u.PR && un != p.n && u.Head != "" && u.Head == p.it.Base && !in(p.repo, un) {
				lines = append(lines, fmt.Sprintf("%s is stacked on #%d, which this does not merge: it merges into #%d's branch, %s, not into the base of #%d.", name, un, un, p.it.Base, un))
			}
		}
		if p.it.Draft {
			lines = append(lines, name+" is a draft: it is marked ready first.")
		}
		if hasLabel(p.it.Labels, LeaveLabel) {
			lines = append(lines, name+" carries "+LeaveLabel+", which keeps the team off it, not you.")
		}
		if t.HoldLabel != "" && hasLabel(p.it.Labels, t.HoldLabel) {
			lines = append(lines, name+" waits for its rollout: it merges all the same, and "+t.HoldLabel+" stays on it, so its rollout is still followed up.")
		}
		for _, d := range p.it.Deps {
			drepo, dn, err := t.parseRef(d, p.repo)
			if err != nil || in(drepo, dn) {
				continue
			}
			dref := drepo + "#" + strconv.Itoa(dn)
			if why := t.unmet(ctx, drepo, dn); why != "" {
				if strings.EqualFold(drepo, p.repo) {
					dref = "#" + strconv.Itoa(dn)
				}
				lines = append(lines, fmt.Sprintf("%s depends on %s, which %s: it merges all the same.", name, dref, strings.TrimPrefix(why, drepo+"#"+strconv.Itoa(dn)+" ")))
			}
		}
		for _, an := range nums {
			if a := open.Repos[p.repo][an]; a.PR && an != p.n && p.it.Head != "" && a.Base == p.it.Head && !in(p.repo, an) {
				lines = append(lines, fmt.Sprintf("#%d, stacked on %s, goes onto %s, rebased onto the merge.", an, name, or(p.it.Base, "its base")))
			}
		}
	}
	var names []string
	ch.Threads = nil
	for _, p := range mergeOrder(prs) {
		ch.Threads, names = append(ch.Threads, p.thread), append(names, or(p.th.Label, p.th.Ref))
	}
	ch.Lines = lines
	if len(prs) == 1 {
		ch.Title, ch.Say = names[0]+" · "+prs[0].th.Subject, "Squash-merge it on GitHub"
	} else {
		ch.Title, ch.Say = strings.Join(names, ", "), "Squash-merge them on GitHub, in this order"
	}
	return nil
}

// waitsOn says whether p waits for q: it is stacked on q's branch, or it depends on q, in any of
// the team's repositories.
func (p *mergeItem) waitsOn(q *mergeItem) bool {
	ref := q.repo + "#" + strconv.Itoa(q.n)
	return p != q && (strings.EqualFold(q.repo, p.repo) && q.it.Head != "" && q.it.Head == p.it.Base ||
		slices.ContainsFunc(p.it.Deps, func(d string) bool { return strings.EqualFold(d, ref) }))
}

// mergeOrder is the order to merge prs in: each after those it is stacked on or depends on
// among them, otherwise as they came.
func mergeOrder(prs []*mergeItem) []*mergeItem {
	var out []*mergeItem
	waits := func(p *mergeItem) bool {
		return slices.ContainsFunc(prs, func(q *mergeItem) bool { return q.pending && p.waitsOn(q) })
	}
	for len(out) < len(prs) {
		next := slices.IndexFunc(prs, func(p *mergeItem) bool { return p.pending && !waits(p) })
		if next < 0 { // they wait on each other: as they came
			next = slices.IndexFunc(prs, func(p *mergeItem) bool { return p.pending })
		}
		prs[next].pending = false
		out = append(out, prs[next])
	}
	return out
}

// ghSaid is how gh's own words begin when it fails: what a card says without it.
var ghSaid = regexp.MustCompile(`^gh [a-z -]+: exit status \d+: (GraphQL: )?`)

// workMerge merges a card's pull requests in its order, its card saying which it merges and what
// came of each. One that waits for one that did not merge is not merged: it would bring that one's
// commits in without it.
func (t *Team) workMerge(ch ChatChange, step func([]string)) ([]string, string) {
	open := t.openWork() // what waits for what, read before these merges move branches
	var prs []*mergeItem
	for _, id := range ch.Threads {
		p := &mergeItem{thread: id}
		repo, n, err := t.parseRef(strings.TrimPrefix(id, "pr:"), "")
		if err == nil {
			p.repo, p.n, p.it = repo, n, open.Repos[repo][n]
		}
		prs = append(prs, p)
	}
	label := func(p *mergeItem) string {
		if p.repo == "" {
			return strings.TrimPrefix(p.thread, "pr:")
		}
		return shortRef(t.teamRepos(), p.repo) + "#" + strconv.Itoa(p.n)
	}
	if !humanMerges.TryLock() {
		step([]string{"Waits for the merge under way to end…"})
		humanMerges.Lock()
	}
	defer humanMerges.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	var lines []string
	var notMerged []*mergeItem
	relist := map[string]bool{}
	for _, p := range prs {
		if i := slices.IndexFunc(notMerged, p.waitsOn); i >= 0 {
			lines, notMerged = append(lines, label(p)+" is not merged: it waits for "+label(notMerged[i])+", which did not merge."), append(notMerged, p)
			step(lines)
			continue
		}
		if p.repo == "" {
			lines, notMerged = append(lines, label(p)+" is not merged: it is not a pull request of the team's."), append(notMerged, p)
			continue
		}
		step(append(slices.Clone(lines), "Merging "+label(p)+"…"))
		o, err := t.mergeAsHuman(ctx, p.repo, p.n)
		if err != nil {
			why := ghSaid.ReplaceAllString(strings.Replace(err.Error(), p.repo+"#"+strconv.Itoa(p.n)+" ", "it ", 1), "")
			lines, notMerged = append(lines, label(p)+" is not merged: "+sentence(why)), append(notMerged, p)
		} else {
			lines, relist[p.repo] = append(lines, o.short(label(p))), true
		}
		step(lines)
	}
	for repo := range relist {
		t.relistOpen(repo)
	}
	switch len(notMerged) {
	case 0:
		return lines, "applied"
	case len(prs):
		return lines, "failed"
	}
	return lines, "partly"
}

// workItem is a pull request or an issue of the team's repositories that a change on GitHub
// names: as GitHub listed it last when it is open, otherwise as GitHub has it now.
type workItem struct {
	repo, title, state string // state: OPEN, CLOSED or MERGED
	n                  int
	pr                 bool
	labels             []string
}

func (w workItem) ref() string { return w.repo + "#" + strconv.Itoa(w.n) }

// is is what gh calls it: pr or issue.
func (w workItem) is() string {
	if w.pr {
		return "pr"
	}
	return "issue"
}

// workItem reads ref, <owner/name or name>#n, in the team's repositories only: others are not
// the team's to change.
func (t *Team) workItem(ref string) (workItem, error) {
	repo, n, err := t.parseRef(ref, "")
	if err != nil {
		return workItem{}, err
	}
	w := workItem{repo: repo, n: n}
	if it, ok := t.openWork().Repos[repo][n]; ok {
		w.pr, w.title, w.state, w.labels = it.PR, it.Title, "OPEN", it.Labels
		return w, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var v struct {
		Title  string    `json:"title"`
		State  string    `json:"state"`
		Labels []ghLabel `json:"labels"`
	}
	w.pr = true
	if err := t.gh(ctx, &v, "pr", "view", strconv.Itoa(n), "-R", repo, "--json", "title,state,labels"); err != nil {
		w.pr = false
		if err := t.gh(ctx, &v, "issue", "view", strconv.Itoa(n), "-R", repo, "--json", "title,state,labels"); err != nil {
			return workItem{}, fmt.Errorf("GitHub has no pull request or issue %s: %v", w.ref(), err)
		}
	}
	w.title, w.state = v.Title, v.State
	for _, l := range v.Labels {
		w.labels = append(w.labels, l.Name)
	}
	return w, nil
}

// workCard fills in what a card about w shows: its name and title, and what w is.
func (t *Team) workCard(ch *ChatChange, w workItem) {
	ch.About, ch.Is = w.ref(), w.is()
	ch.Title = shortRef(t.teamRepos(), w.repo) + "#" + strconv.Itoa(w.n) + " · " + w.title
}

// onGitHub runs gh about the pull request or issue a card names, as the human, and lists the
// repository's open work again, so the pages show it as GitHub has it now.
func (t *Team) onGitHub(ch ChatChange, out any, verb string, args ...string) error {
	repo, n, err := t.parseRef(ch.About, "")
	if err != nil {
		return err
	}
	if ch.Is != "pr" && ch.Is != "issue" {
		return fmt.Errorf("%s is not a pull request or an issue", ch.About)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second) // within the pages' write deadline
	defer cancel()
	if err := t.gh(ctx, out, append([]string{ch.Is, verb, strconv.Itoa(n), "-R", repo}, args...)...); err != nil {
		return err
	}
	t.relistOpen(repo)
	return nil
}

var stateDone = map[string]string{"close": "Closed", "reopen": "Reopened"}

func (t *Team) checkState(ch *ChatChange) error {
	w, err := t.workItem(ch.About)
	if err != nil {
		return err
	}
	switch {
	case ch.Do == "close" && w.state != "OPEN":
		return fmt.Errorf("%s is %s already", w.ref(), strings.ToLower(w.state))
	case ch.Do == "reopen" && w.state == "MERGED":
		return fmt.Errorf("%s is merged: it is not reopened", w.ref())
	case ch.Do == "reopen" && w.state == "OPEN":
		return fmt.Errorf("%s is open", w.ref())
	case len(ch.Text) > githubTextMax:
		return fmt.Errorf("a comment holds at most %d characters", githubTextMax)
	}
	t.workCard(ch, w)
	ch.Say = map[string]string{"close": "Close it on GitHub", "reopen": "Reopen it on GitHub"}[ch.Do]
	if ch.Text != "" {
		ch.Say += ", with this comment"
	}
	return nil
}

func (t *Team) applyState(ch ChatChange, line *ChatMsg) (string, func() error, error) {
	var args []string
	if ch.Text != "" {
		args = []string{"--comment", ch.Text}
	}
	if err := t.onGitHub(ch, nil, ch.Do, args...); err != nil {
		return "", nil, err
	}
	back := map[string]string{"close": "reopen", "reopen": "close"}[ch.Do]
	return stateDone[ch.Do] + " " + ch.About + ".", func() error { return t.onGitHub(ch, nil, back) }, nil
}

func (t *Team) undoState(ch ChatChange, line ChatMsg) (string, error) {
	back := map[string]string{"close": "reopen", "reopen": "close"}[ch.Do]
	if err := t.onGitHub(ch, nil, back); err != nil {
		return "", err
	}
	return stateDone[back] + " " + ch.About + " again.", nil
}

// labelName is a label of repo, as the repository spells it, that a card may put on a pull request
// or an issue there: one the repository has. The team's labels are in every repository.
func (t *Team) labelName(ctx context.Context, repo, label string) (string, error) {
	switch {
	case label == "" || len(label) > 50 || strings.ContainsAny(label, ",\"\r\n"): // GitHub's longest; gh reads a list of them as CSV
		return "", fmt.Errorf("%q is not a label's name", label)
	}
	labels := t.Labels()
	if i := slices.IndexFunc(labels, func(l Label) bool { return strings.EqualFold(l.Name, label) }); i >= 0 {
		return labels[i].Name, nil
	}
	var l ghLabel
	if err := t.gh(ctx, &l, "api", "repos/"+repo+"/labels/"+url.PathEscape(label)); err != nil || l.Name == "" {
		return "", fmt.Errorf("%s has no label %q", repo, label)
	}
	return l.Name, nil
}

func (t *Team) checkLabels(ch *ChatChange) error {
	w, err := t.workItem(ch.About)
	if err != nil {
		return err
	}
	if len(ch.Labels) == 0 {
		return fmt.Errorf("which labels?")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var change []string
	for _, l := range ch.Labels {
		at := slices.IndexFunc(w.labels, func(o string) bool { return strings.EqualFold(o, l) })
		switch {
		case ch.Do == "unlabel" && at >= 0:
			change = append(change, w.labels[at]) // as it is spelled there
		case ch.Do == "unlabel":
		case at >= 0:
		default:
			name, err := t.labelName(ctx, w.repo, l)
			if err != nil {
				return err
			}
			if !hasLabel(change, name) {
				change = append(change, name)
			}
		}
	}
	if len(change) == 0 {
		if ch.Do == "unlabel" {
			return fmt.Errorf("%s carries none of them", w.ref())
		}
		return fmt.Errorf("%s carries them already", w.ref())
	}
	t.workCard(ch, w)
	ch.Labels = change
	ch.Say = "Put on " + strings.Join(change, ", ")
	if ch.Do == "unlabel" {
		ch.Say = "Take off " + strings.Join(change, ", ")
	}
	return nil
}

// editLabels puts a card's labels on (add) or takes them off.
func (t *Team) editLabels(ch ChatChange, add bool) error {
	flag := "--remove-label"
	if add {
		flag = "--add-label"
	}
	return t.onGitHub(ch, nil, "edit", flag, strings.Join(ch.Labels, ","))
}

func (t *Team) applyLabels(ch ChatChange, line *ChatMsg) (string, func() error, error) {
	add := ch.Do == "label"
	if err := t.editLabels(ch, add); err != nil {
		return "", nil, err
	}
	said := ch.About + " carries " + strings.Join(ch.Labels, ", ") + " now."
	if !add {
		said = ch.About + " no longer carries " + strings.Join(ch.Labels, ", ") + "."
	}
	return said, func() error { return t.editLabels(ch, !add) }, nil
}

func (t *Team) undoLabels(ch ChatChange, line ChatMsg) (string, error) {
	add := ch.Do != "label"
	if err := t.editLabels(ch, add); err != nil {
		return "", err
	}
	if add {
		return ch.About + " carries " + strings.Join(ch.Labels, ", ") + " again.", nil
	}
	return ch.About + " no longer carries " + strings.Join(ch.Labels, ", ") + ".", nil
}

func (t *Team) checkComment(ch *ChatChange) error {
	w, err := t.workItem(ch.About)
	switch {
	case err != nil:
		return err
	case ch.Text == "":
		return fmt.Errorf("the comment is empty")
	case len(ch.Text) > githubTextMax:
		return fmt.Errorf("a comment holds at most %d characters", githubTextMax)
	}
	t.workCard(ch, w)
	ch.Say = "Comment on GitHub"
	return nil
}

func (t *Team) applyComment(ch ChatChange, line *ChatMsg) (string, func() error, error) {
	if err := t.onGitHub(ch, nil, "comment", "--body", ch.Text); err != nil {
		return "", nil, err
	}
	return "Commented on " + ch.About + ".", nil, nil
}

// openedURL is the address gh prints of the issue it opened.
var openedURL = regexp.MustCompile(`/issues/(\d+)\s*$`)

func (t *Team) checkIssue(ch *ChatChange) error {
	repo, ok := resolveRepo(t.teamRepos(), ch.Repo)
	switch {
	case !ok:
		return fmt.Errorf("no repository of the team is %q", ch.Repo)
	case ch.Subject == "":
		return fmt.Errorf("give the issue a title: subject")
	case len(ch.Subject) > 256 || strings.ContainsAny(ch.Subject, "\r\n"):
		return fmt.Errorf("an issue's title is one line")
	case len(ch.Text) > githubTextMax:
		return fmt.Errorf("an issue's description holds at most %d characters", githubTextMax)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var labels []string
	for _, l := range ch.Labels {
		name, err := t.labelName(ctx, repo, l)
		if err != nil {
			return err
		}
		if !hasLabel(labels, name) {
			labels = append(labels, name)
		}
	}
	ch.Repo, ch.Labels = repo, labels
	ch.Title, ch.Say = shortRef(t.teamRepos(), repo)+" · "+ch.Subject, "Open this issue on GitHub"
	if len(ch.Labels) > 0 {
		ch.Lines = []string{"With " + strings.Join(ch.Labels, ", ") + "."}
	}
	return nil
}

func (t *Team) applyIssue(ch ChatChange, line *ChatMsg) (string, func() error, error) {
	if _, ok := resolveRepo(t.teamRepos(), ch.Repo); !ok {
		return "", nil, fmt.Errorf("no repository of the team is %q", ch.Repo)
	}
	args := []string{"issue", "create", "-R", ch.Repo, "--title", ch.Subject, "--body", ch.Text}
	for _, l := range ch.Labels {
		args = append(args, "--label", l)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var out string
	if err := t.gh(ctx, &out, args...); err != nil {
		return "", nil, err
	}
	m := openedURL.FindStringSubmatch(out)
	if m == nil {
		return "Opened the issue in " + ch.Repo + ".", nil, nil // GitHub has it; which it is, gh did not say
	}
	ref := ch.Repo + "#" + m[1]
	line.Undo = map[string]string{"issue": ref}
	t.relistOpen(ch.Repo)
	opened := ch
	opened.About, opened.Is = ref, "issue"
	return "Opened " + ref + ".", func() error { return t.onGitHub(opened, nil, "close", "--reason", "not planned") }, nil
}

func (t *Team) undoIssue(ch ChatChange, line ChatMsg) (string, error) {
	ref := line.Undo["issue"]
	if ref == "" {
		return "", fmt.Errorf("which issue it opened is not known: close it on GitHub")
	}
	opened := ch
	opened.About, opened.Is = ref, "issue"
	if err := t.onGitHub(opened, nil, "close", "--reason", "not planned"); err != nil {
		return "", err
	}
	return "Closed " + ref + " again, as not planned.", nil
}

// ghRepo is the repository a gh command that ghScope gave names, owner/name.
func ghRepo(args []string) string {
	for _, a := range args {
		if repo, ok := strings.CutPrefix(a, "--repo="); ok {
			return repo
		}
		if rest, ok := strings.CutPrefix(a, "repos/"); ok {
			parts := strings.SplitN(rest, "/", 3)
			if len(parts) >= 2 {
				return parts[0] + "/" + parts[1]
			}
		}
	}
	return ""
}

// ghWords is a gh command as a card shows it, each argument quoted that needs it.
func ghWords(args []string) string {
	words := []string{"gh"}
	for _, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\n\"'$`\\|&;<>(){}*?#~") {
			a = strconv.Quote(a)
		}
		words = append(words, a)
	}
	return strings.Join(words, " ")
}

func (t *Team) checkGitHub(ch *ChatChange) error {
	args, err := t.ghScope(ch.Args, true)
	if err != nil {
		return err
	}
	ch.Args = args
	ch.Title = shortRef(t.teamRepos(), ghRepo(args)) + " · gh " + args[0]
	if args[0] != "api" {
		ch.Title += " " + args[1]
	}
	ch.Say, ch.Text = "Do this on GitHub, as you", ghWords(args)
	return nil
}

func (t *Team) applyGitHub(ch ChatChange, line *ChatMsg) (string, func() error, error) {
	out, err := t.githubRun(ch.Args)
	if err != nil {
		return "", nil, err
	}
	t.relistOpen(ghRepo(ch.Args))
	if out = FirstLine(out, 200); out != "" {
		return "Done on GitHub: " + out, nil, nil
	}
	return "Done on GitHub.", nil, nil
}

// ---- the mail ----

// markActs are the marks the Team may put on the mail's threads: what MarkMail does for each,
// what a card says it does, and what is said once it did.
var markActs = map[string]struct{ act, arg, say, done string }{
	"archive":   {"archive", "", "Archive", "Archived"},
	"unarchive": {"unarchive", "", "Move back to the inbox", "Moved back to the inbox"},
	"star":      {"star", "", "Star", "Starred"},
	"unstar":    {"unstar", "", "Take the star off", "The star is off"},
	"read":      {"read", "", "Mark read", "Marked read"},
	"unread":    {"unread", "", "Mark unread", "Marked unread"},
	"snooze":    {"snooze", "", "Snooze until ", "Snoozed"},
	"unsnooze":  {"unsnooze", "", "Unsnooze", "Unsnoozed"},
	"focused":   {"place", "focused", "Move to Focused", "Moved to Focused"},
	"other":     {"place", "other", "Move to Other", "Moved to Other"},
}

// marksMax is the most threads one card marks.
const marksMax = 100

func (t *Team) checkMarks(ch *ChatChange) error {
	if ch.Thread != "" {
		ch.Threads, ch.Thread = append(ch.Threads, ch.Thread), ""
	}
	slices.Sort(ch.Threads)
	ch.Threads = slices.Compact(ch.Threads)
	switch {
	case len(ch.Threads) == 0:
		return fmt.Errorf("which threads of the mail?")
	case len(ch.Threads) > marksMax:
		return fmt.Errorf("at most %d threads at once", marksMax)
	}
	ch.Say = markActs[ch.Do].say
	if ch.Do == "snooze" {
		until, err := time.Parse(time.RFC3339, ch.Until)
		if err != nil || !until.After(time.Now()) {
			return fmt.Errorf("snooze until when? a time to come, as 2026-11-09T09:00:00+01:00")
		}
		ch.Say += until.In(t.Zone.Location).Format("Mon 2 Jan 15:04")
	}
	threads := map[string]Thread{}
	for _, th := range t.Mail(time.Now()).Threads {
		threads[th.ID] = th
	}
	ch.Lines = nil
	for _, id := range ch.Threads {
		th, ok := threads[id]
		if !ok {
			return fmt.Errorf("the mail has no thread %s", id)
		}
		ch.Lines = append(ch.Lines, threadWords(th))
	}
	if ch.Title = fmt.Sprintf("%d threads", len(ch.Lines)); len(ch.Lines) == 1 {
		ch.Title, ch.Lines = ch.Lines[0], nil
	}
	return nil
}

func (t *Team) applyMarks(ch ChatChange, line *ChatMsg) (string, func() error, error) {
	at, m := time.Now(), markActs[ch.Do]
	line.Undo[markedAt] = at.Format(time.RFC3339Nano)
	takeBack := func() error { return t.restoreMarks(ch, *line) }
	for _, id := range ch.Threads {
		was, err := t.markMail(id, m.act, markArg(ch), at)
		if err != nil {
			if berr := takeBack(); berr != nil {
				return "", nil, fmt.Errorf("%v; and what was marked before it stays so: %v", err, berr)
			}
			return "", nil, err
		}
		b, _ := json.Marshal(was)
		line.Undo[id] = string(b)
	}
	said := m.done
	if len(ch.Threads) > 1 {
		said += fmt.Sprintf(": %d threads", len(ch.Threads))
	}
	return said + ".", takeBack, nil
}

func (t *Team) undoMarks(ch ChatChange, line ChatMsg) (string, error) {
	return "Undone: the mail is as it was.", t.restoreMarks(ch, line)
}

// markedAt keeps, among what undo needs, when the marks were put on.
const markedAt = ".at"

// markArg is what a mark tells MarkMail: a snooze's end, the box a thread moves to.
func markArg(ch ChatChange) string {
	if ch.Do == "snooze" {
		return ch.Until
	}
	return markActs[ch.Do].arg
}

// restoreMarks puts back what a mark changed on each thread it marked, as it was before, where
// that is still what the mark made it: what the human changed since stays.
func (t *Team) restoreMarks(ch ChatChange, line ChatMsg) error {
	at, _ := time.Parse(time.RFC3339Nano, line.Undo[markedAt])
	until, _ := time.Parse(time.RFC3339, ch.Until)
	return t.changeMail(time.Now(), func(s *mailState) {
		for _, id := range ch.Threads {
			var was mailMark
			if raw, ok := line.Undo[id]; !ok || json.Unmarshal([]byte(raw), &was) != nil {
				continue
			}
			m := s.Marks[id]
			switch ch.Do {
			case "archive":
				if m.Done.Equal(at) {
					m.Done = was.Done
				}
			case "unarchive":
				if m.Done.IsZero() {
					m.Done = was.Done
				}
			case "star", "unstar":
				if m.Star == (ch.Do == "star") {
					m.Star = was.Star
				}
			case "read":
				if m.Read.Equal(at) { // read by the card, and not since; one marked unread since stays so
					m.Read = was.Read
					if !m.Unread {
						m.Unread = was.Unread
					}
				}
			case "unread":
				if m.Unread {
					m.Unread = was.Unread
				}
			case "snooze":
				if m.Snooze.Equal(until) {
					m.Snooze = was.Snooze
				}
			case "unsnooze":
				if m.Snooze.IsZero() {
					m.Snooze = was.Snooze
				}
			default: // focused, other
				if m.Place == markActs[ch.Do].arg {
					m.Place = was.Place
				}
			}
			s.Marks[id] = m
		}
	})
}

// threadWords is a thread of the mail in a few words: who, about what.
func threadWords(th Thread) string {
	var parts []string
	if len(th.Msgs) > 0 {
		if who := th.Msgs[0].From; th.Kind == "msg" {
			parts = append(parts, "to "+th.Msgs[0].To)
		} else if who != "" {
			parts = append(parts, who)
		}
	}
	if th.Label != "" {
		parts = append(parts, th.Label)
	}
	return strings.Join(append(parts, th.Subject), " · ")
}

// prefsText is how the human's inbox works, as text, by the names the Team sets it with.
func prefsText(p MailPrefs) map[string]string {
	return map[string]string{"split": strconv.FormatBool(p.Split), "focus": strconv.Itoa(p.Focus), "after": p.After, "notify": p.Notify}
}

// withPrefs is p with the settings set, given as text; an empty one takes the default.
func withPrefs(p MailPrefs, set map[string]string) (MailPrefs, error) {
	def := prefsText(defaultMailPrefs)
	for k, v := range set {
		if v = strings.TrimSpace(v); v == "" {
			v = def[k]
		}
		var err error
		switch k {
		case "split":
			p.Split, err = strconv.ParseBool(v)
		case "focus":
			p.Focus, err = strconv.Atoi(v)
		case "after":
			p.After = v
		case "notify":
			p.Notify = v
		default:
			return p, fmt.Errorf("the inbox has no setting %s: notify, focus, split or after", k)
		}
		if err != nil {
			return p, fmt.Errorf("%s: %q is not one", k, v)
		}
	}
	return p, p.check()
}

func (t *Team) checkInbox(ch *ChatChange) error {
	if len(ch.Set) == 0 {
		return fmt.Errorf("which of the inbox's settings?")
	}
	now := t.mailPrefs()
	p, err := withPrefs(now, ch.Set)
	if err != nil {
		return err
	}
	was, to := prefsText(now), prefsText(p)
	ch.Was = map[string]string{}
	for k := range ch.Set {
		ch.Set[k], ch.Was[k] = to[k], was[k] // as the inbox says them, so undo knows them again
	}
	ch.Title = "Your inbox · " + strings.Join(sortedKeys(ch.Set), ", ")
	return nil
}

func (t *Team) applyInbox(ch ChatChange, line *ChatMsg) (string, func() error, error) {
	err := t.changePrefs(func(p MailPrefs) (MailPrefs, error) {
		was := prefsText(p)
		for k := range ch.Set {
			line.Undo[k] = was[k]
		}
		return withPrefs(p, ch.Set)
	})
	if err != nil {
		return "", nil, err
	}
	return "Applied: " + changeText(ch), func() error { _, err := t.undoInbox(ch, *line); return err }, nil
}

// undoInbox puts back what a card set of how the inbox works, unless it changed since.
func (t *Team) undoInbox(ch ChatChange, line ChatMsg) (string, error) {
	err := t.changePrefs(func(p MailPrefs) (MailPrefs, error) {
		is := prefsText(p)
		for k, v := range ch.Set {
			if is[k] != v {
				return p, fmt.Errorf("your inbox's %s has changed since: it is %s now; undo would overwrite that", k, is[k])
			}
		}
		return withPrefs(p, line.Undo)
	})
	return "Undone: " + changeText(ch), err
}

// changePrefs changes how the human's inbox works under the mail's lock: read, changed and written
// at once, so a change made meanwhile is not written over.
func (t *Team) changePrefs(change func(MailPrefs) (MailPrefs, error)) error {
	var failed error
	err := t.changeMail(time.Now(), func(s *mailState) {
		p := defaultMailPrefs
		if s.Prefs != nil {
			p = *s.Prefs
		}
		if p, failed = change(p); failed == nil {
			s.Prefs = &p
		}
	})
	return errors.Join(failed, err)
}

// ---- the mail as the Team reads it ----

// teamMail is the human's mail as the Team reads it with each question: what waits on the human,
// what the human waits on, and the latest news, each by the id a proposal names it with.
func (t *Team) teamMail(now time.Time) string {
	loc := t.Zone.Location
	var inbox, waiting, news []string
	for _, th := range t.Mail(now).Threads { // the newest first
		s := "\n- " + th.ID + ": " + mailWords(th, loc)
		switch {
		case th.Box == BoxInbox && len(inbox) < 40:
			inbox = append(inbox, s)
		case th.Box == BoxWaiting && len(waiting) < 20:
			waiting = append(waiting, s)
		case th.Box == BoxUpdates && len(news) < 10:
			news = append(news, s)
		}
	}
	list := func(lines []string) string { return or(strings.Join(lines, ""), " nothing") }
	return "\n\nWhat waits on the human, in their inbox:" + list(inbox) +
		"\n\nWhat the human waits on:" + list(waiting) +
		"\n\nThe latest news for the human:" + list(news)
}

// teamCards are the changes the Team proposed lately and what came of each, as it reads them with
// each question: what the human applied happened between its turns.
func (t *Team) teamCards() string {
	var lines []string
	for _, m := range t.Chat(300) {
		c := m.Change
		if c == nil {
			continue
		}
		state := map[string]string{"": "not applied yet", "working": "under way", "applied": "applied", "partly": "done in part",
			"failed": "failed, and can be applied again", "stopped": "stopped when rota did", "cancelled": "cancelled", "undone": "undone"}[c.State]
		line := fmt.Sprintf("\n- %s (%s): %s: %s.", or(c.Title, c.Do), m.At.In(t.Zone.Location).Format("Mon 15:04"), or(c.Say, c.Do), or(state, c.State))
		if len(c.Done) > 0 {
			line += " " + strings.Join(c.Done, " ")
		}
		if c.Said != "" {
			line += " rota answered: " + strings.TrimSpace(c.Said)
		}
		if len(c.Lines) > 0 {
			line += " Its card said: " + strings.Join(c.Lines, " ")
		}
		lines = append(lines, line)
	}
	if len(lines) > 10 {
		lines = lines[len(lines)-10:]
	}
	return "\n\nWhat you proposed lately, and what came of it:" + or(strings.Join(lines, ""), " nothing")
}

// teamPRs are the team's open pull requests as GitHub listed them last, at most openFresh ago,
// with each what holds it: its labels, what it depends on, and the branch it is stacked on.
func (t *Team) teamPRs() string {
	l := t.openWork()
	repos := t.teamRepos()
	var lines []string
	for _, repo := range repos {
		items := l.Repos[repo]
		for _, n := range slices.Sorted(maps.Keys(items)) {
			it := items[n]
			if !it.PR {
				continue
			}
			line := fmt.Sprintf("\n- %s#%d: %s", shortRef(repos, repo), n, it.Title)
			if it.Draft {
				line += "; a draft"
			}
			if len(it.Labels) > 0 {
				line += "; labels " + strings.Join(it.Labels, ", ")
			}
			if len(it.Deps) > 0 {
				line += "; depends on " + strings.Join(it.Deps, ", ")
			}
			if it.Base != "" {
				line += "; into " + it.Base
			}
			lines = append(lines, line)
		}
	}
	when := "never"
	if !l.Time.IsZero() {
		when = l.Time.In(t.Zone.Location).Format("15:04")
	}
	return "\n\nThe team's open pull requests, as GitHub listed them at " + when + " (ask GitHub for anything newer or closed):" + or(strings.Join(lines, ""), " none")
}

// mailWords is a thread of the mail as the Team reads it: who, about what, what it asks, and
// what the human did to it.
func mailWords(th Thread, loc *time.Location) string {
	if len(th.Msgs) == 0 || th.Kind == "review" && th.PR == nil {
		return th.Kind + ": " + th.Subject
	}
	first := th.Msgs[0]
	last := th.Msgs[len(th.Msgs)-1]
	line := func(s string, n int) string { return cut(spaces.ReplaceAllString(s, " "), n) }
	var s string
	switch th.Kind {
	case "ask", "do":
		s = first.From + " asks"
		if th.Kind == "do" {
			s = "a to-do from " + first.From
		}
		if th.Label != "" {
			s += ", about " + th.Label
		}
		s += ": " + th.Subject
		if th.Kind == "ask" {
			s += ". It says: " + line(first.Text, 300)
		}
		for i, o := range first.Options {
			s += map[bool]string{true: " Its choices: ", false: ", "}[i == 0] + strconv.Quote(o)
			if o == first.Recommend {
				s += " (it recommends this)"
			}
		}
		for i, step := range first.Steps {
			s += map[bool]string{true: " Its steps: ", false: "; "}[i == 0] + strconv.Itoa(i+1) + ". " + line(step, 120)
			if slices.Contains(th.Ticks, i) {
				s += " (ticked)"
			}
		}
	case "review":
		s = or(th.Label, th.Ref) + ", a pull request"
		if th.PR.Author != "" {
			s += " by " + th.PR.Author
		}
		s += ", " + map[string]string{"review": "waits for the human's review", "waiting": "waits on the answer to the human's notes",
			"approved": "approved", "open": "still worked on"}[th.PR.State] + ": " + th.Subject
		if th.PR.Answered {
			s += " (its agent answered the human's notes)"
		}
	case "msg":
		if last.From == "you" {
			s = "the human's message to " + last.To + ", not read yet: " + line(last.Text, 200)
		} else {
			s = last.From + " answered the human's message: " + line(last.Text, 200)
		}
	case "digest":
		s = "the daily report: " + th.Subject
	default:
		s = first.From + ": " + th.Subject
	}
	if th.Unread {
		s += " (unread)"
	}
	if th.Star {
		s += " (starred)"
	}
	if !th.Snooze.IsZero() {
		s += " (snoozed until " + th.Snooze.In(loc).Format("Mon 2 Jan 15:04") + ")"
	}
	if th.Place != "" {
		s += " (moved to " + th.Place + ")"
	}
	return s
}

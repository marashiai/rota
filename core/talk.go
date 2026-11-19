package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// A talk is the human and an agent at work talking in the team chat, as two engineers do: the
// agent gives the human its whole attention and does nothing else until the talk is over. The
// human's first words reach its shift after its current step, as a note from the chat that tells
// it so, and the talk begins; rota wait gives way to them. The agent stops its work, answers with
// rota say and listens with rota listen, which brings what the human wrote since; nothing else
// reaches the shift meanwhile: a teammate's note, or the human's in the mail, waits for the talk's
// end. The talk ends when the agent says its last words in it (rota say --end), once the human
// said they are done; when neither said anything for talk_minutes, as an engineer goes back to
// work when the other went quiet; or with the shift. Then the agent goes back to its work, and
// what waited reaches it.
//
// .talk/<agent>.json is the running shift's talk, under the agent's inbox lock: whether one goes
// on, what the human wrote in it that rota listen has not brought yet, and what it brought, which
// the journal quotes. The organizer's steering routes the human's words to it (notes.go).

// chatTalk is a shift's talk with the human: the one going on, and what it heard in all of them.
type chatTalk struct {
	Open    bool      `json:"open,omitempty"`
	Since   time.Time `json:"since,omitzero"`
	Last    time.Time `json:"last,omitzero"`     // the last words in it, the human's or the agent's: it ends talk_minutes after
	Opening []string  `json:"opening,omitempty"` // the human's first words, sent to the shift and not yet taken: it waits for them
	For     []Note    `json:"for,omitempty"`     // what the human wrote since, for rota listen
	Heard   []Note    `json:"heard,omitempty"`   // what rota listen brought in this shift's talks: read
	Ended   string    `json:"ended,omitempty"`   // why the last one ended: end (the agent ended it) or quiet
}

// talkBegins follows the human's first words in a talk, as the shift gets them: what to do now,
// though the brief, far back in a long shift, says it too.
const talkBegins = "The human is talking to you in the team chat. Stop your work now and give them your whole attention: answer with rota say <answer>, then run rota listen for their next words, and keep talking until they are done. Then end the talk with rota say --end <what you go back to>, and go back to your work where you left it."

func (c *Config) talkFile(agent string) string {
	return c.Path(filepath.Join(talkDir, agent+".json"))
}

// loadTalk is the agent's talk; none, an empty one. It is replaced whole, so it is read without
// a lock; it is changed under the agent's inbox lock.
func (c *Config) loadTalk(agent string) chatTalk {
	var tk chatTalk
	if b, err := os.ReadFile(c.talkFile(agent)); err == nil {
		json.Unmarshal(b, &tk)
	}
	return tk
}

// saveTalk replaces the agent's talk whole; its inbox lock is held.
func (c *Config) saveTalk(agent string, tk chatTalk) error {
	path := c.talkFile(agent)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(tk, "", "  ")
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// changeTalk changes the agent's talk under its inbox lock.
func (c *Config) changeTalk(agent string, f func(tk *chatTalk)) error {
	unlock, err := c.lockInbox(agent)
	if err != nil {
		return err
	}
	defer unlock()
	tk := c.loadTalk(agent)
	f(&tk)
	return c.saveTalk(agent, tk)
}

// Talking says whether the human and the agent talk now, in its running shift.
func (c *Config) Talking(agent string) bool {
	return c.Steerable(agent) && c.loadTalk(agent).Open
}

// talkWaits says whether the human's first words in a talk wait for the agent's current step to
// end: rota wait gives way to them.
func (c *Config) talkWaits(agent string) bool {
	tk := c.loadTalk(agent)
	return tk.Open && len(tk.Opening) > 0 && c.Steerable(agent)
}

// talkQuiet is how long a talk waits for the next words before it ends.
func (c *Config) talkQuiet() time.Duration {
	return time.Duration(c.TalkMinutes * float64(time.Minute))
}

// quiet says whether neither said anything in the talk for talk_minutes. A talk whose first words
// the shift has not taken yet waits for them, however long its current step takes.
func (tk chatTalk) quiet(now time.Time, after time.Duration) bool {
	return tk.Open && len(tk.Opening) == 0 && now.Sub(tk.Last) >= after
}

// end ends the talk: what the human wrote that rota listen did not bring stays in the inbox, and
// reaches the shift as a note, which begins another.
func (tk *chatTalk) end(why string) {
	tk.Open, tk.Opening, tk.For, tk.Ended = false, nil, nil, why
}

// heard is the agent at its part of the talk, saying something or listening: it has the human's
// first words, whether or not its echo of them was seen.
func (tk *chatTalk) heard(now time.Time) {
	if len(tk.Opening) > 0 {
		tk.Opening, tk.Last = nil, now
	}
}

// sayQuiet is the chat saying the talk ended because neither said anything: rota's line for the
// agent, which answers for it what it did not answer, as the Team's failure to answer does.
func (c *Config) sayQuiet(agent string) {
	c.sayFor(agent, fmt.Sprintf("%s went back to its work: neither of you said anything for %s. Write @%s to talk again.", agent, minutesText(c.TalkMinutes), agent))
}

// sayFor is rota's line in the chat about an agent: it answers for the agent what the human wrote
// it that it did not answer, so the chat no longer shows it answering.
func (c *Config) sayFor(agent, text string) {
	line := ChatMsg{Text: text, To: agent}
	if asked := chatPending(c.Chat(400), agent); len(asked) > 0 {
		line.Answer = asked[len(asked)-1].ID
	}
	c.chatAppend(line)
}

func minutesText(m float64) string {
	if m == 1 {
		return "a minute"
	}
	return fmt.Sprintf("%g minutes", m)
}

// Heard is what rota listen brings: the human's words, or why the talk is over.
type Heard struct {
	Notes []Note // the human's words since the agent last listened
	Quiet bool   // the talk ended: neither said anything for talk_minutes
	Ended bool   // the talk had ended already, or there was none
	More  bool   // nothing yet: listening longer can still bring something
	Park  bool   // the shift must put its work away now
}

// Listen waits for the human's next words in the agent's talk, at most max and not past park,
// when the shift must put its work away. It brings every word the human wrote since the agent last
// listened: they are read, leave the inbox, and the journal quotes them.
func (t *Team) Listen(agent string, max time.Duration, park time.Time) (Heard, error) {
	if t.Agent(agent) == nil {
		return Heard{}, fmt.Errorf("%s is not an agent", agent)
	}
	for start := time.Now(); ; time.Sleep(talkLook) {
		var h Heard
		unlock, err := t.lockInbox(agent)
		if err != nil {
			return Heard{}, err
		}
		tk, now := t.loadTalk(agent), time.Now()
		heard := len(tk.Opening) > 0
		tk.heard(now)
		switch {
		case !tk.Open:
			h.Ended = true
		case len(tk.For) > 0:
			h.Notes, tk.Heard, tk.For, tk.Last = tk.For, append(tk.Heard, tk.For...), nil, now
			err = t.dropHeard(agent, h.Notes)
		case tk.quiet(now, t.talkQuiet()):
			tk.end("quiet")
			h.Quiet = true
		case !park.IsZero() && now.After(park):
			h.Park = true
		case now.Sub(start) >= max:
			h.More = true
		}
		if err == nil && (heard || h.Notes != nil || h.Quiet) {
			err = t.saveTalk(agent, tk)
		}
		unlock()
		if err != nil {
			return Heard{}, err
		}
		if h.Quiet {
			t.sayQuiet(agent)
		}
		if h.Notes != nil || h.Quiet || h.Ended || h.Park || h.More {
			return h, nil
		}
	}
}

// dropHeard removes from the inbox the human's words rota listen brought; its lock is held.
func (c *Config) dropHeard(agent string, heard []Note) error {
	notes, err := c.readNotes(agent)
	if err != nil {
		return err
	}
	gone := ids(heard)
	return c.writeNotes(agent, slices.DeleteFunc(notes, func(n Note) bool { return gone[n.ID] }))
}

// talkLook is how often rota listen looks for the human's words.
var talkLook = 500 * time.Millisecond

// HeardText is what rota listen prints: the human's words, a line each.
func (c *Config) HeardText(h Heard) string {
	var b strings.Builder
	for _, n := range h.Notes {
		b.WriteString("the human: " + strings.ReplaceAll(n.Text, "\n", "\n  ") + "\n")
	}
	return b.String()
}

// sayInTalk is the agent's words in its talk, and last, its last words, which end it. Outside a
// talk, words that answer the human's in the chat of the last talk_minutes begin one: those reached
// the shift as it began, in its brief, and the human may be there still.
func (c *Config) sayInTalk(agent string, last bool) error {
	unlock, err := c.lockInbox(agent)
	if err != nil {
		return err
	}
	defer unlock()
	tk, now := c.loadTalk(agent), time.Now()
	switch {
	case tk.Open && last:
		tk.end("end")
	case tk.Open:
		tk.heard(now)
		tk.Last = now
	case !last && c.Steerable(agent) && c.wroteTo(agent, now.Add(-c.talkQuiet())):
		tk = chatTalk{Open: true, Since: now, Last: now, Heard: tk.Heard}
	default:
		return nil
	}
	return c.saveTalk(agent, tk)
}

// wroteTo says whether the human wrote to the agent in the chat since then.
func (c *Config) wroteTo(agent string, since time.Time) bool {
	msgs, _ := c.chatRead(1<<20, 100)
	for i := len(msgs) - 1; i >= 0 && msgs[i].At.After(since); i-- {
		if msgs[i].From == "you" && msgs[i].To == agent {
			return true
		}
	}
	return false
}

// endTalk is the shift's end: the talk with it, if it goes on, and what the human said in its
// talks, read. When they talked, or the human waits for its answer, the chat says that what they
// wrote it waits for its next run.
func (t *Team) endTalk(agent string) []Note {
	var heard []Note
	open := false
	if unlock, err := t.lockInbox(agent); err == nil {
		tk := t.loadTalk(agent)
		heard, open = tk.Heard, tk.Open
		os.Remove(t.talkFile(agent))
		unlock()
	}
	switch asked := chatPending(t.Chat(400), agent); {
	case len(asked) > 0:
		t.sayFor(agent, agent+"'s shift ended before it answered you. It reads what you wrote at its next run.")
	case open:
		t.sayFor(agent, agent+"'s shift ended. What you write it now waits for its next run.")
	}
	return heard
}

// chatNote puts the human's words from the chat in the agent's inbox, and, in a talk, before rota
// listen, which brings them at once.
func (c *Config) chatNote(agent string, n Note) error {
	unlock, err := c.lockInbox(agent)
	if err != nil {
		return err
	}
	defer unlock()
	notes, err := c.readNotes(agent)
	if err != nil {
		return err
	}
	if err := c.writeNotes(agent, append(notes, n)); err != nil {
		return err
	}
	tk := c.loadTalk(agent)
	if !tk.Open {
		return nil
	}
	tk.For, tk.Last = append(tk.For, n), n.At // gone quiet, it goes on all the same: rota listen brings these words, unless it ended meanwhile
	return c.saveTalk(agent, tk)
}

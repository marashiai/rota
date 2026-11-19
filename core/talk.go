package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
// What the human writes an agent that is not at work here waits in its inbox, and its next shift
// answers it in the chat, first thing: its brief has it, with what to do with it (chatWaits); a
// shift on another machine answers it in its final message (chatAway). Words a shift ended before
// it answered go back to the inbox for the next, once (chatAfterShift).
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
	Stop    *talkStop `json:"stop,omitempty"`    // the human stopped the agent: what the organizer's steering tells the shift, at its next look
}

// talkStop is the human stopping an agent at work here that has their words (chatedit.go): the shift
// is told to answer none of them, and its answer, under way in a talk, is cut off first.
type talkStop struct {
	Cut   bool     `json:"cut,omitempty"`   // it answers them now, in a talk: its turn stops at once
	Words []string `json:"words,omitempty"` // the human's words it is not to answer
}

// stopText is what the shift is told when the human stopped it: in a talk, to listen for their next
// words, which may be the ones they stopped it on, edited.
func stopText(st talkStop, talking bool) string {
	var quoted []string
	for _, w := range st.Words {
		quoted = append(quoted, "«"+FirstLine(strings.Join(strings.Fields(w), " "), 200)+"»")
	}
	what := "what they wrote you last"
	if len(quoted) > 0 {
		what += ", " + strings.Join(quoted, " ")
	}
	if !talking {
		return "The human stopped you in the team chat: do not answer " + what + ". Go on with your work."
	}
	return "The human stopped you in the team chat: do not answer " + what + ", and drop what you did for it. They write again, or edit it: run rota listen now for their next words, and go on with the talk."
}

// talkBegins follows the human's first words in a talk, as the shift gets them: what to do now,
// though the brief, far back in a long shift, says it too.
const talkBegins = "The human is talking to you in the team chat. Stop your work now and give them your whole attention: answer with rota say <answer>, then run rota listen for their next words, and keep talking until they are done. Then end the talk with rota say --end <what you go back to>, and go back to your work where you left it."

// chatWaits follows the human's words from the chat in a shift's brief, which came while the
// agent was not at work, as talkBegins follows them as a talk begins: what to do with them, though
// the brief says it too.
const chatWaits = "The human wrote you in the team chat while you were not at work, and waits for your answer there: answer them first, before anything else, with rota say <answer>; for what they ask that takes longer, say there what you will do, do it, and tell them there once it is done."

// chatAway follows the human's words from the chat as a shift on another machine gets them, where
// rota say does not work.
const chatAway = `The human wrote you in the team chat, and reads your answer there. rota say does not work on this machine: answer them with a line "@human say: <answer>" in your final message's last section, which the chat shows as your shift ends.`

// chatSaid is an agent's answer in the team chat in its final message, on a machine where rota say
// does not work: "@human say: <answer>".
var chatSaid = regexp.MustCompile(`(?mi)^\s*(?:[-*]\s*)?@human\s+say:\s*(.+)$`)

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
// the shift as it began, in its brief, and the human may be there still; so do words that answer
// nothing, first: the agent needs the human now (it writes the chat only for that), and waits for
// their answer as in any talk.
func (c *Config) sayInTalk(agent string, last, first bool) error {
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
	case !last && c.Steerable(agent) && (first || c.wroteTo(agent, now.Add(-c.talkQuiet()))):
		tk = chatTalk{Open: true, Since: now, Last: now, Heard: tk.Heard, Stop: tk.Stop}
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

// endTalk is the shift's end: the talk with it ends, if it goes on, which it says; and what the
// human said in its talks, read.
func (t *Team) endTalk(agent string) (heard []Note, talked bool) {
	if unlock, err := t.lockInbox(agent); err == nil {
		tk := t.loadTalk(agent)
		heard, talked = tk.Heard, tk.Open
		os.Remove(t.talkFile(agent))
		unlock()
	}
	return heard, talked
}

// chatAfterShift is the chat once a shift is recorded, its answers in its final message too. What
// the human wrote the agent there that it did not answer goes back to its inbox, unless it is still
// there, for its next shift to answer here, first thing; the human's words ask for that shift, and
// the chat says so, answering nothing: the next shift does. Once: when that shift did not answer
// either, the chat says so, answering for it, and gives them to no other. A talk the shift's end cut
// short, the chat says it ended. A shift that resumes gets them back as it resumes, and the chat says
// nothing.
func (t *Team) chatAfterShift(agent string, talked, resumes bool) {
	msgs := t.Chat(400)
	asked := chatPending(msgs, agent)
	next := agent + "'s shift ended before it answered you: its next shift answers you here, first thing."
	again := len(asked) > 0 && t.saidSince(msgs, agent, next, asked[0].At)
	if len(asked) > 0 && !again {
		err := t.changeNotes(agent, func(notes []Note) []Note {
			for _, m := range asked {
				n := Note{ID: chatNoteID(m.ID), At: m.At, From: "human", Text: chatNoteText(m, agent), Chat: true}
				if slices.ContainsFunc(notes, func(x Note) bool { return x.ID == n.ID || x.Chat && x.Text == n.Text }) {
					continue // it never reached the shift
				}
				at := slices.IndexFunc(notes, func(x Note) bool { return x.At.After(n.At) }) // in its place among those that came after it
				if at < 0 {
					at = len(notes)
				}
				notes = slices.Insert(notes, at, n)
			}
			return notes
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "warn: what the human wrote in the chat does not reach the agent's next shift:", err)
		}
	}
	switch {
	case resumes:
	case again:
		t.sayFor(agent, agent+"'s next shift did not answer you either. Write it again when you still need the answer.")
	case len(asked) > 0:
		t.chatAppend(ChatMsg{Text: next, To: agent})
		t.wakeForHuman(agent)
	case talked:
		t.sayFor(agent, agent+"'s shift ended, and your talk with it: what you write it now, its next shift answers here.")
	}
}

// saidSince says whether rota said line in the agent's chat since then.
func (t *Team) saidSince(msgs []ChatMsg, agent, line string, since time.Time) bool {
	return slices.ContainsFunc(msgs, func(m ChatMsg) bool { return m.From == "" && m.To == agent && m.Text == line && !m.At.Before(since) })
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

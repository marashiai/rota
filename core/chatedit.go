package core

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
)

// The human's words in the chat are theirs until they are answered: they stop whoever answers them,
// the Team or an agent, and edit them, as in any chat with an assistant.
//
// A stop answers for what was not answered yet: rota's line says the human stopped it, and the chat
// no longer shows it answering. Their words that have not reached the agent never do: they leave its
// inbox. An agent at work here that has them is told, at the steering's next look, not to answer
// them; one that answers them in a talk is cut off at once, its turn stopped as Esc stops Claude
// Code, and listens for the human's next words. The Team's turn stops, and what it was saying is
// dropped.
//
// Edited, the human's words leave the chat, and their new words come at its end, marked edited, as
// what they wrote now: what is answered next. The agent gets them in place of the old ones, where it
// never had those; where it has them, as words that replace them, after it stops when it answers
// them now. The Team answers the new words; a turn on the old ones stops. Words that were answered
// are not edited: the talk went on from them.
//
// Deleted, the human's words leave the chat as well, with nothing in their place, on the same terms:
// until they are answered. The Team never reads them; a turn on them stops, and the Team answers what
// else waits on it. An agent never gets them where they have not reached its shift; where they have,
// it is told they are taken back, and its shift here stops when it answers them now.

// teamStopFile says when the human last stopped the Team: a turn that began before then stops.
const teamStopFile = ".chat-stop"

// teamStopLook is how often a turn of the Team looks whether the human stopped it.
var teamStopLook = 250 * time.Millisecond

// ChatStop stops who answers the human in the chat now: the Team ("" or "team"), or an agent.
func (t *Team) ChatStop(who string) (string, error) {
	if who == "" || strings.EqualFold(who, "team") {
		asked := t.teamPending()
		if len(asked) == 0 {
			return "", fmt.Errorf("the Team is not answering you")
		}
		t.stopTeam()
		said := "You stopped the Team."
		_, err := t.chatAppend(ChatMsg{Text: said, Answer: asked[len(asked)-1].ID})
		return said, err
	}
	a := t.Agent(who)
	if a == nil {
		return "", fmt.Errorf("no agent is called %s", who)
	}
	asked := chatPending(t.Chat(400), a.Name)
	if len(asked) == 0 {
		return "", fmt.Errorf("%s is not answering you", a.Name)
	}
	unread, err := t.stopAgent(a, asked)
	if err != nil {
		return "", err
	}
	said := "You stopped " + a.Name + "."
	_, err = t.chatAppend(ChatMsg{Text: said, To: a.Name, Answer: asked[len(asked)-1].ID, Unread: unread})
	return said, err
}

// ChatEdit puts the human's new words in place of their words id, which whoever they wrote has not
// answered: those leave the chat, and the new ones come at its end, to the same Team or agent.
func (t *Team) ChatEdit(id, text string, wakeTeam func()) (ChatMsg, error) {
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return ChatMsg{}, fmt.Errorf("the message is empty")
	case len(text) > chatMax:
		return ChatMsg{}, fmt.Errorf("a message holds at most %d characters", chatMax)
	}
	msgs := t.Chat(400)
	i := slices.IndexFunc(msgs, func(m ChatMsg) bool { return m.ID == id && m.From == "you" })
	if i < 0 {
		return ChatMsg{}, fmt.Errorf("that message is no longer in the chat")
	}
	old, who := msgs[i], msgs[i].With()
	if slices.ContainsFunc(msgs[i+1:], func(m ChatMsg) bool { return m.From == who && m.Re == "" }) {
		return ChatMsg{}, fmt.Errorf("%s answered it already", whoText(who))
	}
	if text == old.Text {
		return old, nil
	}
	answering := slices.ContainsFunc(chatPending(msgs, who), func(m ChatMsg) bool { return m.ID == old.ID })             // it may answer it now
	had := !slices.ContainsFunc(msgs, func(m ChatMsg) bool { return m.From == "" && slices.Contains(m.Unread, old.ID) }) // a stop took it back before it reached the agent
	m := ChatMsg{From: "you", To: old.To, Text: text, About: old.About, Edits: old.ID}
	if who == "team" {
		if answering {
			t.stopTeam()
		}
		ms := []ChatMsg{m}
		if t.ChatModel == "off" {
			ms[0].ID = "c" + time.Now().Format("20060102-150405") + "-" + randHex(3)
			ms = append(ms, ChatMsg{Text: "The Team is off: chat_model in Settings turns it on. To talk with an agent, open its chat, or write @ and its name.", Answer: ms[0].ID})
		}
		if _, err := t.chatAppend(ms...); err != nil {
			return ChatMsg{}, err
		}
		if t.ChatModel != "off" && wakeTeam != nil {
			wakeTeam()
		}
		return ms[0], nil
	}
	a := t.Agent(old.To)
	if a == nil {
		return ChatMsg{}, fmt.Errorf("no agent is called %s", old.To)
	}
	note := time.Now().Format("20060102-150405") + "-" + randHex(3)
	m.ID = chatID(note)
	ms, err := t.chatAppend(m)
	if err != nil {
		return ChatMsg{}, err
	}
	m = ms[0]
	if err := t.editNote(a, old, m, note, answering, had); err != nil {
		return ChatMsg{}, err
	}
	if !t.Steerable(a.Name) { // checked after the note is in, as for a note: a shift that ends meanwhile asks for the next itself
		t.wakeForHuman(a.Name)
	}
	return m, nil
}

// ChatDelete takes back the human's words id, which whoever they wrote has not answered: they leave
// the chat, and the Team or the agent does not answer them.
func (t *Team) ChatDelete(id string, wakeTeam func()) error {
	msgs := t.Chat(400)
	i := slices.IndexFunc(msgs, func(m ChatMsg) bool { return m.ID == id && m.From == "you" })
	if i < 0 {
		return fmt.Errorf("that message is no longer in the chat")
	}
	old, who := msgs[i], msgs[i].With()
	if slices.ContainsFunc(msgs[i+1:], func(m ChatMsg) bool { return m.From == who && m.Re == "" }) {
		return fmt.Errorf("%s answered it already", whoText(who))
	}
	var a *Agent
	if who != "team" {
		if a = t.Agent(old.To); a == nil {
			return fmt.Errorf("no agent is called %s", old.To)
		}
	}
	answering := slices.ContainsFunc(chatPending(msgs, who), func(m ChatMsg) bool { return m.ID == old.ID })             // it may answer it now
	had := !slices.ContainsFunc(msgs, func(m ChatMsg) bool { return m.From == "" && slices.Contains(m.Unread, old.ID) }) // a stop took it back before it reached the agent
	if a != nil {                                                                                                        // first: should this fail, the words are in the chat still, to delete again
		if err := t.takeBackNote(a, old, answering, had); err != nil {
			return err
		}
	}
	if _, err := t.chatAppend(ChatMsg{Deletes: old.ID}); err != nil {
		return err
	}
	if answering && a == nil {
		t.stopTeam()
		if len(t.teamPending()) > 0 && wakeTeam != nil { // what else the human asked it, which it answers now
			wakeTeam()
		}
	}
	return nil
}

// takeBackNote takes the human's words old back from the agent, under its inbox's lock: where they
// have not reached its shift, they leave its inbox, so that it never has them; where they have, a
// note says they are taken back, which rota listen brings in a talk, once its shift here stops on
// them if it answers them now.
func (t *Team) takeBackNote(a *Agent, old ChatMsg, answering, had bool) error {
	unlock, err := t.lockInbox(a.Name)
	if err != nil {
		return err
	}
	defer unlock()
	notes, err := t.readNotes(a.Name)
	if err != nil {
		return err
	}
	tk, began := t.loadTalk(a.Name), t.shiftBegan(a.Name)
	was := chatNoteID(old.ID)
	if i := slices.IndexFunc(notes, func(x Note) bool { return x.ID == was }); i >= 0 && unread(a, notes[i], tk, began) {
		if err := t.writeNotes(a.Name, slices.Delete(notes, i, i+1)); err != nil {
			return err
		}
		if n := len(tk.For); n > 0 {
			if tk.For = slices.DeleteFunc(tk.For, func(x Note) bool { return x.ID == was }); len(tk.For) != n {
				return t.saveTalk(a.Name, tk)
			}
		}
		return nil
	}
	if !had { // a stop took them back before they reached it
		return nil
	}
	at := time.Now()
	n := Note{ID: at.Format("20060102-150405") + "-" + randHex(3), At: at, From: "human", // a note, not words to answer in the chat
		Text: "(taken back: they deleted their words «" + FirstLine(strings.Join(strings.Fields(chatWords(old.Text, a.Name)), " "), 200) + "» from the team chat; do not answer them or act on them)"}
	if err := t.writeNotes(a.Name, append(notes, n)); err != nil {
		return err
	}
	if began.IsZero() {
		return nil
	}
	if answering && a.Host == "" {
		tk.Stop = stopOn(tk, a.Name, []ChatMsg{old})
	}
	if tk.Open { // rota listen brings it, as the human's next words in the talk
		tk.For, tk.Last = append(tk.For, n), n.At
	}
	if tk.Stop == nil && !tk.Open {
		return nil
	}
	return t.saveTalk(a.Name, tk)
}

// whoText is the Team or an agent, as a sentence names them.
func whoText(who string) string {
	if who == "team" {
		return "The Team"
	}
	return who
}

// editedMark begins the human's words they edited, as the Team reads them.
func editedMark(m ChatMsg) string {
	if m.Edits == "" {
		return ""
	}
	return "(edited) "
}

// stopTeam stops the Team's turn, here or in whichever process runs it: the chat no longer shows it
// answering.
func (t *Team) stopTeam() {
	writeWhole(t.Path(teamStopFile), []byte(time.Now().Format(time.RFC3339Nano)+"\n"))
	os.Remove(t.Path(".chat-busy"))
}

// teamStopped says whether the human stopped the Team since its turn began.
func (c *Config) teamStopped(began time.Time) bool {
	b, err := os.ReadFile(c.Path(teamStopFile))
	if err != nil {
		return false
	}
	at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(b)))
	return err == nil && at.After(began)
}

// shiftBegan is when the agent's running shift began, as its steer file says; zero with none.
func (c *Config) shiftBegan(agent string) time.Time {
	if !c.Steerable(agent) {
		return time.Time{}
	}
	fi, err := os.Stat(c.steerFile(agent))
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// unread says whether the human's words from the chat, a note in the agent's inbox, have not reached
// its shift: none runs, and its next reads them; or its shift here has not been sent them: they came
// after it began, and are not on their way to it as a talk's first words. A shift on another machine
// may have them on their way. Its inbox's lock is held.
func unread(a *Agent, n Note, tk chatTalk, began time.Time) bool {
	switch {
	case began.IsZero():
		return true
	case a.Host != "" || slices.Contains(tk.Opening, n.ID):
		return false
	}
	return n.At.After(began)
}

// stopAgent stops the agent on the human's words asked, under its inbox's lock: those that have not
// reached its shift leave its inbox, and it says which; for those it has, its shift here is told not
// to answer them, at the steering's next look, and is cut off when it answers them now in a talk.
func (t *Team) stopAgent(a *Agent, asked []ChatMsg) (unreadIDs []string, err error) {
	unlock, err := t.lockInbox(a.Name)
	if err != nil {
		return nil, err
	}
	defer unlock()
	notes, err := t.readNotes(a.Name)
	if err != nil {
		return nil, err
	}
	tk, began := t.loadTalk(a.Name), t.shiftBegan(a.Name)
	kept, talkChanged := len(notes), false
	var reached []ChatMsg
	for _, m := range asked {
		id := chatNoteID(m.ID)
		if i := slices.IndexFunc(notes, func(n Note) bool { return n.ID == id }); i >= 0 && unread(a, notes[i], tk, began) {
			notes, unreadIDs = slices.Delete(notes, i, i+1), append(unreadIDs, m.ID)
			if n := len(tk.For); n > 0 {
				tk.For = slices.DeleteFunc(tk.For, func(x Note) bool { return x.ID == id })
				talkChanged = talkChanged || len(tk.For) != n
			}
			continue
		}
		reached = append(reached, m)
	}
	if len(notes) != kept {
		if err := t.writeNotes(a.Name, notes); err != nil {
			return nil, err
		}
	}
	if len(reached) > 0 && !began.IsZero() && a.Host == "" {
		tk.Stop, talkChanged = stopOn(tk, a.Name, reached), true
	}
	if talkChanged {
		return unreadIDs, t.saveTalk(a.Name, tk)
	}
	return unreadIDs, nil
}

// stopOn is the stop for the human's words its shift has: with any the steering has not told it yet.
// It cuts the shift's turn off when it answers them now in a talk: it took them, as none is on its
// way to it still.
func stopOn(tk chatTalk, agent string, reached []ChatMsg) *talkStop {
	st := talkStop{}
	if tk.Stop != nil {
		st = *tk.Stop
	}
	queued := slices.ContainsFunc(reached, func(m ChatMsg) bool { return slices.Contains(tk.Opening, chatNoteID(m.ID)) })
	st.Cut = st.Cut || tk.Open && !queued
	for _, m := range reached {
		st.Words = append(st.Words, chatWords(m.Text, agent))
	}
	return &st
}

// editNote puts the human's new words m in the agent's inbox in place of their words old, under its
// lock: where old has not reached its shift, in old's place, so that it never has old; else as their
// next words, which say they replace old where it had old, once its shift here stops on old if it
// answers it now. In a talk, rota listen brings them.
func (t *Team) editNote(a *Agent, old, m ChatMsg, id string, answering, had bool) error {
	unlock, err := t.lockInbox(a.Name)
	if err != nil {
		return err
	}
	defer unlock()
	notes, err := t.readNotes(a.Name)
	if err != nil {
		return err
	}
	tk, began := t.loadTalk(a.Name), t.shiftBegan(a.Name)
	n, was := Note{ID: id, At: m.At, From: "human", Text: chatNoteText(m, a.Name), Chat: true}, chatNoteID(old.ID)
	if i := slices.IndexFunc(notes, func(x Note) bool { return x.ID == was }); i >= 0 && unread(a, notes[i], tk, began) {
		notes[i] = n
		talkChanged := false
		for j := range tk.For {
			if tk.For[j].ID == was {
				tk.For[j], talkChanged = n, true
			}
		}
		if err := t.writeNotes(a.Name, notes); err != nil {
			return err
		}
		if talkChanged {
			return t.saveTalk(a.Name, tk)
		}
		return nil
	}
	if had {
		n.Text = "(edited: in place of their words «" + FirstLine(strings.Join(strings.Fields(chatWords(old.Text, a.Name)), " "), 200) + "») " + n.Text
	}
	if err := t.writeNotes(a.Name, append(notes, n)); err != nil {
		return err
	}
	if began.IsZero() {
		return nil
	}
	if answering && a.Host == "" {
		tk.Stop = stopOn(tk, a.Name, []ChatMsg{old})
	}
	if tk.Open { // rota listen brings them, as the human's next words in the talk
		tk.For, tk.Last = append(tk.For, n), n.At
	}
	if tk.Stop == nil && !tk.Open {
		return nil
	}
	return t.saveTalk(a.Name, tk)
}

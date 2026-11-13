package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
)

// What the human writes an agent from the mail is a message, and messages that follow it in the
// same thread are kept with it in sent/<id>.json, with the agent's answers. Each of the human's
// messages reaches the agent as a note marked as a message, which names its id; the agent answers
// it with "rota reply <id> <answer>", or a line "@human re <id>: <answer>" in its final message.
// The thread's id is its first message's; a later message's id is the thread's and a suffix, so
// a message's id finds its thread.

// MessageThread is a thread of the human's messages to one agent and the agent's answers.
type MessageThread struct {
	ID    string    `json:"-"`
	To    string    `json:"to"`
	About string    `json:"about,omitempty"` // owner/name#n
	Said  []Said    `json:"said"`            // in order
	Gone  time.Time `json:"gone,omitzero"`   // when the human took its only message back
}

// Said is one message of a thread: the human's, which reached the agent as the note Note and
// which its shift read at Read; or the agent's answer to the human's message Re.
type Said struct {
	At   time.Time `json:"at"`
	From string    `json:"from"` // "you" or the agent
	Text string    `json:"text"`
	Note string    `json:"note,omitempty"`
	Read time.Time `json:"read,omitzero"`
	Re   string    `json:"re,omitempty"`
}

// messageID is a thread's id, which is its first message's; a later message's id adds a suffix.
var messageID = regexp.MustCompile(`^(\d{8}-\d{6}-[0-9a-f]{6})(-[0-9a-f]{4})?$`)

// SendMessage sends the human's message to an agent: about ref (owner/name#n of one of its
// repositories) or in general, as a new thread, or after the others of thread. It returns the
// thread's id.
func (t *Team) SendMessage(agent, ref, text, thread string) (string, error) {
	a, err := t.agent(agent)
	if err != nil {
		return "", err
	}
	if text = strings.TrimSpace(text); text == "" {
		return "", fmt.Errorf("the message is empty")
	}
	if m := fullRefOnly.FindStringSubmatch(ref); ref != "" && (m == nil || !slices.Contains(a.Repos, m[1])) {
		return "", fmt.Errorf("%s is not a pull request or issue of %s's repositories", ref, agent)
	}
	now := time.Now()
	n := Note{At: now, From: "human", About: ref, Text: text, Message: true}
	if thread == "" {
		thread = now.Format("20060102-150405") + "-" + randHex(3)
		n.ID = thread
		if err := t.writeThread(thread, MessageThread{To: agent, About: ref, Said: []Said{{At: now, From: "you", Text: text, Note: n.ID}}}); err != nil {
			return "", err
		}
	} else if err := t.changeThread(thread, func(m *MessageThread) error {
		if m.To != agent {
			return fmt.Errorf("that thread is with %s", m.To)
		}
		n.ID, n.About = thread+"-"+randHex(2), m.About
		m.Said, m.Gone = append(m.Said, Said{At: now, From: "you", Text: text, Note: n.ID}), time.Time{}
		return nil
	}); err != nil {
		return "", err
	}
	if err := t.changeNotes(agent, func(notes []Note) []Note { return append(notes, n) }); err != nil {
		t.changeThread(thread, func(m *MessageThread) error { // it never reached the agent
			if m.Said = slices.DeleteFunc(m.Said, func(s Said) bool { return s.Note == n.ID }); len(m.Said) == 0 {
				os.Remove(t.threadPath(thread))
				return fmt.Errorf("gone")
			}
			return nil
		})
		return "", err
	}
	if n.ID != thread { // a thread the human writes in again is not done
		t.MarkMail("m:"+thread, "unarchive", "", now)
	}
	if !t.Steerable(agent) { // its running shift gets it after its current step; else it is woken
		t.wakeForHuman(agent)
	}
	return thread, nil
}

// Unsend takes back a thread's last message while it waits in its agent's inbox: once the agent's
// shift began it is the shift's, in its brief or on its way to it.
func (t *Team) Unsend(thread string) error { return t.unsend(thread, "") }

// unsend takes back a thread's last message, as Unsend; with note, only while that is the last.
func (t *Team) unsend(thread, note string) error {
	return t.changeThread(thread, func(m *MessageThread) error {
		last := m.Said[len(m.Said)-1]
		switch {
		case !m.Gone.IsZero():
			return fmt.Errorf("it is taken back already")
		case last.From != "you" || !last.Read.IsZero():
			return fmt.Errorf("%s has read it already", m.To)
		case note != "" && last.Note != note:
			return fmt.Errorf("you wrote %s again since; take that back in the mail", m.To)
		}
		if err := t.unsendNote(m.To, last.Note); err != nil {
			return err
		}
		if len(m.Said) == 1 {
			m.Gone = time.Now() // the thread is gone; its message is kept
		} else {
			m.Said = m.Said[:len(m.Said)-1]
		}
		return nil
	})
}

// unsendNote takes the human's message note out of an agent's inbox, unless the agent's shift
// runs. A shift writes its steer file under the inbox's lock before it reads the inbox, so the
// note is either gone before its brief or refused here.
func (t *Team) unsendNote(agent, id string) error {
	unlock, err := t.lockInbox(agent)
	if err != nil {
		return err
	}
	defer unlock()
	if t.Steerable(agent) {
		return fmt.Errorf("%s is working, and reads it after its current step", agent)
	}
	notes, err := t.readNotes(agent)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(notes, func(n Note) bool { return n.ID == id })
	if i < 0 {
		return fmt.Errorf("%s has read it already", agent)
	}
	return t.writeNotes(agent, slices.Delete(notes, i, i+1))
}

// ReplyTo keeps an agent's answer to the human's message note: in its thread, after it.
func (c *Config) ReplyTo(agent, note, text string) error {
	if text = strings.TrimSpace(text); text == "" {
		return fmt.Errorf("the answer is empty")
	}
	thread := threadOfNote(note)
	if thread == "" {
		return fmt.Errorf("no message %q from the human", note)
	}
	return c.changeThread(thread, func(m *MessageThread) error {
		i := slices.IndexFunc(m.Said, func(s Said) bool { return s.From == "you" && s.Note == note })
		if i < 0 || !m.Gone.IsZero() {
			return fmt.Errorf("no message %s from the human", note)
		}
		if m.To != agent {
			return fmt.Errorf("message %s is not to you", note)
		}
		if slices.ContainsFunc(m.Said, func(s Said) bool { return s.From == agent && s.Re == note && s.Text == text }) {
			return nil // said once
		}
		if m.Said[i].Read.IsZero() {
			m.Said[i].Read = time.Now() // it answers what it read
		}
		m.Said = append(m.Said, Said{At: time.Now(), From: agent, Text: text, Re: note})
		return nil
	})
}

// threadOfNote is the thread of the human's message note, from its id; "" for an id that is not one.
func threadOfNote(note string) string {
	if m := messageID.FindStringSubmatch(note); m != nil {
		return m[1]
	}
	return ""
}

// messagesRead marks the human's messages among an agent's notes read at at: its shift took them.
func (c *Config) messagesRead(notes []Note, at time.Time) {
	for _, n := range notes {
		if !n.Message {
			continue
		}
		c.changeThread(threadOfNote(n.ID), func(m *MessageThread) error {
			for i := range m.Said {
				if m.Said[i].Note == n.ID && m.Said[i].Read.IsZero() {
					m.Said[i].Read = at
				}
			}
			return nil
		})
	}
}

// MessageThreads are the threads of the human's messages to agents.
func (c *Config) MessageThreads() []MessageThread {
	files, _ := filepath.Glob(filepath.Join(c.Path(sentDir), "*.json"))
	var out []MessageThread
	for _, f := range files {
		var m MessageThread
		if b, err := os.ReadFile(f); err == nil && json.Unmarshal(b, &m) == nil && len(m.Said) > 0 {
			m.ID = strings.TrimSuffix(filepath.Base(f), ".json")
			out = append(out, m)
		}
	}
	return out
}

func (c *Config) threadPath(id string) string { return filepath.Join(c.Path(sentDir), id+".json") }

func (c *Config) writeThread(id string, m MessageThread) error {
	if err := os.MkdirAll(c.Path(sentDir), 0o755); err != nil {
		return err
	}
	b, _ := json.Marshal(m)
	tmp := filepath.Join(c.Path(sentDir), "."+id+".tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.threadPath(id))
}

// changeThread changes one thread under the lock of all of them; a thread that is not one is an
// error, and a change that fails writes nothing.
func (c *Config) changeThread(id string, change func(*MessageThread) error) error {
	if !messageID.MatchString(id) || threadOfNote(id) != id {
		return fmt.Errorf("no thread %q", id)
	}
	if err := os.MkdirAll(c.Path(sentDir), 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(c.Path(sentDir), ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	var m MessageThread
	b, err := os.ReadFile(c.threadPath(id))
	if err != nil || json.Unmarshal(b, &m) != nil || len(m.Said) == 0 {
		return fmt.Errorf("no thread %s", id)
	}
	if err := change(&m); err != nil {
		return err
	}
	return c.writeThread(id, m)
}

package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The chat is what is synchronous, as at work: what an agent or the Team says the human there,
// their devices hear of at once, as from a chat app, unless they see it first. The pages say which
// chat the human has in front of them, and up to which message (ChatSeen); what is said and not seen
// within chatNotifyAfter, the phones hear of (TellChat), and the Mac from the menu bar. The mail is
// what is asynchronous, which they read when they choose, and hear of as they set it (Notifies);
// the chat has a setting of its own (NotifiesChat).

const (
	chatSeenFile = ".chat-seen.json" // by chat, when the newest message the human saw in it was said
	chatToldFile = ".chat-told"      // when the newest message the phones heard of was said
)

// chatNotifyAfter is how long what an agent or the Team says in the chat waits before the human's
// devices hear of it: what they see meanwhile, on any device, they do not.
var chatNotifyAfter = 10 * time.Second

// ChatSeen keeps that the human saw the chat with ("team", or an agent's name) up to its message
// said at at, as a page showed it to them; an earlier one changes nothing.
func (t *Team) ChatSeen(with string, at time.Time) error {
	if with != "team" && t.Agent(with) == nil {
		return fmt.Errorf("no chat with %s", with)
	}
	return t.locked(chatSeenFile, func() error {
		c := t.Config
		seen := c.chatSeen()
		if !at.After(seen[with]) {
			return nil
		}
		seen[with] = at
		b, _ := json.Marshal(seen)
		return writeWhole(c.Path(chatSeenFile), b)
	})
}

// ChatSeenAt is when the newest message the human saw in the chat with was said.
func (c *Config) ChatSeenAt(with string) time.Time { return c.chatSeen()[with] }

func (c *Config) chatSeen() map[string]time.Time {
	seen := map[string]time.Time{}
	if b, err := os.ReadFile(c.Path(chatSeenFile)); err == nil {
		json.Unmarshal(b, &seen)
	}
	return seen
}

// unseen says whether the human has not seen m yet. A page says when it was said to the
// millisecond, as a browser keeps time.
func (c *Config) unseen(m ChatMsg, seen map[string]time.Time) bool {
	return m.At.Truncate(time.Millisecond).After(seen[m.With()])
}

// chatSays says whether m is something an agent or the Team said the human in the chat: not their
// own words, nor rota's lines, nor how a change stands.
func chatSays(m ChatMsg) bool { return m.Re == "" && m.From != "" && m.From != "you" }

// ChatQuiet says whether nothing was said in the chat for d: then there is nothing new to tell.
func (c *Config) ChatQuiet(d time.Duration) bool {
	fi, err := os.Stat(c.Path(chatFile))
	return err != nil || time.Since(fi.ModTime()) > d
}

// ChatToTell is what agents and the Team said the human in the chat after after, at least
// chatNotifyAfter and less than an hour before now, that they have not seen: what their devices
// hear of, unless they turned that off. What is older is no news.
func (c *Config) ChatToTell(after, now time.Time) []ChatMsg {
	if !c.NotifiesChat() {
		return nil
	}
	seen := c.chatSeen()
	var tell []ChatMsg
	for _, m := range c.Chat(100) {
		if age := now.Sub(m.At); chatSays(m) && m.At.After(after) && age >= chatNotifyAfter && age < time.Hour && c.unseen(m, seen) {
			tell = append(tell, m)
		}
	}
	return tell
}

// TellChat tells the human's phones of what agents and the Team said them in the chat, which they
// have not seen, once: whichever rota serves the pages looks, and the first to see something tells
// it. It says how many devices it reached. What was said before the first look is never told.
func (c *Config) TellChat(ctx context.Context, now time.Time) (int, error) {
	var tell []ChatMsg
	err := c.locked(chatToldFile, func() error {
		b, err := os.ReadFile(c.Path(chatToldFile))
		told, perr := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(b)))
		if err != nil || perr != nil { // the first look: from now on
			return writeWhole(c.Path(chatToldFile), []byte(now.Format(time.RFC3339Nano)))
		}
		if tell = c.ChatToTell(told, now); len(tell) == 0 {
			return nil
		}
		return writeWhole(c.Path(chatToldFile), []byte(tell[len(tell)-1].At.Format(time.RFC3339Nano)))
	})
	if err != nil || len(tell) == 0 || len(c.PushSubscriptions()) == 0 {
		return 0, err
	}
	return c.PushAll(ctx)
}

// WatchChat tells the phones of what is said in the chat, as TellChat does, until ctx ends: the
// pages do it while they are served.
func (c *Config) WatchChat(ctx context.Context) {
	c.TellChat(ctx, time.Now()) // what was said while no rota served the pages, if it is news; on a new team folder, nothing
	said := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(chatTellEvery):
		}
		if c.ChatQuiet(chatNotifyAfter + 2*chatTellEvery) { // said nothing lately: nothing to tell
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if _, err := c.TellChat(pctx, time.Now()); err != nil && err.Error() != said {
			said = err.Error() // once: it is tried again
			fmt.Fprintln(os.Stderr, "warn: telling the phones of the chat:", err)
		}
		cancel()
	}
}

// chatTellEvery is how often WatchChat looks.
var chatTellEvery = 2 * time.Second

// Notice is what a device shows when it is told something: the newest of what the human chose to
// hear of.
type Notice struct {
	Title string    `json:"title"`
	Body  string    `json:"body"`
	Tag   string    `json:"tag"` // a newer notice with the same tag takes the place of the last
	URL   string    `json:"url"` // what a tap opens
	At    time.Time `json:"at"`
}

// Notice is the newest of what the human's devices are told of: what an agent or the Team said them
// in the chat in the last hour that they have not seen, a question in their mail, or an agent's
// answer to their notes on a pull request, as they chose to hear of each. With nothing, it says the
// notifications work: a test's.
func (t *Team) Notice(now time.Time) Notice {
	n := Notice{Title: "rota", Body: "Notifications work on this device; nothing waits for you now.", Tag: "rota", URL: "/"}
	if t.NotifiesChat() {
		seen := t.chatSeen()
		for _, m := range t.Chat(100) {
			if chatSays(m) && now.Sub(m.At) < time.Hour && t.unseen(m, seen) && m.At.After(n.At) {
				who := m.From
				if who == "team" {
					who = "Team"
				}
				n = Notice{Title: who, Body: ChatNoticeText(m), Tag: "rota-chat-" + m.With(), URL: "/#chat=" + m.With(), At: m.At}
			}
		}
	}
	var asked []Question
	for _, q := range t.Questions() {
		if t.Notifies(q.Importance) {
			asked = append(asked, q)
		}
	}
	if len(asked) > 0 && asked[len(asked)-1].Asked.After(n.At) {
		q := asked[len(asked)-1]
		n = Notice{Title: q.Agent + " asks you", Body: or(q.Subject, q.Text), Tag: "rota-questions", URL: "/#questions", At: q.Asked}
		if len(asked) > 1 {
			n.Title, n.Body = strconv.Itoa(len(asked))+" questions for you", q.Agent+": "+or(q.Subject, q.Text)
		}
	}
	if t.Notifies("") {
		for _, a := range t.Answers() {
			if a.At.After(n.At) {
				n = Notice{Title: a.By + " answered you on " + a.Name, Body: a.Text, Tag: "rota-answers", URL: a.Page(), At: a.At}
			}
		}
	}
	return n
}

// ChatNoticeText is what a notice says of m: its words, or the change the Team proposes.
func ChatNoticeText(m ChatMsg) string {
	text := strings.TrimSpace(m.Text)
	if text == "" && m.Change != nil {
		text = "Proposes: " + or(m.Change.Title, changeText(*m.Change))
	}
	return FirstLine(strings.Join(strings.Fields(text), " "), 300)
}

// locked runs f while it holds the lock of the team folder's file name: one change of it at a
// time, whichever rota makes it.
func (c *Config) locked(name string, f func() error) error {
	lock, err := os.OpenFile(c.Path(name+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	return f()
}

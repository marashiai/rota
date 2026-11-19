package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// What an agent or the Team says the human in the chat, their phones hear of at once, as from a
// chat app: unless they see it first, on any device, within a moment, or turned the chat's
// notifications off. Never twice, nor what was said before the pages first looked, nor rota's
// lines or the human's own words. The mail has a setting of its own: what the phone shows follows
// both.
func TestTheChatTellsTheHumanAtOnce(t *testing.T) {
	_, team := chatTeam(t)
	defer func(d time.Duration) { chatNotifyAfter = d }(chatNotifyAfter)
	chatNotifyAfter = 40 * time.Millisecond
	var mu sync.Mutex
	pushes := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		pushes++
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)
	defer func(c *http.Client, s []string) { pushClient, pushServices = c, s }(pushClient, pushServices)
	pushClient, pushServices = srv.Client(), append(pushServices, "127.0.0.1") // the test server plays a push service
	team.Subscribe(srv.URL+"/phone", "iPhone")
	ctx := context.Background()
	told := func() int {
		t.Helper()
		mu.Lock()
		before := pushes
		mu.Unlock()
		n, err := team.TellChat(ctx, time.Now())
		mu.Lock()
		defer mu.Unlock()
		if err != nil || n != pushes-before {
			t.Fatalf("told %d devices, pushed %d: %v", n, pushes-before, err)
		}
		return n
	}
	say := func(m ChatMsg) ChatMsg {
		t.Helper()
		ms, err := team.chatAppend(m)
		if err != nil {
			t.Fatal(err)
		}
		return ms[0]
	}

	say(ChatMsg{From: "qa", Text: "said before the pages looked"})
	time.Sleep(50 * time.Millisecond)
	if told() != 0 {
		t.Fatal("what was said before the first look is not told")
	}
	say(ChatMsg{From: "you", To: "qa", Text: "how is it going?"})
	up := say(ChatMsg{From: "qa", Text: "The stack is up.\n\nThe tests run now."})
	if told() != 0 {
		t.Fatal("it waits a moment: the human may see it")
	}
	time.Sleep(50 * time.Millisecond)
	if told() != 1 || told() != 0 {
		t.Fatal("then the phone is told, once")
	}
	if n := team.Notice(time.Now()); n.Title != "qa" || n.Body != "The stack is up. The tests run now." || n.Tag != "rota-chat-qa" || n.URL != "/#chat=qa" || !n.At.Equal(up.At) {
		t.Fatalf("the phone shows what qa said, and a tap opens its chat: %+v", n)
	}
	if later := time.Now().Add(2 * time.Hour); len(team.ChatToTell(time.Time{}, later)) != 0 || team.Notice(later).Title != "rota" {
		t.Fatal("what was said over an hour ago is no news")
	}

	seen := say(ChatMsg{From: "team", Text: "All idle."})
	if err := team.ChatSeen("team", seen.At.Truncate(time.Millisecond)); err != nil { // as a page keeps time
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if told() != 0 {
		t.Fatal("what the human saw on a page is not told")
	}
	if err := team.ChatSeen("team", seen.At.Add(-time.Hour)); err != nil || !team.ChatSeenAt("team").Equal(seen.At.Truncate(time.Millisecond)) {
		t.Fatal("an earlier mark changes nothing")
	}
	say(ChatMsg{Text: "qa went back to its work.", To: "qa"})
	say(ChatMsg{From: "you", To: "qa", Text: "thanks"})
	say(ChatMsg{Re: up.ID, State: "applied"})
	time.Sleep(50 * time.Millisecond)
	if told() != 0 {
		t.Fatal("rota's lines and the human's own words are not told")
	}
	if err := team.ChatSeen("nobody", time.Now()); err == nil {
		t.Fatal("no chat with an agent that is none")
	}

	if _, err := team.Ask("qa", "May I restart the stack?"); err != nil {
		t.Fatal(err)
	}
	if n := team.Notice(time.Now()); n.Title != "qa asks you" || n.URL != "/#questions" {
		t.Fatalf("the newest of what the human hears of: %+v", n)
	}
	p := team.mailPrefs()
	p.Notify = "off"
	if err := team.SetMailPrefs(p); err != nil {
		t.Fatal(err)
	}
	if n := team.Notice(time.Now()); n.Title != "qa" || !strings.Contains(n.Body, "The stack is up.") {
		t.Fatalf("with the mail's notifications off, the chat's still come: %+v", n)
	}
	p.Chat = "off"
	if err := team.SetMailPrefs(p); err != nil {
		t.Fatal(err)
	}
	say(ChatMsg{From: "qa", Text: "One more thing."})
	time.Sleep(50 * time.Millisecond)
	if told() != 0 || team.Notice(time.Now()).Title != "rota" {
		t.Fatal("with the chat's off too, nothing is told")
	}
}

// The chat's notifications have a setting of their own beside the mail's: all by default, also
// for what was set before there was one; a page from then keeps it as it is; the Team sets it as
// the human's inbox, with what it was.
func TestTheChatHasANotificationSettingOfItsOwn(t *testing.T) {
	_, team := chatTeam(t)
	if p := team.mailPrefs(); p.Chat != "all" || p.Notify != "focused" || !team.NotifiesChat() {
		t.Fatalf("by default: %+v", p)
	}
	if err := team.changeMail(time.Now(), func(s *mailState) { s.Prefs = &MailPrefs{Split: true, Focus: 5, After: "next", Notify: "all"} }); err != nil {
		t.Fatal(err)
	}
	if p := team.mailPrefs(); p.Chat != "all" || p.Notify != "all" {
		t.Fatalf("set before the chat had a setting: %+v", p)
	}
	if err := team.SetMailPrefs(MailPrefs{Split: true, Focus: 5, After: "next", Notify: "off", Chat: "off"}); err != nil || team.NotifiesChat() || team.Notifies("high") {
		t.Fatalf("both off: %v", err)
	}
	if err := team.SetMailPrefs(MailPrefs{Split: true, Focus: 5, After: "next", Notify: "focused"}); err != nil || team.NotifiesChat() {
		t.Fatalf("a page from before keeps the chat's as it is: %v", err)
	}
	if err := team.SetMailPrefs(MailPrefs{Split: true, Focus: 5, After: "next", Notify: "focused", Chat: "loud"}); err == nil {
		t.Fatal("the chat's is all or off")
	}
	ch := ChatChange{Do: "inbox", Set: map[string]string{"chat": "all"}}
	if err := team.checkInbox(&ch); err != nil || ch.Was["chat"] != "off" {
		t.Fatalf("the Team sets it: %v %+v", err, ch)
	}
}

// An agent that needs the human now writes them in the chat, and its first words begin a talk: it
// waits there for their answer, which rota listen brings. Its last words end it.
func TestAnAgentWritesTheHumanWhenItNeedsThemNow(t *testing.T) {
	team, _, _ := talkTeam(t)
	if err := team.ChatSay("qa", "The deploy fails its health check: shall I roll it back?", false); err != nil || !team.Talking("qa") {
		t.Fatalf("its first words begin a talk: %v", err)
	}
	if busy := team.ChatBusy(); len(busy) != 0 {
		t.Fatalf("it waits for the human; nobody answers yet: %v", busy)
	}
	team.ChatSend("@qa yes, roll it back", nil)
	if h, err := team.Listen("qa", time.Second, time.Time{}); err != nil || team.HeardText(h) != "the human: yes, roll it back\n" {
		t.Fatalf("rota listen brings their answer: %+v %v", h, err)
	}
	if err := team.ChatSay("qa", "Rolled back; it is healthy again.", true); err != nil || team.Talking("qa") {
		t.Fatalf("its last words end it: %v", err)
	}
}

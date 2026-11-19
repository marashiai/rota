package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// What the human writes in the chat goes with what they had on their screen, as the app says it:
// one line, of a size. The agent's note has it with their words, also when its next shift gets them
// again; so has the Team's turn, and its view of an agent's chat. The pull request's agent is the one
// whose branch it is.
func TestTheChatKnowsWhatIsOnTheScreen(t *testing.T) {
	root, team := chatTeam(t)
	m, err := team.ChatSendAbout("dev", "what do you think of this?", "Reviewing pull request o/app#1 «Retry the flaky login test»,\n the whole change, at src/login.go:42", nil)
	if err != nil || m.About != "Reviewing pull request o/app#1 «Retry the flaky login test», the whole change, at src/login.go:42" {
		t.Fatalf("one line: %+v %v", m, err)
	}
	if notes := inboxText(team, "dev"); !strings.Contains(notes, "in the team chat: what do you think of this?\n  (on their screen: Reviewing pull request o/app#1 «Retry the flaky login test», the whole change, at src/login.go:42)") {
		t.Fatalf("the agent's note has it: %s", notes)
	}
	if long, _ := team.ChatSendAbout("dev", "and this?", strings.Repeat("x", 500), nil); len(long.About) > aboutMax+len("…") {
		t.Fatalf("of a size: %d", len(long.About))
	}
	os.Remove(team.Inbox("dev"))
	team.chatAfterShift("dev", false, false) // its shift read them, and did not answer
	if notes := inboxText(team, "dev"); !strings.Contains(notes, "(on their screen: Reviewing pull request o/app#1") {
		t.Fatalf("its next shift gets them as they were: %s", notes)
	}
	if text, _ := team.agentChat("dev", 10); !strings.Contains(text, "the human: what do you think of this?\n  (on their screen: Reviewing pull request o/app#1") {
		t.Fatalf("the Team reads it in the agent's chat: %s", text)
	}

	claude := filepath.Join(root, "claude")
	write(t, claude, "#!/bin/bash\nread -r prompt\nprintf '%s\\n' \"$prompt\" > "+filepath.Join(root, "prompt")+"\n"+
		`echo '{"type":"result","subtype":"success","is_error":false,"result":"It retries.","num_turns":1,"session_id":"team-1","usage":{}}'`+"\n")
	os.Chmod(claude, 0o755)
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\nchat_model: haiku\nclaude_bin: "+claude+"\n")
	team = open(t, root)
	team.ChatSendAbout("", "is this ready?", "Issue o/app#7 «Login fails on Safari»", nil)
	team.teamTurn()
	if p := readFile(filepath.Join(root, "prompt")); !strings.Contains(p, `is this ready?\n(on their screen: Issue o/app#7 «Login fails on Safari»)`) {
		t.Fatalf("the Team's turn has it: %s", p)
	}

	write(t, filepath.Join(root, ".open.json"), `{"time":"`+time.Now().Add(time.Hour).Format(time.RFC3339)+`","repos":{"o/app":{"1":{"title":"t","pr":true,"head":"dev/flaky-login"},"2":{"title":"u","pr":true,"head":"someone/x"}}}}`)
	if a, b, c := team.PRAgent("o/app", 1), team.PRAgent("o/app", 2), team.PRAgent("o/app", 3); a != "dev" || b != "" || c != "" {
		t.Fatalf("the pull request's agent: %q %q %q", a, b, c)
	}
}

package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// talkTeam is chatTeam with qa at work, and the organizer's steering of its shift, which sends
// what it would hand the shift to sent.
func talkTeam(t *testing.T) (*Team, *steering, *[]string) {
	t.Helper()
	prev := talkLook
	t.Cleanup(func() { talkLook = prev })
	talkLook = 10 * time.Millisecond
	_, team := chatTeam(t)
	os.MkdirAll(team.Path(steerDir), 0o755)
	os.WriteFile(team.steerFile("qa"), []byte(strconv.Itoa(os.Getpid())), 0o644) // qa is at work
	sent := &[]string{}
	return team, &steering{t: team, agent: "qa", talks: true, read: map[string]bool{}}, sent
}

// echo is the shift's echo of what was sent to it, as claude echoes a message it takes.
func echo(text string) string { return strings.TrimPrefix(text, noteHeader+"\n\n") }

// The human talks to an agent at work as to the Team: the chat shows it answering until it does.
// Their first words reach its shift as a note from the chat, with what to do now, and a talk
// begins. While it goes on, the human's next words wait for rota listen, which brings them at once,
// and nothing else reaches the shift: a teammate's note waits for the talk's end. The agent ends
// it with its last words; or it ends once neither said anything for talk_minutes, and the chat
// says so, answering for it what it did not answer. Then what waited reaches the shift.
func TestATalkHasTheAgentsWholeAttention(t *testing.T) {
	team, s, sent := talkTeam(t)
	send := func(text string) bool { *sent = append(*sent, text); return true }
	presence := func() Presence {
		for _, p := range team.ChatPresence(time.Now()) {
			if p.Name == "qa" {
				return p
			}
		}
		return Presence{}
	}

	busy := func(want ...string) {
		t.Helper()
		if got := team.ChatBusy(); !slices.Equal(got, append([]string{}, want...)) {
			t.Fatalf("answering: %v, want %v; the chat ends: %+v", got, want, lastChat(t, team))
		}
	}
	busy()
	if _, err := team.ChatSend("@qa why did the stack not start?", nil); err != nil {
		t.Fatal(err)
	}
	busy("qa")
	if m := lastChat(t, team); m.From != "you" {
		t.Fatalf("the chat says no more than for the Team: %+v", m)
	}
	urgent, err := team.ChatSend("@qa it is urgent", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p := presence(); !p.Online || p.Talking || !strings.Contains(p.Note, "stops to talk with you") {
		t.Fatalf("at work, not yet talking: %+v", p)
	}
	s.look(send)
	if len(*sent) != 1 || !strings.Contains((*sent)[0], "from the human in the team chat: why did the stack not start?\n") ||
		!strings.Contains((*sent)[0], "from the human in the team chat: it is urgent\n") || !strings.HasSuffix((*sent)[0], "\n"+talkBegins+"\n") {
		t.Fatalf("the first words reach the shift as a note, with what to do now: %q", *sent)
	}
	if !team.Talking("qa") || !presence().Talking || !strings.Contains(presence().Note, "talking with you") {
		t.Fatalf("a talk begins: %+v", presence())
	}
	if l := (Event{Kind: "inbox", Text: "- 2026-11-19 from the human in the team chat: why?\n\n" + talkBegins}).Summary("tools"); l != "took a note: - 2026-11-19 from the human in the team chat: why?" {
		t.Fatalf("the live line says the notes, not what they tell the shift: %q", l)
	}
	team.TalkMinutes = 0.0001 // the talk waits for its first words however long the step takes
	time.Sleep(20 * time.Millisecond)
	if s.look(send); !team.Talking("qa") {
		t.Fatal("a talk whose first words the shift did not take yet goes on")
	}
	team.TalkMinutes = 5
	s.taken(echo((*sent)[0]))
	if inboxText(team, "qa") != "" || len(team.loadTalk("qa").Opening) != 0 {
		t.Fatalf("taken: read, and the talk's clock runs: %q %+v", inboxText(team, "qa"), team.loadTalk("qa"))
	}

	if err := team.ChatSay("qa", "It waits for its database.", false); err != nil {
		t.Fatal(err)
	}
	if m := lastChat(t, team); m.From != "qa" || m.Answer != urgent.ID {
		t.Fatalf("its answer names the last it answers, as the Team's does: %+v", m)
	}
	busy()
	if _, err := team.ChatSend("@qa and why is that?", nil); err != nil {
		t.Fatal(err)
	}
	busy("qa")
	if err := team.LeaveNote("qa", "", "dev", "rebase on main"); err != nil {
		t.Fatal(err)
	}
	if s.look(send); len(*sent) != 1 {
		t.Fatalf("nothing else reaches the shift while they talk: %q", *sent)
	}
	h, err := team.Listen("qa", time.Second, time.Time{})
	if err != nil || len(h.Notes) != 1 || team.HeardText(h) != "the human: and why is that?\n" {
		t.Fatalf("rota listen brings the human's next words: %+v %v", h, err)
	}
	busy("qa") // brought, not yet answered
	if strings.Contains(inboxText(team, "qa"), "why is that") || !strings.Contains(inboxText(team, "qa"), "rebase on main") {
		t.Fatalf("heard, they leave the inbox; the teammate's note waits: %s", inboxText(team, "qa"))
	}
	if h, err := team.Listen("qa", 30*time.Millisecond, time.Time{}); err != nil || !h.More {
		t.Fatalf("nothing yet: %+v %v", h, err)
	}
	if h, err := team.Listen("qa", time.Second, time.Now().Add(-time.Second)); err != nil || !h.Park {
		t.Fatalf("the shift must put its work away: %+v %v", h, err)
	}
	if err := team.ChatSay("qa", "Its migration is stuck. Back to the flaky tests.", true); err != nil || team.Talking("qa") {
		t.Fatalf("its last words end the talk: %v", err)
	}
	busy()
	if h, err := team.Listen("qa", time.Second, time.Time{}); err != nil || !h.Ended {
		t.Fatalf("no talk: %+v %v", h, err)
	}
	if s.look(send); len(*sent) != 2 || !strings.Contains((*sent)[1], "from dev: rebase on main") || strings.Contains((*sent)[1], talkBegins) {
		t.Fatalf("once the talk is over, what waited reaches the shift: %q", *sent)
	}

	// another talk, which ends when neither says anything: rota listen says so, and the chat, for
	// qa, as it never answered
	more, _ := team.ChatSend("@qa one more thing", nil)
	s.look(send)
	s.taken(echo((*sent)[2]))
	team.TalkMinutes = 0.0005
	if h, err := team.Listen("qa", time.Second, time.Time{}); err != nil || !h.Quiet || team.Talking("qa") {
		t.Fatalf("quiet: the talk is over: %+v %v", h, err)
	}
	if m := lastChat(t, team); m.From != "" || m.To != "qa" || m.Answer != more.ID || !strings.Contains(m.Text, "qa went back to its work: neither of you said anything for") {
		t.Fatalf("the chat says it went back to its work, answering for it: %+v", m)
	}
	busy()

	// one nobody listens in ends too, at the organizer's look, which also ends a quiet one
	team.TalkMinutes = 5
	team.ChatSend("@qa still there?", nil)
	s.look(send)
	s.taken(echo((*sent)[3]))
	team.TalkMinutes = 0.0005
	time.Sleep(50 * time.Millisecond)
	if s.look(send); team.Talking("qa") || !strings.Contains(lastChat(t, team).Text, "qa went back to its work") {
		t.Fatal("a quiet talk ends at the organizer's next look")
	}
	// the human's words that rota listen never brought reach the shift as a note, which begins
	// another talk at once: the chat does not say the agent went back to its work
	team.TalkMinutes = 5
	team.ChatSend("@qa are you there?", nil)
	s.look(send)
	s.taken(echo((*sent)[4]))
	team.ChatSend("@qa hello?", nil)
	team.TalkMinutes = 0.0005
	time.Sleep(50 * time.Millisecond)
	if s.look(send); lastChat(t, team).From != "you" {
		t.Fatalf("no word of going back to work, as the talk goes on: %+v", lastChat(t, team))
	}
	busy("qa")
	if len(*sent) != 6 || !strings.Contains((*sent)[5], "hello?") || !team.Talking("qa") || len(team.loadTalk("qa").Opening) != 1 {
		t.Fatalf("what rota listen never brought reaches the shift as a note, and begins another talk: %q", *sent)
	}
	team.TalkMinutes = 5
	heard, talked := team.endTalk("qa")
	if len(heard) != 1 || heard[0].Text != "and why is that?" || !talked || team.Talking("qa") {
		t.Fatalf("the shift's end ends the talk, and gives what rota listen brought: %+v %v", heard, talked)
	}
	os.Remove(team.steerFile("qa"))
	team.chatAfterShift("qa", talked, false)
	if m := lastChat(t, team); m.To != "qa" || m.Answer != "" || m.Text != "qa's shift ended before it answered you: its next shift answers you here, first thing." {
		t.Fatalf("the chat says its next shift answers, answering nothing for it: %+v", m)
	}
	if inbox := inboxText(team, "qa"); strings.Count(inbox, "are you there?") != 1 || strings.Count(inbox, "hello?") != 1 {
		t.Fatalf("what it did not answer waits for its next shift, once each, whether it took it or not:\n%s", inbox)
	}
	busy()
}

// Who answers in the chat, as it shows them typing: the Team and the agents alike, each for what
// was written to it. What one answers leaves the others' waiting; rota's line that an agent is not
// at work answers nothing: its next shift does; an agent not at work answers nothing now, nor one
// asked over an hour ago, unless its shift began since.
func TestTheChatShowsWhoAnswers(t *testing.T) {
	team, _, _ := talkTeam(t)
	team.ChatSend("@dev rebase #673", nil) // dev is not at work: its next shift answers
	if m := lastChat(t, team); m.To != "dev" || m.Answer != "" || len(chatPending(team.Chat(100), "dev")) != 1 || len(team.ChatBusy()) != 0 {
		t.Fatalf("rota's line says when dev answers, and answers nothing for it: %+v %v", m, team.ChatBusy())
	}
	asked, _ := team.ChatSend("What is everyone doing?", nil)
	team.ChatSend("@qa what are you doing?", nil)
	if got := chatPending(team.Chat(100), "team"); len(got) != 1 || got[0].ID != asked.ID {
		t.Fatalf("the Team's: %+v", got)
	}
	team.ChatSay("qa", "Testing the stack.", false)
	if got := chatPending(team.Chat(100), "team"); len(got) != 1 || len(chatPending(team.Chat(100), "qa")) != 0 {
		t.Fatalf("qa's answer is not the Team's: %+v", got)
	}
	team.chatAppend(ChatMsg{From: "team", Text: "All busy.", Answer: asked.ID})
	team.ChatSend("@qa and then?", nil)
	if len(chatPending(team.Chat(100), "team")) != 0 || len(chatPending(team.Chat(100), "qa")) != 1 {
		t.Fatal("the Team's answer is not qa's")
	}
	team.chatAppend(ChatMsg{From: "qa", Text: "Then the flaky tests."}) // as an agent said it before its words named what they answer
	if len(chatPending(team.Chat(100), "qa")) != 0 {
		t.Fatal("an agent's words answer all before")
	}
	team.chatAppend(ChatMsg{From: "you", To: "qa", Text: "@qa still there?", At: time.Now().Add(-2 * time.Hour)})
	began := time.Now().Add(-90 * time.Minute)
	os.Chtimes(team.steerFile("qa"), began, began)
	if len(chatPending(team.Chat(100), "qa")) != 1 || len(team.ChatBusy()) != 0 {
		t.Fatalf("asked over an hour ago, before a shift that began over an hour ago, qa is not shown answering: %v", team.ChatBusy())
	}
	began = time.Now().Add(-time.Minute)
	os.Chtimes(team.steerFile("qa"), began, began)
	if busy := team.ChatBusy(); !slices.Equal(busy, []string{"qa"}) {
		t.Fatalf("its shift began since: it answers that first: %v", busy)
	}
}

// The agent saying something or listening in a talk has the human's first words, whether or not
// its echo of them was seen: the talk's clock runs. Words that answer the human's of the last
// talk_minutes, which reached the shift in its brief as it began, begin a talk; older ones, and
// last words, do not.
func TestTheAgentsWordsKeepATalk(t *testing.T) {
	team, s, sent := talkTeam(t)
	send := func(text string) bool { *sent = append(*sent, text); return true }
	team.ChatSend("@qa why did the stack not start?", nil)
	s.look(send)
	if err := team.ChatSay("qa", "Its database is down.", false); err != nil || !team.Talking("qa") || len(team.loadTalk("qa").Opening) != 0 {
		t.Fatalf("it answered: the talk goes on, its clock running: %v %+v", err, team.loadTalk("qa"))
	}
	team.ChatSay("qa", "Back to work.", true)
	team.ChatSend("@qa one more thing", nil)
	s.look(send)
	if h, err := team.Listen("qa", 30*time.Millisecond, time.Time{}); err != nil || !h.More || len(team.loadTalk("qa").Opening) != 0 {
		t.Fatalf("it listens: %+v %v %+v", h, err, team.loadTalk("qa"))
	}
	os.Remove(team.talkFile("qa")) // a new shift

	team.chatAppend(ChatMsg{From: "you", To: "qa", Text: "@qa anything new?", At: time.Now().Add(-time.Hour)})
	if team.ChatSay("qa", "Nothing new.", false); team.Talking("qa") {
		t.Fatal("an hour-old question begins no talk")
	}
	team.chatAppend(ChatMsg{From: "you", To: "qa", Text: "@qa and now?", At: time.Now().Add(-time.Minute)})
	if team.ChatSay("qa", "Bye.", true); team.Talking("qa") {
		t.Fatal("last words begin no talk")
	}
	if team.ChatSay("qa", "The stack is up again.", false); !team.Talking("qa") {
		t.Fatal("answering the human's words of a minute ago begins a talk")
	}
	team.ChatSend("@qa great, thanks", nil)
	if h, err := team.Listen("qa", time.Second, time.Time{}); err != nil || team.HeardText(h) != "the human: great, thanks\n" {
		t.Fatalf("rota listen brings what the human writes next: %+v %v", h, err)
	}
}

// rota wait gives way to the human's first words in a talk: they reach the shift once its wait
// ends, and waiting again can still bring the answer.
func TestAWaitGivesWayToATalk(t *testing.T) {
	team, s, sent := talkTeam(t)
	send := func(text string) bool { *sent = append(*sent, text); return true }
	id, err := team.Ask("qa", "Which database?")
	if err != nil {
		t.Fatal(err)
	}
	if _, more, err := team.WaitAnswer(id, 30*time.Millisecond, time.Time{}); err != nil || !more {
		t.Fatalf("no answer yet: %v %v", more, err)
	}
	team.ChatSend("@qa what do you mean by which database?", nil)
	s.look(send)
	if _, more, err := team.WaitAnswer(id, time.Second, time.Time{}); !errors.Is(err, ErrTalk) || !more {
		t.Fatalf("it gives way to the talk: %v %v", more, err)
	}
	s.taken(echo((*sent)[0]))
	if _, more, err := team.WaitAnswer(id, 30*time.Millisecond, time.Time{}); err != nil || !more {
		t.Fatalf("the first words taken, it waits again: %v %v", more, err)
	}
}

// In a shift, what the human said in the talk is read: the journal quotes the first words and
// those rota listen brought, not what the shift was told to do with them, and a teammate's note
// that came while they talked waits for the next shift. The chat says the shift ended while they
// talked; the agent answered, so nothing goes to its next shift.
func TestTheJournalQuotesTheTalk(t *testing.T) {
	defer func(d, l time.Duration) { steerEvery, talkLook = d, l }(steerEvery, talkLook)
	steerEvery, talkLook = 50*time.Millisecond, 10*time.Millisecond
	root := t.TempDir()
	claude := filepath.Join(root, "claude")
	write(t, claude, "#!/bin/bash\nread -r prompt\ntouch "+root+"/started\n"+
		`read -r note; printf '%s\n' "$note"`+"\n"+ // echoes the human's first words, as claude does once they enter the conversation
		"while [ ! -f "+root+"/listened ]; do sleep 0.02; done\n"+
		`echo '{"type":"result","subtype":"success","is_error":false,"result":"## Summary\ndone","num_turns":2,"session_id":"s1","usage":{}}'`+"\n")
	os.Chmod(claude, 0o755)
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\ntriage_model: off\nreport_model: off\nchat_model: off\nclaude_bin: "+claude+"\n")
	write(t, filepath.Join(root, "agents", "dev.md"), "---\nenabled: true\nrepos: o/backend\n---\ndev\n")
	team := open(t, root)
	done := make(chan error)
	go func() { done <- team.RunShift(context.Background(), "dev", time.Now(), true, false) }()
	waitFor(t, "the shift", func() bool { _, err := os.Stat(filepath.Join(root, "started")); return err == nil })
	if _, err := team.ChatSend("@dev why is CI red?", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first words taken", func() bool { tk := team.loadTalk("dev"); return tk.Open && len(tk.Opening) == 0 })
	team.ChatSend("@dev and the deploy?", nil)
	team.LeaveNote("dev", "", "qa", "the tests pass again")
	if h, err := team.Listen("dev", time.Second, time.Time{}); err != nil || len(h.Notes) != 1 {
		t.Fatalf("heard: %+v %v", h, err)
	}
	if err := team.ChatSay("dev", "CI waits for a runner; the deploy is fine.", false); err != nil { // as rota say does
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "listened"), "")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	journal := readFile(filepath.Join(root, "journal", time.Now().Format("2006-01-02")+".md"))
	if !strings.Contains(journal, "in the team chat: why is CI red?") || !strings.Contains(journal, "in the team chat: and the deploy?") ||
		strings.Contains(journal, "the tests pass again") || strings.Contains(journal, talkBegins) {
		t.Fatalf("the journal quotes the talk, not what waited:\n%s", journal)
	}
	if inbox := inboxText(team, "dev"); strings.Contains(inbox, "CI red") || strings.Contains(inbox, "deploy") || !strings.Contains(inbox, "the tests pass again") {
		t.Fatalf("the talk is read; the teammate's note waits:\n%s", inbox)
	}
	if m := lastChat(t, team); team.Talking("dev") || m.Text != "dev's shift ended, and your talk with it: what you write it now, its next shift answers here." {
		t.Fatalf("the shift ended the talk: %+v", m)
	}
}

// The Team reads an agent's chat with the human with its agent_chat tool, over MCP as its Claude
// asks it: whether the agent is in it now, and what the two said, oldest first, by whom, none of
// the Team's own chat; as many of its last messages as asked. The team as it comes with each
// question says which agent talks with the human now.
func TestTheTeamReadsAnAgentsChat(t *testing.T) {
	team, s, sent := talkTeam(t)
	send := func(text string) bool { *sent = append(*sent, text); return true }
	team.ChatSend("What is everyone doing?", nil)
	team.chatAppend(ChatMsg{From: "team", Text: "qa tests the stack.", Answer: team.teamPending()[0].ID})
	team.ChatSendTo("qa", "why did the stack not start?", nil)
	s.look(send)
	s.taken(echo((*sent)[0]))
	team.ChatSay("qa", "Its database waits for a migration.", false)
	team.ChatSendTo("dev", "rebase #673, please", nil) // dev is not at work

	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	go team.serveTeamTools(ctx, st)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "claude", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	read := func(args map[string]any) (string, bool) {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "agent_chat", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return res.Content[0].(*mcp.TextContent).Text, res.IsError
	}
	day := time.Now().Format("Mon 2006-01-02 ")
	text, failed := read(map[string]any{"agent": "qa"})
	if failed || !strings.HasPrefix(text, "qa is at work, and talking with the human in this chat now") ||
		!strings.Contains(text, "\n\n"+day) || !strings.Contains(text, " the human: why did the stack not start?\n"+day) ||
		!strings.HasSuffix(text, " qa: Its database waits for a migration.") || strings.Contains(text, "everyone") || strings.Contains(text, "#673") {
		t.Fatalf("qa's chat:\n%s", text)
	}
	if text, _ := read(map[string]any{"agent": "qa", "last": 1}); strings.Contains(text, "why did the stack") || !strings.HasSuffix(text, " qa: Its database waits for a migration.") {
		t.Fatalf("its last message:\n%s", text)
	}
	if text, failed := read(map[string]any{"agent": "dev"}); failed || !strings.HasPrefix(text, "dev is not at work (") || !strings.Contains(text, "waits for its next shift, which answers here") ||
		!strings.Contains(text, " the human: rebase #673, please\n") || !strings.Contains(text, " rota: dev is not at work now: its next shift answers you here") {
		t.Fatalf("dev's chat:\n%s", text)
	}
	if text, failed := read(map[string]any{"agent": "nobody"}); !failed || !strings.Contains(text, "the agents are dev, qa") && !strings.Contains(text, "the agents are qa, dev") {
		t.Fatalf("no such agent: %q", text)
	}

	now := time.Now()
	status := Status{Pid: os.Getpid(), Running: &Running{Agent: "qa", Since: now}, Agents: []AgentStatus{{Name: "qa", Enabled: true, State: "ready"}, {Name: "dev", Enabled: true, State: "ready"}}}
	if lines := team.teamAgents(status); !strings.Contains(lines, "talking with the human in its chat now, its work waiting") || !strings.Contains(lines, "its chat with the human: the last message "+now.Local().Format("Mon ")) {
		t.Fatalf("the team as the Team gets it:\n%s", lines)
	}
}

// What the human writes an agent that is not at work waits for its next shift, which gets it in its
// brief with what to do with it, shows the agent typing as it begins, and answers it in the chat,
// never in the mail. Words a shift ended before it answered go to the next, once, and the chat
// says so; when that one does not answer either, the chat says so, answering for it. An answer in
// the final message (from a machine where rota say does not work) is in the chat too. A shift on
// another machine gets the words with how to answer them from there.
func TestTheNextShiftAnswersInTheChat(t *testing.T) {
	defer func(d time.Duration) { steerEvery = d }(steerEvery)
	steerEvery = 50 * time.Millisecond
	root := t.TempDir()
	claude := filepath.Join(root, "claude")
	write(t, claude, "#!/bin/bash\nread -r prompt\nprintf '%s\\n' \"$prompt\" > "+root+"/prompt.tmp\nmv "+root+"/prompt.tmp "+root+"/prompt\n"+
		"while [ ! -f "+root+"/go ]; do sleep 0.02; done\nrm "+root+"/go\ncat "+root+"/result\n")
	os.Chmod(claude, 0o755)
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\ntriage_model: off\nreport_model: off\nchat_model: off\nclaude_bin: "+claude+"\n")
	write(t, filepath.Join(root, "agents", "dev.md"), "---\nenabled: true\nrepos: o/backend\n---\ndev\n")
	team := open(t, root)
	prompt := ""
	shift := func(final string, during func()) {
		t.Helper()
		os.Remove(filepath.Join(root, "prompt"))
		b, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": "## Summary\ndone\n" + final, "num_turns": 2, "session_id": "s1", "usage": map[string]any{}})
		write(t, filepath.Join(root, "result"), string(b)+"\n")
		done := make(chan error)
		go func() { done <- team.RunShift(context.Background(), "dev", time.Now(), true, false) }()
		waitFor(t, "the shift", func() bool { _, err := os.Stat(filepath.Join(root, "prompt")); return err == nil })
		prompt = readFile(filepath.Join(root, "prompt"))
		if during != nil {
			during()
		}
		write(t, filepath.Join(root, "go"), "")
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	chatOf := func() (out []string) {
		for _, m := range team.Chat(100) {
			out = append(out, or(m.From, "rota")+": "+m.Text)
		}
		return out
	}

	why, _ := team.ChatSendTo("dev", "why is CI red?", nil)
	deploy, _ := team.ChatSendTo("dev", "@dev and the deploy?", nil)
	if got := chatOf(); !slices.Equal(got, []string{"you: why is CI red?", "rota: dev is not at work now: its next shift answers you here, first thing.", "you: @dev and the deploy?"}) {
		t.Fatalf("the chat says once when dev answers: %q", got)
	}
	remote := *team.Agent("dev")
	remote.Host = "mini"
	if job, _, _, err := team.brief(&remote, team.schedule(time.Now(), nil, nil)); err != nil || !strings.Contains(job.Prompt, "\n"+chatAway+"\n") {
		t.Fatalf("on another machine, the brief says how to answer from there: %v", err)
	}
	away, sent := &steering{t: team, agent: "dev", read: map[string]bool{}}, []string{}
	if away.look(func(text string) bool { sent = append(sent, text); return true }); len(sent) != 1 || !strings.HasSuffix(sent[0], "\n"+chatAway+"\n") || team.Talking("dev") {
		t.Fatalf("a shift on another machine gets them with how to answer them from there: %q", sent)
	}

	shift("", func() { // it does not answer
		if !regexp.MustCompile(`\d\d:\d\d from the human in the team chat: why is CI red\?`).MatchString(prompt) || !strings.Contains(prompt, "in the team chat: and the deploy?") ||
			!strings.Contains(prompt, "while you were not at work, and waits for your answer there: answer them first") {
			t.Fatalf("the brief has the human's words, when they wrote them, and what to do with them:\n%s", prompt)
		}
		if busy := team.ChatBusy(); !slices.Equal(busy, []string{"dev"}) {
			t.Fatalf("the chat shows dev typing as its shift begins: %v", busy)
		}
	})
	if m := lastChat(t, team); m.To != "dev" || m.Answer != "" || m.Text != "dev's shift ended before it answered you: its next shift answers you here, first thing." {
		t.Fatalf("the chat says its next shift answers, answering nothing for it: %+v", m)
	}
	if notes := team.Notes("dev"); len(notes) != 2 || "c"+notes[0].ID != why.ID || "c"+notes[1].ID != deploy.ID || !notes[1].Chat || notes[1].Text != "and the deploy?" {
		t.Fatalf("its next shift gets them again: %+v", notes)
	}
	if _, err := os.Stat(filepath.Join(team.askedDir(), "dev")); err != nil {
		t.Fatal("and is asked for")
	}
	if journal := readFile(filepath.Join(root, "journal", time.Now().Format("2006-01-02")+".md")); !strings.Contains(journal, "in the team chat: why is CI red?") {
		t.Fatalf("the journal says it read them:\n%s", journal)
	}

	shift("", func() { // nor does the next
		if busy := team.ChatBusy(); !strings.Contains(prompt, "in the team chat: and the deploy?") || !slices.Equal(busy, []string{"dev"}) {
			t.Fatalf("the next shift gets them, and the chat shows dev typing again: %v", busy)
		}
	})
	if m := lastChat(t, team); m.Answer != deploy.ID || m.Text != "dev's next shift did not answer you either. Write it again when you still need the answer." || len(team.Notes("dev")) != 0 {
		t.Fatalf("then the chat says so, answering for it, and no shift gets them again: %+v %+v", m, team.Notes("dev"))
	}

	green, _ := team.ChatSendTo("dev", "is CI green now?", nil)
	shift("## Notes to teammates\n@human say: CI is green again.\n", nil) // as from a machine where rota say does not work
	if m := lastChat(t, team); m.From != "dev" || m.Text != "CI is green again." || m.Answer != green.ID || len(chatPending(team.Chat(100), "dev")) != 0 {
		t.Fatalf("its answer in its final message is in the chat, and the chat says nothing more: %+v", m)
	}
	if len(team.Questions()) != 0 || len(team.Notes("dev")) != 0 || len(team.MessageThreads()) != 0 {
		t.Fatalf("no question for the human, nothing waits, nothing in the mail: %+v %+v", team.Questions(), team.Notes("dev"))
	}
}

package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func chatTeam(t *testing.T) (string, *Team) {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\ntick_minutes: 60\n")
	write(t, filepath.Join(root, "agents", "qa.md"), "---\nenabled: true\nrun_minutes: 45\n---\nqa tests\n")
	write(t, filepath.Join(root, "agents", "dev.md"), "---\nenabled: true\nhours:\n---\ndev codes\n") // hours: any time, whatever the team's default
	return root, open(t, root)
}

func lastChat(t *testing.T, team *Team) ChatMsg {
	t.Helper()
	msgs := team.Chat(100)
	if len(msgs) == 0 {
		t.Fatal("no chat")
	}
	return msgs[len(msgs)-1]
}

// The human writes to the Team, which is woken to answer; to an agent at work here, whose shift
// reads it after its current step as a note from the chat; or to one that is not at work, whose
// next shift gets it from the chat, not the mail, and answers it there: it asks for that shift,
// and the chat says so, once. An agent that is not one is refused, and only an agent says
// something in the chat as itself: never as the Team or the human.
func TestTheHumanWritesTheTeamOrAnAgent(t *testing.T) {
	root, team := chatTeam(t)
	woken := 0
	wake := func() { woken++ }
	if _, err := team.ChatSend("What is everyone doing?", wake); err != nil || woken != 1 {
		t.Fatalf("to the Team: %v, woken %d", err, woken)
	}
	if m := lastChat(t, team); m.From != "you" || m.To != "" || m.Text != "What is everyone doing?" || m.ID == "" {
		t.Fatalf("kept: %+v", m)
	}
	os.MkdirAll(team.Path(steerDir), 0o755)
	os.WriteFile(team.steerFile("qa"), []byte(strconv.Itoa(os.Getpid())), 0o644) // qa is at work
	if _, err := team.ChatSend("@qa why did the stack not start?", wake); err != nil || woken != 1 {
		t.Fatalf("to qa: %v, woken %d", err, woken)
	}
	if notes := inboxText(team, "qa"); !strings.Contains(notes, "from the human in the team chat: why did the stack not start?") {
		t.Fatalf("qa's shift reads it, without its name: %s", notes)
	}
	if _, err := team.ChatSend("@dev rebase #673", wake); err != nil {
		t.Fatal(err)
	}
	away := lastChat(t, team)
	if away.From != "" || away.To != "dev" || away.Answer != "" || away.Text != "dev is not at work now: its next shift answers you here, first thing." {
		t.Fatalf("the chat says when dev answers: %+v", away)
	}
	if notes := inboxText(team, "dev"); !strings.Contains(notes, "from the human in the team chat: rebase #673\n") || len(team.MessageThreads()) != 0 {
		t.Fatalf("dev's next shift gets it from the chat, not the mail: %s %+v", notes, team.MessageThreads())
	}
	if _, err := os.Stat(filepath.Join(team.askedDir(), "dev")); err != nil {
		t.Fatal("the human's words ask for dev's next shift")
	}
	if m, _ := team.ChatSend("@dev and #674", wake); lastChat(t, team).ID != m.ID || !strings.Contains(inboxText(team, "dev"), "in the team chat: and #674\n") {
		t.Fatalf("the chat says it once: %+v", team.Chat(3))
	}
	team.ChatSay("dev", "On it.", false) // its next shift answers
	team.setPaused("dev", true)
	team.ChatSendTo("dev", "thanks", wake)
	if m := lastChat(t, team); m.Text != "dev is paused: once you resume it, its next shift answers you here, first thing." {
		t.Fatalf("after its words, the chat says it again, and why: %+v", m)
	}
	for _, bad := range []string{"", "@nobody hello", strings.Repeat("x", chatMax+1)} {
		if _, err := team.ChatSend(bad, wake); err == nil {
			t.Errorf("%.20q is sent", bad)
		}
	}
	if err := team.ChatSay("qa", "The stack waits for its database.", false); err != nil || lastChat(t, team).From != "qa" {
		t.Fatalf("an agent says: %v", err)
	}
	for _, who := range []string{"team", "you", "Team", "nobody", ""} {
		if err := team.ChatSay(who, "I speak for the team.", false); err == nil {
			t.Errorf("%q says something in the chat", who)
		}
	}
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\nchat_model: off\n")
	off := open(t, root)
	if _, err := off.ChatSend("hello?", wake); err != nil || woken != 1 || !strings.Contains(lastChat(t, off).Text, "The Team is off") {
		t.Fatalf("with no Team, the chat says so: %v %d", err, woken)
	}
}

// The chat with the Team and the chat with each agent: what the human writes in an agent's goes to
// that agent, with no @ needed; in the Team's, an @ and an agent's name writes to that agent, in
// its chat. Each message is in one chat: the human's in the chat of whom they wrote to, the Team's
// and each agent's in their own, rota's line in the chat of the agent it says something of, also
// as said before it named the agent, or the Team's.
func TestEachChatIsItsOwn(t *testing.T) {
	_, team := chatTeam(t)
	os.MkdirAll(team.Path(steerDir), 0o755)
	os.WriteFile(team.steerFile("qa"), []byte(strconv.Itoa(os.Getpid())), 0o644) // qa is at work
	if m, err := team.ChatSendTo("qa", "why is the stack down?", nil); err != nil || m.To != "qa" || m.With() != "qa" {
		t.Fatalf("in qa's chat: %+v %v", m, err)
	}
	if m, err := team.ChatSendTo("qa", "@qa and the database?", nil); err != nil || m.With() != "qa" {
		t.Fatalf("in qa's chat, its name too: %+v %v", m, err)
	}
	if notes := inboxText(team, "qa"); !strings.Contains(notes, "in the team chat: why is the stack down?\n") || !strings.Contains(notes, "in the team chat: and the database?\n") {
		t.Fatalf("qa's shift reads them, without its name: %s", notes)
	}
	if m, err := team.ChatSendTo("qa", "@dev take over", nil); err != nil || m.To != "qa" {
		t.Fatalf("in qa's chat, all of it goes to qa: %+v %v", m, err)
	}
	if m, err := team.ChatSendTo("", "@qa one more", nil); err != nil || m.With() != "qa" {
		t.Fatalf("an @ in the Team's chat writes in qa's: %+v %v", m, err)
	}
	if m, err := team.ChatSendTo("team", "What is everyone doing?", nil); err != nil || m.To != "" || m.With() != "team" {
		t.Fatalf("in the Team's chat: %+v %v", m, err)
	}
	if _, err := team.ChatSendTo("nobody", "hi", nil); err == nil {
		t.Fatal("no chat with an agent that is none")
	}
	for _, c := range []struct {
		m    ChatMsg
		with string
	}{
		{ChatMsg{From: "team", Text: "All idle.", Answer: "c1"}, "team"},
		{ChatMsg{From: "team", Change: &ChatChange{Agent: "qa"}}, "team"},
		{ChatMsg{Text: "The Team is off: chat_model in Settings turns it on.", Answer: "c1"}, "team"},
		{ChatMsg{Re: "c2", State: "applied"}, "team"},
		{ChatMsg{From: "qa", Text: "Testing."}, "qa"},
		{ChatMsg{Text: "qa went back to its work.", To: "qa"}, "qa"},
		{ChatMsg{Text: "dev is not working now. It reads your message at its next run and answers in your inbox; until then the message waits in Waiting."}, "dev"}, // as rota said it before its lines named their agent
		{ChatMsg{Text: "dev is not working now"}, "team"},
		{ChatMsg{Text: "dev is not at work now: its next shift answers you here, first thing.", To: "dev"}, "dev"},
	} {
		if got := c.m.With(); got != c.with {
			t.Errorf("%+v is in %q's chat, want %q's", c.m, got, c.with)
		}
	}
}

// The Team proposes a change, checked first: no setting that is not one, no value of two lines,
// no new agent under a name the chat or the labels use. The human applies it once, cancels it, or
// undoes it, which writes back exactly what it replaced, unless that changed since.
func TestAChangeTheTeamProposes(t *testing.T) {
	root, team := chatTeam(t)
	for _, bad := range []ChatChange{
		{Agent: "qa", Set: map[string]string{"no_such": "1"}},
		{Agent: "qa", Set: map[string]string{"run_minutes": "lots"}},
		{Agent: "qa", Set: map[string]string{"model": "sonnet\npermission_mode: bypassPermissions"}},
		{Set: map[string]string{"chat_model": "haiku\nweb: tailscale"}},
		{Agent: "nobody", Set: map[string]string{"run_minutes": "90"}},
		{Set: map[string]string{"tick_minutes": "-5"}},
		{Agent: "qa", Do: "explode"},
		{Agent: "qa", Do: "pause", Set: map[string]string{"run_minutes": "90"}},
		{Agent: "qa"},
		{New: true, Agent: "qa", Role: "again"},
		{New: true, Agent: "team", Role: "speaks for all"},
		{New: true, Agent: "approved", Role: "approves"},
		{New: true, Agent: "../x", Role: "escape"},
		{New: true, Agent: "links"},
		{New: true, Agent: "links", Role: "x", Set: map[string]string{"model": "a\nb: c"}},
		{New: true, Agent: "links", Role: "x", Set: map[string]string{"#\ntools": "Bash"}}, // a key that writes another
		{New: true, Agent: "links", Role: "x", Set: map[string]string{"no_such": "1"}},
		{New: true, Agent: "links", Role: "x", Set: map[string]string{"run_minutes": "lots"}},
		{Do: "run"},
	} {
		if _, err := team.ChatPropose(bad); err == nil {
			t.Errorf("proposed: %+v", bad)
		}
	}
	if err := team.SetAgent("qa", map[string]string{"model": "sonnet\npermission_mode: bypassPermissions"}); err == nil {
		t.Fatal("a value of two lines is written into an agent's file")
	}
	id, err := team.ChatPropose(ChatChange{Agent: "qa", Set: map[string]string{"run_minutes": "90"}, Why: "its stack starts slowly"})
	if err != nil {
		t.Fatal(err)
	}
	if m := lastChat(t, team); m.From != "team" || m.Change == nil || m.Change.Was["run_minutes"] != "45" || m.Change.State != "" {
		t.Fatalf("a card from the Team, with what it replaces: %+v", m.Change)
	}
	if said, err := team.ChatApply(id); err != nil || !strings.Contains(said, "run_minutes 45 → 90") {
		t.Fatalf("applied: %q %v", said, err)
	}
	if _, err := team.ChatApply(id); err == nil {
		t.Fatal("applied twice")
	}
	if err := team.ChatCancel(id); err == nil {
		t.Fatal("an applied change is cancelled")
	}
	team.SetAgent("qa", map[string]string{"run_minutes": "120"}) // the human changed it since
	if _, err := team.ChatUndo(id); err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Fatalf("undo overwrites a later change: %v", err)
	}
	team.SetAgent("qa", map[string]string{"run_minutes": "90"})
	if said, err := team.ChatUndo(id); err != nil || !strings.HasPrefix(said, "Undone") {
		t.Fatalf("undone: %q %v", said, err)
	}
	if a := open(t, root).Agent("qa"); a.RunMinutes != 45 {
		t.Fatalf("undo writes back what it replaced: %d", a.RunMinutes)
	}
	if _, err := team.ChatUndo(id); err == nil {
		t.Fatal("undone twice")
	}
	if c := lastCard(t, team, id); c.State != "undone" {
		t.Fatalf("the card says it was undone: %+v", c)
	}

	// a setting the file did not have is gone again; one it had empty is empty again
	id, _ = team.ChatPropose(ChatChange{Agent: "dev", Set: map[string]string{"model": "opus", "hours": "06:00-08:00"}, Why: "harder work, early"})
	team.ChatApply(id)
	team.ChatUndo(id)
	if b, _ := os.ReadFile(team.AgentFile("dev")); strings.Contains(string(b), "model") || !strings.Contains(string(b), "\nhours:\n") {
		t.Fatalf("undone exactly: %s", b)
	}

	// a pause, undone, leaves it as it was
	team.PauseAgent("dev")
	id, _ = team.ChatPropose(ChatChange{Agent: "dev", Do: "pause", Why: "a quiet day"})
	team.ChatApply(id)
	team.ChatUndo(id)
	if !open(t, root).PausedAgents()["dev"] {
		t.Fatal("undo resumed an agent that was paused before")
	}
	team.ResumeAgent("dev")
	id, _ = team.ChatPropose(ChatChange{Do: "pause", Why: "a holiday"})
	team.ChatApply(id)
	if !open(t, root).Paused() {
		t.Fatal("the team paused")
	}
	team.ChatUndo(id)
	if open(t, root).Paused() {
		t.Fatal("the team on again")
	}

	// a new agent; undone before it ran, it is gone; once it ran, or was changed, it stays
	id, err = team.ChatPropose(ChatChange{New: true, Agent: "links", Role: "Checks every page for broken links.", Set: map[string]string{"every": "7d", "model": "sonnet"}, Why: "broken links"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := team.ChatApply(id); err != nil {
		t.Fatal(err)
	}
	if a := open(t, root).Agent("links"); a == nil || a.Model != "sonnet" || !strings.Contains(a.Role, "broken links") {
		t.Fatalf("the new agent: %+v", a)
	}
	team = open(t, root)
	if _, err := team.ChatUndo(id); err != nil || open(t, root).Agent("links") != nil {
		t.Fatalf("undone before it ran, it is gone: %v", err)
	}
	team = open(t, root)
	if id, err = team.ChatPropose(ChatChange{New: true, Agent: "links", Role: "Checks links.", Why: "again"}); err != nil {
		t.Fatal(err)
	}
	if _, err := team.ChatApply(id); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, spendFile), strings.Join(SpendColumns, ",")+"\n2026-11-08T09:00:00+01:00,links,3,60,s,ok,0,0,0,0,,,,,\n")
	if _, err := open(t, root).ChatUndo(id); err == nil || open(t, root).Agent("links") == nil {
		t.Fatalf("an agent that ran is removed by undo: %v", err)
	}
	id, _ = team.ChatPropose(ChatChange{Agent: "qa", Set: map[string]string{"run_minutes": "60"}, Why: "x"})
	if err := team.ChatCancel(id); err != nil {
		t.Fatal(err)
	}
	if _, err := team.ChatApply(id); err == nil {
		t.Fatal("a cancelled change is applied")
	}
}

func lastCard(t *testing.T, team *Team, id string) ChatChange {
	t.Helper()
	for _, m := range team.Chat(1000) {
		if m.ID == id && m.Change != nil {
			return *m.Change
		}
	}
	t.Fatalf("no card %s", id)
	return ChatChange{}
}

// The Team answers in its own Claude session, which each turn resumes, with the team as it is now:
// what the human wrote it that it has not answered, also what came while it answered before. Its
// proposals become cards. It runs in Claude's restricted mode: its file tools reach the whole team
// folder and nothing else, rota's own tools read the team's work on GitHub, and it runs no
// commands; a session that is gone is begun anew, any other failure is said in the chat.
func TestTheTeamAnswersInItsSession(t *testing.T) {
	root, team := chatTeam(t)
	args, prompts := filepath.Join(root, "args"), filepath.Join(root, "prompts")
	claude := filepath.Join(root, "claude")
	answer := func(result string) {
		write(t, claude, "#!/bin/bash\nprintf '%s\\n' \"$*\" >> "+args+"\nread -r prompt\nprintf '%s\\n' \"$prompt\" >> "+prompts+"\nsleep 0.4\n"+
			`echo '{"type":"system","subtype":"init","session_id":"team-1"}'`+"\n"+
			`printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":`+result+`,"num_turns":1,"session_id":"team-1","usage":{}}'`+"\n")
		os.Chmod(claude, 0o755)
	}
	answer(`"triage is working; the rest are idle."`)
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\nchat_model: haiku\nclaude_bin: "+claude+"\n")
	team = open(t, root)
	team.ChatSend("What is everyone doing?", nil)
	done := make(chan struct{})
	go func() { team.teamTurn(); close(done) }()
	waitFor(t, "the Team to answer", func() bool { return slices.Equal(team.ChatBusy(), []string{"team"}) })
	team.ChatSend("And qa?", nil) // while it answers
	<-done
	if busy := team.ChatBusy(); len(busy) != 0 {
		t.Fatalf("still busy: %v", busy)
	}
	if m := lastChat(t, team); m.From != "team" || m.Text != "triage is working; the rest are idle." {
		t.Fatalf("its answer: %+v", m)
	}
	if team.teamState().Session != "team-1" {
		t.Fatal("its session is kept")
	}
	b, _ := os.ReadFile(args)
	a := string(b)
	for _, want := range []string{"--restricted", "--strict-mcp-config", "--tools Read,Grep,Glob", `"args":["team-tools","-root","` + root + `","-config","rota.conf"]`,
		"--allowedTools mcp__rota__github", "--model haiku"} {
		if !strings.Contains(a, want) {
			t.Fatalf("its first turn lacks %s: %s", want, a)
		}
	}
	if strings.Contains(a, "--resume team-") || strings.Contains(a, "Bash(") || strings.Contains(a, "--disallowedTools") || strings.Contains(a, "Read(./secrets") {
		t.Fatalf("its first turn: %s", a)
	}
	team.teamTurn() // what came while it answered
	b, _ = os.ReadFile(prompts)
	if p := strings.Split(strings.TrimSpace(string(b)), "\n"); len(p) != 2 || !strings.Contains(p[1], "And qa?") || strings.Contains(p[1], "What is everyone doing?") || !strings.Contains(p[1], "The team now") {
		t.Fatalf("the next turn answers what came meanwhile, with the team as it is: %q", p)
	}
	if b, _ := os.ReadFile(args); !strings.Contains(string(b), "--resume team-1") {
		t.Fatalf("the next turn resumes its session: %s", b)
	}
	team.teamTurn() // nothing new to answer
	if b2, _ := os.ReadFile(args); strings.Count(string(b2), "--input-format") != 2 {
		t.Fatal("a turn with nothing to answer runs claude")
	}

	// its proposals become cards, out of its words
	answer(`"The qa stack starts slowly: it can have 90 minutes.\nPROPOSE {\"agent\": \"qa\", \"set\": {\"run_minutes\": \"90\"}, \"why\": \"its stack starts slowly\"}\nPROPOSE {\"agent\": \"qa\", \"set\": {\"model\": \"a\\nb: c\"}, \"why\": \"x\"}"`)
	team.ChatSend("Why does qa stop?", nil)
	team.teamTurn()
	msgs := team.Chat(100)
	n := len(msgs)
	if words := msgs[n-3]; words.From != "team" || strings.Contains(words.Text, "PROPOSE") || !strings.Contains(words.Text, "90 minutes") {
		t.Fatalf("its words, without its proposals: %+v", words)
	}
	if card := msgs[n-2]; card.Change == nil || card.Change.Set["run_minutes"] != "90" {
		t.Fatalf("its proposal is a card: %+v", card)
	}
	if said := msgs[n-1]; said.From != "" || !strings.Contains(said.Text, "cannot apply") {
		t.Fatalf("one rota cannot apply is said: %+v", said)
	}

	// a session that is gone, and any other failure
	write(t, claude, "#!/bin/bash\nprintf '%s\\n' \"$*\" >> "+args+"\nread -r prompt\ncase \"$*\" in *--resume*) echo 'No conversation found with session ID: team-1' >&2; exit 1;; esac\n"+
		`echo '{"type":"system","subtype":"init","session_id":"team-2"}'`+"\n"+
		`echo '{"type":"result","subtype":"success","is_error":false,"result":"Here again.","num_turns":1,"session_id":"team-2","usage":{}}'`+"\n")
	team.ChatSend("Hello?", nil)
	team.teamTurn()
	if m := lastChat(t, team); m.Text != "Here again." || team.teamState().Session != "team-2" {
		t.Fatalf("a session that is gone is begun anew: %+v %+v", m, team.teamState())
	}
	write(t, claude, "#!/bin/bash\nread -r prompt\necho 'overloaded' >&2\nexit 1\n")
	team.ChatSend("Still there?", nil)
	team.teamTurn()
	if m := lastChat(t, team); m.From != "" || !strings.Contains(m.Text, "The Team could not answer") || team.teamState().Session != "team-2" {
		t.Fatalf("a failure is said, and the session kept: %+v %+v", m, team.teamState())
	}
}

// What a Claude Code session that started the organizer puts in its environment does not reach
// the claudes rota starts: not its id, nor its messaging socket and token. Claude's own settings,
// such as the provider, do.
func TestAgentsDoNotGetTheSessionThatStartedTheOrganizer(t *testing.T) {
	env := cleanEnv([]string{"PATH=/bin", "CLAUDECODE=1", "CLAUDE_CODE_SESSION_ID=s", "CLAUDE_CODE_MESSAGING_SOCKET=/tmp/x", "CLAUDE_CODE_MESSAGING_TOKEN=t",
		"CLAUDE_PID=9", "AI_AGENT=claude", "CLAUDE_CODE_OAUTH_TOKEN=keep", "CLAUDE_CODE_USE_FOUNDRY=1", "HOME=/Users/x"})
	if strings.Join(env, " ") != "PATH=/bin CLAUDE_CODE_OAUTH_TOKEN=keep CLAUDE_CODE_USE_FOUNDRY=1 HOME=/Users/x" {
		t.Fatalf("kept: %v", env)
	}
}

// Undo writes back only over what the change left: a setting written again since, an empty one
// put back, an agent resumed by hand, a new agent that began work, all stay. A change the chat
// applies keeps the rules a page does: no owned hours over another's.
func TestUndoLeavesWhatChangedSince(t *testing.T) {
	root, team := chatTeam(t)
	id, _ := team.ChatPropose(ChatChange{Agent: "dev", Set: map[string]string{"hours": ""}, Why: "the team's hours"})
	if _, err := team.ChatApply(id); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(team.AgentFile("dev")); strings.Contains(string(b), "hours") {
		t.Fatalf("the key is gone: %s", b)
	}
	write(t, team.AgentFile("dev"), "---\nenabled: true\nhours:\n---\ndev codes\n") // put back by hand
	if _, err := team.ChatUndo(id); err == nil {
		t.Fatal("undo overwrote an empty setting put back since")
	}
	id, _ = team.ChatPropose(ChatChange{Agent: "qa", Do: "pause", Why: "x"})
	team.ChatApply(id)
	team.ResumeAgent("qa") // the human resumed it since
	if _, err := team.ChatUndo(id); err == nil {
		t.Fatal("undo overwrote a resume since")
	}
	id, _ = team.ChatPropose(ChatChange{New: true, Agent: "links", Role: "Checks links.", Why: "x"})
	team.ChatApply(id)
	os.MkdirAll(filepath.Join(root, workDir, "links"), 0o755) // its first shift began
	if _, err := open(t, root).ChatUndo(id); err == nil || open(t, root).Agent("links") == nil {
		t.Fatalf("undo removed an agent that began work: %v", err)
	}
	write(t, filepath.Join(root, "agents", "early.md"), "---\nenabled: true\nhours: 05:00-07:00\nown_hours: true\n---\nearly\n")
	team = open(t, root)
	id, err := team.ChatPropose(ChatChange{Agent: "qa", Set: map[string]string{"hours": "06:00-08:00", "own_hours": "true"}, Why: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := team.ChatApply(id); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("owned hours over another's: %v", err)
	}
}

// A question the Team did not answer, as when rota stopped while it answered, waits; reading the
// chat answers it. One answered, or one it failed to answer, waits no more.
func TestAQuestionTheTeamDidNotAnswerWaits(t *testing.T) {
	_, team := chatTeam(t)
	if team.TeamWaits() {
		t.Fatal("nothing asked, nothing waits")
	}
	team.ChatSend("What is everyone doing?", nil)
	if !team.TeamWaits() {
		t.Fatal("unanswered, it waits")
	}
	pending := team.teamPending()
	team.chatAppend(ChatMsg{From: "team", Text: "All idle.", Answer: pending[0].ID})
	if team.TeamWaits() {
		t.Fatal("answered, it waits no more")
	}
	team.ChatSend("@dev hi", nil) // to an agent: not the Team's
	if team.TeamWaits() {
		t.Fatal("a message to an agent waits for the Team")
	}
}

// The Team does what the human can do in the app, as cards: it answers what an agent asked, a
// choice by its letter too, or dismisses it, and ticks a to-do's steps; it writes an agent, and
// takes a message back; it approves a pull request or sends a note on it; it files the mail, sets
// a role and how the inbox works, stops a run and starts the organizer. Each is checked when it is
// proposed, says on its card what it does, and is undone where that can be done; an answer, a run
// stopped or an approval stays.
func TestTheTeamDoesWhatTheHumanCanDo(t *testing.T) {
	root, team := mailTeam(t)
	propose := func(ch ChatChange) (string, ChatChange) {
		t.Helper()
		id, err := team.ChatPropose(ch)
		if err != nil {
			t.Fatalf("%+v: %v", ch, err)
		}
		return id, lastCard(t, team, id)
	}
	apply := func(id, want string) {
		t.Helper()
		if said, err := team.ChatApply(id); err != nil || !strings.Contains(said, want) {
			t.Fatalf("applied: %q %v", said, err)
		}
	}
	undo := func(id, want string) {
		t.Helper()
		if said, err := team.ChatUndo(id); err != nil || !strings.Contains(said, want) {
			t.Fatalf("undone: %q %v", said, err)
		}
	}
	refused := func(chs ...ChatChange) {
		t.Helper()
		for _, ch := range chs {
			if _, err := team.ChatPropose(ch); err == nil {
				t.Errorf("proposed: %+v", ch)
			}
		}
	}

	// an answer: the choice its letter names, word for word
	qid, err := team.AskWith(Question{Agent: "developer", Text: "Share-link games read each other's logins. Which way?", Subject: "Shared logins",
		Options: []string{"A: keep each game's login in its own tab", "B: accept it as a known risk"}, Recommend: "A: keep each game's login in its own tab", About: "#7"})
	if err != nil {
		t.Fatal(err)
	}
	id, card := propose(ChatChange{Do: "answer", Thread: "q:" + qid, Text: "b", Why: "you said so"})
	if card.Text != "B: accept it as a known risk" || card.Agent != "developer" || card.Title != "developer · shop-creator#7 · Shared logins" || card.Say == "" || !card.Once {
		t.Fatalf("the answer's card: %+v", card)
	}
	if b, _ := os.ReadFile(filepath.Join(team.Path(questionsDir), qid+".json")); len(b) == 0 {
		t.Fatal("proposed, it is answered already")
	}
	apply(id, "Answered developer")
	if notes := inboxText(team, "developer"); !strings.Contains(notes, "B: accept it as a known risk") {
		t.Fatalf("the developer gets the answer: %s", notes)
	}
	if th := threadOf(t, team.Mail(time.Now()), "q:"+qid); th.Box != BoxDone || th.Unread {
		t.Fatalf("answered and read, as from the mail: %+v", th)
	}
	if _, err := team.ChatUndo(id); err == nil {
		t.Fatal("an answer is taken back")
	}
	refused(ChatChange{Do: "answer", Thread: "q:" + qid, Text: "A"}, // answered already
		ChatChange{Do: "answer", Thread: "q:nothing", Text: "yes"},
		ChatChange{Do: "answer", Thread: qid, Text: "yes"},
		ChatChange{Do: "explode", Thread: "q:" + qid},
		ChatChange{Do: "answer", Thread: "q:" + qid, Text: "x", Set: map[string]string{"model": "opus"}})
	if got := choice([]string{"A: one", "B: two"}, "c"); got != "c" {
		t.Fatalf("an answer no choice names is kept: %q", got)
	}

	// dismissed, unanswered
	drop := ""
	for _, q := range team.Questions() {
		if strings.Contains(q.Text, "Drop the share code?") {
			drop = q.ID
		}
	}
	refused(ChatChange{Do: "answer", Thread: "q:" + drop})
	id, _ = propose(ChatChange{Do: "dismiss", Thread: "q:" + drop})
	apply(id, "dismissed")
	if len(team.Questions()) != 0 {
		t.Fatalf("still open: %+v", team.Questions())
	}

	// a to-do's step, ticked and back
	todo, _ := team.AskWith(Question{Agent: "developer", Text: "Add the record, then restart.", Steps: []string{"Add the DNS record", "Restart the tunnel"}})
	refused(ChatChange{Do: "tick", Thread: "q:" + todo, Step: 3}, ChatChange{Do: "tick", Thread: "q:" + todo})
	id, card = propose(ChatChange{Do: "tick", Thread: "q:" + todo, Step: 2})
	if card.Say != "Tick step 2" || len(card.Lines) != 1 || card.Lines[0] != "Restart the tunnel" {
		t.Fatalf("the tick's card: %+v", card)
	}
	apply(id, "Ticked step 2")
	if th := threadOf(t, team.Mail(time.Now()), "q:"+todo); len(th.Ticks) != 1 || th.Ticks[0] != 1 {
		t.Fatalf("ticked: %v", th.Ticks)
	}
	undo(id, "step 2 is not ticked")
	if th := threadOf(t, team.Mail(time.Now()), "q:"+todo); len(th.Ticks) != 0 {
		t.Fatalf("not ticked again: %v", th.Ticks)
	}
	id, _ = propose(ChatChange{Do: "tick", Thread: "q:" + todo, Step: 1})
	apply(id, "Ticked step 1")
	team.MarkMail("q:"+todo, "tick", "0:off", time.Now()) // the human unticks it
	if _, err := team.ChatUndo(id); err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Fatalf("undo over the human's own tick: %v", err)
	}
	id, _ = propose(ChatChange{Do: "tick", Thread: "q:" + todo, Step: 2})
	team.Answer(todo, "Done: all the steps are done.")
	if _, err := team.ChatApply(id); err == nil {
		t.Fatal("a step of a to-do that is done is ticked")
	}

	// a message to an agent, taken back while it has not read it
	refused(ChatChange{Do: "message", Agent: "nobody", Text: "hi"}, ChatChange{Do: "message", Agent: "developer"},
		ChatChange{Do: "message", Agent: "developer", Text: "hi", About: "o/elsewhere#1"})
	id, card = propose(ChatChange{Do: "message", Agent: "developer", Text: "Go with B: write the risk down.", About: "#7"})
	if card.About != "o/shop-creator#7" || card.Title != "To developer · shop-creator#7" || card.Once {
		t.Fatalf("the message's card: %+v", card)
	}
	apply(id, "Sent to developer")
	ths := team.MessageThreads()
	if len(ths) != 1 || ths[0].To != "developer" || ths[0].Said[0].Text != "Go with B: write the risk down." || ths[0].About != "o/shop-creator#7" {
		t.Fatalf("sent: %+v", ths)
	}
	undo(id, "Taken back")
	if m := team.MessageThreads()[0]; m.Gone.IsZero() || strings.Contains(inboxText(team, "developer"), "write the risk down") {
		t.Fatalf("taken back: %+v", m)
	}

	// one sent from the mail, taken back by the Team; once read, it stays
	thread, _ := team.SendMessage("triage", "", "Order the reviews by risk.", "")
	id, card = propose(ChatChange{Do: "unsend", Thread: "m:" + thread})
	if card.Agent != "triage" || !card.Once || card.Lines[0] != "Order the reviews by risk." {
		t.Fatalf("the unsend's card: %+v", card)
	}
	apply(id, "Taken back")
	refused(ChatChange{Do: "unsend", Thread: "m:" + thread}, ChatChange{Do: "unsend", Thread: "m:20990101-000000-abcdef"})
	thread, _ = team.SendMessage("triage", "", "First.", "")
	id, card = propose(ChatChange{Do: "unsend", Thread: "m:" + thread})
	team.SendMessage("triage", "", "Second.", thread) // the human writes again meanwhile
	if _, err := team.ChatApply(id); err == nil || !strings.Contains(inboxText(team, "triage"), "Second.") {
		t.Fatalf("a card took back a message it did not show: %v", err)
	}

	// a pull request approved, or a note sent on it, as the human on GitHub
	fakeGh(t, `case "$1 $2" in "pr view") echo '{"headRefName":"developer/one","labels":[]}';; "pr edit"|"pr ready") ;; *) exit 1;; esac # GitHub lists nothing new`)
	f := &fakeReviewer{}
	old := reviewer
	reviewer = func() (reviewing, error) { return f, nil }
	t.Cleanup(func() { reviewer = old })
	refused(ChatChange{Do: "approve", Thread: "pr:o/shop-creator#3"}, // approved already
		ChatChange{Do: "approve", Thread: "pr:o/elsewhere#1"},
		ChatChange{Do: "approve", Thread: "o/shop-creator#1"},
		ChatChange{Do: "review-note", Thread: "pr:o/shop-creator#1"})
	id, card = propose(ChatChange{Do: "approve", Thread: "pr:o/shop-creator#1", Text: "LGTM, ship it"})
	if card.Title != "shop-creator#1 · Ready one" || card.Agent != "developer" || !card.Once || card.Text != "" || len(card.Lines) != 1 || !strings.Contains(card.Lines[0], "drafts") {
		t.Fatalf("the approval's card: %+v", card)
	}
	apply(id, "Approved o/shop-creator#1")
	if strings.Join(f.calls, ",") != "load o/shop-creator,approve" {
		t.Fatalf("approved as from the mail: %v", f.calls)
	}
	f.calls = nil
	id, _ = propose(ChatChange{Do: "review-note", Thread: "pr:o/shop-creator#2", Text: "Rebase it, please."})
	apply(id, "Sent your note")
	if !slices.Contains(f.calls, "draft Rebase it, please.") || !slices.Contains(f.calls, "send") {
		t.Fatalf("the note is sent: %v", f.calls)
	}

	// the mail filed, and back as it was
	if _, err := team.ask("developer", "Keep the old logs?", time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	m := team.Mail(time.Now())
	var ask, report string // the open question and the day's report, which the human files as any thread
	for _, th := range m.Threads {
		switch {
		case th.Subject == "Keep the old logs?":
			ask = th.ID
		case th.Kind == "digest":
			report = th.ID
		}
	}
	refused(ChatChange{Do: "archive"}, ChatChange{Do: "archive", Threads: []string{"u:nothing"}},
		ChatChange{Do: "snooze", Threads: []string{ask}, Until: time.Now().Add(-time.Hour).Format(time.RFC3339)},
		ChatChange{Do: "snooze", Threads: []string{ask}, Until: "tomorrow"})
	id, card = propose(ChatChange{Do: "archive", Threads: []string{ask, report, ask}})
	if card.Title != "2 threads" || len(card.Lines) != 2 || len(card.Threads) != 2 {
		t.Fatalf("the archive's card: %+v", card)
	}
	apply(id, "Archived: 2 threads")
	if m := team.Mail(time.Now()); threadOf(t, m, ask).Box != BoxDone || threadOf(t, m, report).Box != BoxDone {
		t.Fatal("archived")
	}
	undo(id, "as it was")
	if m := team.Mail(time.Now()); threadOf(t, m, ask).Box != BoxInbox || threadOf(t, m, report).Box != BoxUpdates {
		t.Fatal("back where they were")
	}
	until := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	id, card = propose(ChatChange{Do: "snooze", Thread: ask, Until: until.Format(time.RFC3339)})
	if !strings.HasPrefix(card.Say, "Snooze until ") || card.Title != threadWords(threadOf(t, m, ask)) {
		t.Fatalf("the snooze's card: %+v", card)
	}
	apply(id, "Snoozed")
	if th := threadOf(t, team.Mail(time.Now()), ask); !th.Snooze.Equal(until) {
		t.Fatalf("snoozed: %v", th.Snooze)
	}
	undo(id, "as it was")
	if th := threadOf(t, team.Mail(time.Now()), ask); !th.Snooze.IsZero() {
		t.Fatalf("not snoozed: %v", th.Snooze)
	}
	t0 := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	team.MarkMail(report, "archive", "", t0)
	id, _ = propose(ChatChange{Do: "unarchive", Threads: []string{report}})
	apply(id, "Moved back to the inbox")
	undo(id, "as it was")
	if s, _, _ := team.readMail(); !s.Marks[report].Done.Equal(t0) {
		t.Fatalf("archived again since when it was, not now: %v", s.Marks[report].Done)
	}
	id, _ = propose(ChatChange{Do: "snooze", Threads: []string{ask}, Until: until.Format(time.RFC3339)})
	apply(id, "Snoozed")
	later := until.Add(time.Hour)
	team.MarkMail(ask, "snooze", later.Format(time.RFC3339), time.Now()) // the human snoozes it longer
	undo(id, "as it was")
	if th := threadOf(t, team.Mail(time.Now()), ask); !th.Snooze.Equal(later) {
		t.Fatalf("undo took back the human's own snooze: %v", th.Snooze)
	}
	team.MarkMail(ask, "unsnooze", "", time.Now())
	id, _ = propose(ChatChange{Do: "star", Threads: []string{ask}})
	apply(id, "Starred")
	id, _ = propose(ChatChange{Do: "other", Threads: []string{ask}})
	apply(id, "Moved to Other")
	undo(id, "as it was")
	if th := threadOf(t, team.Mail(time.Now()), ask); !th.Star || th.Place != "" {
		t.Fatalf("starred, and not moved: %+v", th)
	}
	s0, _, _ := team.readMail()
	before := s0.Marks[ask]
	read, _ := propose(ChatChange{Do: "read", Threads: []string{ask}})
	apply(read, "Marked read")
	unread, _ := propose(ChatChange{Do: "unread", Threads: []string{ask}})
	apply(unread, "Marked unread")
	undo(read, "as it was") // out of order: each puts back its own
	undo(unread, "as it was")
	if s, _, _ := team.readMail(); !s.Marks[ask].Read.Equal(before.Read) || s.Marks[ask].Unread != before.Unread {
		t.Fatalf("read and unread undone: %+v, before %+v", s.Marks[ask], before)
	}

	// a role, and back unless it changed since
	refused(ChatChange{Do: "role", Agent: "triage"}, ChatChange{Do: "role", Agent: "nobody", Role: "x"})
	id, _ = propose(ChatChange{Do: "role", Agent: "triage", Role: "Orders reviews by risk.\n\nAsks before it labels."})
	apply(id, "new role")
	if a := open(t, root).Agent("triage"); a.Role != "Orders reviews by risk.\n\nAsks before it labels." || a.Repos[1] != "o/shop-backend" {
		t.Fatalf("the new role, its settings kept: %+v", a)
	}
	undo(id, "as it was")
	if a := open(t, root).Agent("triage"); a.Role != "orders reviews" {
		t.Fatalf("the role as it was: %q", a.Role)
	}
	id, _ = propose(ChatChange{Do: "role", Agent: "triage", Role: "Orders reviews."})
	apply(id, "new role")
	team.SetRole("triage", "Changed by hand.")
	if _, err := team.ChatUndo(id); err == nil {
		t.Fatal("undo overwrote a role changed since")
	}

	// how the inbox works
	refused(ChatChange{Do: "inbox", Set: map[string]string{"notify": "loud"}}, ChatChange{Do: "inbox", Set: map[string]string{"color": "red"}},
		ChatChange{Do: "inbox"})
	id, card = propose(ChatChange{Do: "inbox", Set: map[string]string{"notify": "all", "focus": "08"}})
	if card.Was["notify"] != "focused" || card.Set["focus"] != "8" || card.Title != "Your inbox · focus, notify" {
		t.Fatalf("the inbox's card: %+v", card)
	}
	apply(id, "your inbox · focus 5 → 8, notify focused → all")
	if p := team.mailPrefs(); p.Notify != "all" || p.Focus != 8 || !p.Split {
		t.Fatalf("applied: %+v", p)
	}
	undo(id, "Undone")
	if p := team.mailPrefs(); p != defaultMailPrefs {
		t.Fatalf("as it was: %+v", p)
	}
	id, _ = propose(ChatChange{Do: "inbox", Set: map[string]string{"notify": "all"}})
	apply(id, "notify focused → all")
	p := team.mailPrefs()
	p.Focus = 9
	team.SetMailPrefs(p) // the human, meanwhile
	undo(id, "Undone")
	if p := team.mailPrefs(); p.Notify != "focused" || p.Focus != 9 {
		t.Fatalf("undo puts back only what the card set: %+v", p)
	}

	// a run stopped, and the organizer started, with nothing to take back
	refused(ChatChange{Do: "cancel"}, ChatChange{Do: "cancel", Agent: "nobody"})
	id, _ = propose(ChatChange{Do: "cancel", Agent: "developer"})
	if _, err := team.ChatApply(id); err == nil || !strings.Contains(err.Error(), "no shift is running") {
		t.Fatalf("no run to stop: %v", err)
	}
	started := filepath.Join(root, "started")
	exe := filepath.Join(t.TempDir(), "rota")
	write(t, exe, "#!/bin/sh\necho \"$@\" > "+started+"\n")
	os.Chmod(exe, 0o755)
	oldExe := selfExe
	selfExe = func() (string, error) { return exe, nil }
	t.Cleanup(func() { selfExe = oldExe })
	id, card = propose(ChatChange{Do: "start"})
	if !card.Once {
		t.Fatalf("the start's card: %+v", card)
	}
	apply(id, "starting")
	waitFor(t, "the organizer to start", func() bool { b, _ := os.ReadFile(started); return strings.HasPrefix(string(b), "start -root "+root) })
	if _, err := team.ChatUndo(id); err == nil {
		t.Fatal("a start is taken back")
	}
}

// A card that waits, as on GitHub, holds up other cards, never the chat: the human and the agents
// still write in it.
func TestTheChatGoesOnWhileACardWaits(t *testing.T) {
	_, team := chatTeam(t)
	held, release := make(chan struct{}), make(chan struct{})
	go team.cardsLocked(func() error { close(held); <-release; return nil })
	<-held
	defer close(release)
	done := make(chan error)
	go func() { _, err := team.ChatSend("Still there?", nil); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the chat waits on a card")
	}
}

// The Team reads the human's mail with each question, each thread by the id it proposes with:
// what an agent asked, with its choices and the one it recommends, a to-do's steps and those
// ticked, a pull request that waits for a review, a message not read yet, and the latest news.
func TestTheTeamReadsTheMailByItsIDs(t *testing.T) {
	_, team := mailTeam(t)
	qid, _ := team.AskWith(Question{Agent: "developer", Text: "Which way?", Subject: "Shared logins", Options: []string{"A: tabs", "B: accept"}, Recommend: "A: tabs"})
	todo, _ := team.AskWith(Question{Agent: "developer", Text: "Two steps.", Steps: []string{"Add the record", "Restart it"}})
	team.MarkMail("q:"+todo, "tick", "0:on", time.Now())
	thread, _ := team.SendMessage("triage", "", "Order them by risk.", "")
	snap := team.teamSnapshot()
	for _, want := range []string{
		"- q:" + qid + ": developer asks: Shared logins. It says: Which way? Its choices: \"A: tabs\" (it recommends this), \"B: accept\"",
		"- q:" + todo + ": a to-do from developer: ", "Its steps: 1. Add the record (ticked); 2. Restart it",
		"developer asks, about shop-creator#7: Drop the share code?",
		"- pr:o/shop-creator#1: shop-creator#1, a pull request by developer, waits for the human's review: Ready one",
		"- pr:o/shop-creator#2: shop-creator#2, a pull request by developer, waits on the answer to the human's notes",
		"- m:" + thread + ": the human's message to triage, not read yet: Order them by risk.",
		"The latest news for the human:\n- ", "the daily report: Your team today",
		time.Now().Format("2006"),
	} {
		if !strings.Contains(snap, want) {
			t.Errorf("the Team does not read %q in:\n%s", want, snap)
		}
	}
	if b := team.teamBrief(); !strings.Contains(b, `"do": "answer"`) || !strings.Contains(b, "Never say you cannot") || !strings.Contains(b, "developer, triage") {
		t.Fatalf("its brief: %s", b)
	}
}

// The Team changes what every agent is told, the brief, or a passage of one agent's role: the
// passage word for word and once, the brief's placeholders kept. Undo puts it back unless it
// changed since. A team without a brief of its own gets one from the built-in brief.
func TestTheTeamEditsTheBriefAndARoleByThePassage(t *testing.T) {
	root, team := chatTeam(t)
	refused := func(chs ...ChatChange) {
		t.Helper()
		for _, ch := range chs {
			if _, err := team.ChatPropose(ch); err == nil {
				t.Errorf("proposed: %+v", ch)
			}
		}
	}
	passage := `Write "none" when nothing changed that they need to know.`
	if !strings.Contains(DefaultBrief, `never tell them that nothing needs them`) || strings.Contains(DefaultBrief, passage) {
		t.Fatal("the built-in brief tells agents to write for the human only what needs them")
	}
	write(t, filepath.Join(root, briefFile), "Hello {agent}.\nWrite for the human only what needs them.\nYour questions:\n{questions}\nBye.\n")
	refused(ChatChange{Do: "brief", Replace: "not there", With: "x"},
		ChatChange{Do: "brief", Replace: "e", With: "x"}, // there many times
		ChatChange{Do: "brief", Replace: "Your questions:\n{questions}\n", With: ""},
		ChatChange{Do: "brief", With: "x"})
	id, err := team.ChatPropose(ChatChange{Do: "brief", Replace: "only what needs them.", With: "only what needs them, and never that nothing does.", Why: "noise"})
	if err != nil {
		t.Fatal(err)
	}
	if c := lastCard(t, team, id); c.Replace != "only what needs them." || c.Agent != "" || c.Once || !strings.HasPrefix(c.Title, "The agents' brief") {
		t.Fatalf("the brief's card: %+v", c)
	}
	if said, err := team.ChatApply(id); err != nil || !strings.Contains(said, "next run") {
		t.Fatalf("applied: %q %v", said, err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, briefFile)); !strings.Contains(string(b), "never that nothing does.\nYour questions:\n{questions}") {
		t.Fatalf("the brief: %s", b)
	}
	if _, err := team.ChatUndo(id); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, briefFile)); string(b) != "Hello {agent}.\nWrite for the human only what needs them.\nYour questions:\n{questions}\nBye.\n" {
		t.Fatalf("undone: %s", b)
	}
	team.ChatApply(id) // an undone card applies again
	write(t, filepath.Join(root, briefFile), "Changed by hand.\n")
	if _, err := team.ChatUndo(id); err == nil {
		t.Fatal("undo overwrote a brief changed since")
	}
	os.Remove(filepath.Join(root, briefFile))
	id, err = team.ChatPropose(ChatChange{Do: "brief", Replace: `never tell them that nothing needs them`, With: `never tell them that nothing needs them, nor what they asked for`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := team.ChatApply(id); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, briefFile)); !strings.Contains(string(b), "nor what they asked for") || !strings.Contains(string(b), "{questions}") {
		t.Fatal("the built-in brief, changed, is the team's own now")
	}
	if _, err := team.ChatUndo(id); err != nil || fileExists(filepath.Join(root, briefFile)) {
		t.Fatalf("undone, the team has the built-in brief again, as later versions of rota bring it: %v", err)
	}
	if b := team.teamBrief(); !strings.Contains(b, "The team has no brief.md") || !strings.Contains(b, "{questions}") {
		t.Fatal("without brief.md, the Team reads the built-in brief it may change")
	}
	write(t, filepath.Join(root, briefFile), "Hello {agent}.\n")
	if strings.Contains(team.teamBrief(), "The team has no brief.md") {
		t.Fatal("with brief.md, the Team reads it there")
	}

	// a passage of a role
	refused(ChatChange{Do: "role", Agent: "qa", Replace: "qa tests", With: "x", Role: "both"},
		ChatChange{Do: "role", Agent: "qa", Replace: "not there", With: "x"},
		ChatChange{Do: "role", Agent: "qa", Replace: "qa tests", With: ""})
	id, err = team.ChatPropose(ChatChange{Do: "role", Agent: "qa", Replace: "tests", With: "tests, and says nothing when nothing is wrong"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := team.ChatApply(id); err != nil {
		t.Fatal(err)
	}
	if a := open(t, root).Agent("qa"); a.Role != "qa tests, and says nothing when nothing is wrong" || a.RunMinutes != 45 {
		t.Fatalf("the role, its settings kept: %+v", a)
	}
	if _, err := team.ChatUndo(id); err != nil || open(t, root).Agent("qa").Role != "qa tests" {
		t.Fatalf("undone: %v", err)
	}
}

// A new brief of the Team's, as when rota changed what it may do or the team has a new agent,
// begins a new session: what it said under the old one, such as what it could not do then, is
// not what it answers from.
func TestANewBriefBeginsANewSession(t *testing.T) {
	root, _ := chatTeam(t)
	args, claude := filepath.Join(root, "args"), filepath.Join(root, "claude")
	write(t, claude, "#!/bin/bash\nprintf '%s\\n=====\\n' \"$*\" >> "+args+"\nread -r prompt\n"+
		`echo '{"type":"system","subtype":"init","session_id":"team-1"}'`+"\n"+
		`echo '{"type":"result","subtype":"success","is_error":false,"result":"Here.","num_turns":1,"session_id":"team-1","usage":{}}'`+"\n")
	os.Chmod(claude, 0o755)
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\nclaude_bin: "+claude+"\n")
	ask := func(q string) string {
		t.Helper()
		team := open(t, root)
		team.ChatSend(q, nil)
		team.teamTurn()
		b, _ := os.ReadFile(args)
		turns := strings.Split(strings.TrimSuffix(string(b), "=====\n"), "=====\n")
		return turns[len(turns)-1]
	}
	if a := ask("One?"); strings.Contains(a, "--resume") {
		t.Fatalf("its first turn: %s", a)
	}
	if a := ask("Two?"); !strings.Contains(a, "--resume team-1") {
		t.Fatalf("the same brief resumes its session: %s", a)
	}
	write(t, filepath.Join(root, "agents", "links.md"), "---\nenabled: true\n---\nchecks links\n") // its brief names the agents
	if a := ask("Three?"); strings.Contains(a, "--resume") {
		t.Fatalf("a new brief resumed the old session: %s", a)
	}
	if a := ask("Four?"); !strings.Contains(a, "--resume team-1") {
		t.Fatalf("then it resumes the new one: %s", a)
	}
}

// The Team reaches as far as rota's scope: its agents, and their work on GitHub in the team's
// repositories, nothing else. The human merges what they choose, also one held back for its
// rollout or what it depends on, its labels left on; closes, reopens, labels and comments on a pull
// request or an issue; and opens an issue. Each is a card that says what it does; what can be is
// taken back.
func TestTheTeamWorksOnTheTeamsGitHubOnly(t *testing.T) {
	root, _ := mailTeam(t)
	conf := filepath.Join(root, "rota.conf")
	b, _ := os.ReadFile(conf)
	write(t, conf, string(b)+"hold_label: rota:needs-rollout\n")
	var l openList
	b, _ = os.ReadFile(filepath.Join(root, openFile))
	json.Unmarshal(b, &l)
	one := l.Repos["o/shop-creator"][1]
	one.Deps = []string{"o/shop-creator#9"}
	l.Repos["o/shop-creator"][1] = one
	b, _ = json.Marshal(l)
	write(t, filepath.Join(root, openFile), string(b))
	team := open(t, root)

	gh := t.TempDir()
	fakeGh(t, `exec python3 - "$@" <<'PY'
import json, os, sys, time
d = "`+gh+`"
a = sys.argv[1:]
open(d + "/log", "a").write(" ".join(a) + "\n")
def path(n): return os.path.join(d, "pr-" + str(n) + ".json")
def pr(n): return json.load(open(path(n))) if os.path.exists(path(n)) else None
def save(n, v): json.dump(v, open(path(n), "w"))
if a[:2] == ["pr", "view"]:
    v = pr(a[2])
    if v is None: sys.stderr.write("no pull requests found\n"); sys.exit(1)
    print(json.dumps(v))
elif a[:2] == ["issue", "view"]:
    p = os.path.join(d, "issue-" + a[2] + ".json")
    if not os.path.exists(p): sys.stderr.write("not found\n"); sys.exit(1)
    print(open(p).read())
elif a[:2] == ["pr", "list"] and ("--head" in a or "--base" in a):
    k = "--head" if "--head" in a else "--base"
    out = []
    for f in sorted(os.listdir(d)):
        if f.startswith("pr-"):
            v = json.load(open(os.path.join(d, f)))
            if v["state"] == "OPEN" and v["headRefName" if k == "--head" else "baseRefName"] == a[a.index(k) + 1]:
                out.append({"number": v["number"], "headRefName": v["headRefName"], "headRefOid": v["headRefOid"], "isCrossRepository": False, "labels": v["labels"]})
    print(json.dumps(out))
elif a[:2] == ["pr", "edit"] and "--base" in a:
    v = pr(a[2]); v["baseRefName"] = a[a.index("--base") + 1]; save(a[2], v)
elif a[:2] == ["pr", "merge"]:
    if os.path.exists(d + "/fail"):
        sys.stderr.write("GraphQL: Required status check \"ci\" is failing.\n"); sys.exit(1)
    if os.path.exists(d + "/modified"):
        os.remove(d + "/modified")
        if os.path.exists(d + "/elsewhere"):
            v = pr(a[2]); v["baseRefName"] = "developer/elsewhere"; save(a[2], v)
        sys.stderr.write("GraphQL: Head branch was modified. Review and try the merge again.\n"); sys.exit(1)
    if os.path.exists(d + "/slow"): time.sleep(1)
    v = pr(a[2]); v["state"] = "MERGED"; v["mergeCommit"] = {"oid": "m" + a[2]}; save(a[2], v)
elif a[:2] == ["issue", "create"]:
    print("https://github.com/o/shop-creator/issues/12")
elif a[0] == "api" and "/labels/" in a[1]:
    if not a[1].lower().endswith("/labels/bug"): sys.stderr.write("HTTP 404\n"); sys.exit(1)
    print('{"name": "bug"}')
elif a[:2] in (["pr", "list"], ["issue", "list"]):
    sys.exit(1)  # the open list stays as the test made it
PY`)
	set := func(n int, v string) { write(t, filepath.Join(gh, "pr-"+strconv.Itoa(n)+".json"), v) }
	log := func() string { b, _ := os.ReadFile(filepath.Join(gh, "log")); return string(b) }
	set(1, `{"number":1,"state":"OPEN","isDraft":false,"baseRefName":"main","headRefName":"developer/one","headRefOid":"1111111aa","labels":[{"name":"rota:needs-rollout"},{"name":"high"}],"body":"Depends on #9"}`)
	set(6, `{"number":6,"state":"OPEN","isDraft":true,"baseRefName":"developer/one","headRefName":"developer/six","headRefOid":"6666666aa","labels":[],"body":""}`)
	set(9, `{"number":9,"state":"MERGED","title":"Play host","labels":[{"name":"rota:needs-rollout"}]}`)
	propose := func(ch ChatChange) (string, ChatChange) {
		t.Helper()
		id, err := team.ChatPropose(ch)
		if err != nil {
			t.Fatalf("%+v: %v", ch, err)
		}
		return id, lastCard(t, team, id)
	}
	apply := func(id, want string) {
		t.Helper()
		if said, err := team.ChatApply(id); err != nil || !strings.Contains(said, want) {
			t.Fatalf("applied: %q %v", said, err)
		}
	}
	undo := func(id, want string) {
		t.Helper()
		if said, err := team.ChatUndo(id); err != nil || !strings.Contains(said, want) {
			t.Fatalf("undone: %q %v", said, err)
		}
	}
	refused := func(why string, chs ...ChatChange) {
		t.Helper()
		for _, ch := range chs {
			if _, err := team.ChatPropose(ch); err == nil || !strings.Contains(err.Error(), why) {
				t.Errorf("%+v: %v, not %q", ch, err, why)
			}
		}
	}

	// what is not the team's is out of reach
	refused("not a pull request the team has open", ChatChange{Do: "merge", Threads: []string{"pr:o/elsewhere#1"}})
	refused("no repository of the team", ChatChange{Do: "close", About: "elsewhere/x#1"}, ChatChange{Do: "label", About: "x/y#2", Labels: []string{"high"}},
		ChatChange{Do: "issue", Repo: "other/repo", Subject: "Hi"})
	refused("names no repository", ChatChange{Do: "comment", About: "#7", Text: "Hi"})
	if brief := team.teamBrief(); !strings.Contains(brief, "(o/shop-creator, o/shop-backend)") || !strings.Contains(brief, "Outside that scope you do nothing") {
		t.Fatalf("the Team is told its scope:\n%s", brief)
	}
	for kind := range chatKinds {
		if _, mark := markActs[kind]; !mark && kind != "set" && kind != "new" && !strings.Contains(teamBriefText, `"`+kind+`"`) {
			t.Errorf("the Team is not told how to propose %s", kind)
		}
	}

	// merged in the order they stack, though one waits for its rollout and what it depends on: a
	// tap starts it, and its card says how far it got, then what came of each
	merge := func(id string) ChatChange {
		t.Helper()
		if said, err := team.ChatApply(id); err != nil || said != "" {
			t.Fatalf("a merge is started: %q %v", said, err)
		}
		for i := 0; ; i++ {
			if card := lastCard(t, team, id); card.State != "working" {
				return card
			}
			if i == 200 {
				t.Fatalf("the merge did not end: %+v", lastCard(t, team, id))
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	_, card := propose(ChatChange{Do: "merge", Thread: "pr:o/shop-creator#6"})
	if len(card.Lines) != 1 || card.Lines[0] != "shop-creator#6 is stacked on #1, which this does not merge: it merges into #1's branch, developer/one, not into the base of #1." {
		t.Fatalf("a merge of one stacked on another says where it goes: %+v", card)
	}
	id, card := propose(ChatChange{Do: "merge", Threads: []string{"pr:o/shop-creator#6", "pr:o/shop-creator#1", "pr:o/shop-creator#6"}})
	if strings.Join(card.Threads, " ") != "pr:o/shop-creator#1 pr:o/shop-creator#6" || card.Title != "shop-creator#1, shop-creator#6" || !card.Once ||
		len(card.Lines) != 2 || !strings.Contains(card.Lines[0], "shop-creator#1 waits for its rollout: it merges all the same, and rota:needs-rollout stays on it") ||
		card.Lines[1] != "shop-creator#1 depends on #9, which carries rota:needs-rollout: it merges all the same." {
		t.Fatalf("the merge's card: %+v", card)
	}
	card = merge(id)
	if card.State != "applied" || len(card.Done) != 2 || !strings.HasPrefix(card.Done[0], "Merged shop-creator#1 into main; not rebased, so its base still has to be merged into each: #6 (") ||
		card.Done[1] != "Merged shop-creator#6 into main." {
		t.Fatalf("both are merged, the card says: %+v", card)
	}
	got := log()
	edit, first, ready, second := strings.Index(got, "pr edit 6 -R o/shop-creator --base main"), strings.Index(got, "pr merge 1 -R o/shop-creator --squash --match-head-commit 1111111aa"),
		strings.Index(got, "pr ready 6 -R o/shop-creator"), strings.Index(got, "pr merge 6 -R o/shop-creator --squash --match-head-commit 6666666aa")
	if edit < 0 || first < edit || ready < first || second < ready {
		t.Fatalf("the one stacked on it goes onto main, then each is merged, the draft made ready first:\n%s", got)
	}
	if strings.Contains(got, "-label") || strings.Contains(got, "compare") {
		t.Fatalf("a merge the human asked for leaves the labels and does not wait for the base:\n%s", got)
	}
	if _, err := team.ChatUndo(id); err == nil {
		t.Fatal("a merge is taken back")
	}
	if _, err := team.ChatApply(id); err == nil {
		t.Fatal("a merge is done twice")
	}

	// while it works, the card says which it merges; one that cannot merge says why, and its card
	// can be tried again
	set(3, `{"number":3,"state":"OPEN","baseRefName":"main","headRefName":"developer/three","headRefOid":"3333333aa","labels":[],"body":""}`)
	write(t, filepath.Join(gh, "slow"), "")
	id, card = propose(ChatChange{Do: "merge", Thread: "pr:o/shop-creator#3"})
	if card.Title != "shop-creator#3 · Approved one" || card.Say != "Squash-merge it on GitHub" {
		t.Fatalf("one merge's card: %+v", card)
	}
	team.ChatApply(id)
	for i := 0; ; i++ {
		if card = lastCard(t, team, id); card.State == "working" && strings.Join(card.Done, "|") == "Merging shop-creator#3…" {
			break
		}
		if i == 200 {
			t.Fatalf("the card says what it merges meanwhile: %+v", card)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := team.ChatApply(id); err == nil || !strings.Contains(err.Error(), "under way") {
		t.Fatalf("a tap again while it works: %v", err)
	}
	if err := team.ChatCancel(id); err == nil {
		t.Fatal("a merge under way is cancelled")
	}
	for i := 0; lastCard(t, team, id).State == "working"; i++ {
		if i == 200 {
			t.Fatal("the merge did not end")
		}
		time.Sleep(25 * time.Millisecond)
	}
	if card = lastCard(t, team, id); card.State != "applied" || strings.Join(card.Done, "|") != "Merged shop-creator#3 into main." {
		t.Fatalf("then what came of it: %+v", card)
	}
	os.Remove(filepath.Join(gh, "slow"))
	set(2, `{"number":2,"state":"CLOSED","baseRefName":"main","headRefName":"developer/two","headRefOid":"2222222aa","labels":[],"body":""}`)
	id, _ = propose(ChatChange{Do: "merge", Thread: "pr:o/shop-creator#2"})
	if card = merge(id); card.State != "failed" || strings.Join(card.Done, "|") != "shop-creator#2 is not merged: it is closed." {
		t.Fatalf("a merge that did not happen: %+v", card)
	}
	set(2, `{"number":2,"state":"OPEN","baseRefName":"main","headRefName":"developer/two","headRefOid":"2222222aa","labels":[],"body":""}`)
	if card = merge(id); card.State != "applied" || strings.Join(card.Done, "|") != "Merged shop-creator#2 into main." {
		t.Fatalf("tried again, it merges: %+v", card)
	}
	pause := mergePause
	mergePause = 0
	t.Cleanup(func() { mergePause = pause })
	set(4, `{"number":4,"state":"OPEN","baseRefName":"main","headRefName":"developer/four","headRefOid":"4444444aa","labels":[],"body":""}`)
	write(t, filepath.Join(gh, "modified"), "")
	id, _ = propose(ChatChange{Do: "merge", Thread: "pr:o/shop-creator#4"})
	if card = merge(id); card.State != "applied" {
		t.Fatalf("a merge GitHub was not ready for: %+v", card)
	}
	if n := strings.Count(log(), "pr merge 4 "); n != 2 {
		t.Fatalf("a merge GitHub was not ready for is tried again: %d tries\n%s", n, log())
	}
	// one that does not merge leaves what is stacked on it on its branch, and that one waits;
	// another merges all the same
	set(1, `{"number":1,"state":"OPEN","isDraft":false,"baseRefName":"main","headRefName":"developer/one","headRefOid":"1111111aa","labels":[],"body":""}`)
	set(6, `{"number":6,"state":"OPEN","isDraft":false,"baseRefName":"developer/one","headRefName":"developer/six","headRefOid":"6666666aa","labels":[],"body":""}`)
	set(5, `{"number":5,"state":"OPEN","baseRefName":"main","headRefName":"developer/five","headRefOid":"5555555aa","labels":[],"body":""}`)
	write(t, filepath.Join(gh, "fail"), "")
	id, _ = propose(ChatChange{Do: "merge", Threads: []string{"pr:o/shop-creator#1", "pr:o/shop-creator#6"}})
	before := len(log())
	if card = merge(id); card.State != "failed" || len(card.Done) != 2 || card.Done[0] != `shop-creator#1 is not merged: Required status check "ci" is failing.` ||
		card.Done[1] != "shop-creator#6 is not merged: it waits for shop-creator#1, which did not merge." {
		t.Fatalf("what is stacked on one that did not merge is not merged: %+v", card)
	}
	got = log()[before:]
	if moved, back := strings.Index(got, "pr edit 6 -R o/shop-creator --base main\n"), strings.Index(got, "pr edit 6 -R o/shop-creator --base developer/one\n"); moved < 0 || back < moved || strings.Contains(got, "pr merge 6") {
		t.Fatalf("#6 is pointed back at #1's branch, and not merged:\n%s", got)
	}
	os.Remove(filepath.Join(gh, "fail"))
	// nor is one tried again once it was moved onto another base meanwhile
	write(t, filepath.Join(gh, "modified"), "")
	write(t, filepath.Join(gh, "elsewhere"), "")
	id, _ = propose(ChatChange{Do: "merge", Thread: "pr:o/shop-creator#5"})
	if card = merge(id); card.State != "failed" || strings.Join(card.Done, "|") != "shop-creator#5 is not merged: it changed while it was being merged: it is open, into developer/elsewhere now." {
		t.Fatalf("a merge whose base moved is not tried again: %+v", card)
	}
	// one merged meanwhile, as by another card, counts as merged, and what waits for it merges
	set(1, `{"number":1,"state":"MERGED","baseRefName":"main","headRefName":"developer/one","headRefOid":"1111111aa","labels":[],"body":""}`)
	set(6, `{"number":6,"state":"OPEN","isDraft":false,"baseRefName":"main","headRefName":"developer/six","headRefOid":"6666666aa","labels":[],"body":""}`)
	id, _ = propose(ChatChange{Do: "merge", Threads: []string{"pr:o/shop-creator#1", "pr:o/shop-creator#6"}})
	if card = merge(id); card.State != "applied" || strings.Join(card.Done, "|") != "shop-creator#1 was merged into main already.|Merged shop-creator#6 into main." {
		t.Fatalf("a merge of one merged meanwhile: %+v", card)
	}
	// one that waits for another card's merges says so, and its card stays under way meanwhile
	beat := workBeat
	workBeat = 20 * time.Millisecond
	set(6, `{"number":6,"state":"OPEN","isDraft":false,"baseRefName":"main","headRefName":"developer/six","headRefOid":"6666666aa","labels":[],"body":""}`)
	id, _ = propose(ChatChange{Do: "merge", Thread: "pr:o/shop-creator#6"})
	humanMerges.Lock()
	team.ChatApply(id)
	for i := 0; ; i++ {
		if card = lastCard(t, team, id); card.State == "working" && strings.Join(card.Done, "|") == "Waits for the merge under way to end…" {
			break
		}
		if i == 200 {
			humanMerges.Unlock()
			t.Fatalf("a merge that waits its turn says so: %+v", card)
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	humanMerges.Unlock()
	for i := 0; lastCard(t, team, id).State == "working"; i++ {
		if i == 200 {
			t.Fatal("the merge did not end")
		}
		time.Sleep(25 * time.Millisecond)
	}
	workBeat = beat
	var beats int
	var final string
	b, _ = os.ReadFile(filepath.Join(root, chatFile))
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m ChatMsg
		if json.Unmarshal([]byte(line), &m) == nil && m.Re == id {
			final = m.State
			if m.State == "working" {
				beats++
			}
		}
	}
	if beats < 3 || lastCard(t, team, id).State != "applied" || final != "applied" {
		t.Fatalf("while it waited, its card said so again, and what came of it is said last: %d beats, %+v", beats, lastCard(t, team, id))
	}
	// work that says nothing for long stopped with rota, its card says
	stale := workStale
	workStale = 0
	if card = lastCard(t, team, id); card.State != "applied" {
		t.Fatalf("an ended merge stays as it ended: %+v", card)
	}
	team.chatAppend(ChatMsg{Re: id, State: "working", Lines: []string{"Merging shop-creator#6…"}})
	if card = lastCard(t, team, id); card.State != "stopped" {
		t.Fatalf("a merge that stopped with rota: %+v", card)
	}
	workStale = stale

	// an issue closed with a comment, and opened again
	id, card = propose(ChatChange{Do: "close", About: "shop-creator#7", Text: "Fixed by #1."})
	if card.Title != "shop-creator#7 · an issue" || card.Say != "Close it on GitHub, with this comment" || card.Is != "issue" || card.About != "o/shop-creator#7" || card.Once {
		t.Fatalf("the close's card: %+v", card)
	}
	apply(id, "Closed o/shop-creator#7.")
	undo(id, "Reopened o/shop-creator#7 again.")
	if got := log(); !strings.Contains(got, "issue close 7 -R o/shop-creator --comment Fixed by #1.\n") || !strings.Contains(got, "issue reopen 7 -R o/shop-creator\n") {
		t.Fatalf("closed and reopened on GitHub:\n%s", got)
	}
	set(20, `{"number":20,"state":"CLOSED","title":"Old idea","labels":[]}`)
	id, card = propose(ChatChange{Do: "reopen", About: "shop-creator#20"})
	if card.Title != "shop-creator#20 · Old idea" || card.Is != "pr" {
		t.Fatalf("the reopen's card: %+v", card)
	}
	apply(id, "Reopened o/shop-creator#20.")
	refused("is merged: it is not reopened", ChatChange{Do: "reopen", About: "shop-creator#9"})
	refused("is open", ChatChange{Do: "reopen", About: "shop-creator#7"})
	write(t, filepath.Join(gh, "issue-21.json"), `{"state":"CLOSED","title":"Done","labels":[]}`)
	refused("is closed already", ChatChange{Do: "close", About: "shop-creator#21"})
	refused("GitHub has no pull request or issue", ChatChange{Do: "close", About: "shop-creator#99"})

	// labels put on as the repository spells them, and taken off
	id, card = propose(ChatChange{Do: "label", About: "shop-creator#2", Labels: []string{"High", "BUG", "high"}})
	if card.Say != "Put on high, bug" || strings.Join(card.Labels, ",") != "high,bug" {
		t.Fatalf("the label's card: %+v", card)
	}
	apply(id, "o/shop-creator#2 carries high, bug now.")
	undo(id, "o/shop-creator#2 no longer carries high, bug.")
	id, _ = propose(ChatChange{Do: "unlabel", About: "o/shop-creator#2", Labels: []string{"ROTA:needs-review", "low"}})
	apply(id, "no longer carries rota:needs-review")
	if got := log(); !strings.Contains(got, "pr edit 2 -R o/shop-creator --add-label high,bug\n") || !strings.Contains(got, "pr edit 2 -R o/shop-creator --remove-label high,bug\n") ||
		!strings.Contains(got, "pr edit 2 -R o/shop-creator --remove-label rota:needs-review\n") {
		t.Fatalf("labelled on GitHub:\n%s", got)
	}
	refused("carries them already", ChatChange{Do: "label", About: "shop-creator#1", Labels: []string{"HIGH"}})
	refused("carries none of them", ChatChange{Do: "unlabel", About: "shop-creator#1", Labels: []string{"bug"}})
	if _, card := propose(ChatChange{Do: "label", About: "shop-creator#2", Labels: []string{"ROTA:approved"}}); strings.Join(card.Labels, ",") != ApprovedLabel {
		t.Fatalf("the approval, as a label: %+v", card)
	}
	refused(`has no label "nope"`, ChatChange{Do: "label", About: "shop-creator#2", Labels: []string{"nope"}})
	refused("which labels", ChatChange{Do: "label", About: "shop-creator#2"})
	refused("is not a label's name", ChatChange{Do: "label", About: "shop-creator#2", Labels: []string{`"rota:approved"`}}) // gh would read it as rota:approved

	// a comment, which stays
	refused("the comment is empty", ChatChange{Do: "comment", About: "shop-creator#1"})
	id, card = propose(ChatChange{Do: "comment", About: "shop-creator#1", Text: "Merging this tonight."})
	if card.Say != "Comment on GitHub" || card.Text != "Merging this tonight." || !card.Once {
		t.Fatalf("the comment's card: %+v", card)
	}
	apply(id, "Commented on o/shop-creator#1.")
	if !strings.Contains(log(), "pr comment 1 -R o/shop-creator --body Merging this tonight.\n") {
		t.Fatalf("commented on GitHub:\n%s", log())
	}

	// a new issue, and closed again as not planned
	refused("give the issue a title", ChatChange{Do: "issue", Repo: "shop-creator"})
	refused("one line", ChatChange{Do: "issue", Repo: "shop-creator", Subject: "a\nb"})
	id, card = propose(ChatChange{Do: "issue", Repo: "shop-creator", Subject: "Login fails on Safari", Text: "Steps: open it in Safari.", Labels: []string{"HIGH"}})
	if card.Title != "shop-creator · Login fails on Safari" || card.Repo != "o/shop-creator" || strings.Join(card.Labels, ",") != "high" || card.Once {
		t.Fatalf("the issue's card: %+v", card)
	}
	apply(id, "Opened o/shop-creator#12.")
	undo(id, "Closed o/shop-creator#12 again, as not planned.")
	if got := log(); !strings.Contains(got, "issue create -R o/shop-creator --title Login fails on Safari --body Steps: open it in Safari. --label high\n") ||
		!strings.Contains(got, "issue close 12 -R o/shop-creator --reason not planned\n") {
		t.Fatalf("opened and closed on GitHub:\n%s", got)
	}

	// any other change of the team's work on GitHub, as gh's arguments
	refused("not one of the team's repositories", ChatChange{Do: "github", Args: []string{"pr", "ready", "1", "-R", "other/x"}})
	refused("settings, not the team's work", ChatChange{Do: "github", Args: []string{"api", "--method", "PATCH", "repos/o/shop-creator", "--raw-field", "private=false"}})
	refused("not a command that changes", ChatChange{Do: "github", Args: []string{"auth", "logout"}})
	id, card = propose(ChatChange{Do: "github", Args: []string{"run", "rerun", "123", "-R", "shop-creator", "--failed"}})
	if card.Title != "shop-creator · gh run rerun" || card.Text != "gh run rerun --repo=o/shop-creator --failed 123" || card.Say != "Do this on GitHub, as you" || !card.Once {
		t.Fatalf("the github card: %+v", card)
	}
	apply(id, "Done on GitHub.")
	if !strings.Contains(log(), "run rerun --repo=o/shop-creator --failed 123\n") {
		t.Fatalf("run on GitHub:\n%s", log())
	}

	// with each question the Team reads what came of what it proposed, and the open pull requests
	snap := team.teamSnapshot()
	for _, want := range []string{"What you proposed lately, and what came of it:",
		": Squash-merge them on GitHub, in this order: applied. shop-creator#1 was merged into main already. Merged shop-creator#6 into main.",
		": Squash-merge it on GitHub: under way. Merging shop-creator#6…",
		"\n- shop-creator · gh run rerun (", "): Do this on GitHub, as you: applied.", "The team's open pull requests, as GitHub listed them at ",
		"\n- shop-creator#1: fix(studio): ready one; labels rota:needs-review, high, low, rota, rota:needs-rollout; depends on o/shop-creator#9; into main",
		"\n- shop-creator#6: On top of one; labels rota:needs-review; into developer/one"} {
		if !strings.Contains(snap, want) {
			t.Fatalf("the Team reads %q with each question:\n%s", want, snap)
		}
	}
}

// A merge card merges each pull request after those it is stacked on, in its repository, and those
// it depends on, in any of the team's: a branch of the same name elsewhere is another branch.
func TestAMergeCardMergesWhatOthersWaitForFirst(t *testing.T) {
	lib := &mergeItem{repo: "o/lib", n: 1, it: openItem{Head: "dev/lib", Base: "main"}, pending: true}
	app := &mergeItem{repo: "o/app", n: 2, it: openItem{Head: "dev/app", Base: "main", Deps: []string{"o/lib#1"}}, pending: true}
	top := &mergeItem{repo: "o/app", n: 3, it: openItem{Head: "dev/top", Base: "dev/app"}, pending: true}
	other := &mergeItem{repo: "o/lib", n: 3, it: openItem{Head: "dev/x", Base: "dev/app"}, pending: true}
	var got []string
	for _, p := range mergeOrder([]*mergeItem{top, app, other, lib}) {
		got = append(got, p.repo+"#"+strconv.Itoa(p.n))
	}
	if strings.Join(got, " ") != "o/lib#3 o/lib#1 o/app#2 o/app#3" {
		t.Fatalf("merged in the order %v", got)
	}
}

// What a merge did, in a card's line: what it merged, what it rebased onto it, and what the
// team finishes, whatever it was stacked on.
func TestAMergeSaysWhatItDid(t *testing.T) {
	o := mergeOutcome{name: "o/r#1", into: "main", moved: []string{"#2"}, rebased: []string{"#2"}, left: []string{"#3 (conflicts)"}}
	if got := o.short("r#1"); got != "Merged r#1 into main; rebased onto it: #2; not rebased, so its base still has to be merged into each: #3 (conflicts)." {
		t.Fatalf("its line: %q", got)
	}
	if got := (mergeOutcome{name: "o/r#1", into: "main", already: true}).short("r#1"); got != "r#1 was merged into main already." {
		t.Fatalf("merged before: %q", got)
	}
}

// The Team says what the organizer will do, as the organizer decides it: an agent whose fair share
// is used waits however it is asked for, and the Team reads why and until when with each question,
// sees it on a run's card before the tap, and reads what rota answered once it was applied.
func TestTheTeamKnowsWhyAnAgentWaits(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), usageServer(t, root, 12, "")+"pacing: weekly\ndefault_every: 7d\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nalpha\n")
	write(t, filepath.Join(root, "agents", "beta.md"), "---\nenabled: true\n---\nbeta\n")
	write(t, filepath.Join(root, "agents", "gamma.md"), "---\nenabled: true\nweight: 0\n---\ngamma\n")
	if err := runShift(t, root, "beta", time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), true); err != nil {
		t.Fatal(err) // 12% of the window: 60% of the pool, beyond beta's half of it
	}
	appendRun(open(t, root).Config, Run{Time: time.Now().Add(-time.Minute), Agent: "alpha", Status: "ok"}) // alpha is not due; its share is still its own
	stop := organize(t, root)
	defer stop()
	team := open(t, root)
	waitFor(t, "the organizer", func() bool { return team.Status(time.Now()).Pid != 0 })
	if s := agentState(team.Status(time.Now()), "beta").State; !strings.HasPrefix(s, "due ") {
		t.Fatalf("beta ran a minute ago: %q", s)
	}
	r := team.RunWait("beta", team.Status(time.Now()))
	why, next := r.Why, r.Next
	if why != "fair share, 60.0 of 50.0% of the pool used this week" || next == "" {
		t.Fatalf("asked for, beta still waits for its share: %+v", r)
	}
	if r := team.RunWait("gamma", team.Status(time.Now())); r.Why != "no share of the team pool: its weight is 0" || r.Next != "when it has a share of the team pool: its weight is 0" {
		t.Fatalf("gamma, of weight 0, never starts on the pool: %+v", r)
	}

	// with each question: the pool, and why beta waits, what Run now would do, and its part of the pool
	snap := team.teamSnapshot()
	for _, want := range []string{"The team pool, team_pct 20% of Claude's weekly window (pacing weekly): 60.0% of it used this week",
		"- beta: waits: due ", "; Run now would not start it: " + why + "; it would run " + next, "its share is 50.0% of the pool, it used 60.0% this week",
		"Now the organizer would start no one: no agent may start."} {
		if !strings.Contains(snap, want) {
			t.Fatalf("the Team reads %q:\n%s", want, snap)
		}
	}
	if !strings.Contains(team.teamBrief(), "Run now, the human's request (PROPOSE \"run\")") {
		t.Fatal("the Team's brief says what a run the human asks for does, and what it does not")
	}
	// a teammate's request: the Team still reads what the human's would do; paused, only the human's start
	if err := team.askFor("alpha"); err != nil {
		t.Fatal(err)
	}
	if snap := team.teamSnapshot(); !strings.Contains(snap, "- alpha: may start now (ready); asked to run now by a teammate at ") || !strings.Contains(snap, "not started yet; Run now would start it at once") {
		t.Fatalf("alpha, asked for by a teammate:\n%s", snap)
	}
	if err := team.Pause(); err != nil {
		t.Fatal(err)
	}
	if snap := team.teamSnapshot(); !strings.Contains(snap, "The team is paused: the organizer starts only an agent the human asks to run.") || !strings.Contains(snap, "- alpha: would start, but the team is paused (ready)") {
		t.Fatalf("paused:\n%s", snap)
	}
	os.Remove(filepath.Join(team.askedDir(), "alpha"))
	if err := team.Unpause(); err != nil {
		t.Fatal(err)
	}

	// the card says it before the tap; applied, rota's answer comes back to the Team
	id, err := team.ChatPropose(ChatChange{Agent: "beta", Do: "run", Why: "to test what was merged"})
	if err != nil {
		t.Fatal(err)
	}
	card := chatCard(t, team, id)
	if len(card.Lines) != 1 || !strings.HasPrefix(card.Lines[0], "It cannot start now: "+why+". Its next run: ") {
		t.Fatalf("the card says beta cannot start now: %q", card.Lines)
	}
	said, err := team.ChatApply(id)
	if err != nil || !strings.HasPrefix(said, "beta is asked for, but cannot start now: "+why+"; its next run: ") {
		t.Fatalf("applied: %q %v", said, err)
	}
	cards := team.teamCards()
	if !strings.Contains(cards, "rota answered: "+said) || !strings.Contains(cards, "Its card said: It cannot start now") {
		t.Fatalf("the Team reads what came of its card:\n%s", cards)
	}
	if snap := team.teamSnapshot(); !strings.Contains(snap, "asked to run now by the human at ") || !strings.Contains(snap, ", not started yet") {
		t.Fatalf("the Team reads the request that waits:\n%s", snap)
	}
}

func chatCard(t *testing.T, team *Team, id string) ChatChange {
	t.Helper()
	for _, m := range team.Chat(0) {
		if m.ID == id && m.Change != nil {
			return *m.Change
		}
	}
	t.Fatalf("no card %s", id)
	return ChatChange{}
}

// A run the human asks for goes after the ones they asked for before, as the organizer starts them.
func TestARunAskedForGoesAfterTheHumansEarlierOnes(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	write(t, filepath.Join(root, "agents", "first.md"), "---\nenabled: true\n---\nfirst\n")
	write(t, filepath.Join(root, "agents", "second.md"), "---\nenabled: true\n---\nsecond\n")
	team := open(t, root)
	if err := team.RunNow("second"); err != nil {
		t.Fatal(err)
	}
	st := team.Status(time.Now())
	if r := team.RunWait("first", st); r.Why != "" || r.After != "second" {
		t.Fatalf("second was asked for before: %+v", r)
	}
	if r := team.RunWait("second", st); r.Why != "" || r.After != "" {
		t.Fatalf("asked again, second keeps its turn: %+v", r)
	}
	if !team.Asked()["second"] || team.Asked()["first"] {
		t.Fatalf("what is asked stays as it was: %v", team.Asked())
	}
}

// An agent that does not run now says why, by the organizer's own reasons: ready, it waits for
// the running shift, or for whoever starts first, or for the team's pause; outside its hours it
// says them; with its share of the pool used, how much and when the pool fills. Its interval, its
// waiting for work, its being off or the team not running say nothing more than its line. A run
// asked for that has not started says who asked, and what holds it.
func TestWhyAnAgentDoesNotRunNow(t *testing.T) {
	root, _ := chatTeam(t)
	write(t, filepath.Join(root, "agents", "qa.md"), "---\nenabled: true\nhours: 18:00-19:00\n---\nqa tests\n")
	team := open(t, root)
	now := time.Now()
	fills := now.Add(48 * time.Hour)
	base := func(states map[string]string) Status {
		st := Status{Pid: os.Getpid(), Pool: Pool{Used: 4, Quota: 10, Period: "this week", Enforced: true, Fills: fills}}
		for _, name := range []string{"qa", "dev"} {
			st.Agents = append(st.Agents, AgentStatus{Name: name, Enabled: true, State: states[name], PeriodPct: 3, QuotaPct: 2.5})
		}
		return st
	}
	why := func(st Status, name string) string { return team.WhyWaits(st)[name] }

	st := base(map[string]string{"qa": "ready", "dev": "ready"})
	st.Running = &Running{Agent: "qa", Since: now}
	if w := why(st, "dev"); !strings.Contains(w, "starts when qa's shift ends") {
		t.Errorf("after the running shift: %q", w)
	}
	if w := why(st, "qa"); w != "" {
		t.Errorf("the one at work says why: %q", w)
	}
	st = base(map[string]string{"qa": "ready", "dev": "ready"})
	st.Next = "qa"
	if w := why(st, "dev"); !strings.Contains(w, "qa starts first") {
		t.Errorf("another first: %q", w)
	}
	st.Paused = true
	if w := why(st, "dev"); !strings.Contains(w, "the team is paused") {
		t.Errorf("paused: %q", w)
	}
	st = base(map[string]string{"qa": "outside hours, next Thu 05:00", "dev": "fair share, 3.0 of 2.5% of the pool used this week"})
	if w := why(st, "qa"); w != "Outside its hours: it may start only 18:00-19:00." {
		t.Errorf("hours: %q", w)
	}
	if w := why(st, "dev"); !strings.Contains(w, "Its share of the team pool is used up: 30.0 of its 25.0% this week") || !strings.Contains(w, fills.Local().Format("Mon 15:04")) {
		t.Errorf("its share: %q", w)
	}
	st = base(map[string]string{"qa": "due Thu 05:00", "dev": "waiting for new work"})
	if w := team.WhyWaits(st); len(w) != 0 {
		t.Errorf("its line says it: %v", w)
	}
	if err := team.RunNow("qa"); err != nil {
		t.Fatal(err)
	}
	if w := why(st, "qa"); !strings.HasPrefix(w, "You asked it to run at ") || !strings.Contains(w, "It waits: due Thu 05:00.") {
		t.Errorf("a run asked for: %q", w)
	}
	st.Pid = 0
	if w := team.WhyWaits(st); len(w) != 0 {
		t.Errorf("the team not running says so itself: %v", w)
	}
}

// The Team's page and an agent's say what an agent does by the team as it is now: one that may
// start is ready to run, never "next run next tick", and one whose time came since the organizer
// last looked is due since then, never next run at a time gone by.
func TestAnAgentsLineByTheTeamNow(t *testing.T) {
	_, team := chatTeam(t)
	now := time.Now()
	st := Status{Pid: os.Getpid(), NextTick: now.Add(time.Hour), Running: &Running{Agent: "qa", Since: now},
		Agents: []AgentStatus{{Name: "qa", Enabled: true, State: "ready"}, {Name: "dev", Enabled: true, State: "ready", NextNote: "next tick"}}}
	ms := team.MembersNow(st, now)
	if ms[1].Line != "Ready to run" || !strings.Contains(ms[1].Why, "qa's shift ends") || ms[0].State != "working" || ms[0].Why != "" {
		t.Fatalf("ready: %+v", ms)
	}
	st.Agents[1] = AgentStatus{Name: "dev", Enabled: true, State: "due Wed 13:00", NextRun: now.Add(-30 * time.Minute)}
	if l := team.MembersNow(st, now)[1].Line; l != "Due since "+now.Add(-30*time.Minute).Local().Format("15:04") {
		t.Fatalf("due since: %q", l)
	}
}

// The chat says who is in it: an agent at work here is online; each other is offline, with why
// and when it is online next, as the organizer last saw it, the online ones first.
func TestWhoIsOnlineInTheChat(t *testing.T) {
	root, team := chatTeam(t)
	for _, p := range team.ChatPresence(time.Now()) {
		if p.Online || p.Note != "the team is not running" {
			t.Fatalf("no organizer: %+v", p)
		}
	}
	next := time.Now().Add(3 * time.Hour)
	write(t, filepath.Join(root, ".state.json"), fmt.Sprintf(`{"time":%q,"pid":%d,"agents":[{"name":"dev","enabled":true,"state":"due","next_run":%q},{"name":"qa","enabled":true,"state":"ready"}]}`,
		time.Now().Format(time.RFC3339), os.Getpid(), next.Format(time.RFC3339)))
	if ps := team.ChatPresence(time.Now()); ps[0].Name != "dev" || ps[0].Note != "not its turn: next run "+next.Local().Format("Mon 15:04") || ps[1].Note != "not its turn yet: ready to run" {
		t.Fatalf("both offline: %+v", ps)
	}
	os.MkdirAll(team.Path(steerDir), 0o755)
	os.WriteFile(team.steerFile("qa"), []byte(strconv.Itoa(os.Getpid())), 0o644) // qa is at work
	if ps := team.ChatPresence(time.Now()); ps[0].Name != "qa" || !ps[0].Online || ps[1].Online {
		t.Fatalf("qa online, first: %+v", ps)
	}
}

// What holds the whole team holds each agent: while Claude's usage cannot be read nothing starts,
// a paused team too, and a run asked for does not say it starts; a silent organizer's reasons are
// as old as it, so none is given. The chat's presence says it too, and with no agent at all the
// chat has an empty list, not none.
func TestWhatHoldsTheTeamHoldsEachAgent(t *testing.T) {
	root, team := chatTeam(t)
	if err := team.RunNow("qa"); err != nil {
		t.Fatal(err)
	}
	st := Status{Pid: os.Getpid(), Paused: true, UsageError: "429", Next: "qa", Agents: []AgentStatus{{Name: "qa", Enabled: true, State: "ready"}}}
	if w := team.WhyWaits(st)["qa"]; !strings.Contains(w, "nothing starts until Claude's usage can be read") {
		t.Errorf("on hold, paused: %q", w)
	}
	st.UsageError, st.Stale = "", true
	if w := team.WhyWaits(st); len(w) != 0 {
		t.Errorf("silent: %v", w)
	}
	write(t, filepath.Join(root, ".state.json"), fmt.Sprintf(`{"time":%q,"pid":%d,"paused":true,"agents":[{"name":"dev","enabled":true,"state":"ready"}]}`, time.Now().Format(time.RFC3339), os.Getpid()))
	for _, p := range team.ChatPresence(time.Now()) {
		if p.Name == "dev" && p.Note != "the team is paused" {
			t.Errorf("paused team: %+v", p)
		}
	}
	empty := t.TempDir()
	write(t, filepath.Join(empty, "rota.conf"), "usage: off\nlisten: off\n")
	if b, _ := json.Marshal(open(t, empty).ChatPresence(time.Now())); string(b) != "[]" {
		t.Errorf("no agent: %s", b)
	}
}

// rota's line as the human writes an agent that is not here to answer now says when it answers,
// in its chat: its next shift, first thing, once it is on again or resumed; one that runs on
// another machine, as its shift there ends.
func TestRotaSaysWhenAnAgentAnswers(t *testing.T) {
	root, _ := chatTeam(t)
	write(t, filepath.Join(root, "agents", "ops.md"), "---\nenabled: true\nhost: mini\n---\nops runs\n")
	write(t, filepath.Join(root, "agents", "old.md"), "---\nenabled: false\n---\nold\n")
	team := open(t, root)
	team.setPaused("qa", true)
	for _, c := range []struct{ agent, says string }{
		{"dev", "dev is not at work now: its next shift answers you here, first thing."},
		{"qa", "qa is paused: once you resume it, its next shift answers you here, first thing."},
		{"old", "old is off: once it is on again, its next shift answers you here, first thing."},
		{"ops", "ops is not at work now: its next shift, away from the chat, answers you here as it ends."},
	} {
		if got := team.awayText(team.Agent(c.agent)); got != c.says {
			t.Errorf("%s: %q, want %q", c.agent, got, c.says)
		}
	}
	os.MkdirAll(team.Path(steerDir), 0o755)
	os.WriteFile(team.steerFile("ops"), []byte(strconv.Itoa(os.Getpid())), 0o644) // at work on another machine
	if m, err := team.ChatSendTo("ops", "how far are you?", nil); err != nil || lastChat(t, team).ID == m.ID || lastChat(t, team).Text != "ops is at work on another machine, away from the chat: it gets your words after the step it is on, and answers you here as that shift ends." {
		t.Fatalf("at work elsewhere: %+v %v", lastChat(t, team), err)
	}
	if _, err := os.Stat(filepath.Join(team.askedDir(), "ops")); err == nil || !strings.Contains(inboxText(team, "ops"), "in the team chat: how far are you?") {
		t.Fatal("its shift there gets it, and asks for none")
	}
}

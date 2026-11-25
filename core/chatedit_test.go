package core

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// chatWith is the open chat with who, as the app shows it: the human's words, the agent's, rota's.
func chatWith(team *Team, who string) []ChatMsg {
	return slices.DeleteFunc(team.Chat(100), func(m ChatMsg) bool { return m.With() != who })
}

// The human stops an agent at work as it answers, and edits their words, as in any chat. Words that
// have not reached its shift never do: they leave its inbox, and the chat says the human stopped it,
// answering for it. Edited, their new words come at the chat's end and reach the shift in place of the
// old, which it never had. Once it has them and answers them in a talk, a stop cuts its turn off and
// tells it to listen for the human's next words; edited words then say what they replace, and rota
// listen brings them. Words it answered are not edited.
func TestTheHumanStopsAnAgentAndEditsWhatTheyWrote(t *testing.T) {
	team, s, sent := talkTeam(t)
	send := func(text string) bool { *sent = append(*sent, text); return true }

	asked, err := team.ChatSend("@qa why 3 retries?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if said, err := team.ChatStop("qa"); err != nil || said != "You stopped qa." {
		t.Fatalf("stopped: %q %v", said, err)
	}
	if m := lastChat(t, team); m.From != "" || m.To != "qa" || m.Answer != asked.ID || !slices.Equal(m.Unread, []string{asked.ID}) || len(team.ChatBusy()) != 0 {
		t.Fatalf("the chat says so, answering for it, and no longer shows it answering: %+v %v", m, team.ChatBusy())
	}
	if inboxText(team, "qa") != "" {
		t.Fatalf("words that did not reach its shift leave its inbox: %s", inboxText(team, "qa"))
	}
	if s.look(send); len(*sent) != 0 {
		t.Fatalf("nothing reaches the shift: %q", *sent)
	}
	if _, err := team.ChatStop("qa"); err == nil {
		t.Fatal("an agent that answers nothing is stopped")
	}

	five, err := team.ChatEdit(asked.ID, "why 5 retries?", nil)
	if err != nil {
		t.Fatal(err)
	}
	msgs := chatWith(team, "qa")
	if last := msgs[len(msgs)-1]; last.ID != five.ID || last.Edits != asked.ID || last.Text != "why 5 retries?" || slices.ContainsFunc(msgs, func(m ChatMsg) bool { return m.ID == asked.ID }) {
		t.Fatalf("the new words come at the chat's end, in place of the old: %+v", msgs)
	}
	if busy := team.ChatBusy(); !slices.Equal(busy, []string{"qa"}) {
		t.Fatalf("it answers the new words: %v", busy)
	}
	if in := inboxText(team, "qa"); !strings.Contains(in, "why 5 retries?") || strings.Contains(in, "edited") || strings.Contains(in, "3 retries") {
		t.Fatalf("the shift gets the new words as any, the old never having reached it: %s", in)
	}
	s.look(send)
	if len(*sent) != 1 || !strings.Contains((*sent)[0], "why 5 retries?") || !team.Talking("qa") {
		t.Fatalf("they reach the shift, and a talk begins: %q", *sent)
	}
	s.taken(echo((*sent)[0]))

	// it has them, and answers them now: a stop cuts it off, and it listens for what comes next
	if _, err := team.ChatStop("qa"); err != nil {
		t.Fatal(err)
	}
	if m := lastChat(t, team); m.Answer != five.ID || len(m.Unread) != 0 || len(team.ChatBusy()) != 0 {
		t.Fatalf("stopped on words it has: %+v", m)
	}
	s.look(send)
	if len(*sent) != 3 || (*sent)[1] != stopTurn || !strings.Contains((*sent)[2], "The human stopped you in the team chat: do not answer what they wrote you last, «why 5 retries?»") || !strings.Contains((*sent)[2], "run rota listen") {
		t.Fatalf("its turn is cut off, and it is told to listen: %q", *sent)
	}
	if team.loadTalk("qa").Stop != nil {
		t.Fatal("told once")
	}
	if s.look(send); len(*sent) != 3 {
		t.Fatalf("told once: %q", *sent)
	}

	four, err := team.ChatEdit(five.ID, "why 4 retries?", nil)
	if err != nil {
		t.Fatal(err)
	}
	h, err := team.Listen("qa", time.Second)
	if err != nil || len(h.Notes) != 1 || h.Notes[0].Text != "(edited: in place of their words «why 5 retries?») why 4 retries?" {
		t.Fatalf("rota listen brings the new words, which say what they replace: %+v %v", h, err)
	}
	if s.look(send); len(*sent) != 3 {
		t.Fatalf("nothing else reaches the shift: %q", *sent)
	}
	if err := team.ChatSay("qa", "Four, then: the runner takes twenty seconds.", false); err != nil {
		t.Fatal(err)
	}
	if m := lastChat(t, team); m.Answer != four.ID {
		t.Fatalf("its answer names the new words: %+v", m)
	}
	if _, err := team.ChatEdit(four.ID, "why 6 retries?", nil); err == nil || !strings.Contains(err.Error(), "answered it already") {
		t.Fatalf("words it answered are edited: %v", err)
	}
	if _, err := team.ChatEdit(asked.ID, "why 2 retries?", nil); err == nil {
		t.Fatal("words already edited are edited again")
	}
}

// Edited while the agent answers them, the human's words stop it first: its turn is cut off, and
// rota listen brings the new words. Words on their way to its shift, which it has not taken yet,
// leave its turn alone: it is told not to answer them, after its current step.
func TestEditingWhatAnAgentAnswersStopsItFirst(t *testing.T) {
	team, s, sent := talkTeam(t)
	send := func(text string) bool { *sent = append(*sent, text); return true }
	asked, _ := team.ChatSend("@qa is the stack up?", nil)
	s.look(send)
	edited, err := team.ChatEdit(asked.ID, "is the staging stack up?", nil) // on its way: not taken yet
	if err != nil {
		t.Fatal(err)
	}
	if st := team.loadTalk("qa").Stop; st == nil || st.Cut {
		t.Fatalf("on its way, its words do not cut the shift's turn off: %+v", st)
	}
	s.look(send)
	if len(*sent) != 2 || !strings.HasPrefix((*sent)[1], "The human stopped you") {
		t.Fatalf("it is told after its current step: %q", *sent)
	}
	s.taken(echo((*sent)[0]))
	h, _ := team.Listen("qa", time.Second)
	if len(h.Notes) != 1 || !strings.Contains(h.Notes[0].Text, "(edited: in place of their words «is the stack up?») is the staging stack up?") {
		t.Fatalf("rota listen brings the new words: %+v", h)
	}
	// taken, and answered now: an edit cuts its turn off
	if _, err := team.ChatEdit(edited.ID, "is the staging stack up yet?", nil); err != nil {
		t.Fatal(err)
	}
	s.look(send)
	if len(*sent) != 4 || (*sent)[2] != stopTurn {
		t.Fatalf("its turn is cut off: %q", *sent)
	}
	if h, _ := team.Listen("qa", time.Second); len(h.Notes) != 1 || !strings.Contains(h.Notes[0].Text, "is the staging stack up yet?") {
		t.Fatalf("rota listen brings the new words: %+v", h)
	}
}

// The human stops the Team as it answers: its turn stops, and what it would have said is dropped; the
// chat says so, answering for it. Their words edited, it answers the new ones, marked edited.
func TestTheHumanStopsTheTeam(t *testing.T) {
	root, _ := chatTeam(t)
	prev := teamStopLook
	t.Cleanup(func() { teamStopLook = prev })
	teamStopLook = 20 * time.Millisecond
	claude, prompts := filepath.Join(root, "claude"), filepath.Join(root, "prompts")
	write(t, claude, "#!/bin/bash\nread -r prompt\nprintf '%s\\n' \"$prompt\" >> "+prompts+"\nsleep \"${ROTA_TEST_SLEEP:-0}\"\n"+
		`echo '{"type":"system","subtype":"init","session_id":"team-1"}'`+"\n"+
		`echo '{"type":"result","subtype":"success","is_error":false,"result":"All idle.","num_turns":1,"session_id":"team-1","usage":{}}'`+"\n")
	os.Chmod(claude, 0o755)
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\nchat_model: haiku\nclaude_bin: "+claude+"\n")
	team := open(t, root)
	t.Setenv("ROTA_TEST_SLEEP", "5")
	asked, _ := team.ChatSend("What is everyone doing?", nil)
	done := make(chan struct{})
	go func() { team.teamTurn(); close(done) }()
	waitFor(t, "the Team to answer", func() bool { return slices.Equal(team.ChatBusy(), []string{"team"}) })
	began := time.Now()
	if said, err := team.ChatStop("team"); err != nil || said != "You stopped the Team." {
		t.Fatalf("stopped: %q %v", said, err)
	}
	if len(team.ChatBusy()) != 0 {
		t.Fatal("the chat no longer shows it answering")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("its turn goes on")
	}
	if time.Since(began) > 2*time.Second {
		t.Fatal("its turn stopped late")
	}
	if m := lastChat(t, team); m.From != "" || m.Text != "You stopped the Team." || m.Answer != asked.ID {
		t.Fatalf("nothing more is said: %+v", m)
	}
	if team.TeamWaits() {
		t.Fatal("what it was stopped on waits for an answer")
	}

	t.Setenv("ROTA_TEST_SLEEP", "0")
	woken := 0
	if _, err := team.ChatEdit(asked.ID, "What is qa doing?", func() { woken++ }); err != nil || woken != 1 {
		t.Fatalf("edited: %v, woken %d", err, woken)
	}
	team.teamTurn()
	if m := lastChat(t, team); m.From != "team" || m.Text != "All idle." {
		t.Fatalf("it answers the new words: %+v", m)
	}
	b, _ := os.ReadFile(prompts)
	if p := strings.Split(strings.TrimSpace(string(b)), "\n"); !strings.Contains(p[len(p)-1], "(edited) What is qa doing?") || strings.Contains(p[len(p)-1], "everyone") {
		t.Fatalf("the new words, marked edited, and not the old: %q", p)
	}
}

// A stop cuts the running shift's turn off, as Esc does in Claude Code: rota asks claude to
// interrupt it, and the result of that cut-off turn does not end the shift, which goes on with what
// rota sends next; its own end does.
func TestAStopCutsTheShiftsTurnOff(t *testing.T) {
	root := t.TempDir()
	got := filepath.Join(root, "got")
	claude := filepath.Join(root, "claude")
	write(t, claude, `#!/bin/bash
read -r prompt
printf '%s\n' "$prompt"
while read -r line; do
  printf '%s\n' "$line" >> `+got+`
  case "$line" in
  *control_request*interrupt*) echo '{"type":"result","subtype":"error_during_execution","is_error":true,"num_turns":1,"session_id":"s1","usage":{}}' ;;
  *'"type":"user"'*) printf '%s\n' "$line"; echo '{"type":"result","subtype":"success","is_error":false,"result":"## For the human\nnone\n\nListening.","num_turns":1,"session_id":"s1","usage":{}}' ;;
  esac
done
`)
	os.Chmod(claude, 0o755)
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\nclaude_bin: "+claude+"\n")
	team := open(t, root)
	notes := make(chan string, 4)
	notes <- stopTurn
	notes <- "The human stopped you in the team chat."
	done := make(chan Result)
	go func() {
		done <- execShift(context.Background(), team.Config, Job{ID: "j1", Agent: "qa", Prompt: "work", RunMinutes: 1, WorkDir: "work/qa"}, nil, notes)
	}()
	var res Result
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the shift does not end")
	}
	b, _ := os.ReadFile(got)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"subtype":"interrupt"`) || !strings.Contains(lines[1], "The human stopped you") {
		t.Fatalf("claude is asked to interrupt its turn, then told why: %q", lines)
	}
	if final, ok := finalResult(res.Output); res.Status != "done" || !ok || final.IsError || !strings.HasSuffix(final.Result, "Listening.") {
		t.Fatalf("the cut-off turn did not end the shift; its own end did: %+v %+v", res, final)
	}
}

// The human deletes their words to an agent until it answers them, as they edit them. Words that have
// not reached its shift leave the chat and its inbox, and it never has them. Words it has and answers
// now in a talk stop its turn, as a stop does, and rota listen brings a note that they are taken
// back. Words it answered stay.
func TestTheHumanDeletesWhatTheyWroteAnAgent(t *testing.T) {
	team, s, sent := talkTeam(t)
	send := func(text string) bool { *sent = append(*sent, text); return true }

	asked, err := team.ChatSend("@qa why 3 retries?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := team.ChatDelete(asked.ID, nil); err != nil {
		t.Fatal(err)
	}
	if msgs := chatWith(team, "qa"); slices.ContainsFunc(msgs, func(m ChatMsg) bool { return m.ID == asked.ID || m.Deletes != "" }) {
		t.Fatalf("the words leave the chat, and nothing shows in their place: %+v", msgs)
	}
	if in := inboxText(team, "qa"); in != "" || len(team.ChatBusy()) != 0 {
		t.Fatalf("words that did not reach its shift leave its inbox: %q %v", in, team.ChatBusy())
	}
	if s.look(send); len(*sent) != 0 {
		t.Fatalf("nothing reaches the shift: %q", *sent)
	}
	if err := team.ChatDelete(asked.ID, nil); err == nil {
		t.Fatal("words deleted already are deleted again")
	}

	stack, _ := team.ChatSend("@qa is the stack up?", nil)
	s.look(send)
	s.taken(echo((*sent)[0])) // it has them, and answers them now in a talk
	if err := team.ChatDelete(stack.ID, nil); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(chatWith(team, "qa"), func(m ChatMsg) bool { return m.ID == stack.ID }) || len(team.ChatBusy()) != 0 {
		t.Fatalf("they leave the chat, which no longer shows it answering: %v", team.ChatBusy())
	}
	s.look(send)
	if len(*sent) != 3 || (*sent)[1] != stopTurn || !strings.Contains((*sent)[2], "do not answer what they wrote you last, «is the stack up?»") {
		t.Fatalf("its turn is cut off, and it is told not to answer them: %q", *sent)
	}
	h, err := team.Listen("qa", time.Second)
	if err != nil || len(h.Notes) != 1 || !strings.Contains(h.Notes[0].Text, "(taken back: they deleted their words «is the stack up?»") {
		t.Fatalf("rota listen brings the note that they are taken back: %+v %v", h, err)
	}

	answered, _ := team.ChatSend("@qa and the runner?", nil)
	if err := team.ChatSay("qa", "Twenty seconds.", false); err != nil {
		t.Fatal(err)
	}
	if err := team.ChatDelete(answered.ID, nil); err == nil || !strings.Contains(err.Error(), "answered it already") {
		t.Fatalf("words it answered are deleted: %v", err)
	}
	if err := team.ChatDelete("c20991231-000000-abc", nil); err == nil {
		t.Fatal("words that are not in the chat are deleted")
	}
}

// Words to the Team deleted as it answers them stop its turn, and nothing is said in their place;
// deleted while other words wait on it, it answers those, and never reads the deleted ones.
func TestTheHumanDeletesWhatTheyAskedTheTeam(t *testing.T) {
	root, _ := chatTeam(t)
	prev := teamStopLook
	t.Cleanup(func() { teamStopLook = prev })
	teamStopLook = 20 * time.Millisecond
	claude, prompts := filepath.Join(root, "claude"), filepath.Join(root, "prompts")
	write(t, claude, "#!/bin/bash\nread -r prompt\nprintf '%s\\n' \"$prompt\" >> "+prompts+"\nsleep \"${ROTA_TEST_SLEEP:-0}\"\n"+
		`echo '{"type":"system","subtype":"init","session_id":"team-1"}'`+"\n"+
		`echo '{"type":"result","subtype":"success","is_error":false,"result":"All idle.","num_turns":1,"session_id":"team-1","usage":{}}'`+"\n")
	os.Chmod(claude, 0o755)
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\nchat_model: haiku\nclaude_bin: "+claude+"\n")
	team := open(t, root)
	t.Setenv("ROTA_TEST_SLEEP", "5")
	asked, _ := team.ChatSend("What is everyone doing?", nil)
	done := make(chan struct{})
	go func() { team.teamTurn(); close(done) }()
	waitFor(t, "the Team to answer", func() bool { return slices.Equal(team.ChatBusy(), []string{"team"}) })
	woken := 0
	if err := team.ChatDelete(asked.ID, func() { woken++ }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("its turn goes on")
	}
	if msgs := chatWith(team, "team"); len(msgs) != 0 || len(team.ChatBusy()) != 0 || team.TeamWaits() || woken != 0 {
		t.Fatalf("nothing is said, nothing waits, nothing wakes it: %+v %v %d", msgs, team.ChatBusy(), woken)
	}

	t.Setenv("ROTA_TEST_SLEEP", "0")
	gone, _ := team.ChatSend("What is qa doing?", nil)
	team.ChatSend("And the developer?", nil)
	if err := team.ChatDelete(gone.ID, func() { woken++ }); err != nil || woken != 1 {
		t.Fatalf("deleted: %v, woken %d", err, woken)
	}
	team.teamTurn()
	if m := lastChat(t, team); m.From != "team" || m.Text != "All idle." {
		t.Fatalf("it answers what else waits: %+v", m)
	}
	b, _ := os.ReadFile(prompts)
	if p := strings.Split(strings.TrimSpace(string(b)), "\n"); !strings.Contains(p[len(p)-1], "And the developer?") || strings.Contains(p[len(p)-1], "qa doing") {
		t.Fatalf("it reads what waits, and never the deleted words: %q", p[len(p)-1])
	}
}

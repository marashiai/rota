package core

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// msgThread is the mail's thread of the human's message thread id, if the mail has it.
func msgThread(t *testing.T, root, id string) (Thread, bool) {
	t.Helper()
	for _, th := range open(t, root).Mail(time.Now()).Threads {
		if th.ID == "m:"+id {
			return th, true
		}
	}
	return Thread{}, false
}

// noteIDs are the ids of the notes in an agent's inbox whose text has text.
func noteIDs(team *Team, agent, text string) map[string]bool {
	out := map[string]bool{}
	for _, n := range team.Notes(agent) {
		if strings.Contains(n.Text, text) {
			out[n.ID] = true
		}
	}
	return out
}

// The human's message to an agent reaches it as a note that names its id; the thread waits until
// the agent reads it, is only sent once it did, and comes into the inbox when the agent answers,
// with rota reply or a line in its final message, until the human read the answer. A message after it goes on in the same thread
// and has an id of its own, which finds the thread.
func TestAMessageAndItsAnswer(t *testing.T) {
	root, team := mailTeam(t)
	id, err := team.SendMessage("developer", "o/shop-creator#673", "Fold #676 into #673, please.", "")
	if err != nil {
		t.Fatal(err)
	}
	if notes := inboxText(team, "developer"); !strings.Contains(notes, "from the human, message "+id+": Fold #676") {
		t.Fatalf("the note names its message: %s", notes)
	}
	if th, _ := msgThread(t, root, id); th.Box != BoxWaiting || th.Kind != "msg" || th.Msgs[0].To != "developer" || th.Label != "shop-creator#673" || !th.Seen.IsZero() {
		t.Fatalf("waiting for its agent to read it: %+v", th)
	}
	if err := team.dropNotes("developer", noteIDs(team, "developer", "Fold #676")); err != nil { // its shift read it
		t.Fatal(err)
	}
	if th, _ := msgThread(t, root, id); th.Box != BoxDone || th.Seen.IsZero() {
		t.Fatalf("read, it is only sent: %+v", th)
	}
	if err := team.Unsend(id); err == nil {
		t.Fatal("a message the agent read is taken back")
	}
	if lost := team.routeNotes("developer", "## Notes to teammates\n@human re "+id+": Done: #676 is part of #673.\n", time.Now()); len(lost) > 0 {
		t.Fatal(lost)
	}
	th, _ := msgThread(t, root, id)
	if th.Box != BoxInbox || !th.Unread || len(th.Msgs) != 2 || th.Msgs[1].From != "developer" || th.Msgs[1].Text != "Done: #676 is part of #673." {
		t.Fatalf("the answer brings it to the inbox: %+v", th)
	}
	team.MarkMail("m:"+id, "read", "", time.Now())
	if th, _ = msgThread(t, root, id); th.Box != BoxDone || th.Unread {
		t.Fatalf("an answer the human read needs nothing more of them, and leaves the inbox: %+v", th)
	}
	team.MarkMail("m:"+id, "unread", "", time.Now())
	if th, _ = msgThread(t, root, id); th.Box != BoxInbox {
		t.Fatalf("marked unread, it is back: %+v", th)
	}
	if _, err := team.SendMessage("developer", "", "And rebase it.", id); err != nil {
		t.Fatal(err)
	}
	if th, _ = msgThread(t, root, id); th.Box != BoxWaiting || len(th.Msgs) != 3 || th.Msgs[2].From != "you" || !th.Seen.IsZero() {
		t.Fatalf("a message after it goes on in its thread, waiting again: %+v", th)
	}
	var later Note
	for _, n := range team.Notes("developer") {
		if n.Text == "And rebase it." {
			later = n
		}
	}
	if later.About != "o/shop-creator#673" || later.ID == id || threadOfNote(later.ID) != id {
		t.Fatalf("it is about what the thread is about, with an id of its own that finds the thread: %+v", later)
	}
	if err := team.ReplyTo("developer", later.ID, "Done."); err != nil {
		t.Fatal(err)
	}
	if th, _ = msgThread(t, root, id); th.Box != BoxInbox || th.Msgs[3].Text != "Done." {
		t.Fatalf("its answer by its own id: %+v", th)
	}
	if b, _ := os.ReadFile(filepath.Join(root, sentDir, id+".json")); !strings.Contains(string(b), "Fold #676") {
		t.Fatal("the thread is kept in sent/")
	}
}

// What can go wrong with a message: each of the human's messages is read on its own, an answer to
// one message is not taken for an answer to another, ids that are no message's are refused, a
// message that did not reach the agent is not kept, one the agent's running shift has can no
// longer be taken back, and writing in an archived thread brings it back.
func TestWhatCanGoWrongWithAMessage(t *testing.T) {
	root, team := mailTeam(t)
	for _, name := range []string{"qa", "rollout"} {
		write(t, filepath.Join(root, "agents", name+".md"), "---\nenabled: true\nrepos: o/shop-creator\n---\ntests\n")
	}
	team = open(t, root)
	id, _ := team.SendMessage("developer", "", "Rebase #673.", "")
	team.SendMessage("developer", "", "And #541.", id)
	if err := team.dropNotes("developer", noteIDs(team, "developer", "Rebase #673")); err != nil {
		t.Fatal(err)
	}
	if th, _ := msgThread(t, root, id); th.Box != BoxWaiting || !th.Seen.IsZero() {
		t.Fatalf("its shift read the first message, not the second: %+v", th)
	}
	if err := team.Unsend(id); err != nil {
		t.Fatal("the unread second message is not taken back:", err)
	}
	if th, _ := msgThread(t, root, id); th.Box != BoxDone || len(th.Msgs) != 1 || th.Seen.IsZero() {
		t.Fatalf("what is left is the first message, read: %+v", th)
	}

	// two messages, neither read: taking back the second leaves the first unread
	other, _ := team.SendMessage("triage", "", "Hold #673.", "")
	team.SendMessage("triage", "", "And #541.", other)
	if err := team.Unsend(other); err != nil {
		t.Fatal(err)
	}
	if th, _ := msgThread(t, root, other); th.Box != BoxWaiting || len(th.Msgs) != 1 {
		t.Fatalf("the first message still waits: %+v", th)
	}
	if err := team.Unsend(other); err != nil {
		t.Fatal(err)
	}
	if _, there := msgThread(t, root, other); there || strings.Contains(inboxText(team, "triage"), "Hold #673") {
		t.Fatal("taken back before it was read, the thread is gone, and so is its note")
	}

	// the same answer to two messages is two answers
	third, _ := team.SendMessage("qa", "", "Test #673.", "")
	team.ReplyTo("qa", third, "Done.")
	team.ReplyTo("qa", third, "Done.") // said twice: kept once
	team.SendMessage("qa", "", "Now #541.", third)
	var second string
	for n := range noteIDs(team, "qa", "Now #541") {
		second = n
	}
	if err := team.ReplyTo("qa", second, "Done."); err != nil {
		t.Fatal(err)
	}
	if th, _ := msgThread(t, root, third); len(th.Msgs) != 4 || th.Box != BoxInbox {
		t.Fatalf("each message its answer, once: %+v", th.Msgs)
	}
	for _, bad := range []string{"", "../" + third, third + "-zz", "20261108-150000-abcdef", "x"} {
		if err := team.ReplyTo("qa", bad, "Done."); err == nil {
			t.Fatalf("an answer to %q is kept", bad)
		}
	}
	if err := team.ReplyTo("developer", third, "Mine."); err == nil {
		t.Fatal("another agent answers it")
	}

	// a message that does not reach the agent is not kept
	inbox := team.Inbox("rollout")
	os.MkdirAll(filepath.Dir(inbox), 0o755)
	os.WriteFile(inbox, []byte("{"), 0o644)
	if _, err := team.SendMessage("rollout", "", "Roll #673 out.", ""); err == nil {
		t.Fatal("a message is sent to an inbox that cannot be read")
	}
	for _, m := range team.MessageThreads() {
		if m.To == "rollout" {
			t.Fatalf("a message that never reached its agent is kept: %+v", m)
		}
	}
	os.Remove(inbox)
	fourth, _ := team.SendMessage("rollout", "", "Roll #673 out.", "")
	kept, _ := os.ReadFile(inbox)
	os.WriteFile(inbox, []byte("{"), 0o644)
	if _, err := team.SendMessage("rollout", "", "Now.", fourth); err == nil {
		t.Fatal("a later message is sent to an inbox that cannot be read")
	}
	os.WriteFile(inbox, kept, 0o644)
	if th, _ := msgThread(t, root, fourth); len(th.Msgs) != 1 {
		t.Fatalf("the later message that never reached it is not in the thread: %+v", th.Msgs)
	}

	// once the agent's shift runs, the message is the shift's
	os.MkdirAll(team.Path(steerDir), 0o755)
	os.WriteFile(team.steerFile("rollout"), []byte(strconv.Itoa(os.Getpid())), 0o644)
	if err := team.Unsend(fourth); err == nil || !strings.Contains(err.Error(), "is working") {
		t.Fatalf("a message is taken back from a running shift: %v", err)
	}
	os.Remove(team.steerFile("rollout"))
	if !strings.Contains(inboxText(team, "rollout"), "Roll #673 out.") {
		t.Fatal("the refused message left the inbox")
	}

	// a thread the human archived and writes in again waits again
	team.MarkMail("m:"+third, "archive", "", time.Now())
	if th, _ := msgThread(t, root, third); th.Box != BoxDone {
		t.Fatalf("archived: %+v", th)
	}
	time.Sleep(10 * time.Millisecond)
	team.SendMessage("qa", "", "One more: #539.", third)
	if th, _ := msgThread(t, root, third); th.Box != BoxWaiting || !th.Seen.IsZero() {
		t.Fatalf("written in again, it waits for qa: %+v", th)
	}
	if _, err := team.SendMessage("developer", "", "x", third); err == nil {
		t.Fatal("a message goes into another agent's thread")
	}
	if _, err := team.SendMessage("developer", "", "  ", ""); err == nil {
		t.Fatal("an empty message is sent")
	}
	if _, err := team.SendMessage("developer", "o/elsewhere#1", "x", ""); err == nil {
		t.Fatal("a message about another team's pull request is sent")
	}
}

// In a real shift: the human's message is read when claude begins with its brief, one sent while
// the shift runs is the shift's and cannot be taken back, it is read when claude takes it, and
// the agent's answer in its final message comes into the human's inbox.
func TestAShiftReadsAndAnswersTheHumansMessages(t *testing.T) {
	defer func(d time.Duration) { steerEvery = d }(steerEvery)
	steerEvery = 50 * time.Millisecond
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\ntriage_model: off\nreport_model: off\nclaude_bin: "+filepath.Join(root, "claude")+"\n")
	write(t, filepath.Join(root, "agents", "dev.md"), "---\nenabled: true\nuntil_done: true\nrepos: o/backend\n---\ndev\n")
	team := open(t, root)
	id, err := team.SendMessage("dev", "", "Rebase #7.", "")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "claude"), "#!/bin/bash\nread -r prompt\n"+
		`echo '{"type":"system","subtype":"init","session_id":"s1"}'`+"\ntouch "+root+"/started\n"+
		`read -r note; printf '%s\n' "$note"`+"\nwhile [ ! -f "+root+"/go ]; do sleep 0.05; done\n"+
		`echo '{"type":"result","subtype":"success","is_error":false,"result":"## Summary\ndone\n## Notes to teammates\n@human re `+id+`: Rebased.","num_turns":2,"session_id":"s1","usage":{}}'`+"\n")
	os.Chmod(filepath.Join(root, "claude"), 0o755)
	done := make(chan error)
	go func() { done <- team.RunShift(context.Background(), "dev", time.Now(), true, false) }()
	waitFor(t, "the shift", func() bool { _, err := os.Stat(filepath.Join(root, "started")); return err == nil })
	waitFor(t, "the message read", func() bool { th, _ := msgThread(t, root, id); return !th.Seen.IsZero() })
	if _, err := team.SendMessage("dev", "", "And #8.", id); err != nil {
		t.Fatal(err)
	}
	if err := team.Unsend(id); err == nil {
		t.Fatal("a message sent to a running shift is taken back")
	}
	waitFor(t, "the second message read", func() bool { th, _ := msgThread(t, root, id); return th.Box == BoxDone && !th.Seen.IsZero() })
	write(t, filepath.Join(root, "go"), "")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	th, _ := msgThread(t, root, id)
	if th.Box != BoxInbox || len(th.Msgs) != 3 || th.Msgs[2].From != "dev" || th.Msgs[2].Text != "Rebased." || inboxText(team, "dev") != "" {
		t.Fatalf("the answer in its final message comes to the inbox: %+v\ninbox: %s", th, inboxText(team, "dev"))
	}
}

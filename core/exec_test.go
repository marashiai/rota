package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// shiftHears runs a shift on a fake claude (script), sends it note once a line of its output
// holds after, and says what the fake claude got and how the shift ended.
func shiftHears(t *testing.T, script, after, note string) (got string, final string) {
	t.Helper()
	root := t.TempDir()
	gotFile := filepath.Join(root, "got")
	claude := filepath.Join(root, "claude")
	write(t, claude, "#!/bin/bash\n"+strings.ReplaceAll(script, "GOT", gotFile))
	os.Chmod(claude, 0o755)
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\nclaude_bin: "+claude+"\n")
	team := open(t, root)
	notes := make(chan string, 1)
	onLine := func(line string) {
		if strings.Contains(line, after) {
			notes <- note
		}
	}
	done := make(chan Result)
	go func() {
		done <- execShift(context.Background(), team.Config, Job{ID: "j1", Agent: "qa", Prompt: "work", RunMinutes: 1, WorkDir: "work/qa"}, onLine, notes)
	}()
	var res Result
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the shift does not end")
	}
	b, _ := os.ReadFile(gotFile)
	r, _ := finalResult(res.Output)
	return string(b), r.Result
}

// A resumed session first tells of the background tasks its interrupted shift left running, in a
// turn of its own that ends before claude takes rota's prompt. That is not the shift's end: the
// shift goes on, and hears the human.
func TestAResumedShiftHearsTheHuman(t *testing.T) {
	got, final := shiftHears(t, `
echo '{"type":"system","subtype":"init","session_id":"s1"}'
echo '{"type":"result","subtype":"success","is_error":false,"result":"","num_turns":0,"session_id":"s1","origin":{"kind":"task-notification"}}'
read -r prompt; printf '%s\n' "$prompt"
if read -r note; then printf '%s\n' "$note" >> GOT; printf '%s\n' "$note"; fi
echo '{"type":"result","subtype":"success","is_error":false,"result":"## For the human\nHeard.","num_turns":2,"session_id":"s1"}'
cat >/dev/null
`, `"origin"`, "New in your inbox during this shift:\n\nare you stuck?")
	if !strings.Contains(got, "are you stuck?") || !strings.HasSuffix(final, "Heard.") {
		t.Fatalf("the shift should hear the human after the turn that tells of its old tasks: got %q, ended %q", got, final)
	}
}

// A shift that ends its turn to wait for a task of its in the background has not finished: it
// hears the human meanwhile, and ends after the turn the task's end begins.
func TestAShiftWaitingOnItsBackgroundTaskHearsTheHuman(t *testing.T) {
	got, final := shiftHears(t, `
read -r prompt; printf '%s\n' "$prompt"
echo '{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"b1"}]}'
echo '{"type":"result","subtype":"success","is_error":false,"result":"Waiting for the monitor.","num_turns":3,"session_id":"s1"}'
if read -r note; then printf '%s\n' "$note" >> GOT; printf '%s\n' "$note"; fi
echo '{"type":"result","subtype":"success","is_error":false,"result":"Nearly done.","num_turns":4,"session_id":"s1"}'
echo '{"type":"system","subtype":"background_tasks_changed","tasks":[]}'
echo '{"type":"system","subtype":"init","session_id":"s1"}'
echo '{"type":"result","subtype":"success","is_error":false,"result":"## For the human\nDone.","num_turns":5,"session_id":"s1"}'
cat >/dev/null
`, "Waiting for the monitor.", "New in your inbox during this shift:\n\nhow far along?")
	if !strings.Contains(got, "how far along?") || !strings.HasSuffix(final, "Done.") {
		t.Fatalf("the shift should hear the human while its task runs, and end after it: got %q, ended %q", got, final)
	}
}

type inputSink struct{ closed atomic.Bool }

func (s *inputSink) Write(b []byte) (int, error) { return len(b), nil }
func (s *inputSink) Close() error                { s.closed.Store(true); return nil }

// A shift's input closes once claude has finished: its turn has ended with the final message, and
// it has taken every message rota sent. What it still runs in the background does not keep it open
// then; it does after a turn that ends otherwise, as does a turn claude may begin by itself.
func TestAShiftsInputClosesOnceClaudeHasFinished(t *testing.T) {
	const (
		turn   = `{"type":"result","subtype":"success","result":"Waiting for the build."}`
		final  = `{"type":"result","subtype":"success","result":"## For the human\nnone"}`
		echo   = `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"work"}]}}`
		begin  = `{"type":"system","subtype":"init"}`
		busy   = `{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"b1"}]}`
		free   = `{"type":"system","subtype":"background_tasks_changed","tasks":[]}`
		answer = `{"type":"assistant","message":{"content":[{"type":"text","text":"On it."}]}}`
	)
	later := func(d time.Duration) time.Time { return time.Now().Add(d + time.Second) }
	open := func(waiting ...string) (*shiftInput, *inputSink) {
		s := &inputSink{}
		return &shiftInput{w: s, waiting: waiting}, s
	}

	in, s := open("work")
	in.see(begin)
	in.see(turn) // a turn of its own, before it took the prompt
	in.see(echo)
	in.see(busy)
	in.see(turn) // it waits for its task
	in.settle(later(time.Hour))
	if s.closed.Load() {
		t.Fatal("closed while claude waits for its task")
	}
	in.see(free)
	in.see(begin) // the turn the task's end begins
	in.settle(later(time.Hour))
	if s.closed.Load() {
		t.Fatal("closed during a turn")
	}
	in.see(answer)
	in.see(final)
	if !s.closed.Load() {
		t.Fatal("open after the final message")
	}

	in, s = open("work")
	in.see(turn) // and it never echoes the prompt
	in.settle(later(turnGrace))
	if s.closed.Load() {
		t.Fatal("closed before claude could take the prompt")
	}
	in.settle(later(echoGrace))
	if !s.closed.Load() {
		t.Fatal("open long after claude's turn ended without its echo of the prompt")
	}

	in, s = open()
	in.see(busy)
	in.see(turn)
	in.see(free) // and no turn begins
	in.settle(time.Now())
	if s.closed.Load() {
		t.Fatal("closed before the turn the task's end begins could begin")
	}
	in.settle(later(turnGrace))
	if !s.closed.Load() {
		t.Fatal("open long after its last task ended")
	}

	in, s = open()
	in.see(busy)
	in.see(final) // what still runs stops
	if !s.closed.Load() {
		t.Fatal("open after the final message, its task still running")
	}

	in, s = open()
	in.see(busy)
	in.see(`{"type":"result","subtype":"success","is_error":true,"result":"API Error: overloaded"}`)
	if !s.closed.Load() {
		t.Fatal("open after an error ended the shift, its task still running")
	}

	in, s = open("work")
	in.see(echo)
	in.see(final)
	in.send("New in your inbox during this shift:\n\nand one more thing") // as the turn ends
	in.see(`{"type":"assistant","parent_tool_use_id":"toolu_1","message":{"content":[{"type":"text","text":"a subagent's"}]}}`)
	in.settle(later(echoGrace))
	if !s.closed.Load() {
		t.Fatal("a subagent's words kept the shift open")
	}

	in, s = open("work")
	in.see(echo)
	in.see(turn)
	in.interrupt() // a stop between turns: claude cuts no turn off
	in.see(begin)
	in.see(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":""}`)
	if !s.closed.Load() {
		t.Fatal("the next turn's error was taken for the turn the stop cut off")
	}
}

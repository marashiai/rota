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

// checkingClaude is a claude whose shift runs four steps of 300 ms, each a Bash call of
// $FAKE_CMD, and whose check (claude -p with no tools) says a shift that runs "STUCK" works no
// more, any other works on.
func checkingClaude(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "claude")
	script := `#!/bin/bash
if [[ " $* " == *" --tools "* ]]; then
  p=$(cat)
  if [[ "$p" == *STUCK* ]]; then
    printf '%s' '{"type":"result","subtype":"success","is_error":false,"result":"{\"go_on\": false, \"why\": \"it repeats the same failing step\"}"}'
  else
    printf '%s' '{"type":"result","subtype":"success","is_error":false,"result":"{\"go_on\": true, \"why\": \"it runs its tests\"}"}'
  fi
  exit 0
fi
head -n1 >/dev/null
for i in 1 2 3 4; do
  printf '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t%s","name":"Bash","input":{"command":"%s"}}]}}\n' $i "$FAKE_CMD"
  sleep 0.3
  printf '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t%s","content":"ok"}]}}\n' $i
done
printf '{"type":"result","subtype":"success","is_error":false,"result":"done","num_turns":4,"session_id":"s1","usage":{}}\n'
`
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// A shift goes on past its run_minutes while rota's check finds it makes real progress, and ends
// at the check that finds it does not; one that cannot be checked goes on.
func TestAShiftThatWorksGoesOnAndOneThatDoesNotEnds(t *testing.T) {
	root, team := mailTeam(t)
	team.ClaudeBin, team.TriageModel = checkingClaude(t, root), "haiku"
	was := runMinute
	runMinute = 350 * time.Millisecond
	defer func() { runMinute = was }()
	run := func(cmd string) (Result, time.Duration) {
		t.Helper()
		t.Setenv("FAKE_CMD", cmd)
		start := time.Now()
		res := execShift(context.Background(), team.Config, Job{ID: "j", Agent: "developer", Prompt: "work", RunMinutes: 1}, nil, nil)
		return res, time.Since(start)
	}
	if res, took := run("go test ./..."); res.Status != "done" || took < 1100*time.Millisecond {
		t.Fatalf("a shift that works goes on past its run_minutes to its end: %s after %v (%s)", res.Status, took, tail(res.Output, 200))
	}
	if res, took := run("go test ./... STUCK"); res.Status != "timeout" || res.Checked != "it repeats the same failing step" || took > time.Second {
		t.Fatalf("a shift that does not work ends at its check: %s %q after %v", res.Status, res.Checked, took)
	}
	team.TriageModel = "off"
	if res, _ := run("go test ./... STUCK"); res.Status != "done" {
		t.Fatalf("a shift that cannot be checked goes on: %s", res.Status)
	}
}

// A shift the human is talking with goes on unchecked: the talk is work, whatever the check would
// find; and nothing tells it its shift ends.
func TestAShiftTheHumanTalksWithGoesOn(t *testing.T) {
	root, team := mailTeam(t)
	team.ClaudeBin, team.TriageModel = checkingClaude(t, root), "haiku"
	was := runMinute
	runMinute = 350 * time.Millisecond
	defer func() { runMinute = was }()
	os.MkdirAll(filepath.Dir(team.steerFile("developer")), 0o755)
	os.WriteFile(team.steerFile("developer"), []byte(strconv.Itoa(os.Getpid())), 0o644) // its shift runs, steered
	if err := team.saveTalk("developer", chatTalk{Open: true}); err != nil || !team.Talking("developer") {
		t.Fatalf("a talk: %v", err)
	}
	t.Setenv("FAKE_CMD", "go test ./... STUCK")
	res := execShift(context.Background(), team.Config, Job{ID: "j", Agent: "developer", Prompt: "work", RunMinutes: 1, WorkDir: "work/developer"}, nil, nil)
	if res.Status != "done" || res.Checked != "" {
		t.Fatalf("a shift the human talks with goes on: %s %q", res.Status, res.Checked)
	}
	if strings.Contains(res.Output, "ROTA_PARK_BY") {
		t.Fatal("no shift is given a time to put its work away")
	}
}

// The check reads what the shift did since it began or was last checked, and what still runs.
func TestTheCheckReadsWhatTheShiftDid(t *testing.T) {
	var a activity
	t0 := time.Date(2026, 11, 24, 21, 0, 0, 0, time.Local)
	a.see(`{"type":"assistant","message":{"content":[{"type":"text","text":"Running the suites."},{"type":"tool_use","id":"a","name":"Bash","input":{"command":"./test-all"}}]}}`, t0)
	a.see(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"a","content":[{"type":"text","text":"FAIL: 2 tests"}],"is_error":true}]}}`, t0.Add(time.Minute))
	a.see(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"b","name":"Bash","input":{"command":"./record.sh --cases two"}}]}}`, t0.Add(2*time.Minute))
	a.see(`not json`, t0.Add(3*time.Minute))
	did := a.since(t0.Add(30*time.Second), t0.Add(62*time.Minute))
	for _, want := range []string{"21:01 error: FAIL: 2 tests", "21:02 Bash: ./record.sh --cases two", "Still running, for 60 minutes: Bash: ./record.sh --cases two"} {
		if !strings.Contains(did, want) {
			t.Errorf("missing %q in:\n%s", want, did)
		}
	}
	if strings.Contains(did, "Running the suites") {
		t.Errorf("what came before the last check is not read again:\n%s", did)
	}
}

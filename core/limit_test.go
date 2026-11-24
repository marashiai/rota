package core

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestResetTimeIsWhenClaudeSaysItResets(t *testing.T) {
	ams, _ := time.LoadLocation("Europe/Amsterdam")
	now := time.Date(2026, 11, 24, 21, 37, 0, 0, ams) // a Tuesday
	for said, want := range map[string]time.Time{
		"You've hit your 5-hour limit · resets 9:40pm (Europe/Amsterdam)":  time.Date(2026, 11, 24, 21, 41, 0, 0, ams),
		"You've hit your 5-hour limit · resets 9pm (Europe/Amsterdam)":     time.Date(2026, 11, 25, 21, 1, 0, 0, ams), // 9pm has passed today
		"You've hit your weekly limit · resets Fri 8am (Europe/Amsterdam)": time.Date(2026, 11, 27, 8, 1, 0, 0, ams),
		"You've hit your usage limit":                                      now.Add(limitRetry),
	} {
		if got := resetTime(said, now); !got.Equal(want) {
			t.Errorf("%q: %v, want %v", said, got, want)
		}
	}
}

// A shift that meets Claude's usage limit is interrupted, as by an update: what it left running
// goes on, it resumes where it left off, and no shift starts until the limit resets.
func TestAShiftThatMeetsClaudesLimitResumesOnceItResets(t *testing.T) {
	root, team := mailTeam(t)
	marker := "3" + strings.Repeat("1", 6) // a sleep no one else runs
	claude := filepath.Join(root, "claude")
	os.WriteFile(claude, []byte(`#!/bin/bash
head -n1 >/dev/null
printf '{"type":"system","subtype":"init","session_id":"s-limit"}\n'
(set -m; nohup sleep `+marker+` >/dev/null 2>&1 &) # its own process group, as Claude Code's Bash tool runs it
printf '{"type":"result","subtype":"success","is_error":true,"result":"You'"'"'ve hit your 5-hour limit · resets 11:40pm (Europe/Amsterdam)","num_turns":18,"session_id":"s-limit","usage":{}}\n'
`), 0o755)
	team.ClaudeBin = claude
	t.Cleanup(func() { exec.Command("pkill", "-f", "sleep "+marker).Run() })
	res := execShift(context.Background(), team.Config, Job{ID: "j", Agent: "developer", Prompt: "work", RunMinutes: 30}, nil, nil)
	if res.Status != "limited" || !strings.Contains(res.Limit, "5-hour limit") || res.Until.IsZero() {
		t.Fatalf("a shift Claude's limit ended: %s %q %v", res.Status, res.Limit, res.Until)
	}
	out, _ := exec.Command("pgrep", "-f", "sleep "+marker).Output()
	pid := strings.TrimSpace(string(out))
	if pid == "" {
		t.Fatal("what it left running was ended: a shift Claude's limit ends resumes, and needs it")
	}
	a := team.Agent("developer")
	s := team.schedule(time.Now(), nil, nil)
	r, text := team.outcome(context.Background(), context.Background(), a, res, s, nil)
	if r.Status != "interrupted" || r.Session != "s-limit" || !strings.Contains(text, "resumes where it left off") || !strings.Contains(r.Reason, "usage limit") {
		t.Fatalf("recorded as interrupted, to resume: %+v %q", r, text)
	}
	until := team.limitHold(time.Now())
	if !until.Equal(res.Until) {
		t.Fatalf("every shift waits for the reset: %v, want %v", until, res.Until)
	}
	s = team.schedule(time.Now(), nil, nil)
	for _, name := range []string{"developer", "triage"} {
		if ok, why := s.eligible(team.Agent(name)); ok || !strings.Contains(why, "usage limit is reached") {
			t.Errorf("%s starts while the limit is reached: %v %q", name, ok, why)
		}
	}
	if at, _ := s.nextRun(team.Agent("triage")); !at.Equal(until) {
		t.Errorf("the organizer looks again when it resets: %v, want %v", at, until)
	}
	if s = team.schedule(until.Add(time.Second), nil, nil); !s.limit.IsZero() {
		t.Error("the hold outlives the reset")
	}
	if p, _ := os.FindProcess(atoiOr(pid)); p != nil {
		p.Signal(syscall.SIGTERM)
	}
}

func atoiOr(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

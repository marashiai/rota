package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An agent's runs are listed newest first, each with what it said: its line for the human, or, when
// that was nothing, its summary's first line; one that failed says how; each has its report from the
// journal, and its transcript while it is kept.
func TestAnAgentsRunsSayWhatTheySaid(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ntimezone: Europe/Amsterdam\n")
	write(t, filepath.Join(root, "agents", "dev.md"), "---\nenabled: true\n---\ndev\n")
	write(t, filepath.Join(root, "agents", "qa.md"), "---\nenabled: true\n---\nqa\n")
	write(t, filepath.Join(root, spendFile), "time,agent,turns,seconds,session,status,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,weekly_pct_used,weekly_pct_after,session_pct_after,retry,reason\n"+
		"2026-11-08T09:00:00+01:00,dev,20,120,s1,ok,1,2,3,4,0.4,30,5,,\n"+
		"2026-11-08T10:00:00+01:00,qa,5,60,s2,ok,1,2,3,4,,,,,\n"+
		"2026-11-08T11:00:00+01:00,dev,3,30,s3,failed: claude exited 1,1,2,3,4,,,,no,it cannot start\n"+
		"2026-11-08T23:50:00+01:00,dev,9,900,s4,ok,1,2,3,4,,,,,\n")
	write(t, filepath.Join(root, journalDir, "2026-11-08.md"), "# 2026-11-08\n\n"+
		"## 09:00 dev on local | 0.4% of the team pool | 20 turns | 2 min | ok | log logs/2026-11-08/0900-dev.jsonl\n\n"+
		"> Inbox read this shift:\n> - from the human: do it\n\n## For the human\nnone. Nothing changed for users.\n\n## Summary\n- **#673:** answered [your notes](https://x/1) in `one` commit.\n- Rebased #541.\n\n## Notes to teammates\nnone\n\n"+
		"## 10:00 qa on local | n/a of the team pool | 5 turns | 1 min | ok | log logs/2026-11-08/1000-qa.jsonl\n\n## For the human\nThe QA stack is down.\n\n"+
		"## 10:30 qa on local | n/a of the team pool | 1 turns | 0 min | failed | log none\n\n## For the human\nNot this one's.\n\n"+ // its log could not be opened
		"## 23:50 dev on local | n/a of the team pool | 9 turns | 15 min | ok | log logs/2026-11-08/2350-dev.jsonl\n\n## For the human\nMerged **creator#553**.\n") // the day it began, past midnight too
	write(t, filepath.Join(root, logDir, "2026-11-08", "0900-dev.jsonl"), `{"type":"system","subtype":"init","session_id":"s1"}`+"\n")
	team := open(t, root)
	runs := team.AgentRuns("dev", 10)
	if len(runs) != 3 || runs[0].ID != "2026-11-08/2350-dev" || runs[1].ID != "2026-11-08/1100-dev" || runs[2].ID != "2026-11-08/0900-dev" {
		t.Fatalf("its runs, newest first, no other agent's: %+v", runs)
	}
	if runs[0].Title != "Merged creator#553." || runs[0].Kept {
		t.Fatalf("a run past midnight has its entry in the journal of the day it began: %+v", runs[0])
	}
	if rep := team.RunReport("2026-11-08/1000-qa"); !strings.Contains(rep, "The QA stack is down.") || strings.Contains(rep, "Not this one's") {
		t.Fatalf("an entry ends where the next begins, whatever its log: %q", rep)
	}
	if runs[1].Status != "failed: claude exited 1" || runs[1].Title != "" || runs[1].Reason != "it cannot start" {
		t.Fatalf("a failed run says how: %+v", runs[1])
	}
	if r := runs[2]; r.Title != "#673: answered your notes in one commit." || r.Text != "Rebased #541." || !r.Kept || r.Pct == "" || r.Turns != 20 || r.Minutes != 2 {
		t.Fatalf("nothing for the human: its summary, as plain text; its transcript is kept: %+v", r)
	}
	if rep := team.RunReport("2026-11-08/0900-dev"); rep == "" || rep[:2] != "> " || !strings.Contains(rep, "## Summary") || strings.Contains(rep, "10:00 qa") {
		t.Fatalf("its report is its journal entry, up to the next one: %q", rep)
	}
	if team.RunReport("2026-11-08/1100-dev") != "" || team.RunReport("x") != "" {
		t.Fatal("a run the journal has no entry for has no report")
	}
	if _, ok := team.TranscriptOf("2026-11-08/0900-dev"); !ok {
		t.Fatal("its transcript")
	}
	// one run, looked up by itself: also one older than any list, and one that only left a log
	if ri, ok := team.RunInfoOf("2026-11-08/0900-dev"); !ok || ri.Turns != 20 || ri.Title != runs[2].Title || !ri.Kept {
		t.Fatalf("a run by itself: %+v", ri)
	}
	write(t, filepath.Join(root, logDir, "2026-11-07", "2200-dev.jsonl"), `{"type":"system","subtype":"init","session_id":"s0"}`+"\n")
	if ri, ok := open(t, root).RunInfoOf("2026-11-07/2200-dev"); !ok || ri.Status != "unfinished" || !ri.Kept {
		t.Fatalf("a shift that never finished, by its log: %+v", ri)
	}
	for _, id := range []string{"2026-11-08/0800-dev", "2026-11-08/0900-qa", "x", "2026-11-08/0900"} {
		if _, ok := team.RunInfoOf(id); ok {
			t.Fatalf("%s is no run", id)
		}
	}
	if runs := team.AgentRuns("nobody", 5); runs == nil || len(runs) != 0 {
		t.Fatalf("an agent that never ran has an empty list, not none: %#v", runs)
	}
	os.Remove(filepath.Join(root, logDir, "2026-11-08", "0900-dev.jsonl"))
	if _, ok := open(t, root).TranscriptOf("2026-11-08/0900-dev"); ok {
		t.Fatal("a transcript no longer kept")
	}
}

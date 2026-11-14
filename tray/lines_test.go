package tray

import (
	"bytes"
	"image/png"
	"testing"
	"time"

	"rota/core"
)

func TestTheMenuSaysTimesTheShortWay(t *testing.T) {
	now := time.Date(2026, 11, 14, 8, 40, 0, 0, time.Local) // a Saturday
	for at, want := range map[time.Time]string{
		now.Add(27 * time.Minute):                         "09:07",
		time.Date(2026, 11, 15, 5, 0, 0, 0, time.Local):   "tomorrow 05:00",
		time.Date(2026, 11, 13, 15, 50, 0, 0, time.Local): "yesterday 15:50",
		time.Date(2026, 11, 17, 18, 0, 0, 0, time.Local):  "Tue 18:00",
		time.Date(2026, 11, 20, 15, 50, 0, 0, time.Local): "Fri 15:50", // six days ahead, later in the day than now
		time.Date(2026, 11, 21, 8, 0, 0, 0, time.Local):   "21 Nov 08:00",
		time.Date(2026, 12, 2, 9, 0, 0, 0, time.Local):    "2 Dec 09:00",
	} {
		if got := when(at, now); got != want {
			t.Errorf("when(%v) = %q, want %q", at, got, want)
		}
	}
	for d, want := range map[time.Duration]string{20 * time.Second: "just started", 12 * time.Minute: "12 min", 65 * time.Minute: "1 h 5 min"} {
		if got := minutes(d); got != want {
			t.Errorf("minutes(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestAnAgentsLineSaysWhatItDoesInAFewWords(t *testing.T) {
	now := time.Date(2026, 11, 14, 8, 40, 0, 0, time.Local)
	live := &core.Status{Pid: 1, Running: &core.Running{Agent: "developer", Since: now.Add(-12 * time.Minute)}}
	next := core.AgentStatus{NextRun: now.Add(27 * time.Minute)}
	for _, c := range []struct {
		m               core.Member
		as              core.AgentStatus
		enabled, paused bool
		live            *core.Status
		want, look      string
	}{
		{core.Member{State: "working"}, core.AgentStatus{}, true, false, live, "working · 12 min", "working"},
		{core.Member{State: "needs", Line: "Asks you 2 things"}, next, true, false, live, "asks you 2 things", "needs"},
		{core.Member{State: "failed"}, core.AgentStatus{NextRun: time.Date(2026, 11, 14, 21, 38, 0, 0, time.Local)}, true, false, live, "failed · next 21:38", "failed"},
		{core.Member{State: "done"}, core.AgentStatus{LastRun: now.Add(-20 * time.Minute)}, true, false, live, "finished 08:20", "done"},
		{core.Member{State: "idle"}, next, true, false, live, "next 09:07", "idle"},
		{core.Member{State: "idle"}, core.AgentStatus{State: "ready"}, true, false, live, "ready to run", "idle"},
		{core.Member{State: "idle"}, core.AgentStatus{NextRun: time.Date(2026, 11, 13, 15, 50, 0, 0, time.Local)}, true, false, live, "due since yesterday 15:50", "idle"},
		{core.Member{State: "idle"}, next, false, false, live, "off", "off"},
		{core.Member{State: "idle"}, next, true, true, live, "paused", "off"},
		{core.Member{State: "idle"}, core.AgentStatus{}, true, false, nil, "", "off"},
	} {
		if got := doing(c.m, c.as, c.enabled, c.paused, c.live, now); got != c.want {
			t.Errorf("doing(%+v, %+v) = %q, want %q", c.m, c.as, got, c.want)
		}
		if got := look(c.m, c.enabled, c.paused, c.live); got != c.look {
			t.Errorf("look(%+v) = %q, want %q", c.m, got, c.look)
		}
	}
	if got := agentTitle("maintainer", "next 09:07"); got != "maintainer — next 09:07" {
		t.Errorf("agentTitle = %q", got)
	}
	if got := agentTitle("qa", ""); got != "qa" {
		t.Errorf("agentTitle with nothing to say = %q", got)
	}
}

func TestTheMenusFirstLinesSayWhatTheTeamDoes(t *testing.T) {
	now := time.Date(2026, 11, 14, 8, 40, 0, 0, time.Local)
	soon, later := now.Add(27*time.Minute), now.Add(6*time.Hour)
	idle := &core.Status{Pid: 1, Pool: core.Pool{Enforced: true, Quota: 100, Used: 10, Period: "this week"}, Agents: []core.AgentStatus{
		{Name: "stack", Enabled: true, NextRun: later}, {Name: "seo", Enabled: false, NextRun: now.Add(time.Minute)},
		{Name: "maintainer", Enabled: true, NextRun: soon}, {Name: "qa", Enabled: true, Paused: true, NextRun: now.Add(2 * time.Minute)},
	}}
	working := &core.Status{Pid: 1, Running: &core.Running{Agent: "developer", Host: "local", Since: now.Add(-3 * time.Minute)}}
	for _, c := range []struct {
		live    *core.Status
		problem string
		want    string
	}{
		{nil, "", "Not running"},
		{nil, "rota.conf: line 3: unknown setting", "rota.conf: line 3: unknown setting"},
		{working, "", "developer is working · 3 min"},
		{&core.Status{Pid: 1, Paused: true}, "", "Paused: no new shift starts"},
		{idle, "", "Next: maintainer · 09:07"},
		{&core.Status{Pid: 1}, "", "Running"},
	} {
		if got := status(c.live, c.problem, now); got != c.want {
			t.Errorf("status = %q, want %q", got, c.want)
		}
	}
	if got := budget(idle); got != "Team budget: 10% used this week" {
		t.Errorf("budget = %q", got)
	}
	if got := budget(nil); got != "" {
		t.Errorf("budget with no team running = %q, want none", got)
	}
	for _, c := range []struct {
		questions int
		live      *core.Status
		want      string
	}{{0, idle, ""}, {2, idle, " ?2"}, {0, working, " developer"}, {0, nil, " off"}, {1, &core.Status{Pid: 1, Paused: true}, " ?1 paused"}} {
		if got := barTitle(c.questions, c.live); got != c.want {
			t.Errorf("barTitle(%d) = %q, want %q", c.questions, got, c.want)
		}
	}
	as := core.AgentStatus{PeriodPct: 2.2, QuotaPct: 7, LastRun: now.Add(-93 * time.Minute), LastStatus: "ok"}
	if got := share(as, idle); got != "Used 2.2% of its 7.0% share this week" {
		t.Errorf("share = %q", got)
	}
	if got := lastRun(as, now); got != "Last run 07:07 · ok" {
		t.Errorf("lastRun = %q", got)
	}
	if upperFirst("") != "" || lowerFirst("") != "" {
		t.Error("an empty line gained a character")
	}
}

func TestTheDotsAreSharpAndTheOffOneIsARing(t *testing.T) {
	for _, l := range []string{"working", "needs", "failed", "done", "idle", "off"} {
		img, err := png.Decode(bytes.NewReader(dot(l)))
		if err != nil {
			t.Fatalf("%s: %v", l, err)
		}
		if b := img.Bounds(); b.Dx() != 32 || b.Dy() != 32 {
			t.Fatalf("%s is %v, want 32 x 32 pixels: 16 points on a Retina screen", l, b)
		}
		_, _, _, centre := img.At(16, 16).RGBA()
		_, _, _, rim := img.At(16, 10).RGBA() // 6 pixels up: on the ring
		_, _, _, corner := img.At(1, 1).RGBA()
		if corner != 0 || rim == 0 || (l == "off") != (centre == 0) {
			t.Errorf("%s: corner %d, rim %d, centre %d", l, corner, rim, centre)
		}
	}
}

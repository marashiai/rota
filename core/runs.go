package core

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Schedule is when an agent runs, in words: "When there is work, and after developer", "Every
// 20h, 18:00-19:00 on fri".
func (a *Agent) Schedule() string {
	get := func(key string) string {
		if f, ok := AgentField(key); ok {
			return strings.TrimSpace(f.Get(a))
		}
		return ""
	}
	s := "Every " + get("every")
	if a.UntilDone {
		s = "When there is work"
	}
	if h := get("hours"); h != "" && h != "any" {
		s += ", " + h
	}
	if d := get("days"); d != "" {
		s += " on " + d
	}
	if after := get("after"); after != "" {
		s += ", and after " + after
	}
	if a.OwnHours {
		s += " (owns its hours)"
	}
	return s
}

// RunInfo is one of an agent's shifts as its page lists it: when, how it ended, and what it said.
type RunInfo struct {
	ID      string    `json:"id"` // <date>/<HHMM>-<agent>: its transcript's, and its journal entry's log
	At      time.Time `json:"at"`
	Status  string    `json:"status"` // ok, running, or how it ended
	Turns   int       `json:"turns,omitempty"`
	Minutes int       `json:"minutes,omitempty"`
	Pct     string    `json:"pct,omitempty"`    // of the team pool
	Reason  string    `json:"reason,omitempty"` // why a shift that did not finish is retried, or not
	Title   string    `json:"title"`            // what it said: the first line for the human, or of its summary
	Text    string    `json:"text,omitempty"`   // the lines after it, plain
	Kept    bool      `json:"kept"`             // its transcript is still kept
}

// entryHead is a journal entry's heading; one that ends with its shift's log, logs/<id>.jsonl,
// names the shift.
var (
	entryHead = regexp.MustCompile(`^## \d\d:\d\d \S+ on .*\| log (\S+)$`)
	entryLog  = regexp.MustCompile(`^logs/(\S+)\.jsonl$`)
)

// AgentRuns are an agent's last shifts, newest first: the one it works on now, then those in
// spend.csv.
func (t *Team) AgentRuns(agent string, most int) []RunInfo {
	out := []RunInfo{}
	kept := map[string]bool{}
	trs := t.Transcripts(agent)
	for _, tr := range trs {
		kept[transcriptID(tr.Path)] = true
	}
	if len(trs) > 0 && trs[0].Run == nil && t.runningNow(agent, trs[0].Start) { // its newest log, not yet recorded
		out = append(out, RunInfo{ID: transcriptID(trs[0].Path), At: trs[0].Start, Status: "running", Kept: true})
	}
	for i := len(t.Runs) - 1; i >= 0 && len(out) < most; i-- {
		if r := t.Runs[i]; r.Agent == agent {
			out = append(out, t.runInfo(r, kept))
		}
	}
	entries := map[string]string{}
	for _, r := range out {
		if _, read := entries["day:"+r.ID[:10]]; !read {
			entries["day:"+r.ID[:10]] = ""
			t.journalEntries(r.ID[:10], entries)
		}
	}
	for i := range out {
		out[i].Title, out[i].Text = said(entries[out[i].ID])
	}
	return out
}

// RunInfoOf is the shift id (<date>/<HHMM>-<agent>) as an agent's page lists it; false when
// neither spend.csv, its transcript nor the journal knows it.
func (t *Team) RunInfoOf(id string) (RunInfo, bool) {
	if len(id) < len("2006-01-02/1504-x") || id[10] != '/' || id[15] != '-' {
		return RunInfo{}, false
	}
	agent := id[16:]
	ri, found := RunInfo{ID: id}, false
	tr, kept := t.TranscriptOf(id)
	for i := len(t.Runs) - 1; i >= 0; i-- {
		if r := t.Runs[i]; r.Agent == agent && t.runID(r) == id {
			ri, found = t.runInfo(r, map[string]bool{id: kept}), true
			break
		}
	}
	if !found && kept {
		ri, found = RunInfo{ID: id, At: tr.Start, Status: "unfinished", Kept: true}, true
		if trs := t.Transcripts(agent); len(trs) > 0 && transcriptID(trs[0].Path) == id && t.runningNow(agent, tr.Start) {
			ri.Status = "running"
		}
	}
	entry := t.RunReport(id)
	if !found && entry != "" {
		at, _ := time.ParseInLocation("2006-01-02/1504", id[:15], t.Zone.Location)
		ri, found = RunInfo{ID: id, At: at, Status: "ok"}, true
	}
	ri.Title, ri.Text = said(entry)
	return ri, found
}

// runID names a recorded shift as its log does: <date>/<HHMM>-<agent>, at its start.
func (t *Team) runID(r Run) string {
	local := r.Time.In(t.Zone.Location)
	return local.Format("2006-01-02") + "/" + local.Format("1504") + "-" + r.Agent
}

func (t *Team) runInfo(r Run, kept map[string]bool) RunInfo {
	id := t.runID(r)
	ri := RunInfo{ID: id, At: r.Time, Status: r.Status, Turns: r.Turns, Minutes: r.Seconds / 60, Reason: r.Reason, Kept: kept[id]}
	if r.HasPct {
		ri.Pct = r.PctText(t.Config)
	}
	return ri
}

// RunReport is the journal entry of the shift id (<date>/<HHMM>-<agent>): what it said at its
// end, without its heading; "" when the journal has none. The journal of the day it began has it.
func (t *Team) RunReport(id string) string {
	if len(id) < 10 {
		return ""
	}
	entries := map[string]string{}
	t.journalEntries(id[:10], entries)
	return entries[id]
}

// journalEntries adds the entries of the day's journal to by, by their shift's log id.
func (t *Team) journalEntries(day string, by map[string]string) {
	b, err := os.ReadFile(filepath.Join(t.Path(journalDir), day+".md"))
	if err != nil {
		return
	}
	id, lines := "", []string{}
	flush := func() {
		if id != "" {
			by[id] = strings.TrimSpace(strings.Join(lines, "\n"))
		}
	}
	for _, l := range strings.Split(string(b), "\n") {
		if m := entryHead.FindStringSubmatch(l); m != nil { // a shift's entry, whatever its log
			flush()
			id, lines = "", nil
			if lm := entryLog.FindStringSubmatch(m[1]); lm != nil {
				id = lm[1]
			}
			continue
		}
		lines = append(lines, l)
	}
	flush()
}

// said is what a journal entry says first, and what follows, as plain text: what it said for the
// human, unless that was nothing, else its summary.
func said(entry string) (title, rest string) {
	sections := map[string][]string{}
	name := ""
	for _, l := range strings.Split(entry, "\n") {
		if h, ok := strings.CutPrefix(l, "## "); ok {
			name = strings.ToLower(strings.TrimSpace(h))
			continue
		}
		if name != "" && !strings.HasPrefix(l, ">") {
			if l = strings.TrimSpace(plainLine(l)); l != "" {
				sections[name] = append(sections[name], l)
			}
		}
	}
	for _, s := range []string{"for the human", "summary"} {
		lines := sections[s]
		if len(lines) > 0 && s == "for the human" && strings.HasPrefix(strings.ToLower(lines[0]), "none") {
			continue
		}
		if len(lines) > 0 {
			return cut(lines[0], 140), cut(strings.Join(lines[1:], " "), 300)
		}
	}
	return "", ""
}

// linkText is a markdown link, [text](url), whose text stays.
var linkText = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)

// plainLine is a markdown line as plain text: no list mark, emphasis, code marks or link targets.
func plainLine(l string) string {
	l = strings.TrimLeft(strings.TrimSpace(l), "-*+ ")
	l = linkText.ReplaceAllString(l, "$1")
	return strings.NewReplacer("**", "", "__", "", "`", "").Replace(l)
}

// transcriptID names a shift log: <date>/<HHMM>-<agent>.
func transcriptID(path string) string {
	return filepath.Base(filepath.Dir(path)) + "/" + strings.TrimSuffix(filepath.Base(path), ".jsonl")
}

// runningNow says whether the agent's shift that began at start, to the minute its log names,
// runs now, as the organizer says: a log a crash left is not the shift that runs.
func (t *Team) runningNow(agent string, start time.Time) bool {
	st := t.Live()
	if st == nil || st.Running == nil || st.Running.Agent != agent {
		return false
	}
	d := st.Running.Since.Sub(start)
	return d > -2*time.Minute && d < 2*time.Minute
}

// TranscriptOf is the kept transcript of the shift id, if it is kept.
func (t *Team) TranscriptOf(id string) (Transcript, bool) {
	trs := t.Transcripts("")
	i := slices.IndexFunc(trs, func(tr Transcript) bool { return transcriptID(tr.Path) == id })
	if i < 0 {
		return Transcript{}, false
	}
	return trs[i], true
}

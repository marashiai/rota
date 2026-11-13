package core

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// What a shift leaves behind: a row in spend.csv, an entry in the journal, its lines in the
// human's report, notes in its teammates' inboxes, its transcript in logs/, and a commit of
// the team folder.

// Run is one row of spend.csv: one shift, or one attempt at it.
type Run struct {
	Time                           time.Time
	Agent                          string
	Turns                          int
	Seconds                        int
	Session                        string
	Status                         string // ok, pool (stopped when the team pool ran out), or how it failed
	In, Out, CacheRead, CacheWrite int    // exact token counts reported by Claude
	HasPct                         bool
	PctUsed, PctAfter, SessAfter   float64 // weekly window this shift used, and the account after it
	Retry, Reason                  string  // for a shift that did not finish: the organizer's verdict (yes/no) and why
}

// IsRun says whether a shift counts as the agent's run: it finished, or the organizer stopped
// it because the team pool ran out. Anything else is an attempt.
func (r Run) IsRun() bool { return r.Status == "ok" || r.Status == "pool" || r.Status == "hours" }

// SpendColumns are spend.csv's columns; Run.Row is one shift's row.
var SpendColumns = []string{"time", "agent", "turns", "seconds", "session", "status",
	"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens",
	"weekly_pct_used", "weekly_pct_after", "session_pct_after", "retry", "reason"}

// loadRuns reads spend.csv: today's header, then one row per shift, in Row's order.
func loadRuns(c *Config) ([]Run, error) {
	b, err := os.ReadFile(c.Path(spendFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	recs, err := csv.NewReader(strings.NewReader(string(b))).ReadAll() // every row as wide as the header
	if err != nil || len(recs) == 0 {
		return nil, err
	}
	if strings.Join(recs[0], ",") != strings.Join(SpendColumns, ",") {
		return nil, fmt.Errorf("%s: the header is not %s", spendFile, strings.Join(SpendColumns, ","))
	}
	var runs []Run
	for i, rec := range recs[1:] {
		num := func(j int) float64 { f, _ := strconv.ParseFloat(rec[j], 64); return f }
		t, err := time.Parse(time.RFC3339, rec[0])
		if err != nil {
			return nil, fmt.Errorf("%s line %d: %w", spendFile, i+2, err)
		}
		runs = append(runs, Run{Time: t.In(c.Zone.Location), Agent: rec[1], Turns: int(num(2)), Seconds: int(num(3)), Session: rec[4], Status: rec[5],
			In: int(num(6)), Out: int(num(7)), CacheRead: int(num(8)), CacheWrite: int(num(9)),
			HasPct: rec[10] != "", PctUsed: num(10), PctAfter: num(11), SessAfter: num(12), Retry: rec[13], Reason: rec[14]})
	}
	return runs, nil
}

func isDate(s string) bool { _, err := time.Parse("2006-01-02", s); return err == nil }

func (r Run) Row() []string {
	pct := func(v float64) string {
		if !r.HasPct {
			return ""
		}
		return fmt.Sprintf("%.1f", v)
	}
	return []string{r.Time.Format(time.RFC3339), r.Agent, strconv.Itoa(r.Turns), strconv.Itoa(r.Seconds), r.Session, r.Status,
		strconv.Itoa(r.In), strconv.Itoa(r.Out), strconv.Itoa(r.CacheRead), strconv.Itoa(r.CacheWrite),
		pct(r.PctUsed), pct(r.PctAfter), pct(r.SessAfter), r.Retry, r.Reason}
}

// appendRun adds a shift's row to spend.csv, after the header when the file is new.
func appendRun(c *Config, r Run) error {
	f, err := os.OpenFile(c.Path(spendFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	rows := [][]string{r.Row()}
	if st, err := f.Stat(); err == nil && st.Size() == 0 {
		rows = append([][]string{SpendColumns}, rows...)
	}
	w := csv.NewWriter(f)
	w.WriteAll(rows)
	return w.Error()
}

// PctText is the part of the team pool a shift used, as the journal says it.
func (r Run) PctText(c *Config) string {
	if !r.HasPct {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%%", c.OfPool(r.PctUsed))
}

// journalForPrompt lists the last journalDays of journal files, or inlines their text
// (capped at journalMaxChars, newest first) for an agent that cannot read this machine.
func journalForPrompt(c *Config, now time.Time, inline bool) string {
	cutoff := now.AddDate(0, 0, -journalDays).Format("2006-01-02")
	var b strings.Builder
	for _, f := range newestFirst(filepath.Join(c.Path(journalDir), "*.md")) {
		if strings.TrimSuffix(filepath.Base(f), ".md") < cutoff {
			continue
		}
		if !inline {
			fmt.Fprintf(&b, "- %s\n", f)
			continue
		}
		if b.Len() > journalMaxChars {
			b.WriteString("\n(older entries omitted)\n")
			break
		}
		fmt.Fprintf(&b, "\n--- %s ---\n%s\n", filepath.Base(f), readFile(f))
	}
	return orNone(strings.TrimSpace(b.String()))
}

// writeJournal appends a shift's entry: the notes it was briefed with and those it read while
// it ran, quoted, then its final message.
func writeJournal(c *Config, header, text, inbox, during string, now time.Time) error {
	path := filepath.Join(c.Path(journalDir), now.Format("2006-01-02")+".md")
	var b strings.Builder
	if _, err := os.Stat(path); err != nil {
		fmt.Fprintf(&b, "# %s\n", now.Format("2006-01-02"))
	}
	fmt.Fprintf(&b, "\n%s\n\n", header)
	for _, q := range [][2]string{{"Inbox read this shift", inbox}, {"Read while it ran", during}} {
		if q[1] == "" {
			continue
		}
		b.WriteString("> " + q[0] + ":\n")
		for _, line := range strings.Split(strings.TrimSpace(q[1]), "\n") {
			b.WriteString("> " + line + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(strings.TrimSpace(text) + "\n")
	return appendFile(path, b.String())
}

// routeNotes delivers the "@name: message" lines of a final message to the inboxes, a note
// about one pull request or issue ("@name repo#n: message") under that one's heading, and
// records the "@human: question" lines as questions for the human; "@human withdraw <id>"
// withdraws one of the agent's questions that no longer needs an answer. An agent may leave itself
// notes about a pull request or issue, to pick up when it next works on it. A note addressed
// to an until-done agent by another is work for it, and wakes it: the triage's "not ready",
// say. It returns the notes it could not deliver, and why.
func (t *Team) routeNotes(from, text string, now time.Time) []string {
	var undelivered []string
	for _, m := range withdrawLine.FindAllStringSubmatch(text, -1) {
		if err := t.Withdraw(from, strings.Trim(m[1], "`")); err != nil {
			undelivered = append(undelivered, fmt.Sprintf("@human withdraw %s: %v", m[1], err))
		}
	}
	for _, m := range replyLine.FindAllStringSubmatch(text, -1) { // its answer to the human's message
		if err := t.ReplyTo(from, m[1], m[2]); err != nil {
			undelivered = append(undelivered, fmt.Sprintf("@human re %s: %v", m[1], err))
		}
	}
	for _, m := range noteLine.FindAllStringSubmatch(text, -1) {
		to, short, num, msg := strings.ToLower(m[1]), m[2], m[3], strings.TrimSpace(m[4])
		if msg == "" || strings.EqualFold(msg, "none") {
			continue
		}
		if to == "human" { // a question about a pull request or issue names it in full: the answer goes with its notes
			if short != "" {
				if a := t.Agent(from); a != nil {
					if repo, ok := resolveRepo(a.Repos, short); ok {
						short = repo
					}
				}
				msg = short + "#" + num + ": " + msg
			}
			t.ask(from, msg, now)
			continue
		}
		placed := false // some recipient has that repository
		for _, a := range t.Agents {
			if to != a.Name && (to != "all" || a.Name == from) || a.Name == from && short == "" {
				continue
			}
			ref := ""
			if short != "" {
				repo, ok := resolveRepo(a.Repos, short)
				if !ok {
					continue
				}
				ref = repo + "#" + num
			}
			placed = true
			if err := t.addNote(a.Name, ref, from, msg, now); err != nil {
				undelivered = append(undelivered, fmt.Sprintf("@%s: %v", a.Name, err))
				continue
			}
			if s := t.Agent(from); to == a.Name && a.Name != from && a.UntilDone && a.Enabled && (s == nil || !s.UntilDone) { // two until-done agents would take turns forever
				t.askFor(a.Name)
			}
		}
		if !placed && short != "" {
			undelivered = append(undelivered, fmt.Sprintf("@%s %s#%s: no such repository for %s", m[1], short, num, m[1]))
		}
	}
	return undelivered
}

// resolveRepo finds the repository a note names among an agent's: owner/name, name, or the
// end of a name after a dash ("backend" for "shop-backend") when only one has it.
func resolveRepo(repos []string, short string) (string, bool) {
	var names, ends []string
	for _, r := range repos {
		name := r[strings.LastIndex(r, "/")+1:]
		switch {
		case strings.EqualFold(r, short):
			return r, true
		case strings.EqualFold(name, short):
			names = append(names, r)
		case strings.HasSuffix(strings.ToLower(name), "-"+strings.ToLower(short)):
			ends = append(ends, r)
		}
	}
	switch { // a name two repositories share names neither
	case len(names) == 1:
		return names[0], true
	case len(names) == 0 && len(ends) == 1:
		return ends[0], true
	}
	return "", false
}

// openLog creates logs/<date>/<HHMM>-<agent>.jsonl, written through while the shift runs.
func openLog(c *Config, agent string, now time.Time) (string, *os.File) {
	dir := c.Path(filepath.Join(logDir, now.Format("2006-01-02")))
	if os.MkdirAll(dir, 0o755) != nil {
		return "", nil
	}
	path := filepath.Join(dir, now.Format("1504")+"-"+agent+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return "", nil
	}
	return c.Rel(path), f
}

// pruneLogs deletes the day folders of shift logs older than log_keep_days.
func pruneLogs(c *Config) {
	if c.LogKeepDays <= 0 {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -c.LogKeepDays).Format("2006-01-02")
	days, _ := filepath.Glob(filepath.Join(c.Path(logDir), "*"))
	for _, d := range days {
		if name := filepath.Base(d); name < cutoff && isDate(name) {
			os.RemoveAll(d)
		}
	}
}

// commit commits the journal, the inboxes and spend.csv, when the team folder is a git
// repository, and pushes when push is on.
func commit(c *Config, msg string) {
	if _, err := os.Stat(filepath.Join(c.Root, ".git")); err != nil {
		return
	}
	env := os.Environ()
	if name, email, ok := strings.Cut(strings.TrimSuffix(c.GitAuthor, ">"), "<"); ok {
		name = strings.TrimSpace(name)
		env = append(env, "GIT_AUTHOR_NAME="+name, "GIT_AUTHOR_EMAIL="+email, "GIT_COMMITTER_NAME="+name, "GIT_COMMITTER_EMAIL="+email)
	}
	// the bookkeeping of a stopped shift is committed too: a fresh, bounded context
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	sh(ctx, c, env, c.Root, "git", "add", "-A", "--", c.Path(journalDir), c.Path(inboxDir), c.Path(spendFile))
	if _, err := probe(c, c.Root, env, "git", "diff", "--cached", "--quiet"); err == nil {
		return // nothing staged
	}
	if sh(ctx, c, env, c.Root, "git", "commit", "-q", "-m", msg) == nil && c.Push {
		sh(ctx, c, env, c.Root, "git", "push", "-q")
	}
}

func sortRunsByTime(runs []Run) []Run {
	out := append([]Run{}, runs...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}

package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// A shift that stops because the organizer stops (installing a new rota, a restart, a
// logout, even a crash) is not lost. Its Claude session is kept in .resume/<agent>.json from
// its first event until it ends, and kept on when the organizer's stop ended it: the agent's
// next shift, asked for at once (and again when the organizer starts), resumes that session
// where it stopped, on the same machine, in the checkouts as they were, instead of starting
// over. It stays until the resumed shift has started claude. A shift the operator stops, or
// that runs out of time or pool, or ends any other way, is not resumed.

// resumable is an interrupted shift waiting to be resumed.
type resumable struct {
	Session string    `json:"session"`
	Began   time.Time `json:"began"`          // when the shift started, before any interruption
	Minutes int       `json:"minutes"`        // how long it ran before it stopped, all interruptions together
	Host    string    `json:"host,omitempty"` // the joined host it ran on, where its session is
}

// resumeMinutes is the least a resumed shift gets, however little of run_minutes is left.
const resumeMinutes = 15

func (c *Config) resumeFile(agent string) string {
	return c.Path(filepath.Join(resumeDir, agent+".json"))
}

// keepForResume keeps a shift's session, to resume if the organizer's stop ends it.
func (t *Team) keepForResume(agent string, r resumable) error {
	if err := os.MkdirAll(t.Path(resumeDir), 0o755); err != nil {
		return err
	}
	b, _ := json.Marshal(r)
	tmp := t.resumeFile(agent) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, t.resumeFile(agent))
}

// dropResume forgets the session: the shift ended in a way that is not resumed.
func (t *Team) dropResume(agent string) {
	if err := os.Remove(t.resumeFile(agent)); err != nil && !os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, "warn: the finished shift may be resumed again:", err)
	}
}

// askToResume asks for the shifts that resume an interrupted one: at the organizer's start,
// for those the organizer could not record before it went, and when the team is resumed.
// While the team is paused they wait.
func (t *Team) askToResume() {
	if t.Paused() {
		return
	}
	for _, a := range t.Agents {
		if _, ok := t.toResume(a.Name); ok {
			t.askFor(a.Name)
		}
	}
}

// toResume is the interrupted shift the agent's next shift resumes, if any.
func (c *Config) toResume(agent string) (resumable, bool) {
	var r resumable
	b, err := os.ReadFile(c.resumeFile(agent))
	return r, err == nil && json.Unmarshal(b, &r) == nil && r.Session != ""
}

// sessionOf is the Claude session of a shift's stream, named by its first event.
func sessionOf(output string) string {
	for _, line := range strings.Split(output, "\n") {
		var e struct {
			Session string `json:"session_id"`
		}
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &e) == nil && e.Session != "" {
			return e.Session
		}
	}
	return ""
}

// resumeJob turns a shift's job into the resumption of the interrupted one: the same agent
// and settings, its Claude session, the notes that came since, and what is left of
// run_minutes, but no more than window, the minutes left in a window the agent owns (0: none).
// The checkouts are not synced: they hold the work in progress.
func resumeJob(job Job, a *Agent, r resumable, inbox string, window int) Job {
	job.Args = withResume(job.Args, r.Session)
	job.Began = r.Began
	job.Repos = nil
	job.RunMinutes = max(a.RunMinutes-r.Minutes, resumeMinutes)
	if window > 0 {
		job.RunMinutes = min(job.RunMinutes, window)
	}
	notes := ""
	if inbox != "" {
		notes = "\n\nNotes teammates left for you since:\n" + inbox
	}
	job.Prompt = expand(resumePrompt, map[string]string{"{began}": r.Began.Format("15:04"), "{minutes}": strconv.Itoa(r.Minutes),
		"{run_minutes}": strconv.Itoa(job.RunMinutes), "{notes}": notes})
	return job
}

// withResume adds --resume before the variadic flags, which would swallow it.
func withResume(args []string, session string) []string {
	out := make([]string, 0, len(args)+2)
	at := len(args)
	for i, a := range args {
		if a == "--disallowedTools" || a == "--allowedTools" {
			at = i
			break
		}
	}
	out = append(out, args[:at]...)
	out = append(out, "--resume", session)
	return append(out, args[at:]...)
}

const resumePrompt = `Your shift that began at {began} stopped after {minutes} minutes: the organizer was stopped, and claude with it. It resumes now, in the same checkouts. What you ran in the background may still run or have finished: check before you run it again. Check where you were, finish the work, and end with your final message in the sections the brief asks for. You have {run_minutes} minutes.{notes}`

// resumeText is what the journal says of a shift that will resume.
func resumeText(minutes int, why string) string {
	return fmt.Sprintf("Interrupted after %d minutes because %s. The shift resumes where it left off when the organizer runs again.", minutes, why)
}

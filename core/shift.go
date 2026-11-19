package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// A shift: the agent is briefed (its role, the journal, its inbox, its share of the pool),
// claude runs in its work folder here or on a host until it finishes, its time is up, the
// team pool runs out, the window it owns ends or someone stops it, and what it did is recorded.

var (
	errOperatorCancel = errors.New("stopped by the operator")
	errUpdating       = errors.New("rota was updated") // the shift stops where it is and resumes in the new one
	errPoolUsed       = errors.New("the team pool ran out")
	errHoursEnded     = errors.New("its hours ended")
)

// A running shift's use of the weekly window is read more often the closer the team comes to
// the end of its pool: poolPollMax apart with all of the pool left, poolPollMin (as often as
// Claude's usage endpoint is asked at all, usageFloor) with none, in proportion between.
var (
	poolPollMin = time.Minute
	poolPollMax = 20 * time.Minute
)

// nextPoolCheck is how long until the next reading, with used of the team's pool used so far.
func nextPoolCheck(pool, used float64) time.Duration {
	left := 0.0
	if pool > 0 {
		left = min(max((pool-used)/pool, 0), 1)
	}
	return poolPollMin + time.Duration(left*float64(poolPollMax-poolPollMin))
}

// RunShift runs one shift now, for rota run: the named agent, or the next eligible one.
// force runs the agent whatever the schedule and the pool say; dry prints the command and
// the prompt instead.
func (t *Team) RunShift(ctx context.Context, name string, now time.Time, force, dry bool) error {
	var u *Usage
	if !dry { // a dry run touches nothing, not even the usage endpoint
		var err error
		if u, err = t.usageForShift(); err != nil {
			return fmt.Errorf("%s (%w)", heldText, err)
		}
	}
	s := t.schedule(now, u, knownHosts(t.Config))
	a := s.pick()
	if name != "" {
		var err error
		if a, err = t.agent(name); err != nil {
			return err
		}
		if ok, why := s.eligible(a); !ok && !force && !dry {
			return fmt.Errorf("%s is not eligible: %s (use -force)", name, why)
		}
	}
	if a == nil {
		fmt.Printf("%s nothing to run; see: rota status\n", s.now.Format("15:04"))
		return nil
	}
	fmt.Printf("%s shift: %s\n", s.now.Format("15:04"), a.Name)
	if dry {
		job, _, _, err := t.brief(a, s)
		if err != nil {
			return err
		}
		role := a.Role // as the shift gets it: its placeholders filled in
		if i := slices.Index(job.Args, "--append-system-prompt"); i >= 0 && i+1 < len(job.Args) {
			role = job.Args[i+1]
		}
		fmt.Printf("runs on: %s\ngh account: %s\n%s %s\n--- prompt ---\n%s\n--- system prompt ---\n%s\n",
			or(a.Host, "local"), orNone(a.GhUser), t.ClaudeBin, shellJoin(job.Args), job.Prompt, role)
		return nil
	}
	if a.Host != "" {
		return fmt.Errorf("%s runs on host %s, which needs the organizer: rota start", a.Name, a.Host)
	}
	if t.WeekGuessed(u) {
		fmt.Fprintln(os.Stderr, "warn: week_start is claude but no usage data; using Monday")
	}
	return t.shift(ctx, s, a, u, nil, nil, force)
}

// usageForShift reads Claude's windows before a shift; with usage on, no shift starts without them.
func (t *Team) usageForShift() (*Usage, error) {
	if !usageEnabled(t.Config) {
		return nil, nil
	}
	return fetchUsage(t.Config, usageReuse, usageRetries)
}

// brief is the job for the agent's shift: the claude command and the prompt, with the notes
// it is briefed with.
func (t *Team) brief(a *Agent, s *schedule) (Job, map[string]string, []Note, error) {
	remote := a.Host != ""
	where, work := "local", t.Path(a.WorkDir)
	if remote {
		where, work = a.Host, "your current directory on host "+a.Host
	}
	notes := t.Notes(a.Name)
	inbox := t.NotesText(notes)
	tmpl := DefaultBrief
	if b, err := os.ReadFile(t.Path(briefFile)); err == nil {
		tmpl = string(b)
	} else if !os.IsNotExist(err) {
		return Job{}, nil, nil, err
	}
	var repos, others []string
	for _, r := range a.Repos {
		dir := filepath.Join(work, r[strings.LastIndex(r, "/")+1:])
		if remote {
			dir = "./" + r[strings.LastIndex(r, "/")+1:]
		}
		repos = append(repos, dir+" ("+r+")")
	}
	for _, b := range t.Agents {
		if b.Name != a.Name {
			others = append(others, "@"+b.Name)
		}
	}
	minutes, window := a.RunMinutes, 0
	if end, ok := s.ownedEnd(a); ok {
		window = int(math.Ceil(end.Sub(s.now).Minutes()))
		minutes = min(minutes, window)
	}
	vars := map[string]string{
		"{agent}": a.Name, "{date}": s.now.Format("2006-01-02"), "{time}": s.now.Format("15:04"),
		"{root}": t.Root, "{work}": work, "{repos}": orNone(strings.Join(repos, ", ")), "{host}": where,
		"{journal}": journalForPrompt(t.Config, s.now, remote), "{inbox}": orNone(inbox),
		"{agents}": orNone(strings.Join(others, ", ")), "{weekly}": s.shareText(a),
		"{model}": orNone(a.Model), "{run_minutes}": strconv.Itoa(minutes), "{questions}": t.questionsFor(a.Name),
		"{pages}": t.PagesURL(""), "{labels}": t.labelsText(), "{disk}": t.diskText(a),
	}
	role := *a // the role links to the pages with {pages}
	role.Role = expand(a.Role, vars)
	job := Job{ID: randHex(8), Agent: a.Name, Repos: a.Repos, Args: claudeArgs(t.Config, &role),
		Prompt: expand(tmpl, vars), RunMinutes: minutes, GhUser: a.GhUser, LiveLog: t.LiveLog}
	if !remote {
		job.WorkDir, job.EnvFile = a.WorkDir, a.EnvFile
	}
	if r, ok := t.toResume(a.Name); ok {
		job = resumeJob(job, a, r, inbox, window)
	}
	return job, vars, notes, nil
}

// shift runs a's shift, here or on its host through h, and records it. Cancelling ctx stops
// it wherever it runs; mon, when set, is the organizer's live record.
func (t *Team) shift(ctx context.Context, s *schedule, a *Agent, u *Usage, h *hub, mon *monitor, force bool) error {
	lock, err := os.OpenFile(t.Path(lockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		fmt.Println("another shift is running, skipping")
		return nil
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	began := time.Now()

	// from here on a note for the agent is this shift's: in its brief, or sent to it while it runs.
	// Under the inbox's lock: a message the human takes back is gone before the brief, or refused.
	unlock, lockErr := t.lockInbox(a.Name)
	if os.MkdirAll(t.Path(steerDir), 0o755) == nil {
		os.WriteFile(t.steerFile(a.Name), []byte(strconv.Itoa(os.Getpid())), 0o644)
	}
	os.Remove(t.talkFile(a.Name)) // no talk yet: one an earlier shift left is over
	if lockErr == nil {
		unlock()
	}
	defer os.Remove(t.steerFile(a.Name))
	dropped, kept := t.dropUnlisted(a) // no shift works on a repository the agent no longer has
	job, vars, read, err := t.brief(a, s)
	if err != nil {
		return err
	}
	t.answered(a.Name) // a request to run now is answered by this shift; one made from here on asks for the next
	// the session to resume when the organizer's stop ends this shift: the interrupted one's, or
	// this one's from its first event; on the host that has it
	keep, resuming := t.toResume(a.Name)
	if !resuming {
		keep = resumable{Began: s.now}
	}
	host := a.Host
	if resuming && a.Host != "" && keep.Host != "" {
		host = keep.Host
	}
	logPath, logFile := openLog(t.Config, a.Name, s.now)
	where, label := vars["{host}"], a.Name
	if a.Host != "" {
		label = a.Host + "/" + a.Name
	}
	sessionKept := false
	steer := &steering{t: t, agent: a.Name, talks: a.Host == "", read: ids(read)}
	onLine := func(line string) { // every line claude prints, here or forwarded from the host
		if logFile != nil {
			logFile.WriteString(line + "\n")
		}
		if id := sessionOf(line); id != "" && !sessionKept {
			sessionKept, keep.Session = true, id
			t.messagesRead(read, time.Now()) // claude began with its brief: the human's messages in it are read
			if a.Host != "" {
				keep.Host = h.hostOf(job.ID)
			}
			if err := t.keepForResume(a.Name, keep); err != nil {
				fmt.Fprintln(os.Stderr, "warn: this shift cannot resume if it is interrupted:", err)
			}
		}
		for _, e := range ParseLine(line) {
			if e.Kind == "inbox" {
				steer.taken(e.Text)
			}
			if mon != nil {
				mon.step(e) // which tool calls are open: an update stops the shift between them
			}
			if l := e.Summary("tools"); l != "" && mon != nil {
				mon.shiftLine(l)
			}
			if l := e.Summary(t.LiveLog); l != "" {
				fmt.Printf("%s [%s] %s\n", time.Now().Format("15:04"), label, l)
			}
		}
	}
	started := false
	if mon != nil {
		mon.shiftStarted(a.Name, where)
		defer func() { mon.shiftEnded(started) }()
	}
	sctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	if end, ok := s.ownedEnd(a); ok {
		defer time.AfterFunc(end.Sub(s.now), func() { stop(errHoursEnded) }).Stop()
	}
	watched := make(chan struct{})
	if u != nil && !force {
		go func() { t.watchPool(sctx, a.Name, u, s.pool, s.used, stop); close(watched) }()
	} else {
		close(watched)
	}
	// notes that arrive meanwhile go to the shift; the marker tells those who leave them
	notes := make(chan string, 16)
	send := func(text string) bool {
		if a.Host != "" {
			return h.note(job.ID, text)
		}
		select {
		case notes <- text:
			return true
		default:
			return false
		}
	}
	wctx, stopWatch := context.WithCancel(sctx)
	steered := make(chan struct{})
	go func() { steer.watch(wctx, send); close(steered) }()
	var res Result
	if a.Host != "" {
		res = h.dispatch(sctx, host, job, onLine)
	} else {
		res = execShift(sctx, t.Config, job, onLine, notes)
	}
	stopWatch()
	<-steered                      // it sends nothing more
	os.Remove(t.steerFile(a.Name)) // before record looks for notes that did not reach the shift
	during := append(steer.during(), t.endTalk(a.Name)...)
	slices.SortStableFunc(during, func(x, y Note) int { return x.At.Compare(y.At) })
	if logFile != nil {
		if st, _ := logFile.Stat(); st != nil && st.Size() == 0 && strings.TrimSpace(res.Output) != "" {
			logFile.WriteString(res.Output) // a failure message, never streamed
		}
		logFile.Close()
	}
	started = res.Started
	stop(nil) // the shift is over: its pool watch ends before the reading that measures it
	<-watched
	r, text := t.outcome(ctx, sctx, a, res, s, u)
	if len(dropped) > 0 {
		text = strings.TrimRight(text, "\n") + "\n\n_Removed its checkouts of repositories it no longer has: " + strings.Join(dropped, "; ") + "._"
	}
	if len(kept) > 0 {
		text = strings.TrimRight(text, "\n") + "\n\n_Kept, though it no longer has the repository: " + strings.Join(kept, "; ") + ". Delete it by hand once that is not needed._"
	}
	vars["{log}"] = orNone(logPath)
	switch { // a shift the organizer's stop ended resumes; one whose claude never ran still waits
	case r.Status == "interrupted":
	case sessionKept || r.Status == "stopped" || r.Status == "pool" || r.Status == "hours" || r.Status == "timeout":
		t.dropResume(a.Name)
	}
	return t.record(ctx, a, r, res, text, read, during, vars, s.now.Add(time.Since(began)), keep)
}

// watchPool stops a running shift when the team pool runs out: it reads the weekly window as
// often as nextPoolCheck says, and stops the shift once its rise reaches what the pool had left
// (pool, of which before was used when it started). Claude
// reports whole percents, so this is the pool to the nearest percent.
func (t *Team) watchPool(ctx context.Context, agent string, start *Usage, pool, before float64, stop context.CancelCauseFunc) {
	left, used := pool-before, 0.0
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(nextPoolCheck(pool, before+used)):
		}
		u, err := fetchUsage(t.Config, 0, 0)
		if err != nil {
			continue // a missed reading is no reason to stop the shift
		}
		if used = u.Weekly - start.Weekly; used >= left {
			fmt.Printf("%s [%s] team pool used up (%.0f%% of it this shift, %.0f%% was left), stopping the shift\n", time.Now().Format("15:04"), agent, t.OfPool(used), t.OfPool(left))
			stop(errPoolUsed)
			return
		}
	}
}

// outcome reads how the shift ended into its spend.csv row and journal text.
func (t *Team) outcome(ctx, sctx context.Context, a *Agent, res Result, s *schedule, u *Usage) (Run, string) {
	r := Run{Time: s.now, Agent: a.Name, Seconds: res.Seconds}
	cr, haveResult := finalResult(res.Output)
	partial, mins := tail(res.Output, 4000), r.Seconds/60
	text := cr.Result
	switch {
	case res.Status == "stopped" && context.Cause(sctx) == errPoolUsed:
		r.Status, text = "pool", fmt.Sprintf("Stopped after %d minutes: the team pool for %s ran out. Partial output:\n\n%s", mins, s.period, partial)
	case res.Status == "stopped" && context.Cause(sctx) == errHoursEnded:
		end, _ := s.ownedEnd(a)
		r.Status, text = "hours", fmt.Sprintf("Stopped after %d minutes: its hours ended at %s. Partial output:\n\n%s", mins, end.Format("15:04"), partial)
	case res.Status == "stopped" && context.Cause(ctx) != errOperatorCancel:
		why := "the organizer was stopped"
		if context.Cause(ctx) == errUpdating {
			why = "rota was updated"
		}
		r.Status, r.Session, text = "interrupted", sessionOf(res.Output), fmt.Sprintf("Interrupted after %d minutes because %s. Partial output:\n\n%s", mins, why, partial)
		if r.Session != "" {
			text = resumeText(mins, why)
		}
	case res.Status == "stopped":
		r.Status, text = "stopped", fmt.Sprintf("Stopped by the operator after %d minutes. Partial output:\n\n%s", mins, partial)
	case res.Status == "timeout":
		r.Status, text = "timeout", fmt.Sprintf("Killed after %d minutes. Partial output:\n\n%s", a.RunMinutes, partial)
	case res.Status == "lost" || res.Status == "busy" || res.Status == "failed":
		r.Status, text = res.Status, res.Output
	case !haveResult:
		r.Status, text = "no-json", fmt.Sprintf("claude gave no JSON result. Output:\n\n%s", tail(res.Output, 4000))
	default:
		r.Turns, r.Session, r.Status = cr.Turns, cr.Session, "ok"
		r.In, r.Out, r.CacheRead, r.CacheWrite = cr.Usage.In, cr.Usage.Out, cr.Usage.CacheRead, cr.Usage.CacheWrite
		if cr.IsError {
			r.Status = "error:" + orNone(cr.Subtype)
		}
	}
	if u != nil && res.Started { // claude ran: measure what it used
		retries := usageRetries
		if ctx.Err() != nil {
			retries = 0 // stopping: do not wait
		}
		if after, err := fetchUsage(t.Config, measure, retries); err != nil {
			fmt.Fprintln(os.Stderr, "warn: usage after shift:", err)
		} else {
			r.HasPct, r.PctAfter, r.SessAfter = true, after.Weekly, after.Session
			r.PctUsed = max(0, after.Weekly-u.Weekly) // the rolling window may drop old usage meanwhile
		}
	}
	if r.Status != "ok" && !haveResult {
		r.Turns = assistantTurns(res.Output) // what a cut-off shift got done
	}
	return r, text
}

// record writes what the shift leaves behind: for a shift that did not finish, the
// organizer's judgment whether to retry; then spend.csv, the journal, the inboxes, the commit
// and, as of when it ended, the human's report. read are the notes the shift was briefed
// with, during those it read while it ran.
func (t *Team) record(ctx context.Context, a *Agent, r Run, res Result, text string, read, during []Note, vars map[string]string, ended time.Time, keep resumable) error {
	briefed, missed, missedHuman := ids(read), false, false
	for _, n := range t.Notes(a.Name) { // what came during the shift and did not reach it (what did has left)
		if !briefed[n.ID] {
			missed, missedHuman = true, missedHuman || n.From == "human"
		}
	}
	var advice string
	resumes := r.Status == "interrupted" && r.Session != ""
	if resumes {
		r.Retry, r.Reason = "yes", "The organizer stopped (an update, a restart, a logout); the shift resumes where it left off."
	} else if !r.IsRun() {
		attempts := t.schedule(r.Time, nil, nil).stat(a).Attempts
		v := judge(ctx, t.Config, a, r, res, attempts+1)
		r.Retry, r.Reason, advice = "no", v.Reason, v.Note
		if v.Retry {
			r.Retry = "yes"
		}
	}
	if resumes { // first: the organizer may be killed while it finishes the rest
		keep.Session, keep.Minutes = r.Session, keep.Minutes+r.Seconds/60
		if err := t.keepForResume(a.Name, keep); err != nil {
			fmt.Fprintln(os.Stderr, "warn: the interrupted shift cannot resume:", err)
		}
		if !t.Paused() { // paused, it resumes once the team is resumed
			t.askFor(a.Name)
		}
	}
	if err := appendRun(t.Config, r); err != nil {
		return err
	}
	t.Runs = append(t.Runs, r)
	if !r.IsRun() && !resumes { // say what happens next: a retry, or the series is over
		s := t.schedule(r.Time, nil, nil)
		st := s.stat(a)
		next := st.Last.Add(time.Duration(a.Every)).Format("Mon 15:04")
		var note string
		switch {
		case st.Attempts > 0:
			note = fmt.Sprintf("Attempt %d of %d; retry at %s. Organizer: %s", st.Attempts, t.MaxAttempts, s.retryAt(st).Format("Mon 15:04"), r.Reason)
		case r.Retry == "no":
			note = fmt.Sprintf("Not retried: %s This counts as the run; the next one is due %s.", r.Reason, next)
		default:
			note = fmt.Sprintf("Failed after %d attempts; this counts as the run, the next one is due %s.", t.MaxAttempts, next)
		}
		if advice != "" {
			note += " Note left for the agent: " + advice
		}
		text = strings.TrimRight(text, "\n") + "\n\n_" + note + "_"
		fmt.Printf("%s: %s\n", a.Name, note)
	}
	if left := t.leftOver(a); len(left) > 0 && !resumes { // a shift that resumes needs its work where it is
		text = strings.TrimRight(text, "\n") + "\n\n_Left unclean: " + strings.Join(left, "; ") + "._"
	}
	var disk Disk
	if res.Disk != nil {
		disk = Disk{Bytes: *res.Disk, At: ended}
		if err := t.keepDisk(a.Name, disk); err != nil {
			fmt.Fprintln(os.Stderr, "warn: the size of its work folder:", err)
		}
		if overCap(disk.Bytes, a.DiskGB) && !resumes {
			text = strings.TrimRight(text, "\n") + fmt.Sprintf("\n\n_Over its disk cap: its work folder holds %s of %d GB._", GB(disk.Bytes), a.DiskGB)
		}
	}
	vars["{turns}"], vars["{minutes}"] = strconv.Itoa(r.Turns), strconv.Itoa(r.Seconds/60)
	vars["{status}"], vars["{session}"], vars["{pct}"] = r.Status, orNone(r.Session), r.PctText(t.Config)
	if lost := t.routeNotes(a.Name, text, r.Time); len(lost) > 0 {
		text = strings.TrimRight(text, "\n") + "\n\n_Notes not delivered: " + strings.Join(lost, "; ") + "._"
	}
	if !res.Started { // claude never ran: it read nothing
		read = nil
	}
	if err := writeJournal(t.Config, expand(journalHeader, vars), text, t.NotesText(read), t.NotesText(during), r.Time); err != nil {
		return err
	}
	if len(read) > 0 { // the notes it read are done; those that did not reach it wait for the next
		if err := t.dropNotes(a.Name, ids(read)); err != nil {
			fmt.Fprintln(os.Stderr, "warn: the notes the shift read stay in its inbox:", err)
		}
	}
	switch {
	case missedHuman:
		t.wakeForHuman(a.Name)
	case missed:
		t.wakeFor(a.Name)
	}
	if advice != "" { // the organizer's advice waits in the agent's inbox for its next shift
		if err := t.addNote(a.Name, "", "organizer", advice, ended); err != nil {
			fmt.Fprintln(os.Stderr, "warn: the organizer's advice:", err)
		}
	}
	if left := t.leftOver(a); len(left) > 0 && !resumes {
		if err := t.addNote(a.Name, "", "organizer", fmt.Sprintf("your last shift left %s. Before anything else, put each on its default branch with nothing uncommitted, keeping the work in a pull request.",
			strings.Join(left, "; ")), ended); err != nil {
			fmt.Fprintln(os.Stderr, "warn: the note to clean up:", err)
		}
	}
	if res.Disk != nil && overCap(disk.Bytes, a.DiskGB) && !resumes {
		if err := t.addNote(a.Name, "", "organizer", overCapNote(a, disk), ended); err != nil {
			fmt.Fprintln(os.Stderr, "warn: the note to free space:", err)
		}
	}
	t.wake(a, r, text)
	if t.Commit {
		commit(t.Config, expand(commitMessage, vars))
	}
	fmt.Printf("%s: %s on %s, %s of the team pool, %d turns, %d min\n", a.Name, r.Status, vars["{host}"], r.PctText(t.Config), r.Turns, r.Seconds/60)
	if lines, asks := forHuman(text), humanQuestions(text); r.Status == "ok" && (lines != "" || len(asks) > 0) {
		if lines != "" { // the human's mail has it as the agent's message, beside the day's report
			if err := t.keepUpdate(a.Name, lines, vars["{log}"], ended); err != nil {
				fmt.Fprintln(os.Stderr, "warn: the update for the human:", err)
			}
		}
		if err := t.report(ctx, a.Name, lines, asks, ended); err != nil {
			fmt.Fprintln(os.Stderr, "warn: the report:", err)
		}
	}
	return nil
}

func shellJoin(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		if !shellSafe.MatchString(a) {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		q[i] = a
	}
	return strings.Join(q, " ")
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=,-]+$`)

// moreLine is how an until-done agent ends its final message: "More: yes" or "More: no".
var moreLine = regexp.MustCompile(`(?mi)^\W*more:\W*(yes|no)\b`)

// wake asks for the shifts this one leaves work for. An agent with this one in its after is
// asked for first, so it goes before this one's next shift. An until-done agent runs again at once when it says it has more, or when the pool
// stopped it; without "More: yes" it waits for new work. The until-done agents wake after a
// scheduled teammate's shift that opened an issue, which may be work for them, but not after
// the shift of one that runs after them, nor of another until-done agent: they could take turns
// forever. A teammate's note to one wakes it when it is delivered (routeNotes); any other shift
// brings them nothing. A shift that did not finish is retried by the schedule instead.
func (t *Team) wake(a *Agent, r Run, text string) {
	if !r.IsRun() {
		return
	}
	for _, b := range t.Agents {
		if b.Enabled && b.Name != a.Name && slices.Contains(b.After, a.Name) && !slices.Contains(a.After, b.Name) { // two that follow each other would take turns forever
			t.askFor(b.Name)
		}
	}
	if a.UntilDone {
		m := moreLine.FindAllStringSubmatch(text, -1)
		if r.Status == "pool" || r.Status == "hours" || len(m) > 0 && strings.EqualFold(m[len(m)-1][1], "yes") {
			t.askFor(a.Name)
		}
		return
	}
	if !t.openedIssue(a) {
		return
	}
	for _, b := range t.Agents {
		if b.UntilDone && b.Enabled && !slices.Contains(a.After, b.Name) {
			t.askFor(b.Name)
		}
	}
}

// issueCreate and issueURL find an issue a shift opened: gh issue create, and the address it prints.
var (
	issueCreate = regexp.MustCompile(`\bgh\s+issue\s+create\b`)
	issueURL    = regexp.MustCompile(`https://github\.com/[^/\s]+/[^/\s]+/issues/\d+`)
)

// openedIssue says whether the agent's latest shift opened an issue: in its transcript a Bash
// command runs gh issue create, and its result is an issue's address.
func (t *Team) openedIssue(a *Agent) bool {
	for _, tr := range t.Transcripts(a.Name) {
		if tr.Agent != a.Name { // the glob also matches a name that ends in this one
			continue
		}
		b, _ := os.ReadFile(tr.Path)
		creates := map[string]bool{} // the tool calls that ran it, by id
		for _, line := range strings.Split(string(b), "\n") {
			var e struct {
				Message struct {
					Content []struct {
						Type, ID, Name string
						Input          struct{ Command string }
						ToolUseID      string          `json:"tool_use_id"`
						Content        json.RawMessage // a tool result: a string, or parts
						IsError        bool            `json:"is_error"`
					}
				}
			}
			if json.Unmarshal([]byte(line), &e) != nil {
				continue
			}
			for _, c := range e.Message.Content {
				switch {
				case c.Type == "tool_use" && c.Name == "Bash" && issueCreate.MatchString(c.Input.Command):
					creates[c.ID] = true
				case c.Type == "tool_result" && creates[c.ToolUseID] && !c.IsError && issueURL.MatchString(resultText(c.Content)):
					return true
				}
			}
		}
		return false
	}
	return false
}

// originRepo is the owner and name of the GitHub repository a checkout's origin points at, as
// clone_cmd sets it: https://github.com/, ssh://git@github.com/ or an ssh host alias (user@alias:).
var originRepo = regexp.MustCompile(`^(?:https://github\.com/|ssh://git@github\.com/|[A-Za-z0-9_.-]+@[A-Za-z0-9_.-]+:)([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?/?$`)

// dropUnlisted removes the agent's checkouts of repositories that are no longer its own, before
// its shift, so the shift cannot work on them: each folder of its work folder that is a clone as
// rota makes them (named for the repository, its origin on one of the team's GitHub owners) of a
// repository not in its repos. One that holds work found nowhere else is kept, and says why.
// Only a shift on this machine is checked.
func (t *Team) dropUnlisted(a *Agent) (dropped, kept []string) {
	if a.Host != "" {
		return nil, nil
	}
	owners, mine := map[string]bool{}, map[string]bool{}
	for _, r := range append(t.teamRepos(), a.Repos...) {
		if owner, _, ok := strings.Cut(r, "/"); ok {
			owners[strings.ToLower(owner)] = true
		}
	}
	for _, r := range a.Repos {
		mine[strings.ToLower(r)] = true
	}
	work := t.Path(a.WorkDir)
	entries, _ := os.ReadDir(work)
	for _, e := range entries {
		dir := filepath.Join(work, e.Name())
		if fi, err := os.Stat(filepath.Join(dir, ".git")); !e.IsDir() || err != nil || !fi.IsDir() { // a worktree has a .git file
			continue
		}
		out, err := probe(t.Config, dir, nil, "git", "remote", "get-url", "origin")
		m := originRepo.FindStringSubmatch(strings.TrimSpace(string(out)))
		if err != nil || m == nil || !strings.EqualFold(m[2], e.Name()) || !owners[strings.ToLower(m[1])] || mine[strings.ToLower(m[1]+"/"+m[2])] {
			continue
		}
		name := e.Name() + " (" + m[1] + "/" + m[2] + ")"
		if why := unsaved(t.Config, dir); why != "" {
			kept = append(kept, name+", which holds "+why)
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			kept = append(kept, name+", which could not be removed: "+err.Error())
			continue
		}
		fmt.Printf("%s: removed its checkout of %s, no longer one of its repositories\n", a.Name, name)
		dropped = append(dropped, name)
	}
	return dropped, kept
}

// unsaved says what a checkout holds that may exist nowhere else, or "": changes or new files not
// committed, commits no remote has (on a branch, a tag or a detached HEAD), a stash, submodules,
// other worktrees, even those whose folder is gone. A checkout git cannot read holds something.
func unsaved(c *Config, dir string) string {
	var what []string
	if _, err := os.Stat(filepath.Join(dir, ".git", "modules")); err == nil {
		what = append(what, "submodules")
	}
	for _, check := range []struct {
		what string
		args []string
	}{
		{"uncommitted changes", []string{"status", "--porcelain"}},
		{"commits on no remote", []string{"log", "--oneline", "--branches", "--tags", "HEAD", "--not", "--remotes"}},
		{"a stash", []string{"stash", "list"}},
		{"other worktrees", []string{"worktree", "list", "--porcelain"}},
	} {
		out, err := probe(c, dir, nil, "git", check.args...)
		switch {
		case err != nil:
			return "a repository git cannot read (" + err.Error() + ")"
		case check.what == "other worktrees":
			if strings.Count("\n"+string(out), "\nworktree ") > 1 {
				what = append(what, check.what)
			}
		case strings.TrimSpace(string(out)) != "":
			what = append(what, check.what)
		}
	}
	return strings.Join(what, " and ")
}

// leftOver lists the agent's checkouts its shift did not leave clean: with uncommitted changes,
// or on another branch than the default. Only a shift on this machine is checked.
func (t *Team) leftOver(a *Agent) []string {
	if a.Host != "" {
		return nil
	}
	var out []string
	for _, repo := range a.Repos {
		dir := filepath.Join(t.Path(a.WorkDir), repo[strings.LastIndex(repo, "/")+1:])
		if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
			continue
		}
		var what []string
		if b, err := probe(t.Config, dir, nil, "git", "status", "--porcelain"); err == nil && strings.TrimSpace(string(b)) != "" {
			what = append(what, "uncommitted changes")
		}
		head, err1 := probe(t.Config, dir, nil, "git", "rev-parse", "--abbrev-ref", "HEAD")
		def, err2 := probe(t.Config, dir, nil, "git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
		if h, d := strings.TrimSpace(string(head)), strings.TrimPrefix(strings.TrimSpace(string(def)), "origin/"); err1 == nil && err2 == nil && h != d {
			what = append(what, "branch "+h+" checked out, not "+d)
		}
		if len(what) > 0 {
			out = append(out, filepath.Base(dir)+" with "+strings.Join(what, " and "))
		}
	}
	return out
}

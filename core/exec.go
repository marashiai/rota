package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// A shift runs as a child process: claude -p in the agent's work folder, its repositories
// synced first. Every child runs in a process group of its own, so stopping it stops
// everything it started, and every wait on the outside world is bounded.

// Job is one shift as handed to whoever executes it: the organizer itself for a
// local agent, or a joined host. It carries everything the executor needs except
// secrets, which stay on the executing machine.
type Job struct {
	ID         string    `json:"id"`
	Agent      string    `json:"agent"`
	Repos      []string  `json:"repos,omitempty"`
	Args       []string  `json:"args"`
	Prompt     string    `json:"prompt"`
	RunMinutes int       `json:"run_minutes"`
	WorkDir    string    `json:"work_dir,omitempty"` // relative to the executor's root; default work/<agent>
	EnvFile    string    `json:"env_file,omitempty"` // relative to the executor's root; default secrets/<agent>.env
	GhUser     string    `json:"gh_user,omitempty"`  // GH_TOKEN comes from this account's gh login on the executor
	LiveLog    string    `json:"live_log,omitempty"` // off | tools | all: what the executor reports live
	Began      time.Time `json:"began,omitzero"`     // a resumed shift: when the shift it resumes began
	Extend     bool      `json:"extend,omitempty"`   // at run_minutes, checked: real progress goes on (check.go)
}

// Result is what comes back: claude's raw stdout plus how the shift ended.
type Result struct {
	ID      string `json:"id"`
	Output  string `json:"output"`
	Status  string `json:"status"`            // done | failed | stopped | timeout | lost | busy
	Checked string `json:"checked,omitempty"` // a timeout: why rota's check ended it (extend)
	Seconds int    `json:"seconds"`
	Started bool   `json:"started"`        // claude was actually launched (so some of the pool may have been used)
	Disk    *int64 `json:"disk,omitempty"` // the work folder's size in bytes once claude ended; nil: not measured
}

// execShift runs one shift on this machine, calling onLine for every line claude
// prints. Cancelling ctx kills claude and everything it started and reports the
// shift as stopped. Each message from notes reaches claude after its current step,
// until it gives its result.
func execShift(ctx context.Context, c *Config, job Job, onLine func(string), notes <-chan string) Result {
	res := Result{ID: job.ID}
	dir, envFile := job.WorkDir, job.EnvFile
	if dir == "" { // a job from the organizer for this host: its own work folder and secrets
		dir = filepath.Join(workDir, job.Agent)
	}
	if envFile == "" {
		envFile = filepath.Join(secretsDir, job.Agent+".env")
	}
	work := c.Path(dir)
	if err := os.MkdirAll(work, 0o755); err != nil {
		return failed(res, err)
	}
	env, err := loadEnv(c, envFile)
	if err != nil {
		return failed(res, err)
	}
	if job.WorkDir != "" { // a shift on the organizer's machine: rota ask and rota wait reach its team
		env = append(env, "ROTA_AGENT="+job.Agent, "ROTA_ROOT="+c.Root, "ROTA_CONFIG="+c.File,
			"ROTA_PARK_BY="+time.Now().Add(time.Duration(job.RunMinutes)*time.Minute-parkMinutes*time.Minute).Format(time.RFC3339)) // rota wait stops waiting then
	}
	if job.GhUser != "" && !hasEnv(env, "GH_TOKEN") {
		out, err := probe(c, work, env, "gh", "auth", "token", "--user", job.GhUser)
		if err != nil || len(strings.TrimSpace(string(out))) == 0 {
			return failed(res, fmt.Errorf("gh is not logged in as %s on this machine (gh auth login): %v", job.GhUser, err))
		}
		env = append(env, "GH_TOKEN="+strings.TrimSpace(string(out)))
	}
	env = append(env, "GLOSS_AUTHOR="+job.Agent) // the name on the notes it writes on pull requests with gloss
	if c.SyncRepos && len(job.Repos) > 0 {
		sctx, cancel := context.WithTimeout(ctx, syncTimeout)
		for _, repo := range job.Repos {
			if err := syncRepo(sctx, c, env, work, repo); err != nil {
				cancel()
				if ctx.Err() != nil {
					res.Status = "stopped"
					return res
				}
				if sctx.Err() == context.DeadlineExceeded {
					err = fmt.Errorf("timed out after %s", syncTimeout)
				}
				res.Status, res.Output = "failed", "repository sync failed, claude was not started: "+err.Error()
				return res
			}
		}
		cancel()
	}
	if ctx.Err() != nil {
		res.Status = "stopped"
		return res
	}
	for _, repo := range job.Repos {
		if err := placeRepoEnv(c, job.Agent, work, repo); err != nil {
			fmt.Fprintln(os.Stderr, "warn:", err)
		}
	}
	// Claude Code's auto memory keeps what the agent learns in the team folder, beside its work,
	// not under ~/.claude, where the human's own sessions live
	memory := c.Path(filepath.Join(memoryDir, job.Agent))
	if err := os.MkdirAll(memory, 0o755); err != nil {
		return failed(res, err)
	}
	settings, _ := json.Marshal(map[string]string{"autoMemoryDirectory": memory})
	args := append([]string{"--settings", string(settings)}, job.Args...) // first: the flags at the end take every word after them
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// The time limit: at run_minutes the shift ends, or, with extend, rota checks it, and one that
	// makes real progress goes on for another run_minutes, checked again.
	var timedOut atomic.Bool
	var checked string
	done := &activity{}
	go func() {
		limit, from, worked := time.Duration(job.RunMinutes)*runMinute, time.Now(), 0
		timer := time.NewTimer(limit)
		defer timer.Stop()
		for {
			select {
			case <-cctx.Done():
				return
			case <-timer.C:
			}
			worked += job.RunMinutes
			if job.Extend {
				now := time.Now()
				goOn, why := checkShift(cctx, c, job.Agent, worked, done.since(from, now))
				if cctx.Err() != nil {
					return
				}
				if goOn {
					logf("%s's shift checked after %s: it goes on, %s", job.Agent, minutesText(float64(worked)), why)
					from = now
					timer.Reset(limit)
					continue
				}
				logf("%s's shift checked after %s: it ends, %s", job.Agent, minutesText(float64(worked)), why)
				checked = why
			}
			timedOut.Store(true)
			cancel()
			return
		}
	}()
	cmd := command(cctx, c, work, env, c.ClaudeBin, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return failed(res, err)
	}
	// the prompt first, then the notes; what claude prints before it takes the prompt is not its end
	in := &shiftInput{w: stdin, waiting: []string{job.Prompt}}
	go func() {
		in.write(job.Prompt)
		for {
			select {
			case text := <-notes:
				if text == stopTurn {
					in.interrupt()
					continue
				}
				in.send(text)
			case <-cctx.Done():
				return
			}
		}
	}()
	go func() { // a grace ends with no line
		tick := time.NewTicker(settleEvery)
		defer tick.Stop()
		for !in.isClosed() {
			select {
			case now := <-tick.C:
				in.settle(now)
			case <-cctx.Done():
				return
			}
		}
	}()
	var out bytes.Buffer
	lw := newLineWriter(&out, func(line string) {
		done.see(line, time.Now())
		in.see(line) // once claude has finished, its input closes, and it exits
		if onLine != nil {
			onLine(line)
		}
	})
	cmd.Stdout, cmd.Stderr = lw, os.Stderr
	start := time.Now()
	runErr := runTracked(cmd)
	in.close()
	res.Started = cmd.Process != nil // claude was launched
	lw.flush()
	res.Seconds = int(time.Since(start).Seconds())
	res.Output = out.String()
	switch {
	case ctx.Err() != nil:
		res.Status = "stopped"
	case timedOut.Load():
		res.Status, res.Checked = "timeout", checked
	default:
		res.Status = "done"
		if runErr != nil && out.Len() == 0 {
			res.Output = fmt.Sprintf("claude exited: %v", runErr)
		}
	}
	if res.Started && !(res.Status == "stopped" && resumes(context.Cause(ctx))) {
		since := start // what the shift it resumes left running, it ends too
		if !job.Began.IsZero() && job.Began.Before(since) {
			since = job.Began
		}
		if ended := endLeftBehind(work, since); len(ended) > 0 {
			logf("%s's shift left running, now ended: %s", job.Agent, strings.Join(ended, "; "))
		}
	}
	if res.Started && ctx.Err() == nil { // a shift being stopped does not wait for the measurement
		if n, err := folderSize(ctx, c, work); err != nil {
			fmt.Fprintf(os.Stderr, "warn: %s's work folder could not be measured: %v\n", job.Agent, err)
		} else {
			res.Disk = &n
		}
	}
	return res
}

// folderSize is what dir takes on disk, in bytes, as du counts it: each hard link once.
func folderSize(ctx context.Context, c *Config, dir string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, diskTimeout)
	defer cancel()
	cmd := command(ctx, c, dir, nil, "du", "-sk", ".")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := runTracked(cmd)
	if ctx.Err() == context.DeadlineExceeded {
		return 0, fmt.Errorf("du timed out after %s", diskTimeout)
	}
	f := strings.Fields(out.String())
	if len(f) == 0 {
		return 0, fmt.Errorf("du: %v", err)
	}
	kb, perr := strconv.ParseInt(f[0], 10, 64)
	if perr != nil {
		return 0, fmt.Errorf("du printed %q: %v", strings.TrimSpace(out.String()), err)
	}
	return kb * 1024, nil // du's exit status also reports files it could not read; what it counted stands
}

// shiftInput is a running claude's input: one stream-json user message per line, from one
// writer, until it is closed. Closing does not wait for a write: a blocked one fails.
//
// A shift's input closes once claude has finished: its turn has ended with the final message, the
// one "For the human", and it has taken every message rota sent. A turn that ends otherwise is not
// the end: claude goes on with a message it has not taken yet, waits for a task of its running in
// the background, or begins a turn by itself when one has finished, as the turn that, first thing
// in a resumed session, tells of the tasks the interrupted one left running. Its input closed, the
// shift would hear nothing more: not the human, not a stop. With its input closed, claude stops
// what it still runs in the background, and exits.
type shiftInput struct {
	mu      sync.Mutex
	w       io.WriteCloser
	closed  bool
	cut     int       // turns cut off (interrupt) whose result has not come yet: it does not end the shift
	waiting []string  // the messages sent that claude has not echoed: it has not taken them yet
	idle    bool      // claude's last turn has ended, and no other has begun
	since   time.Time // when it ended, or a message was sent since
	final   bool      // it ended with the final message
	tasks   int       // claude's tasks running in the background
}

// echoGrace is how long claude may take to echo a message once its turn has ended: it takes one
// at once, so one it does not echo it took without, and it has finished.
var echoGrace = time.Minute

// turnGrace is how long claude may take to begin a turn by itself once one has ended without the
// final message, and nothing runs in the background: a task that has just ended begins one.
var turnGrace = 10 * time.Second

// settleEvery is how often a shift looks whether a grace has ended.
var settleEvery = time.Second

// stopTurn, sent to a running shift among its notes, cuts its turn off at once, as the human's stop
// in the team chat does (chatedit.go): what claude does stops, as with Esc, and the shift goes on
// with what is sent next.
const stopTurn = "\x00rota: stop the turn"

// interrupt cuts claude's turn off: it ends with an error_during_execution result, and claude takes
// the next message.
func (in *shiftInput) interrupt() {
	in.mu.Lock()
	if in.closed {
		in.mu.Unlock()
		return
	}
	in.cut++
	in.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"type": "control_request", "request_id": "rota-stop-" + randHex(4), "request": map[string]string{"subtype": "interrupt"}})
	in.w.Write(append(b, '\n'))
}

// see follows a line of claude's output, and closes its input once claude has finished.
func (in *shiftInput) see(line string) {
	var e struct {
		Type    string            `json:"type"`
		Subtype string            `json:"subtype"`
		Result  string            `json:"result"`
		IsError bool              `json:"is_error"`
		Tasks   []json.RawMessage `json:"tasks"`
		Parent  string            `json:"parent_tool_use_id"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal([]byte(line), &e) != nil || e.Parent != "" { // a subagent's, or a tool's progress: not claude's turn
		return
	}
	in.mu.Lock()
	switch {
	case e.Type == "result" && in.cut > 0 && e.Subtype == "error_during_execution": // a turn cut off: what rota sends next follows
		in.cut--
	case e.Type == "result": // an error ends the shift, as the final message does
		in.idle, in.since, in.final = true, time.Now(), e.IsError || forHumanHeading.MatchString(e.Result)
	case e.Type == "system" && e.Subtype == "init": // a turn begins: a stop that came between turns cut none off
		in.idle, in.cut = false, 0
	case e.Type == "assistant":
		in.idle = false
	case e.Type == "user":
		in.idle = false
		for _, text := range userTexts(e.Message.Content) { // its echo: it has taken the message
			if i := slices.IndexFunc(in.waiting, func(w string) bool { return strings.TrimSpace(w) == strings.TrimSpace(text) }); i >= 0 {
				in.waiting = slices.Delete(in.waiting, i, i+1)
			}
		}
	case e.Type == "system" && e.Subtype == "background_tasks_changed":
		in.tasks = len(e.Tasks)
		if in.idle && in.tasks == 0 {
			in.since = time.Now() // the task's end may begin a turn
		}
	}
	in.mu.Unlock()
	in.settle(time.Now())
}

// settle closes the input if claude has finished, or as good as: a grace has ended. It is closed
// at once, so a message sent meanwhile is not taken for one claude has.
func (in *shiftInput) settle(now time.Time) {
	in.mu.Lock()
	idle := now.Sub(in.since)
	var done bool
	switch {
	case in.closed || !in.idle:
	case len(in.waiting) > 0: // claude takes it at once, unless it waits for its task
		done = (in.final || in.tasks == 0) && idle >= echoGrace
	case in.final: // what still runs in the background stops
		done = true
	case in.tasks == 0:
		done = idle >= turnGrace
	}
	in.closed = in.closed || done
	in.mu.Unlock()
	if done {
		in.w.Close()
	}
}

func (in *shiftInput) isClosed() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.closed
}

// userTexts are the texts of a user message's content: a string, or parts.
func userTexts(content json.RawMessage) []string {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return []string{s}
	}
	var parts []struct{ Type, Text string }
	json.Unmarshal(content, &parts)
	var texts []string
	for _, p := range parts {
		if p.Type == "text" {
			texts = append(texts, p.Text)
		}
	}
	return texts
}

// send writes a message, unless the input is closed; a claude that has exited reads nothing.
func (in *shiftInput) send(text string) {
	in.mu.Lock()
	closed := in.closed
	if !closed {
		in.waiting = append(in.waiting, text) // before it is written: claude may echo it at once
		if in.idle {
			in.since = time.Now()
		}
	}
	in.mu.Unlock()
	if !closed {
		in.write(text)
	}
}

// write writes a message.
func (in *shiftInput) write(text string) {
	b, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user",
		"content": []map[string]string{{"type": "text", "text": text}}}})
	in.w.Write(append(b, '\n'))
}

func (in *shiftInput) close() {
	in.mu.Lock()
	closed := in.closed
	in.closed = true
	in.mu.Unlock()
	if !closed {
		in.w.Close()
	}
}

// isResult says whether a line of claude's output is its result.
func isResult(line string) bool {
	var e struct {
		Type string `json:"type"`
	}
	return strings.Contains(line, `"result"`) && json.Unmarshal([]byte(line), &e) == nil && e.Type == "result"
}

// lineWriter stores everything written and hands each complete line to onLine.
type lineWriter struct {
	buf    *bytes.Buffer
	part   []byte
	onLine func(string)
}

func newLineWriter(buf *bytes.Buffer, onLine func(string)) *lineWriter {
	return &lineWriter{buf: buf, onLine: onLine}
}

// flush hands over a last line that ended without a newline.
func (w *lineWriter) flush() {
	if w.onLine != nil {
		if line := strings.TrimSpace(string(w.part)); line != "" {
			w.onLine(line)
		}
	}
	w.part = nil
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	if w.onLine == nil {
		return len(p), nil
	}
	w.part = append(w.part, p...)
	for {
		i := bytes.IndexByte(w.part, '\n')
		if i < 0 {
			break
		}
		if line := strings.TrimSpace(string(w.part[:i])); line != "" {
			w.onLine(line)
		}
		w.part = w.part[i+1:]
	}
	return len(p), nil
}

func hasEnv(env []string, key string) bool {
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") && len(kv) > len(key)+1 {
			return true
		}
	}
	return false
}

func failed(res Result, err error) Result {
	res.Status, res.Output = "failed", "shift could not start: "+err.Error()
	return res
}

// loadEnv is the process environment plus secrets/common.env and one agent env file; {root}
// in a value is the team folder on this machine, so a setting can point into it anywhere.
func loadEnv(c *Config, envFile string) ([]string, error) {
	env := cleanEnv(os.Environ())
	for _, f := range []string{c.Path(filepath.Join(secretsDir, commonEnv)), c.Path(envFile)} {
		b, err := os.ReadFile(f)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for n, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if !strings.Contains(line, "=") {
				return nil, fmt.Errorf("%s line %d: expected KEY=VALUE", f, n+1)
			}
			env = append(env, strings.ReplaceAll(line, "{root}", c.Root))
		}
	}
	return env, nil
}

// capabilities reports what this machine can offer the organizer: which of the requested
// gh logins exist here, and whether (and as whom) claude is logged in.
func capabilities(c *Config, ghUsers []string) *Caps {
	caps := &Caps{GhUsers: []string{}}
	for _, u := range ghUsers {
		if out, err := probe(c, "", os.Environ(), "gh", "auth", "token", "--user", u); err == nil && len(strings.TrimSpace(string(out))) > 0 {
			caps.GhUsers = append(caps.GhUsers, u)
		}
	}
	caps.ClaudeLoggedIn, caps.ClaudeEmail = claudeLogin(c)
	return caps
}

// claudeLogin asks the claude CLI whether it is logged in, and as which account.
func claudeLogin(c *Config) (bool, string) {
	out, err := probe(c, "", os.Environ(), c.ClaudeBin, "auth", "status")
	if err != nil {
		return false, ""
	}
	var v struct {
		LoggedIn bool   `json:"loggedIn"`
		Email    string `json:"email"`
	}
	if json.Unmarshal(out, &v) != nil {
		return false, ""
	}
	return v.LoggedIn, v.Email
}

// noMemory is env for a one-off claude call rota makes itself, such as a summary: it learns
// nothing to keep, so Claude Code's auto memory is off and writes nothing under ~/.claude.
func noMemory(env []string) []string {
	return append(slices.Clone(env), "CLAUDE_CODE_DISABLE_AUTO_MEMORY=1")
}

func claudeArgs(c *Config, a *Agent) []string {
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--replay-user-messages", "--permission-mode", a.PermissionMode} // a message is echoed once it enters the conversation
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}
	if a.Effort != "" {
		args = append(args, "--effort", a.Effort)
	}
	if a.Role != "" {
		args = append(args, "--append-system-prompt", a.Role)
	}
	args = append(args, c.ExtraArgs...)
	args = append(args, a.ExtraArgs...)
	// variadic flags go last so they cannot swallow other arguments
	if a.DisallowedTools != "" {
		args = append(args, "--disallowedTools", a.DisallowedTools)
	}
	tools := "Bash(rota say *),Bash(rota listen),Bash(rota listen *)" // every agent may talk with the human in the team chat
	if a.Tools != "" {
		tools = a.Tools + "," + tools
	}
	return append(args, "--allowedTools", tools)
}

// placeRepoEnv puts the agent's setup for a repository, secrets/<agent>/<name>.env, into its
// checkout as .env, {work} replaced by the agent's work folder on this machine. A repository's
// local setup (its own endpoints and data folder for a stack the agent runs) belongs to the
// agent, not to the checkout: it moves with the agent to another machine.
func placeRepoEnv(c *Config, agent, work, repo string) error {
	name := repo[strings.LastIndex(repo, "/")+1:]
	dir := filepath.Join(work, name)
	if _, err := os.Stat(dir); err != nil {
		return nil // not checked out here
	}
	dst := filepath.Join(dir, ".env")
	b, err := os.ReadFile(c.Path(filepath.Join(secretsDir, agent, name+".env")))
	if os.IsNotExist(err) { // no setup (any more): a copy rota placed goes
		if old, err := os.ReadFile(dst); err == nil && strings.HasPrefix(string(old), placedMark) {
			return os.Remove(dst)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if fi, err := os.Lstat(dst); err == nil && !fi.Mode().IsRegular() {
		return fmt.Errorf("%s's .env for %s: %s is not a plain file; not written", agent, name, dst)
	}
	if _, err := probe(c, dir, nil, "git", "ls-files", "--error-unmatch", ".env"); err == nil {
		return fmt.Errorf("%s's .env for %s: git tracks .env there; not written, so it is never committed", agent, name)
	}
	text := placedMark + "\n" + strings.ReplaceAll(string(b), "{work}", work)
	if old, err := os.ReadFile(dst); err == nil && string(old) == text {
		return nil
	}
	tmp := dst + ".rota-tmp" // written whole, and a link in its place is replaced, never followed
	if err := os.WriteFile(tmp, []byte(text), 0o600); err != nil {
		return fmt.Errorf("%s's .env for %s: %w", agent, name, err)
	}
	return os.Rename(tmp, dst)
}

// placedMark starts a .env that rota placed from an agent's secrets.
const placedMark = "# placed by rota from the agent's secrets; edit it there, not here"

// command builds a child in its own process group, so cancelling ctx kills it and
// everything it spawned (SIGTERM, then SIGKILL after killGrace).
func command(ctx context.Context, c *Config, dir string, env []string, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env = dir, env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = killGrace
	return cmd
}

// syncRepo clones owner/name into work/<name> with clone_cmd, or fast-forwards the
// default branch of an existing clone. Any failure fails the shift before claude starts.
func syncRepo(ctx context.Context, c *Config, env []string, work, repo string) error {
	name := repo[strings.LastIndex(repo, "/")+1:]
	dir := filepath.Join(work, name)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		words := strings.Fields(expand(c.CloneCmd, map[string]string{"{repo}": repo, "{dir}": dir, "{name}": name}))
		if len(words) == 0 {
			return fmt.Errorf("clone_cmd is empty")
		}
		if err := sh(ctx, c, env, work, words[0], words[1:]...); err != nil {
			return fmt.Errorf("clone %s: %w", repo, err)
		}
		return nil
	}
	if err := sh(ctx, c, env, dir, "git", "fetch", "-q", "origin"); err != nil {
		return fmt.Errorf("fetch %s: %w", repo, err)
	}
	head, _ := probe(c, dir, env, "git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	branch := strings.TrimPrefix(strings.TrimSpace(string(head)), "origin/")
	if branch == "" {
		return fmt.Errorf("%s: cannot tell the default branch", repo)
	}
	if err := sh(ctx, c, env, dir, "git", "checkout", "-q", branch); err != nil {
		return fmt.Errorf("%s: checkout %s (did an agent leave uncommitted changes there?): %w", repo, branch, err)
	}
	if err := sh(ctx, c, env, dir, "git", "pull", "-q", "--ff-only"); err != nil {
		return fmt.Errorf("%s: pull %s: %w", repo, branch, err)
	}
	return nil
}

func sh(ctx context.Context, c *Config, env []string, dir, name string, args ...string) error {
	cmd := command(ctx, c, dir, env, name, args...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := runTracked(cmd); err != nil {
		if ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "warn: %s %s: %v\n", name, strings.Join(args, " "), err)
		}
		return err
	}
	return nil
}

// probe runs a short external command with the probe timeout, so a stuck keychain
// prompt, a hung gh or a slow login shell can never stall the organizer.
func probe(c *Config, dir string, env []string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	cmd := command(ctx, c, dir, env, name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := runTracked(cmd)
	if ctx.Err() == context.DeadlineExceeded {
		return out.Bytes(), fmt.Errorf("%s timed out after %s", name, probeTimeout)
	}
	return out.Bytes(), err
}

// children are the process groups KillChildren kills outright, for a second Ctrl-C.
var (
	childMu  sync.Mutex
	children = map[int]bool{}
)

func runTracked(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	childMu.Lock()
	children[cmd.Process.Pid] = true
	childMu.Unlock()
	err := cmd.Wait()
	// the direct child is gone; sweep anything it left behind in its process group
	syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	childMu.Lock()
	delete(children, cmd.Process.Pid)
	childMu.Unlock()
	return err
}

func KillChildren() {
	childMu.Lock()
	defer childMu.Unlock()
	for pid := range children {
		syscall.Kill(-pid, syscall.SIGKILL)
	}
}

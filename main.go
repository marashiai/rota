// rota is the smallest possible AI maintenance team: one command, a team folder of
// Markdown files, Claude Code, git and gh. This is the command line; the team itself is
// package core, and the console, the web pages and the menu bar are built on it.
package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"rota/core"
	"rota/icon"
	"rota/tray"
	"rota/tui"
	"rota/web"
)

// version is set by release.sh (-ldflags "-X main.version=v0.1.0").
var version = "dev"

const usageText = `usage: rota <command> [agent] [flags]   (flags may also come first)
       rota                 in a terminal: the console (rota ui)

commands:
  ui            the console: reports, status, journal, transcripts, inbox, spend, agents and settings, with every control
  init          create the team folder (~/.rota) from the starter files
  start         the organizer: run shifts as they come due until this process is closed; closing it stops everything
  run [agent]   run one shift now: the next eligible agent, or the named one (-force skips the schedule and the pool)
  status        every agent's state, share of the team pool, last and next run
  pause | unpause [agent]   stop picking new shifts, the team's or one agent's (the running one
                finishes; Run now still runs a paused agent) | resume
  kick [agent]  run the next shift now; with an agent, it runs ahead of every agent you did not ask
                for (its interval and hours skipped; the team pool still applies)
  cancel        stop the running shift; the organizer goes on
  stop          stop the organizer (SIGTERM, like Ctrl-C)
  enable <agent...>  | disable <agent...>   turn agents on or off
  log <agent> [n]   the transcript of the agent's latest (or n-th latest) shift
  attach <agent>    follow the agent's running shift
  pair          print a one-time code with which a host may join this organizer
  join <organizer> [code]   this machine runs shifts for the organizer (its Tailscale name or address)
  web           serve the pages on localhost while the organizer is off
  tray          the macOS menu bar
  usage         what Claude reports about the subscription's rate windows
  config        every setting in force
  version       which build this is, and where it lives
  brief         the built-in shift brief (save it as brief.md to change it)

in an agent's shift:
  labels            create or correct the team's labels (name, color, description) in every team
                    repository; the organizer does it when it starts and when the team changes
  merge <repo>#<n> <head commit>
                    squash-merge a pull request at the commit checked, unless it carries the
                    hold_label, once what it depends on is merged or closed without that label,
                    and it is neither stacked on another nor behind its base; then rebase those
                    stacked on it onto the merge, so each lists only its own commits
  delete-branch <repo> <branch>
                    delete your own remote branch (<agent>/<topic>) once every pull request
                    from it is merged or closed, its tip is the head of one of them, and no
                    open pull request is based on it
  review-order "<repo>#<n> <why>"...
                    the order in which the human reviews the open pull requests, most worth it
                    first, each with why it comes there; it replaces the last one, and none
                    clears it. The Reviews tab lists each after the open pull requests it waits
                    for, whatever the order says
  ask <question> [--subject "<a few words>"] [--option "<answer>"]... [--recommend "<answer>"]
        [--step "<step>"]... [--importance high|low] [--about <repo>#<n>]
                    ask the human now: they are notified; prints the question's id. Options are
                    the answers they tap, steps make a to-do they tick and mark done
  wait <id>         wait up to 8 minutes for the answer; says whether to keep waiting
  say <text>        an agent at work answers the human in the team chat
  reply <id> <answer>
                    answer the human's message <id> (a note from the human names its id)
  withdraw <id>     withdraw your question that no longer needs an answer

flags:
`

func main() {
	self, _ := os.Executable()
	bundle := strings.Contains(self, ".app/Contents/MacOS/")
	if bundle { // opened from Spotlight or Finder: no PATH, and none of the shell's variables
		os.Setenv("PATH", loginPath())
		for k, v := range loginVars("ROTA_ROOT", "ROTA_CONFIG") { // the team folder rota picks in a terminal
			if os.Getenv(k) == "" {
				os.Setenv(k, v)
			}
		}
	}
	fs := flag.NewFlagSet("rota", flag.ExitOnError)
	root := fs.String("root", envOr("ROTA_ROOT", defaultRoot()), "team folder (also $ROTA_ROOT)")
	conf := fs.String("config", envOr("ROTA_CONFIG", "rota.conf"), "config file, relative to root (also $ROTA_CONFIG)")
	dry := fs.Bool("dry", false, "with run: print the claude command and prompt instead of running")
	force := fs.Bool("force", false, "with run <agent>: ignore enabled, interval, hours and the team pool")
	at := fs.String("now", "", "pretend it is this time (RFC3339), for testing")
	fs.Usage = func() { fmt.Fprint(os.Stderr, usageText); fs.PrintDefaults() }
	fs.Parse(flagsFirst(fs, os.Args[1:]))
	if bundle && fs.NArg() == 0 { // opened from Spotlight or Finder: the menu bar
		fs.Parse([]string{"tray"})
	}
	if fs.NArg() == 0 && tui.IsTerminal(os.Stdin) && tui.IsTerminal(os.Stdout) {
		fs.Parse([]string{"ui"})
	}
	if fs.NArg() == 0 {
		fs.Usage()
		os.Exit(2)
	}
	args := fs.Args()
	if args[0] == "help" {
		fmt.Print(usageText)
		fs.SetOutput(os.Stdout)
		fs.PrintDefaults()
		return
	}
	// Ctrl-C, SIGTERM or a closed terminal cancel ctx: a running shift is stopped, with
	// everything it started, and recorded as interrupted. A second signal does not wait.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	go func() {
		<-ctx.Done()
		again := make(chan os.Signal, 1)
		signal.Notify(again, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		<-again
		fmt.Fprintln(os.Stderr, "rota: killed")
		core.KillChildren()
		os.Exit(130)
	}()
	now := time.Now()
	if *at != "" {
		var err error
		if now, err = time.Parse(time.RFC3339, *at); err != nil {
			fatal(fmt.Errorf("-now: %w", err))
		}
	}
	tui.Version, web.Version = buildDesc(), buildDesc()
	if err := command(ctx, args, *root, *conf, now, *dry, *force, self, bundle); err != nil && ctx.Err() == nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "rota:", err)
	os.Exit(1)
}

func command(ctx context.Context, args []string, root, conf string, now time.Time, dry, force bool, self string, bundle bool) error {
	arg := func(i int, what string) (string, error) {
		if len(args) <= i {
			return "", fmt.Errorf("%s: name %s", args[0], what)
		}
		return args[i], nil
	}
	switch args[0] {
	case "init":
		abs, _ := filepath.Abs(root)
		return core.Init(abs)
	case "brief":
		fmt.Print(core.DefaultBrief)
		return nil
	case "version":
		fmt.Printf("rota %s\n%s\n", buildDesc(), exe())
		return nil
	case "icon":
		path, err := arg(1, "an output path")
		if err != nil {
			return err
		}
		size := 1024
		if len(args) > 2 {
			size, _ = strconv.Atoi(args[2])
		}
		return icon.Write(path, size)
	case "ui", "tui":
		return tui.Run(ctx, root, conf)
	case "tray":
		return tray.Run(ctx, root, conf, self, bundle)
	}
	t, err := core.Open(root, conf)
	if err != nil {
		return err
	}
	for _, w := range t.Warnings {
		if args[0] != "start" { // the organizer says them itself, whenever they change
			fmt.Fprintln(os.Stderr, "warn:", w)
		}
	}
	switch args[0] {
	case "start":
		// the pages follow their settings while the organizer runs: a change applies within seconds
		go web.Follow(ctx, t.Root, conf, false, func(where string) { fmt.Printf("%s %s\n", time.Now().Format("15:04"), where) })
		return core.Organize(ctx, t.Root, conf)
	case "web": // while web is off too, on this machine
		web.Follow(ctx, t.Root, conf, true, func(where string) { fmt.Printf("%s %s (Ctrl-C to stop)\n", time.Now().Format("15:04"), where) })
		return nil
	case "run":
		name, _ := arg(1, "")
		return t.RunShift(ctx, name, now, force, dry)
	case "status":
		st := t.Status(now)
		if t.WeekGuessed(st.Usage) {
			fmt.Fprintln(os.Stderr, "warn: week_start is claude but no usage data; using Monday")
		}
		if tui.IsTerminal(os.Stdout) {
			tui.PrintStatus(os.Stdout, t, st)
		} else {
			printStatus(st)
		}
		return nil
	case "pause":
		if name, _ := arg(1, ""); name != "" {
			return t.PauseAgent(name)
		}
		return t.Pause()
	case "unpause":
		if name, _ := arg(1, ""); name != "" {
			return t.ResumeAgent(name)
		}
		return t.Unpause()
	case "kick":
		name, _ := arg(1, "")
		return t.RunNow(name)
	case "cancel":
		return t.CancelShift("")
	case "stop":
		if err := t.StopOrganizer(); err != nil {
			return err
		}
		fmt.Println("organizer stopped")
		return nil
	case "enable", "disable":
		if _, err := arg(1, "at least one agent"); err != nil {
			return err
		}
		for _, name := range args[1:] {
			if err := t.SetAgent(name, map[string]string{"enabled": fmt.Sprint(args[0] == "enable")}); err != nil {
				return err
			}
			fmt.Printf("%s: %sd\n", name, args[0])
		}
		return command(ctx, []string{"status"}, root, conf, now, dry, force, self, bundle)
	case "log", "attach":
		name, err := arg(1, "an agent")
		if err != nil {
			return err
		}
		n := 1
		if args[0] == "log" && len(args) > 2 {
			n, _ = strconv.Atoi(args[2])
		}
		trs := t.Transcripts(name)
		for args[0] == "attach" && len(trs) == 0 {
			fmt.Printf("waiting for a shift of %s to start\n", name)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(2 * time.Second):
			}
			trs = t.Transcripts(name)
		}
		if n < 1 || n > len(trs) {
			return fmt.Errorf("%s has %d shift logs", name, len(trs))
		}
		tr := trs[n-1]
		fmt.Println(tr.Path)
		if args[0] == "log" {
			return printTranscript(ctx, tr.Path, nil)
		}
		return printTranscript(ctx, tr.Path, func() bool { // the shift is over once spend.csv has its row
			t, err := core.Open(root, conf)
			if err != nil {
				return false
			}
			for _, x := range t.Transcripts(name) {
				if x.Path == tr.Path {
					return x.Run != nil
				}
			}
			return true // its log is gone
		})
	case "labels":
		changed, err := t.SyncLabels(ctx)
		for _, c := range changed {
			fmt.Println(c)
		}
		if err == nil && len(changed) == 0 {
			fmt.Println("the team's labels are right in every repository")
		}
		return err
	case "team-tools": // the Team's tools over MCP, which its Claude starts: not for people
		return t.TeamTools(ctx)
	case "merge":
		ref, err := arg(1, "a pull request, <repository>#<number>")
		if err != nil {
			return err
		}
		head, err := arg(2, "the head commit you judged")
		if err != nil {
			return err
		}
		msg, err := t.Merge(ctx, ref, head)
		if err != nil {
			return err
		}
		fmt.Println(msg)
		return nil
	case "delete-branch":
		agent := os.Getenv("ROTA_AGENT")
		if agent == "" {
			return fmt.Errorf("rota delete-branch is for an agent's shift on the organizer's machine")
		}
		repo, err := arg(1, "a repository")
		if err != nil {
			return err
		}
		branch, err := arg(2, "your branch")
		if err != nil {
			return err
		}
		msg, err := t.DeleteBranch(ctx, agent, repo, branch)
		if err != nil {
			return err
		}
		fmt.Println(msg)
		return nil
	case "review-order":
		agent := os.Getenv("ROTA_AGENT")
		if agent == "" {
			return fmt.Errorf("rota review-order is for an agent's shift on the organizer's machine")
		}
		unknown, err := t.SetReviewOrder(agent, args[1:])
		if err != nil {
			return err
		}
		if len(args) < 2 {
			fmt.Println("no order: the Reviews tab lists the open pull requests by repository, oldest first, each after the open ones it waits for.")
			return nil
		}
		fmt.Printf("the Reviews tab lists %d pull requests in this order, each after the open ones it waits for.\n", len(args)-1-len(unknown))
		if len(unknown) > 0 {
			fmt.Printf("GitHub did not list these as open pull requests when rota last asked, a few minutes ago at most; they are not shown: %s\n", strings.Join(unknown, ", "))
		}
		return nil
	case "ask":
		agent := os.Getenv("ROTA_AGENT")
		if agent == "" {
			return fmt.Errorf("rota ask is for an agent's shift on the organizer's machine; elsewhere, end the final message with a line @human: <question>")
		}
		q, err := askArgs(agent, args[1:])
		if err != nil {
			return err
		}
		id, err := t.AskWith(q)
		if err != nil {
			return err
		}
		fmt.Printf("asked; the human is notified. Keep working on what does not depend on it; rota wait %s waits for the answer.\n", id)
		return nil
	case "wait":
		id, err := arg(1, "a question's id, from rota ask")
		if err != nil {
			return err
		}
		end, _ := time.Parse(time.RFC3339, os.Getenv("ROTA_PARK_BY"))
		answer, more, err := t.WaitAnswer(id, 8*time.Minute, end)
		switch {
		case errors.Is(err, core.ErrDismissed):
			fmt.Println("the human dismissed this question without an answer: go on without it, and do not ask it again unless something about it changes.")
		case err != nil:
			return err
		case answer != "":
			fmt.Println("the human answered:", answer)
		case more:
			fmt.Printf("no answer yet; rota wait %s waits longer.\n", id)
		default:
			fmt.Printf("no answer after %g minutes: set this work aside as your role says, and go on with work that is not blocked. The answer will be in your inbox at a later shift.\n", t.AnswerMinutes)
		}
		return nil
	case "say":
		agent := os.Getenv("ROTA_AGENT")
		if agent == "" {
			return fmt.Errorf("rota say is for an agent's shift on the organizer's machine")
		}
		if err := t.ChatSay(agent, strings.Join(args[1:], " ")); err != nil {
			return err
		}
		fmt.Println("said in the team chat.")
		return nil
	case "reply":
		agent := os.Getenv("ROTA_AGENT")
		if agent == "" {
			return fmt.Errorf("rota reply is for an agent's shift on the organizer's machine; elsewhere, end the final message with a line @human re <id>: <answer>")
		}
		id, err := arg(1, "the id of the human's message, from its note")
		if err != nil {
			return err
		}
		if err := t.ReplyTo(agent, id, strings.Join(args[2:], " ")); err != nil {
			return err
		}
		fmt.Println("answered; the human finds it in the message's thread.")
		return nil
	case "withdraw":
		agent := os.Getenv("ROTA_AGENT")
		if agent == "" {
			return fmt.Errorf("rota withdraw is for an agent's shift on the organizer's machine; elsewhere, end the final message with a line @human withdraw <id>")
		}
		id, err := arg(1, "a question's id, from rota ask or your brief")
		if err != nil {
			return err
		}
		if err := t.Withdraw(agent, id); err != nil {
			return err
		}
		fmt.Println("withdrawn: the human no longer sees it.")
		return nil
	case "usage":
		u, err := t.ReadUsage()
		if err != nil {
			return err
		}
		fmt.Println(u)
		return nil
	case "config":
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintf(w, "version:\t%s\nroot:\t%s\n", buildDesc(), t.Root)
		for _, f := range core.ConfigFields {
			v := f.Get(t.Config)
			if f.Key == "worker_name" {
				v = t.Worker()
			}
			fmt.Fprintf(w, "%s:\t%s\n", f.Key, or(v, "none"))
		}
		return w.Flush()
	case "pair":
		code, err := t.Pair()
		if err != nil {
			return err
		}
		me := core.TailscaleIP()
		if me == "" {
			me = "<this machine's address>"
		}
		fmt.Printf("pairing code %s, valid %d minutes, one host.\nOn that host run:  rota join %s %s\n", code, core.PairMinutes, me, code)
		return nil
	case "join":
		org, err := arg(1, "the organizer, e.g. rota join mac <code>")
		if err != nil {
			return err
		}
		code, _ := arg(2, "")
		return t.Join(ctx, org, code)
	}
	return fmt.Errorf("unknown command %q (rota help lists them)", args[0])
}

// printStatus is rota status piped or redirected: plain text, one table.
func printStatus(st core.Status) {
	fmt.Println("organizer:", st.Organizer())
	if r := st.Running; r != nil {
		fmt.Printf("shift: %s on %s since %s\n", r.Agent, r.Host, r.Since.Local().Format("15:04"))
	}
	switch {
	case st.Usage != nil:
		fmt.Println("claude:", st.Usage)
	case st.UsageError != "":
		fmt.Println("claude: usage cannot be read:", st.UsageError)
	default:
		fmt.Println("claude: usage data off")
	}
	var hosts []string
	for _, h := range st.Hosts {
		hosts = append(hosts, h.String())
	}
	fmt.Println("hosts:", or(strings.Join(hosts, "; "), "none"))
	if st.Pool.Enforced {
		fmt.Printf("team pool: %.1f%% used %s (team_pct %g%% of the weekly window, pacing %s); every percent below is of the pool\n", st.Pool.Of(st.Pool.Used), st.Pool.Period, st.Pool.TeamPct, st.Pool.Pacing)
		if f := st.Pool.FreeText(); f != "" {
			fmt.Println("free:", f)
		}
	} else if st.UsageError != "" {
		fmt.Println("team pool: not enforced without Claude usage data")
	} else {
		fmt.Println("team pool: not enforced without Claude usage data; nothing limits the agents")
	}
	fmt.Printf("week since: %s\n\n", st.WeekSince.Local().Format("Mon 2006-01-02 15:04"))
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "AGENT\tMODEL/EFFORT\tPRIO\tWEIGHT\tRUNS\t%s%%/SHARE\tWEEK%%\tDISK\tLAST RUN\tNEXT RUN\tSTATE\n", strings.ToUpper(strings.TrimPrefix(st.Pool.Period, "this ")))
	for _, a := range st.Agents {
		last, share := "never", "-"
		if !a.LastRun.IsZero() {
			last = a.LastRun.Local().Format("Mon 15:04")
		}
		if a.QuotaPct > 0 {
			share = fmt.Sprintf("%.1f/%.1f", st.Pool.Of(a.PeriodPct), st.Pool.Of(a.QuotaPct))
			if f := a.Frees(); f >= 0.005 {
				share += fmt.Sprintf(" frees %.1f", st.Pool.Of(f))
			}
		}
		disk := a.DiskText()
		if a.OverCap() {
			disk += " over"
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%g\t%d\t%s\t%.1f\t%s\t%s\t%s\t%s\n", a.Name, a.ModelEffort(), a.Priority, a.Weight, a.Runs, share, st.Pool.OfWeek(a.WeekPct), disk, last, a.NextText(st.NextTick), a.State)
	}
	w.Flush()
	fmt.Printf("\nnext: %s\n", or(st.Next, "nothing"))
}

// printTranscript prints a shift's transcript; with over it waits for more until over says
// the shift has ended, or ctx ends (rota attach). A result does not end it: a background job
// that ends after the agent's answer wakes claude again, and that wake has its own result.
func printTranscript(ctx context.Context, path string, over func() bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	rd := bufio.NewReaderSize(f, 1<<20)
	var checked time.Time
	ended := false
	for {
		line, err := rd.ReadString('\n')
		for _, e := range core.ParseLine(line) {
			switch e.Kind {
			case "start":
				fmt.Printf("--- claude started with %d tools ---\n", e.Tools)
			case "text", "note":
				fmt.Println(strings.TrimSpace(e.Text))
			case "tool":
				fmt.Println("> " + e.Tool + " " + e.Arg)
			case "output":
				fmt.Println("  < " + core.FirstLine(e.Text, 300))
			case "inbox":
				fmt.Println("--- took a note from its inbox ---\n" + strings.TrimSpace(e.Text))
			case "end":
				how := "ok"
				if e.Error {
					how = "error " + e.Subtype
				}
				fmt.Printf("--- finished: %s, %d turns ---\n%s\n", how, e.Turns, strings.TrimSpace(e.Text))
			}
		}
		if err != nil {
			if over == nil || ended {
				return nil
			}
			if time.Since(checked) >= 2*time.Second {
				checked = time.Now()
				if ended = over(); ended {
					continue // print what came meanwhile, then stop
				}
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
}

// flagsFirst lets flags come before or after the command: "rota run -dry errors" works. What
// follows rota ask or rota reply is the question's or the answer's own: its flags are its.
func flagsFirst(fs *flag.FlagSet, args []string) []string {
	var flags, rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(rest) == 1 && (rest[0] == "ask" || rest[0] == "reply" || rest[0] == "say") { // their own words and flags
			rest = append(rest, args[i:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" || a == "--" {
			rest = append(rest, a)
			continue
		}
		flags = append(flags, a)
		f := fs.Lookup(strings.TrimLeft(a, "-"))
		if f == nil || strings.Contains(a, "=") {
			continue
		}
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); !(ok && b.IsBoolFlag()) && i+1 < len(args) {
			i++ // a flag that takes a value
			flags = append(flags, args[i])
		}
	}
	return append(flags, rest...)
}

// loginPath is PATH as the login shell sets it: an app opened from Spotlight sees only the
// system's, without claude, gh or git.
func loginPath() string {
	shell := envOr("SHELL", "/bin/zsh")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, shell, "-lc", `printf %s "$PATH"`).Output()
	if err != nil || len(out) == 0 {
		return os.Getenv("PATH")
	}
	return string(bytes.TrimSpace(out))
}

// loginVars are the named variables as your interactive login shell sets them, ~/.zshrc
// included; unset ones are left out.
func loginVars(names ...string) map[string]string {
	var script strings.Builder
	for _, n := range names { // each between record separators: whatever else the shell prints is ignored
		fmt.Fprintf(&script, `printf '\036%s=%%s\036' "$%s";`, n, n)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, envOr("SHELL", "/bin/zsh"), "-ilc", script.String()).Output()
	vars := map[string]string{}
	for _, part := range strings.Split(string(out), "\x1e") {
		if k, v, ok := strings.Cut(part, "="); ok && v != "" && slices.Contains(names, k) {
			vars[k] = v
		}
	}
	return vars
}

// defaultRoot is ~/.rota: the team folder the installed command works in.
func defaultRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".rota"
	}
	return filepath.Join(home, ".rota")
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func or(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}

// exe is this program, links resolved.
func exe() string {
	p, err := os.Executable()
	if err != nil {
		return "rota"
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// buildDesc says which build this is: version, commit, and when the program was built.
func buildDesc() string {
	desc := version
	if info, ok := debug.ReadBuildInfo(); ok {
		rev, dirty := "", false
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
		if len(rev) > 7 {
			desc += " " + rev[:7]
			if dirty {
				desc += "+changes"
			}
		}
	}
	if st, err := os.Stat(exe()); err == nil {
		desc += ", built " + st.ModTime().Format("2006-01-02 15:04")
	}
	return desc
}

// askArgs reads rota ask's arguments: the question's text, with --subject, --option (once per
// answer), --recommend, --step (once per step), --importance and --about before or after it.
func askArgs(agent string, args []string) (core.Question, error) {
	q := core.Question{Agent: agent}
	var text []string
	for i := 0; i < len(args); i++ {
		name, value, inline := strings.Cut(args[i], "=")
		if !strings.HasPrefix(name, "--") {
			text = append(text, args[i])
			continue
		}
		if !inline {
			if i+1 >= len(args) {
				return q, fmt.Errorf("%s needs a value", name)
			}
			i++
			value = args[i]
		}
		switch name {
		case "--subject":
			q.Subject = value
		case "--option":
			q.Options = append(q.Options, value)
		case "--recommend":
			q.Recommend = value
		case "--step":
			q.Steps = append(q.Steps, value)
		case "--importance":
			q.Importance = value
		case "--about":
			q.About = value
		default:
			return q, fmt.Errorf("rota ask knows --subject, --option, --recommend, --step, --importance and --about, not %s", name)
		}
	}
	q.Text = strings.Join(text, " ")
	return q, nil
}

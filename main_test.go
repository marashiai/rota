package main

import (
	"bytes"
	"context"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"rota/core"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	f()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

// team is a team folder whose fake claude reads a file and finishes.
func team(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	stub := filepath.Join(root, "claude")
	write(t, stub, "#!/bin/bash\nhead -n1 >/dev/null\n"+
		`echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"README.md"}}]}}'`+"\n"+
		`echo '{"type":"user","message":{"content":[{"type":"tool_result","content":"# hello"}]}}'`+"\n"+
		`echo '{"type":"result","subtype":"success","is_error":false,"result":"## Summary\ndone","num_turns":1,"session_id":"sess-42","usage":{}}'`+"\n")
	os.Chmod(stub, 0o755)
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\nweb: off\nclaude_bin: "+stub+"\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nalpha\n")
	return root
}

func cli(t *testing.T, root string, args ...string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() {
		err = command(context.Background(), args, root, "rota.conf", time.Now(), false, false, "rota", false)
	})
	return out, err
}

// Every command reaches the core and says what it did.
func TestTheCommands(t *testing.T) {
	root := team(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"run", "alpha"}, "alpha: ok on local"},
		{[]string{"status"}, "NEXT RUN"},
		{[]string{"status"}, "DISK"},
		{[]string{"config"}, "team_pct:"},
		{[]string{"disable", "alpha"}, "alpha: disabled"},
		{[]string{"enable", "alpha"}, "alpha: enabled"},
		{[]string{"log", "alpha"}, "finished: ok, 1 turns"},
		{[]string{"attach", "alpha"}, "> Read README.md"},
		{[]string{"brief"}, "{agent}"},
		{[]string{"version"}, "rota dev"},
	} {
		out, err := cli(t, root, tc.args...)
		if err != nil || !strings.Contains(out, tc.want) {
			t.Errorf("rota %s: %v, want %q in:\n%s", strings.Join(tc.args, " "), err, tc.want, out)
		}
		if money.MatchString(out) {
			t.Errorf("rota %s shows a dollar value:\n%s", strings.Join(tc.args, " "), out)
		}
	}
	for _, args := range [][]string{{"pause"}, {"unpause"}, {"kick", "alpha"}} {
		if _, err := cli(t, root, args...); err != nil {
			t.Errorf("rota %s: %v", args[0], err)
		}
	}
	tm, _ := core.Open(root, "rota.conf")
	if !tm.Asked()["alpha"] || tm.Paused() {
		t.Error("kick asks for alpha; unpause undoes pause")
	}
	for _, args := range [][]string{{"usage"}, {"stop"}, {"cancel"}, {"kick", "nobody"}, {"log"}, {"nonsense"}} { // cancel: no shift runs
		if _, err := cli(t, root, args...); err == nil {
			t.Errorf("rota %s should fail", strings.Join(args, " "))
		}
	}
	out := captureStdout(t, func() {
		command(context.Background(), []string{"run", "alpha"}, root, "rota.conf", time.Now(), true, false, "rota", false)
	})
	if !strings.Contains(out, "shift: alpha\nruns on: local") || strings.Contains(out, "budget") {
		t.Errorf("a dry run names the shift and has no budget:\n%s", out)
	}
}

// money is a dollar amount or a cost column, which rota never shows.
var money = regexp.MustCompile(`\$\s?\d|(?i)\busd\b|(?i)\bcost\b`)

// A background job that ends after the agent's answer wakes claude again, and that wake has its
// own result: log and attach show the whole shift, not only up to its first result.
func TestLogAndAttachShowAShiftWokenAfterItsResult(t *testing.T) {
	root := team(t)
	write(t, filepath.Join(root, "claude"), "#!/bin/bash\nhead -n1 >/dev/null\n"+
		`echo '{"type":"result","subtype":"success","is_error":false,"result":"first report","num_turns":161,"session_id":"s1","usage":{}}'`+"\n"+
		`echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"tail test-all.txt"}}]}}'`+"\n"+
		`echo '{"type":"result","subtype":"success","is_error":false,"result":"last report","num_turns":3,"session_id":"s1","usage":{}}'`+"\n")
	if out, err := cli(t, root, "run", "alpha"); err != nil || !strings.Contains(out, "164 turns") {
		t.Fatalf("rota run: %v\n%s", err, out)
	}
	for _, cmd := range []string{"log", "attach"} {
		out, err := cli(t, root, cmd, "alpha")
		if err != nil || !strings.Contains(out, "finished: ok, 161 turns") || !strings.Contains(out, "> Bash tail test-all.txt") || !strings.Contains(out, "finished: ok, 3 turns ---\nlast report") {
			t.Errorf("rota %s alpha shows the whole shift: %v\n%s", cmd, err, out)
		}
	}
}

// attach follows a shift past a result until the shift is over.
func TestAttachFollowsUntilTheShiftIsOver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shift.jsonl")
	write(t, path, `{"type":"result","subtype":"success","is_error":false,"result":"first report","num_turns":161}`+"\n")
	var over atomic.Bool
	go func() {
		time.Sleep(time.Second)
		f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		f.WriteString(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"tail test-all.txt"}}]}}` + "\n" +
			`{"type":"result","subtype":"success","is_error":false,"result":"last report","num_turns":3}` + "\n")
		f.Close()
		over.Store(true)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out := captureStdout(t, func() {
		if err := printTranscript(ctx, path, over.Load); err != nil {
			t.Error(err)
		}
	})
	if ctx.Err() != nil || !strings.Contains(out, "> Bash tail test-all.txt") || !strings.Contains(out, "finished: ok, 3 turns ---\nlast report") {
		t.Fatalf("attach waits past the first result and stops once the shift is over (timed out: %v):\n%s", ctx.Err() != nil, out)
	}
}

// rota setup hands Claude Code the guide of the build it runs, with no copy of the repository:
// the same files as setup/, and none an older build left behind.
func TestSetupWritesThisBuildsGuideAndSaysWhere(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", "")
	dir := guideDir()
	write(t, filepath.Join(dir, "blueprints", "stack.md"), "a role this build no longer has")
	out, err := cli(t, t.TempDir(), "setup") // before any team folder exists
	if err != nil || !strings.Contains(out, dir) || !strings.Contains(out, "SETUP.md") {
		t.Fatalf("rota setup: %v\n%s", err, out)
	}
	want, _ := filepath.Glob("setup/blueprints/*.md")
	got, _ := filepath.Glob(filepath.Join(dir, "blueprints", "*.md"))
	if len(want) == 0 || len(got) != len(want) {
		t.Fatalf("blueprints: wrote %d, the build has %d", len(got), len(want))
	}
	for _, f := range append(want, "setup/SETUP.md") {
		b, _ := os.ReadFile(f)
		rel, _ := filepath.Rel("setup", f)
		if w, err := os.ReadFile(filepath.Join(dir, rel)); err != nil || !bytes.Equal(w, b) {
			t.Errorf("%s: %v, or not the build's copy", rel, err)
		}
	}
}

// Every blueprint's agent file loads as it is, placeholders and all: its settings are ones rota
// has, and each agent it waits for or runs after is a blueprint too.
func TestEveryBlueprintsAgentFileLoads(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	files, _ := filepath.Glob("setup/blueprints/*.md")
	block := regexp.MustCompile("(?s)````markdown\n(.*?)\n````")
	for _, f := range files {
		b, _ := os.ReadFile(f)
		m := block.FindSubmatch(b)
		if m == nil {
			t.Fatalf("%s has no agent file", f)
		}
		write(t, filepath.Join(root, "agents", filepath.Base(f)), string(m[1]))
	}
	team, err := core.Open(root, "rota.conf")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range team.Agents {
		if len(a.Warnings) > 0 {
			t.Errorf("%s: %v", a.Name, a.Warnings)
		}
		for _, other := range append(append([]string{}, a.After...), a.WaitFor...) {
			if team.Agent(other) == nil {
				t.Errorf("%s waits for or runs after %q, which no blueprint is", a.Name, other)
			}
		}
	}
	if len(team.Agents) != len(files) {
		t.Fatalf("%d agents from %d blueprints", len(team.Agents), len(files))
	}
}

func TestHelpListsTheCommands(t *testing.T) {
	cmd := exec.Command("go", "run", ".", "help")
	if b, err := cmd.Output(); err != nil || !strings.Contains(string(b), "commands:") || !strings.Contains(string(b), "-root") {
		t.Fatalf("rota help: %v\n%s", err, b)
	}
}

func TestFlagsMayComeAfterTheCommand(t *testing.T) {
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	fs.String("root", "", "")
	fs.Bool("dry", false, "")
	got := flagsFirst(fs, []string{"run", "-dry", "errors", "-root", "/tmp/x", "-unknown"})
	if strings.Join(got, " ") != "-dry -root /tmp/x -unknown run errors" {
		t.Fatalf("got %v", got)
	}
}

// The app, opened from Finder, takes the team folder's variables from your login shell, as
// rota in a terminal sees them, whatever else the shell prints.
func TestTheAppReadsTheTeamFolderFromTheLoginShell(t *testing.T) {
	shell := filepath.Join(t.TempDir(), "shell")
	write(t, shell, "#!/bin/bash\necho 'Welcome back'\nexport ROTA_ROOT=/teams/one\neval \"$2\"\necho bye\n")
	os.Chmod(shell, 0o755)
	t.Setenv("SHELL", shell)
	if v := loginVars("ROTA_ROOT", "ROTA_CONFIG"); len(v) != 1 || v["ROTA_ROOT"] != "/teams/one" {
		t.Fatalf("want only ROTA_ROOT=/teams/one, got %v", v)
	}
}

func TestTheAppFindsTheLoginPath(t *testing.T) {
	if p := loginPath(); !strings.Contains(p, "/bin") {
		t.Fatalf("login PATH looks wrong: %q", p)
	}
}

func TestAskReadsItsFlags(t *testing.T) {
	q, err := askArgs("dev", []string{"--subject", "Drop it?", "Is", "--option", "Drop it", "--option=Keep it", "--recommend", "Drop it",
		"the code", "--step", "one", "--importance", "high", "--about", "#519", "used?"})
	if err != nil {
		t.Fatal(err)
	}
	if q.Agent != "dev" || q.Text != "Is the code used?" || q.Subject != "Drop it?" || strings.Join(q.Options, "|") != "Drop it|Keep it" ||
		q.Recommend != "Drop it" || len(q.Steps) != 1 || q.Importance != "high" || q.About != "#519" {
		t.Fatalf("%+v", q)
	}
	for _, bad := range [][]string{{"--colour", "red", "x"}, {"x", "--subject"}} {
		if _, err := askArgs("dev", bad); err == nil {
			t.Errorf("%q is read", bad)
		}
	}
}

func TestAsksFlagsAreItsOwn(t *testing.T) {
	fs := flag.NewFlagSet("rota", flag.ContinueOnError)
	fs.String("root", "", "")
	fs.Bool("dry", false, "")
	args := []string{"-root", "/team", "ask", "--subject", "Drop it?", "--option", "a", "--option", "b", "-dry", "the question"}
	if err := fs.Parse(flagsFirst(fs, args)); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fs.Args(), "|"); got != "ask|--subject|Drop it?|--option|a|--option|b|-dry|the question" {
		t.Fatalf("what follows ask is the question's: %s", got)
	}
	if fs.Lookup("root").Value.String() != "/team" || fs.Lookup("dry").Value.String() != "false" {
		t.Fatal("rota's own flags before ask still count, and none after it")
	}
	if got := flagsFirst(fs, []string{"reply", "20261108-1-ab", "use", "-dry", "now"}); strings.Join(got, " ") != "reply 20261108-1-ab use -dry now" {
		t.Fatalf("an answer's words are its own: %v", got)
	}
	if got := flagsFirst(fs, []string{"work-order", "o/app#1 why", "--out", "o/app#2 why not", "-dry"}); strings.Join(got, " ") != "work-order o/app#1 why --out o/app#2 why not -dry" {
		t.Fatalf("a work order's entries and --out are its own: %v", got)
	}
	fs = flag.NewFlagSet("rota", flag.ContinueOnError)
	fs.Bool("dry", false, "")
	if err := fs.Parse(flagsFirst(fs, []string{"run", "-dry", "errors"})); err != nil || fs.Lookup("dry").Value.String() != "true" || strings.Join(fs.Args(), " ") != "run errors" {
		t.Fatalf("other commands take flags after them: %v %v", err, fs.Args())
	}
}

package core

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The organizer restarts itself when another rota is installed in its place, so the pages and
// the schedule are the new version's without anyone restarting it: between shifts, so none is
// cut short, and as the same process, so the menu bar that started it still has it.

// selfSettle is how long a new rota must stay as it is before it runs: installing it copies it
// in after removing the old one. selfGiveUp is how long the organizer waits for one that does not
// settle (an install that failed halfway) before it schedules again; selfRetry, when it tries
// again one that would not start.
var selfSettle, selfGiveUp, selfRetry = 5 * time.Second, 2 * time.Minute, 10 * time.Minute

// restart runs the rota at exe in this process's place, with the same arguments; it returns
// only when that fails.
var restart = func(exe string) error { return restartWith(exe, os.Environ()) }

// restartWith is restart with env as the new one's environment.
var restartWith = func(exe string, env []string) error { return syscall.Exec(exe, os.Args, env) }

// opens says whether the rota at exe opens the team folder and its config as they are now, as
// rota config does, before it takes this process's place: one that refuses them stops as it
// starts, and the team with it, with no organizer left to say why. That is a setting this one
// takes and the new one does not, or a file edited by hand since this one last read it.
var opens = func(ctx context.Context, exe string, c *Config) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, exe, "-root", c.Root, "-config", c.File, "config")
	cmd.Stdout, cmd.Stderr = io.Discard, &stderr
	cmd.WaitDelay = time.Second // what it started and left holding its output does not hold the organizer
	if err := cmd.Run(); err != nil {
		lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
		if why := strings.TrimPrefix(lines[len(lines)-1], "rota: "); why != "" {
			return errors.New(why)
		}
		return err
	}
	return nil
}

type fileSig struct {
	size int64
	mod  time.Time
	ino  uint64
}

func sigOf(path string) (fileSig, bool) {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode()&0o111 == 0 {
		return fileSig{}, false
	}
	var ino uint64
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		ino = uint64(st.Ino)
	}
	return fileSig{fi.Size(), fi.ModTime(), ino}, true
}

// selfWatch notices another rota installed where this one was started from.
type selfWatch struct {
	mu       sync.Mutex // the organizer's loop and its watch both ask
	exe      string
	started  fileSig // the one this process runs
	seen     fileSig // another, first seen at seenAt
	seenAt   time.Time
	failed   fileSig // one that would not start, at failedAt: tried again after selfRetry
	failedAt time.Time
	refused  string        // failed refused the team as stamp said it was: tried again once that changes
	stamp    func() string // the team's settings as they are now, as settingsStamp; nil for the menu bar
}

// watchSelf watches exe; missing (an install under way as the organizer started), whatever is
// copied there next counts as new.
func watchSelf(exe string) *selfWatch {
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	sig, _ := sigOf(exe)
	return &selfWatch{exe: exe, started: sig, stamp: func() string { return "" }}
}

// check says whether another rota is installed (pending), and whether it has stayed as it is
// for selfSettle, ready to run.
func (w *selfWatch) check(now time.Time) (pending, ready bool) {
	if w == nil {
		return false, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	sig, ok := sigOf(w.exe)
	switch {
	case ok && (sig == w.started || sig == w.failed && (now.Sub(w.failedAt) < selfRetry || w.refused != "" && w.refused == w.stamp())):
		w.seen = fileSig{}
		return false, false
	case !ok || sig != w.seen: // being copied in, or just copied
		w.seen, w.seenAt = sig, now
		return true, false
	}
	return true, now.Sub(w.seenAt) >= selfSettle
}

// updateSelf, between shifts, runs a newly installed rota in this process's place, once it has
// settled; while it settles it waits, so no shift starts on the old one. It returns false when
// there is nothing to do or the new one would not start, and the organizer goes on. One that
// refuses the team as it is is not tried again, nor does it stop a shift, until a setting changes.
func (o *organizer) updateSelf(ctx context.Context, w *selfWatch) bool {
	for since := time.Now(); ; {
		pending, ready := w.check(time.Now())
		if !pending || time.Since(since) > selfGiveUp {
			return false
		}
		if ready {
			stamp := w.stamp()
			err := opens(ctx, w.exe, o.Config)
			if ctx.Err() != nil {
				return false // the organizer stops: neither the new one nor this one goes on
			}
			if err != nil {
				logf("warn: the new rota refuses the team as it is, so this one goes on until a setting changes: %v", err)
			} else {
				logf("a new rota is installed: restarting with it")
				o.write()
				err = restart(w.exe)
				logf("warn: the new rota would not start, so this one goes on: %v", err)
				stamp = ""
			}
			w.mu.Lock()
			w.failed, w.failedAt, w.refused = w.seen, time.Now(), stamp
			w.mu.Unlock()
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(selfSettle):
		}
	}
}

// SelfUpdate notices another rota installed where this one runs from, for a program that takes
// it as soon as it has settled, such as the menu bar, rather than between shifts.
type SelfUpdate struct{ w *selfWatch }

// WatchSelfUpdate watches the running rota; nil when it cannot tell where that is.
func WatchSelfUpdate() *SelfUpdate {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	return &SelfUpdate{watchSelf(exe)}
}

// Ready says another rota is installed and has settled, ready to run.
func (s *SelfUpdate) Ready() bool {
	if s == nil {
		return false
	}
	_, ready := s.w.check(time.Now())
	return ready
}

// Restart runs the new rota in this process's place, with the same arguments and env added to
// the environment; it returns only when that fails, and that one is then not tried again for a
// while.
func (s *SelfUpdate) Restart(env ...string) error {
	err := restartWith(s.w.exe, append(os.Environ(), env...))
	s.w.failed, s.w.failedAt = s.w.seen, time.Now()
	return err
}

// Reopen opens the new rota as another instance of the app bundle it is installed in, with env
// added to its environment, for the menu bar: macOS shows a menu bar app's icon only when the app
// was opened as an app, so one that replaced itself in its process would run on without one. nil
// means the new one was opened and this one should end; otherwise that one is not tried again
// for a while.
func (s *SelfUpdate) Reopen(bundle string, env ...string) error {
	args := []string{"-n", "-a", bundle}
	for _, e := range env {
		args = append(args, "--env", e)
	}
	err := reopen(args)
	if err != nil {
		s.w.mu.Lock()
		s.w.failed, s.w.failedAt = s.w.seen, time.Now()
		s.w.mu.Unlock()
	}
	return err
}

// reopen runs macOS's open with args.
var reopen = func(args []string) error { return exec.Command("open", args...).Run() }

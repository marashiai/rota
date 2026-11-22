package core

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// endLeftBehind ends what a shift left running once its claude has ended. A command that the
// agent ran in the background, as "nohup ./test-all > log &", leaves claude's process group, so
// stopping the shift misses it, and once the shell that started it is done, its parent is
// launchd (pid 1): it would run on for weeks, holding its ports, its build folder and the
// machine. Such a process is one whose parent is pid 1, begun since the shift began, working in
// the agent's work folder; it ends with everything it started, SIGTERM first and SIGKILL after
// killGrace. The human's own terminal in that folder has a parent and is never one. It says
// which commands it ended.
func endLeftBehind(work string, since time.Time) []string {
	if real, err := filepath.EvalSymlinks(work); err == nil {
		work = real
	}
	procs, err := listProcs()
	if err != nil {
		return nil
	}
	var roots []int
	for _, p := range procs {
		if p.ppid == 1 && p.pid != os.Getpid() && !p.started.Before(since.Truncate(time.Second)) {
			roots = append(roots, p.pid)
		}
	}
	if len(roots) == 0 {
		return nil
	}
	cwds := cwdsOf(roots)
	children := map[int][]int{}
	for _, p := range procs {
		children[p.ppid] = append(children[p.ppid], p.pid)
	}
	var doomed, said []int
	for _, pid := range roots {
		if d := cwds[pid]; d != work && !strings.HasPrefix(d, work+string(filepath.Separator)) {
			continue
		}
		said = append(said, pid)
		for todo := []int{pid}; len(todo) > 0; todo = todo[1:] {
			doomed = append(doomed, todo[0])
			todo = append(todo, children[todo[0]]...)
		}
	}
	if len(doomed) == 0 {
		return nil
	}
	names := make([]string, 0, len(said))
	for _, pid := range said {
		names = append(names, procs[slices.IndexFunc(procs, func(p proc) bool { return p.pid == pid })].command)
	}
	for _, pid := range doomed {
		syscall.Kill(pid, syscall.SIGTERM)
	}
	for end := time.Now().Add(killGrace); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		doomed = slices.DeleteFunc(doomed, func(pid int) bool { return syscall.Kill(pid, 0) != nil })
		if len(doomed) == 0 {
			return names
		}
	}
	for _, pid := range doomed {
		syscall.Kill(pid, syscall.SIGKILL)
	}
	return names
}

type proc struct {
	pid, ppid int
	started   time.Time
	command   string // its first 60 characters
}

// listProcs lists this machine's processes, as ps does.
func listProcs() ([]proc, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ps", "-A", "-o", "pid=", "-o", "ppid=", "-o", "lstart=", "-o", "args=")
	cmd.Env = append(os.Environ(), "LC_ALL=C") // lstart in English
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	var procs []proc
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 8 { // pid, ppid, the five words of lstart, the command
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		started, err3 := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(f[2:7], " "), time.Local)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		command := strings.Join(f[7:], " ")
		if r := []rune(command); len(r) > 60 {
			command = string(r[:60]) + "…"
		}
		procs = append(procs, proc{pid, ppid, started, command})
	}
	return procs, nil
}

// cwdsOf is the folder each of pids works in, as far as it can tell.
func cwdsOf(pids []int) map[int]string {
	cwds := map[int]string{}
	if runtime.GOOS == "linux" {
		for _, pid := range pids {
			if d, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid)); err == nil {
				cwds[pid] = d
			}
		}
		return cwds
	}
	list := make([]string, len(pids))
	for i, pid := range pids {
		list[i] = strconv.Itoa(pid)
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "lsof", "-a", "-d", "cwd", "-Fn", "-p", strings.Join(list, ",")).Output() // fails for a pid already gone
	pid := 0
	for _, line := range bytes.Split(out, []byte("\n")) {
		switch {
		case len(line) > 1 && line[0] == 'p':
			pid, _ = strconv.Atoi(string(line[1:]))
		case len(line) > 1 && line[0] == 'n' && pid != 0:
			cwds[pid] = string(line[1:])
		}
	}
	return cwds
}

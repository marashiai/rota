package core

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Hosts join the organizer over the tailnet with one outbound TCP connection and
// newline-delimited JSON. The organizer listens on its Tailscale address only;
// hosts listen on nothing. A host pairs once with a one-time code and keeps a
// per-host token; the organizer keeps only the token's hash.

type msg struct {
	Type   string  `json:"type"` // hello paired ok error check caps job stream note cancel result ping pong
	Host   string  `json:"host,omitempty"`
	Code   string  `json:"code,omitempty"`
	Token  string  `json:"token,omitempty"`
	Text   string  `json:"text,omitempty"`
	Job    *Job    `json:"job,omitempty"`
	Result *Result `json:"result,omitempty"`
	Check  *Check  `json:"check,omitempty"`
	Caps   *Caps   `json:"caps,omitempty"`
}

// Check is what the organizer asks a host to verify; Caps is the host's answer.
type Check struct {
	GhUsers []string `json:"gh_users"`
}

type Caps struct {
	GhUsers        []string `json:"gh_users"` // the requested logins this host has
	ClaudeLoggedIn bool     `json:"claude_logged_in"`
	ClaudeEmail    string   `json:"claude_email"`
}

const (
	pingEvery    = 30 * time.Second
	readDeadline = 90 * time.Second
	helloTimeout = 30 * time.Second
	writeTimeout = 10 * time.Second // a stalled peer can never block a sender
)

// ---- organizer side ----

type hostRec struct {
	TokenHash string    `json:"token_hash"`
	LastSeen  time.Time `json:"last_seen"`
}

type worker struct {
	name     string
	conn     net.Conn
	enc      *json.Encoder
	wmu      sync.Mutex
	pending  map[string]chan Result
	busy     bool
	agent    string       // agent of the running shift
	onLine   func(string) // receives the running shift's raw stream lines
	lastSeen time.Time
	caps     *Caps // nil until the host has answered the check
}

func (w *worker) send(m msg) error {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	w.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	return w.enc.Encode(m)
}

type hub struct {
	c       *Config
	mu      sync.Mutex
	workers map[string]*worker
	addr    string
	needs   []string // gh logins the agents need; sent to hosts as a check
	email   string   // the organizer's Claude account, which hosts must share
}

func newHub(c *Config) *hub {
	h := &hub{c: c, workers: map[string]*worker{}}
	_, h.email = claudeLogin(c)
	return h
}

// ErrTailscaleUnreachable is Tailscale not answering: while it does not, it serves nothing, but
// what it was set to serve may come back with it.
var ErrTailscaleUnreachable = errors.New("Tailscale does not answer")

// StopTailscaleHTTPS has Tailscale stop serving the pages at this machine's tailnet name, when it
// serves them from port: they are this machine's only, or not served at all. Whatever else
// Tailscale serves is left as it is.
//
// It reads Tailscale afresh, not from the minute-old reading: what it turns off must be the pages.
// nil means Tailscale does not serve them: it is not installed, or it answered that it serves
// something else or nothing, also after turning them off. Otherwise it says why, and the caller
// tries again.
func StopTailscaleHTTPS(port int) error {
	if tailscaleCLI() == "" {
		return nil // no Tailscale on this machine
	}
	target := "http://127.0.0.1:" + strconv.Itoa(port)
	for try := 0; ; try++ {
		tailnet.Lock()
		tailnet.at = time.Time{} // afresh
		tailnet.Unlock()
		st := tailnetState()
		switch {
		case st.why != "": // its status unread: Tailscale is off, and serves nothing
			return fmt.Errorf("%w: %s", ErrTailscaleUnreachable, st.why)
		case st.name == "": // no HTTPS name on the tailnet: nothing served there
			return nil
		case st.unknown: // it answers, but not what it serves: that may be the pages
			return fmt.Errorf("Tailscale's serve setup cannot be read, so the pages may still be on https://%s", st.name)
		case st.serve != target:
			return nil
		case st.others:
			return fmt.Errorf("Tailscale serves more than the pages on https://%s; turn the pages off there by hand: tailscale serve --https=443 off", st.name)
		case try > 0:
			return fmt.Errorf("Tailscale still serves the pages at https://%s after it was told to stop", st.name)
		}
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		out, err := tailscaleCmd(ctx, tailscaleCLI(), "serve", "--https=443", "off").CombinedOutput()
		cancel()
		if err != nil {
			return fmt.Errorf("tailscale serve off: %v: %s", err, strings.TrimSpace(string(out)))
		}
	}
}

// setNeeds records which gh logins agents need and re-checks hosts when that changes.
func (h *hub) setNeeds(users []string) {
	sort.Strings(users)
	h.mu.Lock()
	changed := strings.Join(users, ",") != strings.Join(h.needs, ",")
	h.needs = users
	var ws []*worker
	for _, w := range h.workers {
		ws = append(ws, w)
	}
	h.mu.Unlock()
	if changed {
		for _, w := range ws {
			w.send(msg{Type: "check", Check: &Check{GhUsers: users}})
		}
	}
}

// listen binds to the Tailscale address (or what listen: says) on listen_port.
func (h *hub) listen() (net.Listener, error) { return h.listenOn(h.c.Listen, h.c.ListenPort) }

// listenOn binds to the Tailscale address, or the address listen names, on port.
func (h *hub) listenOn(listen string, port int) (net.Listener, error) {
	ip := listen
	if ip == "" || strings.EqualFold(ip, "tailscale") { // never every interface
		ip = TailscaleIP()
		if ip == "" {
			fmt.Fprintln(os.Stderr, "warn: no Tailscale address on this machine; hosts can only join from localhost")
			ip = "127.0.0.1"
		}
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	h.setAddr(ln.Addr().String())
	return ln, nil
}

// setAddr and address are where hosts join, "" when they cannot: the listener follows the
// settings while the organizer reads it.
func (h *hub) setAddr(a string) {
	h.mu.Lock()
	h.addr = a
	h.mu.Unlock()
}

func (h *hub) address() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.addr
}

// TailscaleIP is this machine's address in Tailscale's 100.64.0.0/10 range, on Tailscale's
// own interface (utun on macOS, tailscale0 on Linux): a carrier or a hotspot hands out
// addresses in that range too.
func TailscaleIP() string {
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if !strings.HasPrefix(i.Name, "utun") && !strings.HasPrefix(i.Name, "tailscale") {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && cgnat.Contains(ipn.IP) {
				return ipn.IP.String()
			}
		}
	}
	return ""
}

func (h *hub) serve(ctx context.Context, ln net.Listener) {
	stopped := make(chan struct{}) // a listener the settings replaced: its helper ends with it
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
			ln.Close()
		case <-stopped:
		}
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go h.handle(ctx, conn)
	}
}

func (h *hub) handle(ctx context.Context, conn net.Conn) {
	dec, enc := json.NewDecoder(conn), json.NewEncoder(conn)
	conn.SetReadDeadline(time.Now().Add(helloTimeout))
	conn.SetWriteDeadline(time.Now().Add(helloTimeout))
	var m msg
	if err := dec.Decode(&m); err != nil || m.Type != "hello" || !hostName.MatchString(m.Host) {
		enc.Encode(msg{Type: "error", Text: "expected hello with a host name"})
		conn.Close()
		return
	}
	hosts, _ := loadHosts(h.c)
	switch {
	case m.Code != "":
		if !checkPair(h.c, m.Code) {
			enc.Encode(msg{Type: "error", Text: "pairing code rejected; run rota pair on the organizer and try again"})
			conn.Close()
			return
		}
		token := randHex(32)
		hosts[m.Host] = hostRec{TokenHash: hashToken(token), LastSeen: time.Now()}
		if err := saveHosts(h.c, hosts); err != nil {
			enc.Encode(msg{Type: "error", Text: "organizer could not save hosts file: " + err.Error()})
			conn.Close()
			return
		}
		enc.Encode(msg{Type: "paired", Token: token})
	case m.Token != "":
		rec, ok := hosts[m.Host]
		if !ok || subtle.ConstantTimeCompare([]byte(rec.TokenHash), []byte(hashToken(m.Token))) != 1 {
			enc.Encode(msg{Type: "error", Text: "unknown host or bad token; pair again with rota pair"})
			conn.Close()
			return
		}
		enc.Encode(msg{Type: "ok"})
	default:
		enc.Encode(msg{Type: "error", Text: "hello needs a pairing code or a token"})
		conn.Close()
		return
	}

	w := &worker{name: m.Host, conn: conn, enc: enc, pending: map[string]chan Result{}, lastSeen: time.Now()}
	h.mu.Lock()
	if old := h.workers[w.name]; old != nil {
		old.conn.Close()
	}
	h.workers[w.name] = w
	h.mu.Unlock()
	fmt.Printf("%s host %s joined from %s\n", time.Now().Format("15:04"), w.name, conn.RemoteAddr())
	h.mu.Lock()
	needs := append([]string{}, h.needs...)
	h.mu.Unlock()
	w.send(msg{Type: "check", Check: &Check{GhUsers: needs}})

	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(pingEvery)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				w.send(msg{Type: "ping"})
			}
		}
	}()
	for {
		conn.SetReadDeadline(time.Now().Add(readDeadline))
		var in msg
		if err := dec.Decode(&in); err != nil {
			break
		}
		h.mu.Lock()
		w.lastSeen = time.Now()
		h.mu.Unlock()
		switch {
		case in.Type == "result" && in.Result != nil:
			h.mu.Lock()
			ch := w.pending[in.Result.ID]
			h.mu.Unlock()
			if ch != nil {
				ch <- *in.Result
			}
		case in.Type == "stream":
			h.mu.Lock()
			onLine := w.onLine
			h.mu.Unlock()
			if onLine != nil {
				onLine(in.Text)
			}
		case in.Type == "caps" && in.Caps != nil:
			h.mu.Lock()
			w.caps = in.Caps
			h.mu.Unlock()
			h.mu.Lock()
			needs := h.needs
			h.mu.Unlock()
			if m := append(problems(in.Caps, h.email), missingGh(in.Caps, needs)...); len(m) > 0 {
				fmt.Printf("%s host %s is missing: %s\n", time.Now().Format("15:04"), w.name, strings.Join(m, ", "))
			}
		}
	}
	close(stop)
	conn.Close()
	h.mu.Lock()
	if h.workers[w.name] == w {
		delete(h.workers, w.name)
	}
	for id, ch := range w.pending {
		select { // never block holding the lock: a full channel already holds the host's result
		case ch <- Result{ID: id, Status: "lost", Output: "connection to host " + w.name + " was lost"}:
		default:
		}
	}
	h.mu.Unlock()
	if hosts, err := loadHosts(h.c); err == nil {
		if rec, ok := hosts[w.name]; ok {
			rec.LastSeen = time.Now()
			hosts[w.name] = rec
			saveHosts(h.c, hosts)
		}
	}
	fmt.Printf("%s host %s left\n", time.Now().Format("15:04"), w.name)
}

// hostOf is the host running a job, while it runs.
func (h *hub) hostOf(id string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, w := range h.workers {
		if _, ok := w.pending[id]; ok {
			return w.name
		}
	}
	return ""
}

// note sends a note to the host running job id, for its shift; false when no host runs it.
func (h *hub) note(id, text string) bool {
	h.mu.Lock()
	var w *worker
	for _, cand := range h.workers {
		if _, ok := cand.pending[id]; ok {
			w = cand
		}
	}
	h.mu.Unlock()
	return w != nil && w.send(msg{Type: "note", Job: &Job{ID: id}, Text: text}) == nil
}

// dispatch sends a job to the named host ("any" = the idle host seen least recently)
// and waits for its result. Cancelling ctx tells the host to kill the shift.
func (h *hub) dispatch(ctx context.Context, host string, job Job, onLine func(string)) Result {
	h.mu.Lock()
	var w *worker
	if host == "any" {
		for _, cand := range h.workers {
			if !cand.busy && len(problems(cand.caps, h.email)) == 0 && len(missingGh(cand.caps, []string{job.GhUser})) == 0 && (w == nil || cand.lastSeen.Before(w.lastSeen)) {
				w = cand
			}
		}
	} else if cand := h.workers[host]; cand != nil && !cand.busy {
		w = cand
	}
	if w == nil {
		h.mu.Unlock()
		return Result{ID: job.ID, Status: "lost", Output: "host " + host + " is not available"}
	}
	ch := make(chan Result, 1)
	w.pending[job.ID], w.busy, w.agent, w.onLine = ch, true, job.Agent, onLine
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(w.pending, job.ID)
		w.busy, w.agent, w.onLine = false, "", nil
		h.mu.Unlock()
	}()
	if err := w.send(msg{Type: "job", Job: &job}); err != nil {
		return Result{ID: job.ID, Status: "lost", Output: "could not send job to host " + w.name}
	}
	select {
	case r := <-ch:
		return r
	case <-ctx.Done():
		w.send(msg{Type: "cancel", Job: &Job{ID: job.ID}})
		select {
		case r := <-ch:
			r.Status = "stopped"
			return r
		case <-time.After(killGrace + 5*time.Second):
			return Result{ID: job.ID, Status: "stopped", Output: "host did not confirm the stop in time"}
		}
	}
}

// HostState is one line of the status snapshot.
type HostState struct {
	Name      string    `json:"name"`
	Online    bool      `json:"online"`
	Busy      bool      `json:"busy"`
	LastSeen  time.Time `json:"last_seen"`
	Caps      *Caps     `json:"caps,omitempty"`
	Problems  []string  `json:"problems,omitempty"`   // why no agent can use this host at all
	MissingGh []string  `json:"missing_gh,omitempty"` // gh logins the team needs that this host lacks
}

// problems lists what stops a host from running anything: no answer yet, no claude
// login, or a claude login on a different account than the organizer's.
func problems(caps *Caps, email string) []string {
	switch {
	case caps == nil:
		return []string{"still checking"}
	case !caps.ClaudeLoggedIn:
		return []string{"claude login"}
	case email != "" && caps.ClaudeEmail != "" && caps.ClaudeEmail != email:
		return []string{"claude account differs (" + caps.ClaudeEmail + ")"}
	}
	return nil
}

// missingGh lists the requested gh logins the host did not report having.
func missingGh(caps *Caps, users []string) []string {
	var m []string
	for _, u := range users {
		if u != "" && (caps == nil || !contains(caps.GhUsers, u)) {
			m = append(m, "gh "+u)
		}
	}
	return m
}

func (h *hub) hosts() []HostState {
	known, _ := loadHosts(h.c)
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []HostState
	for name, rec := range known {
		s := HostState{Name: name, LastSeen: rec.LastSeen}
		if w := h.workers[name]; w != nil {
			s.Online, s.Busy, s.LastSeen, s.Caps = true, w.busy, w.lastSeen, w.caps
			s.Problems, s.MissingGh = problems(w.caps, h.email), missingGh(w.caps, h.needs)
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func loadHosts(c *Config) (map[string]hostRec, error) {
	hosts := map[string]hostRec{}
	b, err := os.ReadFile(c.Path(hostsFile))
	if os.IsNotExist(err) {
		return hosts, nil
	}
	if err != nil {
		return nil, err
	}
	return hosts, json.Unmarshal(b, &hosts)
}

func saveHosts(c *Config, hosts map[string]hostRec) error {
	b, _ := json.MarshalIndent(hosts, "", "  ")
	return os.WriteFile(c.Path(hostsFile), b, 0o600)
}

// Pair writes a one-time code the next joining host may use within PairMinutes.
func (c *Config) Pair() (string, error) {
	code := strings.ToUpper(randHex(4))
	expiry := time.Now().Add(time.Duration(PairMinutes) * time.Minute)
	return code, os.WriteFile(c.Path(pairFile), []byte(code+" "+expiry.Format(time.RFC3339)+"\n"), 0o600)
}

func checkPair(c *Config, code string) bool {
	b, err := os.ReadFile(c.Path(pairFile))
	if err != nil {
		return false
	}
	want, exp, _ := strings.Cut(strings.TrimSpace(string(b)), " ")
	t, err := time.Parse(time.RFC3339, exp)
	if err != nil || time.Now().After(t) {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(strings.ToUpper(code)), []byte(want)) != 1 {
		return false
	}
	os.Remove(c.Path(pairFile))
	return true
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- host side ----

var errRejected = errors.New("rejected by organizer")

// Join connects to the organizer, pairs if a code is given, then runs the jobs it
// sends, one at a time, until ctx is cancelled. It reconnects after a drop.
func (c *Config) Join(ctx context.Context, organizer, code string) error {
	addr := organizer
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(organizer, strconv.Itoa(c.ListenPort))
	}
	if code == "" && loadWorkerToken(c) == "" {
		return fmt.Errorf("not paired with %s yet: run rota pair there, then rota join %s <code>", organizer, organizer)
	}
	backoff := 2 * time.Second
	for {
		conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", addr)
		if err == nil {
			err = session(ctx, c, conn, code)
			code = ""
			backoff = 2 * time.Second
		}
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, errRejected) {
			return err
		}
		fmt.Fprintf(os.Stderr, "%s not connected (%v); retrying in %s\n", time.Now().Format("15:04"), err, backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}

func session(ctx context.Context, c *Config, conn net.Conn, code string) error {
	defer conn.Close()
	dec, enc := json.NewDecoder(conn), json.NewEncoder(conn)
	var wmu sync.Mutex
	send := func(m msg) error {
		wmu.Lock()
		defer wmu.Unlock()
		conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		return enc.Encode(m)
	}

	hello := msg{Type: "hello", Host: c.Worker(), Code: code}
	if code == "" {
		hello.Token = loadWorkerToken(c)
	}
	if err := send(hello); err != nil {
		return err
	}
	conn.SetReadDeadline(time.Now().Add(helloTimeout))
	var reply msg
	if err := dec.Decode(&reply); err != nil {
		return err
	}
	switch reply.Type {
	case "paired":
		if err := os.WriteFile(c.Path(workerFile), []byte(reply.Token), 0o600); err != nil {
			return err
		}
		fmt.Printf("%s paired with %s as %s\n", time.Now().Format("15:04"), conn.RemoteAddr(), c.Worker())
	case "ok":
	default:
		return fmt.Errorf("%w: %s", errRejected, reply.Text)
	}
	fmt.Printf("%s joined %s as %s; waiting for shifts\n", time.Now().Format("15:04"), conn.RemoteAddr(), c.Worker())

	go func() { <-ctx.Done(); conn.Close() }()
	var mu sync.Mutex
	var runningID string
	var cancelRunning context.CancelFunc
	var runningNotes chan string
	var runningCtx context.Context
	var err error
	for {
		conn.SetReadDeadline(time.Now().Add(readDeadline))
		var m msg
		if err = dec.Decode(&m); err != nil {
			break
		}
		switch m.Type {
		case "ping":
			send(msg{Type: "pong"})
		case "check":
			if m.Check != nil {
				send(msg{Type: "caps", Caps: capabilities(c, m.Check.GhUsers)})
			}
		case "job":
			if m.Job == nil {
				continue
			}
			mu.Lock()
			if runningID != "" {
				mu.Unlock()
				send(msg{Type: "result", Result: &Result{ID: m.Job.ID, Status: "busy", Output: "host is already running a shift"}})
				continue
			}
			jctx, cancel := context.WithCancel(ctx)
			notes := make(chan string, 16)
			runningID, cancelRunning, runningNotes, runningCtx = m.Job.ID, cancel, notes, jctx
			mu.Unlock()
			go func(job Job) {
				fmt.Printf("%s shift %s for the organizer\n", time.Now().Format("15:04"), job.Agent)
				res := execShift(jctx, c, job, func(line string) { send(msg{Type: "stream", Text: line}) }, notes)
				send(msg{Type: "result", Result: &res})
				fmt.Printf("%s shift %s %s\n", time.Now().Format("15:04"), job.Agent, res.Status)
				mu.Lock()
				if runningID == job.ID {
					runningID, cancelRunning, runningNotes = "", nil, nil
				}
				mu.Unlock()
				cancel()
			}(*m.Job)
		case "note": // a note for the running shift, which echoes it when it takes it
			mu.Lock()
			if m.Job != nil && runningID == m.Job.ID && runningNotes != nil {
				go func(notes chan string, ctx context.Context, text string) { // never dropped, never holding up the connection
					select {
					case notes <- text:
					case <-ctx.Done():
					}
				}(runningNotes, runningCtx, m.Text)
			}
			mu.Unlock()
		case "cancel":
			mu.Lock()
			if m.Job != nil && runningID == m.Job.ID && cancelRunning != nil {
				cancelRunning()
			}
			mu.Unlock()
		}
	}
	// the connection is gone: whatever is running must stop with it
	mu.Lock()
	if cancelRunning != nil {
		cancelRunning()
	}
	mu.Unlock()
	return err
}

func loadWorkerToken(c *Config) string {
	b, err := os.ReadFile(c.Path(workerFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// knownHosts are the paired hosts as the hosts file has them, for a status taken while the
// organizer is not running: all offline.
func knownHosts(c *Config) []HostState {
	known, _ := loadHosts(c)
	var out []HostState
	for name, rec := range known {
		out = append(out, HostState{Name: name, LastSeen: rec.LastSeen})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// tailscaleCLI is Tailscale's command line: on PATH, or inside the macOS app.
func tailscaleCLI() string {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p
	}
	if p := "/Applications/Tailscale.app/Contents/MacOS/Tailscale"; fileExists(p) {
		return p
	}
	return ""
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// tailscaleCmd runs Tailscale's command line. On macOS it is the app's own binary: unless its
// environment looks like a terminal's (TERM, SHLVL) or sets TAILSCALE_BE_CLI, it takes itself for
// the app, tries to start a second one, prints that it could not, and exits 0. An rota opened
// from Spotlight or Finder, and what it starts, has no terminal's variables.
func tailscaleCmd(ctx context.Context, cli string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, cli, args...)
	cmd.Env = append(os.Environ(), "TAILSCALE_BE_CLI=1")
	return cmd
}

// tailnet is what Tailscale says about this machine, read at most once a minute: its name on
// the tailnet when it may have an HTTPS certificate, and port 443: what Tailscale proxies there,
// whether anything else holds it, whether Funnel opens it to the internet, and whether Tailscale
// could be asked at all; why is what Tailscale said instead of this machine's name.
var tailnet struct {
	sync.Mutex
	at                     time.Time
	name, serve, why       string
	taken, funnel, unknown bool
	others                 bool // Tailscale serves more on 443 than a proxy at its root
}

type tailnetPort struct {
	name, serve, why       string
	taken, funnel, unknown bool
	others                 bool
}

func tailnetState() tailnetPort {
	tailnet.Lock()
	defer tailnet.Unlock()
	now := func() tailnetPort {
		return tailnetPort{tailnet.name, tailnet.serve, tailnet.why, tailnet.taken, tailnet.funnel, tailnet.unknown, tailnet.others}
	}
	if time.Since(tailnet.at) < time.Minute {
		return now()
	}
	tailnet.at, tailnet.name, tailnet.serve, tailnet.why, tailnet.taken, tailnet.funnel, tailnet.unknown, tailnet.others = time.Now(), "", "", "", false, false, true, false
	cli := tailscaleCLI()
	if cli == "" {
		return now()
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	var st struct {
		Self        struct{ DNSName string }
		CertDomains []string
	}
	out, err := tailscaleCmd(ctx, cli, "status", "--json").Output()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit) && len(bytes.TrimSpace(exit.Stderr)) > 0:
		tailnet.why = err.Error() + ": " + firstLine(exit.Stderr)
	case err != nil && len(bytes.TrimSpace(out)) > 0:
		tailnet.why = err.Error() + ": " + firstLine(out)
	case err != nil:
		tailnet.why = err.Error()
	case json.Unmarshal(out, &st) != nil:
		tailnet.why = firstLine(out)
	default:
		if n := strings.TrimSuffix(st.Self.DNSName, "."); n != "" && slices.Contains(st.CertDomains, n) {
			tailnet.name = n
		}
	}
	var sv struct {
		TCP map[string]struct { // {"HTTPS": true} for any web handler, the pages' too; a forward is someone's
			TCPForward, TerminateTLS string
		}
		Web map[string]struct {
			Handlers map[string]json.RawMessage
		}
		AllowFunnel map[string]bool
	}
	out, err = tailscaleCmd(ctx, cli, "serve", "status", "--json").Output()
	if text := strings.TrimSpace(string(out)); err == nil && (text == "" || text == "{}" || json.Unmarshal(out, &sv) == nil) {
		web := sv.Web[tailnet.name+":443"]
		var root struct{ Proxy string }
		json.Unmarshal(web.Handlers["/"], &root)
		tailnet.serve = root.Proxy
		forward := sv.TCP["443"].TCPForward != "" || sv.TCP["443"].TerminateTLS != ""
		tailnet.taken = forward || len(web.Handlers) > 0                                                // a forward, a file, any handler: someone's
		tailnet.others = forward || len(web.Handlers) > 1 || len(web.Handlers) == 1 && root.Proxy == "" // besides the root's proxy
		tailnet.funnel = sv.AllowFunnel[tailnet.name+":443"]
		tailnet.unknown = false
	}
	return now()
}

// firstLine is the first line of a command's output, for a message.
func firstLine(out []byte) string {
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(line)
}

// TailscaleName is this machine's name on the tailnet when Tailscale can give it an HTTPS
// certificate, or "".
func TailscaleName() string { return tailnetState().name }

// TailscaleHTTPS is where the pages are on the tailnet over HTTPS, https://<name>, when
// Tailscale serves them there (tailscale serve, with its certificate) and only on the tailnet,
// or "".
func TailscaleHTTPS(port int) string {
	if st := tailnetState(); st.name != "" && st.serve == "http://127.0.0.1:"+strconv.Itoa(port) && !st.funnel {
		return "https://" + st.name
	}
	return ""
}

// ServeTailscaleHTTPS has Tailscale serve the pages over HTTPS at this machine's tailnet name,
// with its certificate: phones only allow notifications from a secure page. Something else
// Tailscale already serves on port 443 is left as it is.
func ServeTailscaleHTTPS(ctx context.Context, port int) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	st := tailnetState()
	target := "http://127.0.0.1:" + strconv.Itoa(port)
	switch {
	case st.name == "" && st.why != "":
		return fmt.Errorf("Tailscale did not say this machine's name: %s", st.why)
	case st.name == "":
		return fmt.Errorf("no HTTPS on the tailnet: turn on MagicDNS and HTTPS certificates in Tailscale's admin console")
	case st.unknown:
		return fmt.Errorf("Tailscale's serve setup cannot be read; nothing is changed")
	case st.funnel:
		return fmt.Errorf("Tailscale Funnel opens https://%s to the internet; the pages have no login, so they are not served there", st.name)
	case st.serve == target:
		return nil
	case st.taken:
		return fmt.Errorf("Tailscale already serves something on https://%s; it is left as it is", st.name)
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout) // the pages stopping stop it too
	defer cancel()
	if out, err := tailscaleCmd(ctx, tailscaleCLI(), "serve", "--bg", "--https=443", target).CombinedOutput(); err != nil {
		return fmt.Errorf("tailscale serve: %v: %s", err, strings.TrimSpace(string(out)))
	}
	tailnet.Lock()
	tailnet.at = time.Time{} // read it again
	tailnet.Unlock()
	return nil
}

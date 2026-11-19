package web

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"rota/core"
)

// The app's team pages: the team (/api/team), one agent (/api/agent?name=) with its runs, role
// and every setting, and one run (/api/run?id=) with its report and transcript.

// apiTeam is the team as its page shows it: whether it works, its budget, and its agents.
type apiTeam struct {
	State     string        `json:"state"` // on, working, paused, held, silent or off, as the header says
	Title     string        `json:"title"`
	Line      string        `json:"line"`
	Organizer bool          `json:"organizer"` // it runs
	Paused    bool          `json:"paused"`
	Budget    string        `json:"budget,omitempty"`
	Fill      int           `json:"fill"`
	Free      string        `json:"free,omitempty"`
	Usage     string        `json:"usage,omitempty"`
	Running   *core.Running `json:"running,omitempty"`
	Warnings  []string      `json:"warnings,omitempty"`
	Agents    []apiAgent    `json:"agents"`
}

// apiAgent is one agent as the team's page lists it and its own page begins.
type apiAgent struct {
	core.Member
	Enabled  bool      `json:"enabled"`
	Paused   bool      `json:"paused"`
	Schedule string    `json:"schedule"` // when it runs, in words
	Next     string    `json:"next,omitempty"`
	Last     string    `json:"last,omitempty"`
	Runs     int       `json:"runs"`            // this week
	Share    string    `json:"share,omitempty"` // of the pool: used / its fair share
	Week     string    `json:"week,omitempty"`  // of the pool, this week
	Disk     string    `json:"disk,omitempty"`
	Over     bool      `json:"over,omitempty"`
	Model    string    `json:"model,omitempty"`
	Live     string    `json:"live,omitempty"` // working: its last step
	Since    time.Time `json:"since,omitzero"`
}

// apiSettings are the team's settings as the Settings page shows them: what its Team section
// says, and every rota.conf setting for Advanced.
type apiSettings struct {
	Fields  []apiField `json:"fields"`
	Repos   []string   `json:"repos"` // the team's, default_repos, in the file's order
	Own     []apiOwn   `json:"own"`   // the agents that work on a list of their own instead
	Web     string     `json:"web"`
	Phone   string     `json:"phone,omitempty"` // where a phone opens the pages, while they are on the tailnet
	TeamPct string     `json:"teamPct"`
	Pacing  string     `json:"pacing"`
	Budget  string     `json:"budget,omitempty"`
	Fill    int        `json:"fill"`
	Version string     `json:"version"`
}

func (s *site) settingsJSON(w http.ResponseWriter, r *http.Request) {
	t, err := core.Open(s.root, s.conf)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	set, h := t.Settings(), headerOf(t)
	out := apiSettings{Fields: []apiField{}, Repos: append([]string{}, t.Def.Repos...), Own: []apiOwn{}, Web: t.Web, TeamPct: strconv.FormatFloat(t.TeamPct, 'f', -1, 64), Pacing: t.Pacing,
		Budget: h.Budget, Fill: h.Fill, Version: Version}
	for _, f := range core.ConfigFields {
		out.Fields = append(out.Fields, apiField{Key: f.Key, Label: or(f.Label, f.Key), Section: f.Section, Help: f.Help, Kind: f.Kind,
			Options: t.Options(f), Free: len(f.Suggest) > 0, Value: set[f.Key], Effective: f.Get(t.Config)})
	}
	for _, a := range t.Agents {
		if _, own := t.AgentSettings(a.Name)["repos"]; own {
			out.Own = append(out.Own, apiOwn{Name: a.Name, On: a.Enabled, Repos: append([]string{}, a.Repos...)})
		}
	}
	if t.Web == "tailscale" {
		out.Phone = t.PagesURL("/")
	}
	writeJSON(w, r, out)
}

// andList is "a", "a and b", "a, b and c".
func andList(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// apiOwn is an agent whose file names its repositories itself, so the team's do not reach it.
type apiOwn struct {
	Name  string   `json:"name"`
	On    bool     `json:"on"`
	Repos []string `json:"repos"`
}

// apiField is one setting as the app edits it.
type apiField struct {
	Key       string   `json:"key"`
	Label     string   `json:"label"`
	Section   string   `json:"section"`
	Help      string   `json:"help,omitempty"`
	Kind      string   `json:"kind"`
	Options   []string `json:"options,omitempty"`
	Free      bool     `json:"free,omitempty"` // a choice that takes other values too
	Value     string   `json:"value"`          // as the file has it; "" takes the default
	Effective string   `json:"effective"`      // what applies
}

// apiAgentPage is one agent's page: the agent, its runs and reports, its role and its settings.
type apiAgentPage struct {
	apiAgent
	Role     string         `json:"role"`
	RoleHTML string         `json:"roleHTML"`
	RunList  []core.RunInfo `json:"runList"`
	Reports  []apiReport    `json:"reports"`
	Fields   []apiField     `json:"fields"`
}

// apiReport is a report an agent keeps for the human, in its reports folder.
type apiReport struct {
	Name string    `json:"name"`
	At   time.Time `json:"at"`
	URL  string    `json:"url"`
}

// apiShares is a proposal of new weights, from how each agent used the pool.
type apiShares struct {
	Since     time.Time  `json:"since"`
	MinShifts int        `json:"minShifts"`
	Note      string     `json:"note,omitempty"`
	Agents    []apiShare `json:"agents"`
}

type apiShare struct {
	Name     string  `json:"name"`
	Weight   float64 `json:"weight"`
	Share    float64 `json:"share"` // of the pool now, in percent
	Proposed float64 `json:"proposed"`
	Shifts   int     `json:"shifts"`
	Each     string  `json:"each,omitempty"` // of the pool, one shift
	Week     string  `json:"week,omitempty"` // shifts a week
	Need     string  `json:"need,omitempty"` // of the pool a week, or the rest
	Why      string  `json:"why,omitempty"`
}

func (s *site) sharesJSON(w http.ResponseWriter, r *http.Request) {
	t, err := core.Open(s.root, s.conf)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	p := t.Rebalance(time.Now())
	out := apiShares{Since: p.Since, MinShifts: p.MinShifts, Note: p.Note, Agents: []apiShare{}}
	for _, a := range p.Agents {
		x := apiShare{Name: a.Name, Weight: a.Weight, Share: a.Share, Proposed: a.Proposed, Shifts: a.Shifts, Why: a.Why}
		if a.Shifts >= p.MinShifts {
			x.Each, x.Week, x.Need = fmt.Sprintf("%.2f%%", p.Of(a.PerShift)), fmt.Sprintf("%.0f", a.PerWeek), fmt.Sprintf("%.1f%%", p.Of(a.Need))
		}
		if a.Rest {
			x.Need = "the rest"
		}
		out.Agents = append(out.Agents, x)
	}
	writeJSON(w, r, out)
}

// apiRun is one run: what it said at its end, and its transcript.
type apiRun struct {
	core.RunInfo
	Agent   string `json:"agent"`
	Report  string `json:"report,omitempty"`  // HTML
	Events  string `json:"events,omitempty"`  // HTML
	Running bool   `json:"running,omitempty"` // it still runs: the transcript grows
}

func (s *site) teamJSON(w http.ResponseWriter, r *http.Request) {
	t, err := core.Open(s.root, s.conf)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, r, teamOf(t, time.Now()))
}

func teamOf(t *core.Team, now time.Time) apiTeam {
	st, h := t.Status(now), headerOf(t)
	out := apiTeam{State: h.Dot, Title: teamTitle(st), Organizer: st.Pid != 0, Paused: st.Paused, Budget: h.Budget, Fill: h.Fill,
		Running: st.Running, Warnings: t.Warnings, Agents: []apiAgent{}}
	var line []string
	if r := st.Running; r != nil {
		line = append(line, r.Agent+" is working")
	}
	if st.Pid != 0 && st.Next != "" {
		line = append(line, "next: "+st.Next)
	}
	out.Line = strings.Join(line, " · ")
	if st.Pool.Enforced {
		out.Free = st.Pool.FreeText()
	}
	if st.Usage != nil {
		out.Usage = st.Usage.String()
	}
	for _, m := range members(t, st, now) {
		out.Agents = append(out.Agents, agentOf(t, st, statusOf(st, m.Name), m))
	}
	return out
}

// members are the team's agents as their pages show them, by the team as it is now, st, each with
// why it does not run now; one the organizer has not seen yet, as it is now added, is there too.
func members(t *core.Team, st core.Status, now time.Time) []core.Member {
	ms := t.MembersNow(st, now)
	for _, a := range t.Agents {
		if !slices.ContainsFunc(ms, func(m core.Member) bool { return m.Name == a.Name }) {
			ms = append(ms, core.Member{Name: a.Name, State: "idle", Line: "New: the organizer has not looked at it yet"})
		}
	}
	return slices.DeleteFunc(ms, func(m core.Member) bool { return t.Agent(m.Name) == nil }) // and one removed is gone
}

func statusOf(st core.Status, name string) core.AgentStatus {
	for _, a := range st.Agents {
		if a.Name == name {
			return a
		}
	}
	return core.AgentStatus{Name: name}
}

// teamTitle says in a few words whether the team works.
func teamTitle(st core.Status) string {
	switch {
	case st.Pid == 0:
		return "The team is not running"
	case st.Paused:
		return "The team is paused"
	case st.Held() != "":
		return "The team is on hold"
	}
	return "The team is on"
}

func agentOf(t *core.Team, st core.Status, as core.AgentStatus, m core.Member) apiAgent {
	a := apiAgent{Member: m, Enabled: as.Enabled, Paused: as.Paused, Runs: as.Runs, Model: as.ModelEffort(), Disk: as.DiskText(), Over: as.OverCap()}
	if ag := t.Agent(m.Name); ag != nil {
		a.Schedule = ag.Schedule()
	}
	if as.Enabled && !as.Paused {
		a.Next = as.NextText(st.NextTick)
	}
	if !as.LastRun.IsZero() {
		a.Last = as.LastRun.Local().Format("Mon 15:04") + " " + as.LastStatus
	}
	if as.QuotaPct > 0 {
		a.Share = fmt.Sprintf("%.1f%% of its %.1f%%", st.Pool.Of(as.PeriodPct), st.Pool.Of(as.QuotaPct))
	}
	a.Week = fmt.Sprintf("%.1f%%", st.Pool.OfWeek(as.WeekPct))
	if r := st.Running; r != nil && r.Agent == m.Name {
		a.Live, a.Since = r.Live, r.Since
	}
	return a
}

func (s *site) agentJSON(w http.ResponseWriter, r *http.Request) {
	t, err := core.Open(s.root, s.conf)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	name := r.URL.Query().Get("name")
	ag := t.Agent(name)
	if ag == nil {
		http.Error(w, "no such agent", http.StatusNotFound)
		return
	}
	now := time.Now()
	st := t.Status(now)
	m := core.Member{Name: name, State: "idle"}
	for _, x := range members(t, st, now) {
		if x.Name == name {
			m = x
		}
	}
	page := apiAgentPage{apiAgent: agentOf(t, st, statusOf(st, name), m), Role: ag.Role, RoleHTML: string(markdown(ag.Role)), RunList: t.AgentRuns(name, 40), Reports: []apiReport{}, Fields: []apiField{}}
	for _, p := range t.AgentReports(name) {
		rep := apiReport{Name: filepath.Base(p), URL: "/reports/" + url.PathEscape(name) + "/" + url.PathEscape(filepath.Base(p))}
		if fi, err := os.Stat(p); err == nil {
			rep.At = fi.ModTime()
		}
		page.Reports = append(page.Reports, rep)
	}
	set := t.AgentSettings(name)
	for _, f := range core.AgentFields {
		page.Fields = append(page.Fields, apiField{Key: f.Key, Label: or(f.Label, f.Key), Section: f.Section, Help: f.Help, Kind: f.Kind,
			Options: t.Options(f), Free: len(f.Suggest) > 0, Value: set[f.Key], Effective: f.Get(ag)})
	}
	writeJSON(w, r, page)
}

// runID names a run: <date>/<HHMM>-<agent>.
var runID = regexp.MustCompile(`^\d{4}-\d\d-\d\d/\d{4}-[A-Za-z0-9_-][A-Za-z0-9_.-]*$`)

func (s *site) runJSON(w http.ResponseWriter, r *http.Request) {
	t, err := core.Open(s.root, s.conf)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id := r.URL.Query().Get("id")
	ri, ok := core.RunInfo{}, false
	if runID.MatchString(id) && !strings.Contains(id, "..") {
		ri, ok = t.RunInfoOf(id)
	}
	if !ok {
		http.Error(w, "no such run", http.StatusNotFound)
		return
	}
	out := apiRun{Agent: id[len("2006-01-02/1504-"):], RunInfo: ri} // the run id's pattern holds it
	if rep := t.RunReport(id); rep != "" {
		out.Report = string(markdown(rep))
	}
	if tr, ok := t.TranscriptOf(id); ok {
		events, _ := core.ReadEvents(tr.Path)
		var b strings.Builder
		for _, e := range events {
			b.WriteString(eventHTML(e))
		}
		out.Events = b.String()
		out.Running = ri.Status == "running" // its agent's shift now: a log a crash left is not
	}
	writeJSON(w, r, out)
}

// apiChat is the team chat as the app shows it: its messages, each as HTML, who answers now, who
// is in it (the agents at work) and why each other is not, and whether the Team is.
type apiChat struct {
	Msgs   []apiChatMsg    `json:"msgs"`
	Busy   []string        `json:"busy"` // who answers now, the Team ("team") or an agent; always said: [] ends the "typing" the page shows once it sent a message
	Here   []string        `json:"here"`
	Agents []core.Presence `json:"agents"` // every agent, online or why offline
	Team   bool            `json:"team"`
}

type apiChatMsg struct {
	ID     string           `json:"id"`
	At     time.Time        `json:"at"`
	With   string           `json:"with"` // the chat it is in: the Team's ("team"), or an agent's
	From   string           `json:"from,omitempty"`
	To     string           `json:"to,omitempty"`
	HTML   string           `json:"html"`
	About  string           `json:"about,omitempty"` // the human's: what was on their screen as they wrote it
	Change *core.ChatChange `json:"change,omitempty"`
}

func (s *site) chatJSON(w http.ResponseWriter, r *http.Request) {
	t, err := core.Open(s.root, s.conf)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	msgs := t.Chat(300)
	out := apiChat{Msgs: []apiChatMsg{}, Busy: t.ChatBusy(), Here: []string{}, Agents: t.ChatPresence(time.Now()), Team: t.ChatModel != "off"}
	if !slices.Contains(out.Busy, "team") && t.TeamWaits() { // a question a stop of rota left unanswered
		core.WakeTeam(s.root, s.conf)
		out.Busy = append([]string{"team"}, out.Busy...)
	}
	for _, m := range msgs {
		html := string(markdown(m.Text)) // escaped there, as the mail's
		if m.From == "" {
			html = esc(m.Text)
		}
		out.Msgs = append(out.Msgs, apiChatMsg{ID: m.ID, At: m.At, With: m.With(), From: m.From, To: m.To, HTML: html, About: m.About, Change: m.Change})
	}
	for _, p := range out.Agents {
		if p.Online {
			out.Here = append(out.Here, p.Name)
		}
	}
	body, _ := json.Marshal(out)
	sum := sha256.Sum256(body)
	tag := `"` + hex.EncodeToString(sum[:12]) + `"`
	w.Header().Set("ETag", tag)
	if r.Header.Get("If-None-Match") == tag { // the chat as the page has it
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, r, out)
}

// writeJSON writes v, gzipped when the browser takes it.
func writeJSON(w http.ResponseWriter, r *http.Request, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Vary", "Accept-Encoding")
	if len(body) < 2048 || !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Write(body)
		return
	}
	w.Header().Set("Content-Encoding", "gzip")
	gz := gzip.NewWriter(w)
	gz.Write(body)
	gz.Close()
}

// teamAct does what the human did on the team's pages; ok is false for an action that is not one.
func (s *site) teamAct(t *core.Team, action, arg, text, about string) (said string, ok bool, err error) {
	switch action {
	case "pause", "resume", "run": // arg: the agent; "" the team
		said, err = s.control(t, action, arg, "", "", "")
	case "cancel": // the running shift of the agent arg, and no other
		if err := t.CancelShift(arg); err != nil {
			return "", true, err
		}
		said = "stopping " + arg + "'s run"
	case "start":
		said, err = s.control(t, "start", "", "", "", "")
	case "agent-set": // arg: the agent; text: its settings as JSON, key to value, "" for the team default
		var set map[string]string
		if err := json.Unmarshal([]byte(text), &set); err != nil || len(set) == 0 {
			return "", true, fmt.Errorf("which settings?")
		}
		for k := range set {
			if _, known := core.AgentField(k); !known {
				return "", true, fmt.Errorf("no setting %q", k)
			}
		}
		said, err = "Saved. Its next run has it.", t.SetAgent(arg, set)
	case "team-set": // text: the team's settings as JSON, key to value, "" for the default
		var set map[string]string
		if err := json.Unmarshal([]byte(text), &set); err != nil || len(set) == 0 {
			return "", true, fmt.Errorf("which settings?")
		}
		for k := range set {
			if _, known := core.ConfigField(k); !known {
				return "", true, fmt.Errorf("no setting %q", k)
			}
		}
		said, err = "Saved. It applies within seconds.", t.SetSettings(set)
	case "repo-add": // arg: owner/name, or its link on GitHub; text: its place in the list, for Undo ("": the end)
		at := -1
		if n, err := strconv.Atoi(text); err == nil {
			at = n
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second) // GitHub is asked whether it exists
		defer cancel()
		name, err := t.AddRepo(ctx, arg, at)
		if err != nil {
			return "", true, err
		}
		var own []string // whom the team's list does not reach
		for _, a := range t.Agents {
			if _, ok := t.AgentSettings(a.Name)["repos"]; ok {
				own = append(own, a.Name)
			}
		}
		said = "added " + name + ": every agent works on it from its next run"
		if len(own) > 0 {
			said = "added " + name + ": every agent but " + andList(own) + " works on it from its next run"
		}
	case "repo-remove": // arg: owner/name
		name, err := t.RemoveRepo(arg)
		if err != nil {
			return "", true, err
		}
		said = "removed " + name
	case "chat-seen": // arg: a chat, "team" or an agent's; text: when the newest message the human saw in it was said
		at, perr := time.Parse(time.RFC3339Nano, text)
		if perr != nil {
			return "", true, fmt.Errorf("seen up to when?")
		}
		err = t.ChatSeen(arg, at)
	case "chat": // text: what the human writes in the team chat; arg: the chat, an agent's, or "" for the Team's; about: what is on their screen
		root, conf := s.root, s.conf
		_, err = t.ChatSendAbout(arg, text, about, func() { core.WakeTeam(root, conf) })
	case "chat-apply": // arg: the change's id
		said, err = t.ChatApply(arg)
	case "chat-cancel":
		said, err = "", t.ChatCancel(arg)
	case "chat-undo":
		said, err = t.ChatUndo(arg)
	case "shares-apply": // text: the enabled agents' new weights, as JSON, name to weight
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(text), &raw); err != nil || len(raw) == 0 {
			return "", true, fmt.Errorf("which weights?")
		}
		weights := map[string]float64{}
		for name, v := range raw {
			var w float64
			if err := json.Unmarshal(v, &w); err != nil || string(v) == "null" || w < 0 {
				return "", true, fmt.Errorf("%s: a weight is a number, 0 or more; nothing was saved", name)
			}
			weights[name] = w
		}
		for name := range weights {
			if a := t.Agent(name); a == nil || !a.Enabled { // one disabled since the page was made is left as it is
				delete(weights, name)
			}
		}
		said, err = "Weights applied: the organizer uses them within seconds.", t.SetWeights(weights)
	case "role": // arg: the agent; text: its new role
		if strings.TrimSpace(text) == "" {
			return "", true, fmt.Errorf("the role is empty")
		}
		said, err = "Saved. Its next run works under the new role.", t.SetRole(arg, text)
	default:
		return "", false, nil
	}
	return said, true, err
}

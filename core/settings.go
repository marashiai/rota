package core

import (
	"encoding"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
)

// Every setting is declared once, as a field of Config or Agent with tags: its key, default,
// help, section and allowed values. Reading and writing rota.conf and the agent files, the
// documented rota.conf that rota init writes, rota config, the editors in the console and
// the web pages, and the hints for mistyped keys all come from these declarations.

// Config is rota.conf: only what someone running a team would want to change. The team
// folder's layout and the fixed parts of a shift are constants (layout.go).
type Config struct {
	Root     string   // the team folder
	File     string   // the config file, relative to Root
	Warnings []string // settings of the file that were ignored, and why

	ClaudeBin string `key:"claude_bin" default:"claude" section:"Claude" help:"the Claude Code CLI"`
	ExtraArgs Words  `key:"extra_args" help:"appended to every claude command"`
	Usage     string `key:"usage" default:"auto" choices:"auto|on|off" help:"read the subscription's rate windows from Claude: auto is on when a Claude token is found"`

	TeamPct float64 `key:"team_pct" default:"20" check:">=0" section:"The team pool" help:"the part of each day's seventh of the weekly window the agents use together: the only limit on them"`
	Pacing  string  `key:"pacing" default:"daily" choices:"daily|weekly" help:"daily: a day's unused pool does not carry over"`
	Week    Week    `key:"week_start" default:"monday" choices:"claude|monday|tuesday|wednesday|thursday|friday|saturday|sunday" help:"claude: Claude's rolling 7-day window, or the weekday a calendar week starts"`
	Zone    Zone    `key:"timezone" default:"Local" help:"an IANA name, e.g. Europe/Amsterdam"`

	TickMinutes   float64 `key:"tick_minutes" default:"60" check:">0" section:"Scheduling" help:"how often rota start looks for a shift to run"`
	MaxAttempts   int     `key:"max_attempts" default:"3" check:">=1" help:"only a shift that finishes ok is a run; anything else is retried, at most this many attempts in all"`
	RetryMinutes  float64 `key:"retry_minutes" default:"15" check:">0" help:"wait before the first retry; doubles with every attempt"`
	AnswerMinutes float64 `key:"answer_minutes" default:"30" check:">0" help:"how long an agent may wait in its shift for your answer to its question (rota wait) before it sets that work aside"`
	TalkMinutes   float64 `key:"talk_minutes" default:"5" check:">0" help:"how long an agent at work, its work stopped while you talk with it in the chat, waits for your next words before it goes back to its work"`
	TriageModel   string  `key:"triage_model" default:"haiku" suggest:"haiku|sonnet|off" help:"the model that judges whether a failed shift is worth retrying; off: always retry"`
	ReportModel   string  `key:"report_model" default:"sonnet" suggest:"haiku|sonnet|opus|off" help:"the model that rewrites your report into a short executive summary when a shift ends with something you should know; off: the report lists what each shift said"`
	ChatModel     string  `key:"chat_model" default:"sonnet" suggest:"haiku|sonnet|opus|off" help:"the model of the Team in the chat, which answers you about the team and proposes changes you apply; off: no Team, only the agents at work"`

	Listen     string `key:"listen" default:"tailscale" suggest:"tailscale|off|127.0.0.1" section:"Hosts" help:"where hosts may join: tailscale (this machine's Tailscale address), off, or an IP"`
	ListenPort int    `key:"listen_port" default:"7800"`
	WorkerName string `key:"worker_name" help:"the name this machine joins under; empty: its hostname"`

	Web     string `key:"web" default:"local" choices:"local|tailscale|off" section:"Web pages" help:"where rota start serves the pages: local (this machine only), tailscale (also at this machine's Tailscale address, for your phone and other devices; there is no login, so anyone on your tailnet can use them), or off; rota web serves them while it is off"`
	WebPort int    `key:"web_port" default:"7900"`

	TrayAutostart bool `key:"tray_autostart_organizer" default:"false" section:"Menu bar (macOS)" help:"opening the app also starts the organizer"`

	SyncRepos bool   `key:"sync_repos" default:"true" section:"Repositories and git" help:"clone or fast-forward each agent's repositories before its shift"`
	CloneCmd  string `key:"clone_cmd" default:"gh repo clone {repo} {dir}" help:"how a missing repository is cloned; {repo} {dir} {name} are replaced"`
	Commit    bool   `key:"commit" default:"true" help:"commit journal, inbox and spend.csv when the team folder is a git repository"`
	Push      bool   `key:"push" default:"true"`
	GitAuthor string `key:"git_author" help:"Name <email> of those commits; empty: the machine's git config"`

	Labels      Labels `key:"labels" section:"GitHub labels" help:"the team's own labels besides rota and rota:<agent>, the same in every team repository: name color [emoji] description, separated by ;. The emoji marks a pull request that carries it in the Reviews tab"`
	HoldLabel   string `key:"hold_label" help:"rota merge does not merge a pull request with this label, nor one that depends on one with it, merged or not, until the label is removed"`
	ReviewLabel string `key:"review_label" help:"the label that says a pull request waits for your review: the Reviews tab shows rota:approved in its place once you approved the pull request"`

	LiveLog     string `key:"live_log" default:"tools" choices:"off|tools|all" section:"Live output and logs" help:"what a running shift prints: each tool call (tools), also the agent's text (all), or nothing"`
	LogKeepDays int    `key:"log_keep_days" default:"30" check:">=0" help:"shift logs older than this are deleted; 0 keeps everything"`

	Def Agent `key:"default_" section:"Agent defaults"` // any agent key, as default_<key>
}

// Agent is one teammate: agents/<name>.md, front matter plus its role.
type Agent struct {
	Name    string
	Role    string // the body of the agent file: the system prompt
	WorkDir string // work/<name>: its working directory, repositories and Claude memory
	EnvFile string // secrets/<name>.env: KEY=VALUE lines put in its environment

	Enabled    bool     `key:"enabled" default:"false" section:"Schedule" label:"Enabled" help:"the scheduler may pick this agent"`
	Every      Interval `key:"every" default:"20h" label:"Every" help:"one run is due this long after the last: 90m, 20h, 1d, 7d; a missed due time runs once, as soon as it can"`
	Hours      Hours    `key:"hours" label:"Hours" help:"local clock windows a shift may start in, e.g. 05:00-07:00, 15:00-17:00; empty: any time"`
	Days       Days     `key:"days" label:"Days" help:"weekdays its hours apply on, e.g. fri or mon, thu; a window past midnight belongs to the day it starts; empty: every day"`
	OwnHours   bool     `key:"own_hours" default:"false" label:"Owns its hours" help:"in its hours it goes ahead of all but the agents you ask for and starts no shift in a window's last 15 minutes; its shift stops when the window ends, and that counts as a run; an hour has one owner: where two enabled agents' owned hours overlap, the first by priority, then name, keeps them"`
	RunMinutes int      `key:"run_minutes" default:"45" check:">0" label:"Timeout" help:"minutes before the shift is stopped"`
	After      List     `key:"after" label:"After" help:"also runs after each shift of these agents, e.g. developer: each asks for it, and it waits its turn; it does not ask for them back"`
	WaitFor    List     `key:"wait_for" label:"Waits for" help:"holds back while one of these agents would start a shift now, so its own starts from what theirs leaves, e.g. planner for a developer that works from the planner's order; one that cannot start then (off, paused, outside its hours, out of pool) holds nothing back, nor one that waits for it"`
	On         Triggers `key:"on" label:"On" help:"starts a shift on these events too, as a request does: on GitHub, in its repositories (rota looks every 2 minutes): issue-opened, issue-labeled, issue-unlabeled, issue-commented, issue-closed, issue-reopened, pr-opened, pr-ready, pr-labeled, pr-unlabeled, pr-commented, pr-approved, pr-merged, pr-closed (without a merge), pr-conflict, pr-checks-failed, base-moved, tag-created; a label after a colon narrows the labeled and unlabeled ones, * matching any text, e.g. issue-labeled:priority:*; and note: a teammate's note to it. Each reaches it as a note; one during its own shift reaches that shift and starts no other"`
	UntilDone  bool     `key:"until_done" default:"false" label:"Until done" help:"shift after shift while each ends with More: yes, then idle until one of its triggers (on), your note or your answer; every does not apply"`

	Priority int     `key:"priority" default:"100" section:"Share of the team pool" label:"Priority" help:"lower runs first, and gets its fair share of the pool first"`
	Weight   float64 `key:"weight" default:"1" check:">=0" label:"Weight" help:"its fair share of the team pool, relative to the other enabled agents"`

	Model           string `key:"model" section:"Claude" label:"Model" suggest:"|fable|opus|sonnet|haiku" help:"empty: Claude Code's default; or a full model name"`
	Effort          string `key:"effort" label:"Effort" choices:"|low|medium|high|xhigh|max" help:"empty: Claude Code's default"`
	PermissionMode  string `key:"permission_mode" default:"default" label:"Permission mode" suggest:"auto|default|acceptEdits|bypassPermissions|plan" help:"auto: a classifier reviews commands and nothing prompts; default: whatever would prompt is denied"`
	Tools           string `key:"tools" default:"Read,Grep,Glob" label:"Allowed tools" help:"comma separated, e.g. Read,Grep,Glob,Edit,Bash(git *)"`
	DisallowedTools string `key:"disallowed_tools" label:"Disallowed tools"`
	ExtraArgs       Words  `key:"extra_args" label:"Extra claude args" help:"space separated"`

	Host   string `key:"host" suggest:"|any" section:"Where it works" label:"Host" help:"where shifts run: empty is this machine, any is any joined host, or a host's name"`
	GhUser string `key:"gh_user" label:"GitHub account" help:"the gh login shifts act as, on the machine that runs them"`
	Repos  List   `key:"repos" label:"Repositories" help:"owner/name, comma separated; cloned into its work folder"`
	DiskGB int    `key:"disk_gb" default:"100" check:">=0" label:"Disk cap" help:"the most its work folder may hold, in GB, as measured after each shift: over it, its next shift frees its own unused files first, or asks you for more; 0: no cap"`

	Warnings []string // settings in the file that were ignored, and why
}

// Field is one declared setting.
type Field struct {
	Key, Section, Label, Help string
	Default                   string
	Kind                      string   // how an editor shows it: check, number, choice or text
	Choices                   []string // the only values allowed
	Suggest                   []string // values offered; others are allowed too
	check                     string   // a lower limit: >0 or >=1
	index                     []int
}

// Options are the values an editor offers for a choice.
func (f Field) Options() []string { return append(append([]string{}, f.Choices...), f.Suggest...) }

var (
	ConfigFields = fieldsOf(reflect.TypeOf(Config{}), "", nil)
	AgentFields  = fieldsOf(reflect.TypeOf(Agent{}), "", nil)
)

func fieldsOf(t reflect.Type, prefix string, at []int) []Field {
	var out []Field
	section := ""
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		key, ok := sf.Tag.Lookup("key")
		if !ok {
			continue
		}
		if s := sf.Tag.Get("section"); s != "" {
			section = s
		}
		index := append(append([]int{}, at...), i)
		if sf.Type == reflect.TypeOf(Agent{}) { // default_<key>: every agent field again, for the whole team
			for _, f := range fieldsOf(sf.Type, key, index) {
				f.Section = section
				out = append(out, f)
			}
			continue
		}
		split := func(s string) []string {
			if s == "" {
				return nil
			}
			return strings.Split(s, "|")
		}
		f := Field{Key: prefix + key, Section: section, Label: sf.Tag.Get("label"), Help: sf.Tag.Get("help"), Kind: "text",
			Default: sf.Tag.Get("default"), Choices: split(sf.Tag.Get("choices")), Suggest: split(sf.Tag.Get("suggest")),
			check: sf.Tag.Get("check"), index: index}
		switch {
		case f.Choices != nil || f.Suggest != nil:
			f.Kind = "choice"
		case sf.Type.Kind() == reflect.Bool:
			f.Kind = "check"
		case sf.Type.Kind() == reflect.Int || sf.Type.Kind() == reflect.Float64:
			f.Kind = "number"
		}
		out = append(out, f)
	}
	return out
}

// AgentField is the agent setting key.
func AgentField(key string) (Field, bool) { return lookup(AgentFields, key) }

// ConfigField is the rota.conf setting key.
func ConfigField(key string) (Field, bool) { return lookup(ConfigFields, key) }

func lookup(fields []Field, key string) (Field, bool) {
	for _, f := range fields {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

// Get is the field's value in v (a *Config or *Agent) as the file writes it.
func (f Field) Get(v any) string {
	fv := reflect.ValueOf(v).Elem().FieldByIndex(f.index)
	if m, ok := fv.Interface().(encoding.TextMarshaler); ok {
		b, _ := m.MarshalText()
		return string(b)
	}
	if fv.Kind() == reflect.Float64 {
		return strconv.FormatFloat(fv.Float(), 'f', -1, 64)
	}
	return fmt.Sprint(fv.Interface())
}

// Set parses text into the field of v (a *Config or *Agent), or says why it cannot.
func (f Field) Set(v any, text string) error {
	text = strings.TrimSpace(text)
	if len(f.Choices) > 0 {
		i := slices.IndexFunc(f.Choices, func(c string) bool { return strings.EqualFold(c, text) })
		if i < 0 {
			return fmt.Errorf("one of %s", strings.Join(f.Choices, ", "))
		}
		text = f.Choices[i]
	}
	fv := reflect.ValueOf(v).Elem().FieldByIndex(f.index)
	var n float64
	var err error
	if u, ok := fv.Addr().Interface().(encoding.TextUnmarshaler); ok {
		err = u.UnmarshalText([]byte(text))
	} else {
		switch fv.Kind() {
		case reflect.String:
			fv.SetString(text)
		case reflect.Bool:
			var b bool
			if b, err = strconv.ParseBool(text); err == nil {
				fv.SetBool(b)
			}
		case reflect.Int:
			var i int
			if i, err = strconv.Atoi(text); err == nil {
				fv.SetInt(int64(i))
				n = float64(i)
			}
		case reflect.Float64:
			if n, err = strconv.ParseFloat(text, 64); err == nil {
				fv.SetFloat(n)
			}
		}
	}
	if err != nil {
		return err
	}
	if f.check != "" {
		l, _ := strconv.ParseFloat(strings.TrimLeft(f.check, ">="), 64)
		if n < l || n == l && !strings.HasPrefix(f.check, ">=") {
			return fmt.Errorf("must be %s", f.check)
		}
	}
	if check := checks[strings.TrimPrefix(f.Key, "default_")]; check != nil {
		return check(text)
	}
	return nil
}

// checks are the few values with a shape of their own.
var checks = map[string]func(string) error{
	"clone_cmd": func(v string) error {
		if !strings.Contains(v, "{repo}") {
			return errors.New("must contain {repo}")
		}
		return nil
	},
	"git_author": func(v string) error {
		if v != "" && (!strings.Contains(v, "<") || !strings.HasSuffix(v, ">")) {
			return errors.New(`expected "Name <email>"`)
		}
		return nil
	},
	"worker_name": func(v string) error {
		if v != "" && !hostName.MatchString(v) {
			return errors.New("letters, digits, . _ - only")
		}
		return nil
	},
	"host": func(v string) error {
		if v != "" && v != "local" && v != "any" && !hostName.MatchString(v) {
			return errors.New("local, any or a host name")
		}
		return nil
	},
	"hold_label": func(v string) error {
		if strings.ContainsAny(v, " \t") {
			return errors.New("one label name, without spaces")
		}
		return nil
	},
}

var hostName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)

// apply sets the key=value pairs of a file into v: a wrong value is an error, an unknown key
// (a typo) a warning.
func apply(v any, fields []Field, kv map[string]string, file string) (warnings []string, err error) {
	for _, k := range sortedKeys(kv) {
		val := kv[k]
		f, ok := lookup(fields, k)
		switch {
		case !ok:
			warnings = append(warnings, unknownText(file, k, fields))
		case val == "": // empty is empty (no repos, any hours); a field that needs a value keeps its default
			fv := reflect.ValueOf(v).Elem().FieldByIndex(f.index)
			def := reflect.New(fv.Type()).Elem()
			def.Set(fv)
			if f.Set(v, "") != nil {
				fv.Set(def)
			}
		default:
			if err := f.Set(v, val); err != nil {
				return nil, fmt.Errorf("%s: %s: %w", file, k, err)
			}
		}
	}
	return warnings, nil
}

func unknownText(file, key string, fields []Field) string {
	s := fmt.Sprintf("%s: %s is not a setting, ignored", file, key)
	if alt := suggestKey(key, fields); alt != "" {
		return s + " (did you mean " + alt + "?)"
	}
	return s
}

// suggestKey names the known setting closest to an unknown one: the same key with or
// without default_ in the wrong file, else one within two typos.
func suggestKey(k string, fields []Field) string {
	if _, ok := lookup(fields, "default_"+k); ok {
		return "default_" + k
	}
	if _, ok := lookup(fields, strings.TrimPrefix(k, "default_")); ok && strings.HasPrefix(k, "default_") {
		return strings.TrimPrefix(k, "default_")
	}
	best, bestD := "", 3
	if len(k) <= 5 {
		bestD = 2
	}
	for _, f := range fields {
		if d := editDistance(k, f.Key); d < bestD {
			best, bestD = f.Key, d
		}
	}
	return best
}

// editDistance counts insertions, deletions, substitutions and swaps of neighbors.
func editDistance(a, b string) int {
	d := make([][]int, len(a)+1)
	for i := range d {
		d[i] = make([]int, len(b)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			sub := 1
			if a[i-1] == b[j-1] {
				sub = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+sub)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(a)][len(b)]
}

// DefaultConfig is every setting at its default.
func DefaultConfig(root string) *Config {
	c := &Config{Root: root, File: "rota.conf"}
	for _, f := range ConfigFields {
		if err := f.Set(c, f.Default); err != nil && f.Default != "" {
			panic(fmt.Sprintf("default of %s: %v", f.Key, err))
		}
	}
	return c
}

// LoadConfig reads root/file over the defaults; a missing file is all defaults.
func LoadConfig(root, file string) (*Config, error) {
	c := DefaultConfig(root)
	c.File = file
	b, err := os.ReadFile(c.Path(file))
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	kv, err := parseKV(string(b))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	c.Warnings, err = apply(c, ConfigFields, kv, file)
	return c, err
}

// Path resolves p against the team folder.
func (c *Config) Path(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(c.Root, p)
}

// Rel is path relative to the team folder, for messages.
func (c *Config) Rel(path string) string {
	if r, err := filepath.Rel(c.Root, path); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return path
}

// Worker is the name this machine joins under.
func (c *Config) Worker() string {
	if c.WorkerName != "" {
		return c.WorkerName
	}
	h, _ := os.Hostname()
	if h, _, _ = strings.Cut(h, "."); hostName.MatchString(h) {
		return h
	}
	return "host"
}

// loadAgent reads agents/<name>.md over the team's agent defaults.
func loadAgent(c *Config, path string) (*Agent, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseAgent(c, path, b)
}

// parseAgent is the agent the file at path would be with content b.
func parseAgent(c *Config, path string, b []byte) (*Agent, error) {
	a := c.Def
	a.Name = strings.TrimSuffix(filepath.Base(path), ".md")
	if strings.EqualFold("rota:"+a.Name, LeaveLabel) || strings.EqualFold("rota:"+a.Name, ApprovedLabel) { // its issues would carry the human's word
		return nil, fmt.Errorf("%s: an agent may not be named %s", c.Rel(path), a.Name)
	}
	head, body, err := splitFront(string(b))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.Rel(path), err)
	}
	kv, err := parseKV(head)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.Rel(path), err)
	}
	if a.Warnings, err = apply(&a, AgentFields, kv, c.Rel(path)); err != nil {
		return nil, err
	}
	if a.Host == "local" {
		a.Host = ""
	}
	a.Role = strings.TrimSpace(body)
	a.WorkDir, a.EnvFile = filepath.Join(workDir, a.Name), filepath.Join(secretsDir, a.Name+".env")
	return &a, nil
}

// ---- writing ----

// withFrontMatter is the agent file b with keys of its front matter rewritten, the rest byte
// for byte; an empty value removes the key, so the team default applies. Values are checked
// first.
func withFrontMatter(b string, set map[string]string) (string, error) {
	return frontMatter(b, set, nil, false)
}

// frontMatter is withFrontMatter; exact writes an empty value as it is ("hours:", any time, which
// is not the team's default) and removes the keys in absent.
func frontMatter(b string, set map[string]string, absent []string, exact bool) (string, error) {
	var probe Agent
	for k, v := range set {
		f, ok := lookup(AgentFields, k)
		if !ok {
			return "", fmt.Errorf("%s: not an agent setting", k)
		}
		if strings.ContainsAny(v, "\r\n") { // one line, one setting
			return "", fmt.Errorf("%s: a value is one line", k)
		}
		if v != "" {
			if err := f.Set(&probe, v); err != nil {
				return "", fmt.Errorf("%s: %w", k, err)
			}
		}
	}
	head, body, err := splitFront(b)
	if err != nil {
		return "", err
	}
	var lines []string
	done := map[string]bool{}
	for _, line := range strings.Split(head, "\n") {
		k, _, ok := strings.Cut(line, ":")
		k = strings.ToLower(strings.TrimSpace(k))
		if ok && slices.Contains(absent, k) {
			continue
		}
		if v, want := set[k]; ok && want {
			done[k] = true
			if v == "" && !exact {
				continue
			}
			line = strings.TrimRight(k+": "+v, " ")
		}
		lines = append(lines, line)
	}
	for _, k := range sortedKeys(set) {
		if !done[k] && (set[k] != "" || exact) {
			lines = append(lines, strings.TrimRight(k+": "+set[k], " "))
		}
	}
	return "---\n" + strings.Join(lines, "\n") + "\n---\n" + body, nil
}

// setRole replaces the body of an agent file: its role.
// writeWhole replaces a file at once, through a temporary file renamed over it: the organizer and
// the pages, which read the settings every few seconds, see the old file or the new one, never
// one half written, which would read as the defaults.
func writeWhole(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp") // its own: two writers never share one
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(f.Name(), 0o644)
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// changeLocked runs change, a read and a write of path, while it holds path's lock: two changes
// at once, from the pages and the console, do not undo each other.
func changeLocked(path string, change func() error) error {
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	return change()
}

func setRole(path, role string) error {
	return changeLocked(path, func() error {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		head, _, err := splitFront(string(b))
		if err != nil {
			return err
		}
		return writeWhole(path, []byte("---\n"+head+"\n---\n"+strings.TrimSpace(role)+"\n"))
	})
}

// setConfKey writes one setting into rota.conf: every line that sets it gets the new value
// with its comment kept in place; a key the file has only commented out is uncommented; a
// key the file lacks is appended. Everything else stays byte for byte.
// checkSetting says whether value can be written for key in rota.conf.
func checkSetting(key, value string) error {
	value = strings.TrimSpace(value)
	f, ok := lookup(ConfigFields, key)
	switch {
	case !ok:
		return fmt.Errorf("%s: not a setting", key)
	case strings.ContainsAny(value, "\r\n") || strings.Contains(value, " #") || value == "#" || strings.HasPrefix(value, "# "):
		return fmt.Errorf("%s: a value cannot hold a line break or \" #\", which starts a comment", key)
	case value != "":
		if err := f.Set(DefaultConfig(""), value); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	return nil
}

func setConfKey(path, key, value string) error {
	return setConfKeys(path, map[string]string{key: value})
}

// setConfKeys writes settings into rota.conf in one write, all or none: each is checked first.
func setConfKeys(path string, set map[string]string) error {
	_, _, err := swapConf(path, set)
	return err
}

// swapConf writes settings into rota.conf as setConfKeys does, and says what they replaced:
// each key's value as written, or that the file had none.
func swapConf(path string, set map[string]string) (was map[string]string, absent []string, err error) {
	for k, v := range set {
		if strings.ContainsAny(v, "\r\n") {
			return nil, nil, fmt.Errorf("%s: a value is one line", k)
		}
		if err := checkSetting(k, v); err != nil {
			return nil, nil, err
		}
	}
	was = map[string]string{}
	err = changeLocked(path, func() error {
		b, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		kv, _ := parseKV(string(b))
		text := string(b)
		for _, k := range sortedKeys(set) {
			if v, ok := kv[k]; ok {
				was[k] = v
			} else {
				absent = append(absent, k)
			}
			text = withConfKey(text, k, strings.TrimSpace(set[k]))
		}
		return writeWhole(path, []byte(text))
	})
	return was, absent, err
}

// restoreConf writes back what swapConf replaced: the values as they were, keys that were not
// there removed; unless one of them changed since from what was set then (expect).
func restoreConf(path string, was map[string]string, absent []string, expect map[string]string) error {
	return changeLocked(path, func() error {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		kv, _ := parseKV(string(b))
		for k, v := range expect { // as the swap left it: written, an empty value too
			if now, there := kv[k]; !there || now != strings.TrimSpace(v) {
				return fmt.Errorf("%s has changed since: it is %q now; undo would overwrite that", k, now)
			}
		}
		text := string(b)
		for _, k := range sortedKeys(was) {
			text = withConfKey(text, k, was[k])
		}
		var keep []string
		for _, line := range strings.Split(text, "\n") {
			k, _, ok := strings.Cut(line, ":")
			if ok && !strings.HasPrefix(strings.TrimSpace(line), "#") && slices.Contains(absent, strings.ToLower(strings.TrimSpace(k))) {
				continue
			}
			keep = append(keep, line)
		}
		return writeWhole(path, []byte(strings.Join(keep, "\n")))
	})
}

// withConfKey is rota.conf's text with key set: its line rewritten, or a commented-out one
// taken back, or a line added.
func withConfKey(text, key, value string) string {
	lines := strings.Split(text, "\n")
	done, commented := false, -1
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if k, _, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(t, "#")), ":"); ok && strings.EqualFold(strings.TrimSpace(k), key) {
			if !strings.HasPrefix(t, "#") {
				lines[i], done = confLine(line, key, value), true
			} else if commented < 0 {
				commented = i
			}
		}
	}
	switch {
	case done:
	case commented >= 0:
		lines[commented] = confLine(strings.TrimPrefix(strings.TrimSpace(lines[commented]), "#"), key, value)
	default:
		if n := len(lines); n > 0 && lines[n-1] == "" {
			lines = lines[:n-1]
		}
		lines = append(lines, key+": "+value, "")
	}
	return strings.Join(lines, "\n")
}

// confLine is "key: value" with the old line's comment in the same column.
func confLine(old, key, value string) string {
	out := key + ": " + value
	_, rest, _ := strings.Cut(old, ":")
	if _, comment := cutComment(rest); comment != "" {
		col := len(old) - len(comment)
		if pad := col - len(out); pad >= 1 {
			return out + strings.Repeat(" ", pad) + comment
		}
		return out + " " + comment
	}
	return out
}

// cutComment splits a value from its comment: "# ..." right after the colon, or " #"
// anywhere after; "##" is a value (a markdown heading).
func cutComment(rest string) (value, comment string) {
	t := strings.TrimLeft(rest, " \t")
	if t == "#" || strings.HasPrefix(t, "# ") {
		return "", t
	}
	if i := strings.Index(t, " #"); i >= 0 {
		return strings.TrimSpace(t[:i]), strings.TrimLeft(t[i:], " ")
	}
	return strings.TrimSpace(t), ""
}

// ConfFile is rota.conf documented: every setting by section with its help, the values
// given set and the rest commented out at their defaults. rota init writes it.
func ConfFile(values map[string]string) string {
	var b strings.Builder
	b.WriteString("# rota.conf: every setting of the team, by section, with its default. A line that starts\n" +
		"# with # is off; remove the # to change it. Paths are relative to the team folder.\n")
	section := ""
	for _, f := range ConfigFields {
		if f.Section != section {
			section = f.Section
			fmt.Fprintf(&b, "\n# %s\n", section)
		}
		b.WriteString(confEntry(f, values))
	}
	return b.String()
}

func confEntry(f Field, values map[string]string) string {
	line := f.Key + ": " + f.Default
	if v, ok := values[f.Key]; ok {
		line = f.Key + ": " + v
	} else {
		line = "#" + line
	}
	if f.Help == "" {
		return line + "\n"
	}
	pad := max(1, 34-len(line))
	return line + strings.Repeat(" ", pad) + "# " + f.Help + "\n"
}

// ---- the value types with a text form of their own ----

// Interval is an agent's every: 90m, 20h, 1d, 7d, or a plain number of hours.
type Interval time.Duration

func (d *Interval) UnmarshalText(b []byte) error {
	v := strings.ToLower(strings.TrimSpace(string(b)))
	if n, err := strconv.ParseFloat(v, 64); err == nil {
		*d = Interval(n * float64(time.Hour))
		return nil
	}
	if n, err := strconv.ParseFloat(strings.TrimSuffix(v, "d"), 64); err == nil && strings.HasSuffix(v, "d") {
		*d = Interval(n * 24 * float64(time.Hour))
		return nil
	}
	x, err := time.ParseDuration(v)
	if err != nil {
		return errors.New("use 90m, 2h, 1d or 7d")
	}
	*d = Interval(x)
	return nil
}

func (d Interval) MarshalText() ([]byte, error) {
	x := time.Duration(d)
	switch {
	case x == 0:
		return []byte("0"), nil
	case x%(24*time.Hour) == 0:
		return []byte(fmt.Sprintf("%dd", x/(24*time.Hour))), nil
	case x%time.Hour == 0:
		return []byte(fmt.Sprintf("%dh", x/time.Hour)), nil
	case x%time.Minute == 0:
		return []byte(fmt.Sprintf("%dm", x/time.Minute)), nil
	}
	return []byte(x.String()), nil
}

// Window is a span of the local day a shift may start in, in minutes after midnight; one
// that ends at or before its start wraps past midnight (23:00-01:00).
type Window struct{ Start, End int }

func (w Window) contains(m int) bool {
	if w.Start < w.End {
		return m >= w.Start && m < w.End
	}
	return m >= w.Start || m < w.End
}

func (w Window) String() string {
	return fmt.Sprintf("%02d:%02d-%02d:%02d", w.Start/60, w.Start%60, w.End/60, w.End%60)
}

// Hours are an agent's windows: "05:00-07:00, 15:00-17:00"; empty or any is any time.
type Hours []Window

func (h *Hours) UnmarshalText(b []byte) error {
	v := strings.TrimSpace(strings.ToLower(string(b)))
	*h = nil
	if v == "" || v == "any" {
		return nil
	}
	clock := func(s string) (int, error) {
		if s = strings.TrimSpace(s); s == "24:00" {
			return 24 * 60, nil
		}
		t, err := time.Parse("15:04", s)
		return t.Hour()*60 + t.Minute(), err
	}
	for _, part := range strings.Split(v, ",") {
		from, to, ok := strings.Cut(part, "-")
		var w Window
		var err error
		if ok {
			if w.Start, err = clock(from); err == nil {
				w.End, err = clock(to)
			}
		}
		if !ok || err != nil || w.Start == w.End {
			*h = nil
			return errors.New("use HH:MM-HH:MM windows separated by commas, e.g. 05:00-07:00, 15:00-17:00")
		}
		*h = append(*h, w)
	}
	return nil
}

func (h Hours) MarshalText() ([]byte, error) {
	s := make([]string, len(h))
	for i, w := range h {
		s[i] = w.String()
	}
	return []byte(strings.Join(s, ", ")), nil
}

// Days are the weekdays an agent's hours apply on: "fri" or "mon, thu"; empty or any is every day.
type Days []time.Weekday

func (d *Days) UnmarshalText(b []byte) error {
	v := strings.TrimSpace(strings.ToLower(string(b)))
	*d = nil
	if v == "" || v == "any" {
		return nil
	}
	for _, part := range strings.Split(v, ",") {
		name, found := strings.TrimSpace(part), false
		for w := time.Sunday; w <= time.Saturday && len(name) >= 3; w++ {
			if found = strings.HasPrefix(strings.ToLower(w.String()), name); found {
				*d = append(*d, w)
				break
			}
		}
		if !found {
			*d = nil
			return errors.New("use weekdays separated by commas, e.g. fri or mon, thu")
		}
	}
	return nil
}

func (d Days) MarshalText() ([]byte, error) {
	s := make([]string, len(d))
	for i, w := range d {
		s[i] = strings.ToLower(w.String()[:3])
	}
	return []byte(strings.Join(s, ", ")), nil
}

// has says whether the hours apply on w.
func (d Days) has(w time.Weekday) bool { return len(d) == 0 || slices.Contains(d, w) }

// List is comma separated; Words space separated.
type (
	List  []string
	Words []string
)

func (l *List) UnmarshalText(b []byte) error {
	*l = nil
	for _, p := range strings.Split(string(b), ",") {
		if p = strings.TrimSpace(p); p != "" {
			*l = append(*l, p)
		}
	}
	return nil
}

func (l List) MarshalText() ([]byte, error)   { return []byte(strings.Join(l, ", ")), nil }
func (w *Words) UnmarshalText(b []byte) error { *w = strings.Fields(string(b)); return nil }
func (w Words) MarshalText() ([]byte, error)  { return []byte(strings.Join(w, " ")), nil }
func (z Zone) MarshalText() ([]byte, error)   { return []byte(z.String()), nil }

// Labels are the team's own labels: "name color [emoji] description" entries separated by ";",
// the color as six hex digits, e.g. "priority:high B60205 Fix first; rota:needs-review 1D76DB 👀
// Waits for the human's review".
type Labels []Label

var labelColor = regexp.MustCompile(`^[0-9A-Fa-f]{6}$`)

func (l *Labels) UnmarshalText(b []byte) error {
	var out Labels
	for _, part := range strings.Split(string(b), ";") {
		f := strings.Fields(part)
		switch {
		case len(f) == 0:
			continue
		case len(f) < 2 || !labelColor.MatchString(f[1]):
			return fmt.Errorf("%q: use name color [emoji] description, the color as six hex digits, e.g. priority:high B60205 Fix first", strings.TrimSpace(part))
		case slices.ContainsFunc(out, func(x Label) bool { return strings.EqualFold(x.Name, f[0]) }):
			return fmt.Errorf("%s is there twice", f[0])
		}
		x := Label{Name: f[0], Color: f[1]}
		if len(f) > 2 && isEmoji(f[2]) {
			x.Emoji, f = f[2], f[1:]
		}
		x.Description = strings.Join(f[2:], " ")
		if len(x.Description) > 100 {
			return fmt.Errorf("%s: GitHub takes a description of at most 100 characters", x.Name)
		}
		out = append(out, x)
	}
	*l = out
	return nil
}

func (l Labels) MarshalText() ([]byte, error) {
	s := make([]string, len(l))
	for i, x := range l {
		s[i] = strings.Join(slices.DeleteFunc([]string{x.Name, x.Color, x.Emoji, x.Description}, func(f string) bool { return f == "" }), " ")
	}
	return []byte(strings.Join(s, "; ")), nil
}

// isEmoji says whether a word is an emoji: symbols, with the joiners, variation selectors and
// skin tones that combine them.
func isEmoji(s string) bool {
	symbol := false
	for _, r := range s {
		switch {
		case unicode.Is(unicode.So, r):
			symbol = true
		case r == 0x200d, r == 0xfe0f, r >= 0x1f3fb && r <= 0x1f3ff:
		default:
			return false
		}
	}
	return symbol
}

// Zone is a time zone: Local or an IANA name.
type Zone struct{ *time.Location }

func (z *Zone) UnmarshalText(b []byte) (err error) {
	v := strings.TrimSpace(string(b))
	if v == "" || strings.EqualFold(v, "local") {
		z.Location = time.Local
		return nil
	}
	z.Location, err = time.LoadLocation(v)
	return err
}

// Week is when a budget week starts: Claude's rolling window, or a weekday.
type Week struct {
	Claude bool
	Day    time.Weekday
}

func (w *Week) UnmarshalText(b []byte) error {
	v := strings.TrimSpace(string(b))
	if *w = (Week{}); strings.EqualFold(v, "claude") {
		w.Claude = true
		return nil
	}
	for d := time.Sunday; d <= time.Saturday; d++ {
		if strings.EqualFold(d.String(), v) {
			w.Day = d
			return nil
		}
	}
	return errors.New("claude, or a weekday")
}

func (w Week) MarshalText() ([]byte, error) {
	if w.Claude {
		return []byte("claude"), nil
	}
	return []byte(strings.ToLower(w.Day.String())), nil
}

// ---- small file helpers ----

// splitFront separates a "---" front matter block from the body.
func splitFront(s string) (head, body string, err error) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return "", "", errors.New("the file must start with a --- front matter block")
	}
	rest := s[4:]
	if i := strings.Index(rest, "\n---\n"); i >= 0 {
		return rest[:i], rest[i+5:], nil
	}
	if strings.HasSuffix(rest, "\n---") {
		return strings.TrimSuffix(rest, "\n---"), "", nil
	}
	return "", "", errors.New("the front matter is not closed with ---")
}

// parseKV reads "key: value" lines; blank lines and # comments are skipped.
func parseKV(s string) (map[string]string, error) {
	kv := map[string]string{}
	for n, line := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, rest, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("line %d: expected key: value", n+1)
		}
		v, _ := cutComment(rest)
		kv[strings.ToLower(strings.TrimSpace(k))] = v
	}
	return kv, nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

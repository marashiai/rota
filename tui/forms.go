package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"rota/core"
)

// The Agents and Settings tabs are the same form: every declared setting by section, the
// file's own values set apart from the defaults, and each changed with the input its kind
// calls for: a switch toggles, a choice opens a picker, a number or text a line editor.

// row is one line of a form: a setting, or the role, or the console's theme.
type row struct {
	section, key, label, value, help string
	set                              bool // the file sets it; else the default applies
	field                            *core.Field
	raw                              string // the value in force, as the file writes it
}

type form struct{ sel, top int }

// emptyText says what an empty setting means.
var emptyText = map[string]string{"model": "Claude Code's default", "effort": "Claude Code's default", "host": "this machine", "hours": "any time"}

func fieldRow(f *core.Field, v any, set map[string]string, label string) row {
	raw := f.Get(v)
	show := raw
	switch {
	case f.Kind == "check" && raw == "true":
		show = "● on"
	case f.Kind == "check":
		show = "○ off"
	case raw == "":
		show = or(emptyText[strings.TrimPrefix(f.Key, "default_")], "none")
	}
	_, isSet := set[f.Key]
	return row{section: f.Section, key: f.Key, label: or(label, f.Key), value: show, set: isSet, help: f.Help, field: f, raw: raw}
}

// drawForm draws the head lines, the rows by section and the tail lines; the selected row
// has the band (with the keys) and its help under it.
func (c *console) drawForm(f *frame, r rect, fm *form, head []string, rows []row, tail []string, focused bool, write func(row, string)) {
	p := c.pal
	labelW := 0
	for _, rw := range rows {
		labelW = max(labelW, width(rw.label))
	}
	labelW += 4
	type line struct {
		text string
		row  int
	}
	var lines []line
	for _, h := range head {
		lines = append(lines, line{truncate(h, r.w-3), -1})
	}
	fm.sel = max(0, min(fm.sel, len(rows)-1))
	selLine, end := 0, 0
	for i, rw := range rows {
		if i == 0 || rw.section != rows[i-1].section {
			lines = append(lines, line{"", -1}, line{p.section(rw.section, "", r.w-3), -1})
		}
		val := rw.value
		if !rw.set {
			val = sty(p.Muted(), rw.value+"  · default")
		} else if rw.field != nil && rw.field.Kind == "check" && rw.raw == "true" {
			val = sty(p.OK(), rw.value)
		}
		if rw.field != nil && rw.field.Kind == "choice" {
			val += sty(p.Muted(), "  ▾")
		}
		if i == fm.sel {
			selLine = len(lines)
		}
		lines = append(lines, line{truncate(sty(p.Muted(), fit("  "+rw.label, labelW))+val, r.w-3), i})
		if i == fm.sel && focused && rw.help != "" {
			for _, l := range wrap([]span{{rw.help, p.Muted()}}, r.w-3, strings.Repeat(" ", labelW), strings.Repeat(" ", labelW)) {
				lines = append(lines, line{l, -1})
			}
		}
		if i == fm.sel {
			end = len(lines)
		}
	}
	for _, l := range tail {
		lines = append(lines, line{l, -1})
	}
	if focused {
		if selLine-3 < fm.top {
			fm.top = selLine - 3
		}
		if end > fm.top+r.h {
			fm.top = end - r.h
		}
	} else {
		fm.top = 0
	}
	fm.top = max(0, min(fm.top, len(lines)-r.h))
	for i := 0; i < r.h && fm.top+i < len(lines); i++ {
		l := lines[fm.top+i]
		f.put(r.x+1, r.y+i, l.text, r.w-3)
		if focused && fm.top+i == selLine {
			f.band(p, r.x, r.y+i, r.w-1, true)
		}
		if l.row >= 0 {
			n := l.row
			c.hits = append(c.hits, hit{r: rect{r.x, r.y + i, r.w, 1}, click: func(int, int) {
				if focused && fm.sel == n { // a second click changes it
					c.change(rows[n], write)
				}
				fm.sel = n
			}})
		}
	}
	c.scrollbar(f, r.x+r.w-1, r.y, r.h, fm.top, len(lines))
	c.hits = append(c.hits, hit{r: r, wheel: func(d int) { fm.sel = max(0, min(len(rows)-1, fm.sel+d/3)) }})
}

// formKey moves in a form and changes its rows: write sets a value, "" meaning the default.
func (c *console) formKey(fm *form, rows []row, k key, write func(row, string)) bool {
	n := len(rows)
	switch {
	case k.name == "up" || k.r == 'k':
		fm.sel = max(0, fm.sel-1)
	case k.name == "down" || k.r == 'j':
		fm.sel = min(n-1, fm.sel+1)
	case k.name == "pgup" || k.r == 'b':
		fm.sel = max(0, fm.sel-8)
	case k.name == "pgdn":
		fm.sel = min(n-1, fm.sel+8)
	case k.name == "home" || k.r == 'g':
		fm.sel = 0
	case k.name == "end" || k.r == 'G':
		fm.sel = n - 1
	case (k.name == "enter" || k.r == ' ' || k.r == 'l') && fm.sel < n:
		c.change(rows[fm.sel], write)
	case (k.r == 'd' || k.name == "delete" || k.name == "backspace") && fm.sel < n && rows[fm.sel].field != nil:
		write(rows[fm.sel], "")
	default:
		return false
	}
	return true
}

// change asks for a new value of a row in the way its kind calls for.
func (c *console) change(r row, write func(row, string)) {
	f := r.field
	if f == nil {
		write(r, "")
		return
	}
	cur := ""
	if r.set {
		cur = r.raw
	}
	switch f.Kind {
	case "check":
		write(r, strconv.FormatBool(r.raw != "true"))
	case "choice":
		values := []string{""}
		labels := []string{"default (" + r.value + ")"}
		if r.set {
			labels[0] = "default"
		}
		for _, o := range c.t.Options(*f) {
			if o != "" && !contains(values, o) {
				values, labels = append(values, o), append(labels, o)
			}
		}
		if len(f.Suggest) > 0 {
			values, labels = append(values, "\x00"), append(labels, "other…")
		}
		c.pick(r.label, values, labels, cur, func(v string) {
			if v == "\x00" {
				c.prompt(r.label+": ", cur, func(s string) { write(r, s) })
				return
			}
			write(r, v)
		})
	default:
		init := cur
		if !r.set {
			init = r.raw
		}
		c.prompt(r.label+" (empty: the default): ", init, func(s string) {
			if r.set || strings.TrimSpace(s) != init {
				write(r, s)
			}
		})
	}
}

// ---- agents ----

type agentsPage struct {
	sel   int
	top   int  // the first agent in view
	focus bool // the form has the keys
	form  form
}

func (ap *agentsPage) agent(c *console) *core.Agent {
	if len(c.t.Agents) == 0 {
		return nil
	}
	ap.sel = max(0, min(ap.sel, len(c.t.Agents)-1))
	return c.t.Agents[ap.sel]
}

func (ap *agentsPage) rows(c *console, a *core.Agent) []row {
	set := c.t.AgentSettings(a.Name)
	var rows []row
	for i := range core.AgentFields {
		f := &core.AgentFields[i]
		rows = append(rows, fieldRow(f, a, set, f.Label))
	}
	return append(rows, row{section: "Role", key: "role", label: "Edit", value: "⏎ opens it in " + filepath.Base(editor()[0]), set: true})
}

func (ap *agentsPage) draw(c *console, f *frame, a rect) {
	p := c.pal
	ag := ap.agent(c)
	if ag == nil {
		f.put(margin, a.y+a.h/3, sty(p.Muted(), "No agents yet. Add one as agents/<name>.md in the team folder."), a.w)
		return
	}
	nameW, modelW := 0, 0
	for _, x := range c.t.Agents {
		nameW, modelW = max(nameW, width(x.Name)), max(modelW, width(x.Model))
	}
	lw := max(22, min(34, margin+nameW+modelW+6))
	gap := 2 - btoi(a.h < 16)
	f.put(margin, a.y, sty(map[bool]string{true: bold, false: p.Head()}[ap.focus], "Agents")+sty(p.Muted(), fmt.Sprintf("  %d", len(c.t.Agents))), lw-margin-1)
	ap.top = keepVisible(ap.top, ap.sel, a.h-gap, len(c.t.Agents))
	for i, x := range c.t.Agents[ap.top:] {
		y, i := a.y+gap+i, ap.top+i
		if y >= a.y+a.h {
			break
		}
		label := sty(p.OK(), "●") + " " + fit(x.Name, nameW)
		if !x.Enabled {
			label = sty(p.Muted(), "○ "+fit(x.Name, nameW))
		}
		f.put(margin, y, label+"  "+sty(p.Muted(), x.Model), lw-margin-1)
		if i == ap.sel {
			f.band(p, 0, y, lw, !ap.focus)
		}
		n := i
		c.hits = append(c.hits, hit{r: rect{0, y, lw, 1}, click: func(int, int) { ap.focus, ap.sel = n == ap.sel, n }})
	}
	for y := a.y; y < a.y+a.h; y++ {
		f.put(lw, y, sty(p.Rule(), "│"), 1)
	}
	head := []string{sty(p.Head(), ag.Name)}
	for _, as := range c.st.Agents {
		if as.Name == ag.Name {
			if r := c.st.Running; r != nil && r.Agent == ag.Name {
				head[0] += "   " + sty(p.Head(), "▸ running now")
			} else {
				head[0] += "   " + p.state(as.State)
			}
			last := "never"
			if !as.LastRun.IsZero() {
				last = as.LastRun.Local().Format("Mon 15:04") + " " + as.LastStatus
			}
			info := fmt.Sprintf("next %s · last %s · %d runs this week · disk %s", as.NextText(c.st.NextTick), last, as.Runs, as.DiskText())
			if as.OverCap() {
				info += " over its cap"
			}
			if as.QuotaPct > 0 {
				info += fmt.Sprintf(" · share %.1f of %.1f%% of the pool %s", c.st.Pool.Of(as.PeriodPct), c.st.Pool.Of(as.QuotaPct), c.st.Pool.Period)
				if f := as.Frees(); f >= 0.005 {
					info += fmt.Sprintf(" · frees %.1f%%: %s", c.st.Pool.Of(f), as.CanUseWhy)
				}
			}
			head = append(head, sty(p.Muted(), info))
		}
	}
	head = append(head, sty(p.Muted(), c.t.Rel(c.t.AgentFile(ag.Name))+" · an empty field takes the team default from "+c.t.File))
	for _, w := range ag.Warnings {
		head = append(head, sty(p.Warn(), "▲ "+w))
	}
	r := rect{lw + 2, a.y, a.w - lw - 3, a.h}
	role, _ := p.markdown(ag.Role, r.w-6)
	for i := range role {
		role[i] = "  " + role[i]
	}
	c.drawForm(f, r, &ap.form, head, ap.rows(c, ag), append([]string{""}, role...), ap.focus, ap.write(c, ag.Name))
	if !ap.focus {
		c.hits = append(c.hits, hit{r: r, click: func(int, int) { ap.focus = true }})
	}
}

func (ap *agentsPage) key(c *console, k key) bool {
	ag := ap.agent(c)
	if ag == nil {
		return false
	}
	if !ap.focus {
		switch {
		case k.name == "up" || k.r == 'k':
			ap.sel = max(0, ap.sel-1)
		case k.name == "down" || k.r == 'j':
			ap.sel = min(len(c.t.Agents)-1, ap.sel+1)
		case k.name == "pgup":
			ap.sel = max(0, ap.sel-10)
		case k.name == "pgdn":
			ap.sel = min(len(c.t.Agents)-1, ap.sel+10)
		case k.name == "home" || k.r == 'g':
			ap.sel = 0
		case k.name == "end" || k.r == 'G':
			ap.sel = len(c.t.Agents) - 1
		case k.name == "enter" || k.name == "right" || k.r == 'l':
			ap.focus = true
		case k.r == 'B':
			c.rebalance()
		default:
			return c.agentKey(ag.Name, k)
		}
		return true
	}
	if k.name == "left" || k.r == 'h' || k.name == "esc" {
		ap.focus = false
		return true
	}
	if k.r == 'B' {
		c.rebalance()
		return true
	}
	return c.formKey(&ap.form, ap.rows(c, ag), k, ap.write(c, ag.Name)) || c.agentKey(ag.Name, k)
}

// rebalance opens the proposal of new shares, which lives on the Spend tab.
func (c *console) rebalance() {
	c.tab = tabSpend
	c.pages[tabSpend].(*spendPage).openBalance(c)
}

// write saves one row of the agent's form: a setting, or the role through the editor.
func (ap *agentsPage) write(c *console, name string) func(row, string) {
	return func(r row, v string) {
		if r.key == "role" {
			c.editRole(name)
			return
		}
		c.setAgent(name, r.key, v)
	}
}

func (ap *agentsPage) hints(c *console) []hint {
	if ap.focus {
		return []hint{{"↑↓", "field"}, {"⏎", "change"}, {"d", "team default"}, {"E", "file"}, {"←", "agents"}, {"B", "rebalance shares"}}
	}
	return []hint{{"↑↓", "agent"}, {"⏎", "edit"}, {"e", "enable"}, {"P", "pause"}, {"r", "run"}, {"i", "note"}, {"o", "transcript"}, {"B", "rebalance shares"}}
}

// editRole opens an agent's role, its system prompt, in the editor, and saves it back.
func (c *console) editRole(name string) {
	tmp, err := os.CreateTemp("", "rota-role-"+name+"-*.md")
	if c.sayErr(err) {
		return
	}
	defer os.Remove(tmp.Name())
	before := c.t.Agent(name).Role
	tmp.WriteString(before + "\n")
	tmp.Close()
	if c.edit(tmp.Name()) != nil {
		return
	}
	after := strings.TrimSpace(readFile(tmp.Name()))
	switch {
	case after == strings.TrimSpace(before):
		c.say("role unchanged", false)
	case !c.sayErr(c.t.SetRole(name, after)):
		c.reread()
		c.say(name+": role saved", false)
	}
}

// ---- settings ----

type settingsPage struct {
	form   form
	filter string
}

// Version is this build, shown on the Settings tab.
var Version = "dev"

func (sp *settingsPage) rows(c *console) []row {
	name, source := themeSetting(c.root)
	val := name + " · " + source
	if name == "auto" {
		val = "auto · " + c.pal.name + ", from " + c.themeFrom
	}
	rows := []row{{section: "Console", key: "theme", label: "theme", value: val, set: true,
		help: "auto follows the terminal's background, light or dark; mono uses no color. Kept in this team folder (.theme); ROTA_THEME overrides it, and NO_COLOR asks for mono."}}
	set := c.t.Settings()
	for i := range core.ConfigFields {
		f := &core.ConfigFields[i]
		if f.Key == "voice" && strings.Contains(voiceKeyRow, strings.ToLower(sp.filter)) { // the key, with the voice it reads in
			key := "none: the pages read aloud and take dictation with the browser's own"
			if k := c.t.VoiceKey(); k != "" {
				key = "set, in secrets/elevenlabs.key"
				if end := core.KeyEnd(k); end != "" {
					key = "••••" + end + ", in secrets/elevenlabs.key"
				}
			}
			rows = append(rows, row{section: f.Section, key: voiceKeyRow, label: voiceKeyRow, value: key, set: c.t.VoiceKey() != "",
				help: "With an ElevenLabs key, the pages read aloud and take dictation with ElevenLabs, for what the key may do; else with the browser's own. ⏎ pastes a key; an empty one removes it."})
		}
		if strings.Contains(f.Key, strings.ToLower(sp.filter)) {
			rows = append(rows, fieldRow(f, c.t.Config, set, ""))
		}
	}
	if sp.filter != "" {
		rows = rows[1:]
	}
	return rows
}

func (sp *settingsPage) draw(c *console, f *frame, a rect) {
	p := c.pal
	head := []string{sty(p.Head(), c.t.File) + sty(p.Muted(), "  "+c.t.Path(c.t.File)+" · this rota "+Version)}
	for _, w := range c.t.Warnings {
		head = append(head, sty(p.Warn(), "▲ "+w))
	}
	if sp.filter != "" {
		head = append(head, sty(p.Muted(), "showing settings with ")+sty(p.Head(), sp.filter)+sty(p.Muted(), " · Esc shows all"))
	}
	rows := sp.rows(c)
	if len(rows) == 0 {
		head = append(head, "", sty(p.Muted(), "no setting matches"))
	}
	c.drawForm(f, rect{margin - 1, a.y, a.w - margin, a.h}, &sp.form, head, rows, nil, true, c.setSetting)
}

func (sp *settingsPage) key(c *console, k key) bool {
	switch {
	case k.r == '/':
		c.prompt("show settings with: ", sp.filter, func(s string) { sp.filter, sp.form = strings.TrimSpace(s), form{} })
	case k.name == "esc":
		sp.filter = ""
	case k.r == 'E':
		c.edit(c.t.Path(c.t.File))
	default:
		return c.formKey(&sp.form, sp.rows(c), k, c.setSetting)
	}
	return true
}

func (sp *settingsPage) hints(c *console) []hint {
	return []hint{{"↑↓", "setting"}, {"⏎", "change"}, {"d", "default"}, {"/", "filter"}, {"E", "edit file"}}
}

func (c *console) setSetting(r row, v string) {
	if r.key == "theme" {
		labels := []string{"auto · follow the terminal (now " + c.pal.name + ")", "dark", "light", "mono · no color"}
		name, _ := themeSetting(c.root)
		c.pick("Theme of this console", themeNames, labels, name, func(v string) {
			if c.sayErr(saveTheme(c.root, v)) {
				return
			}
			if v == "auto" && c.ask11 != nil {
				c.ask11() // and follow the answer
			}
			c.applyTheme()
			if name, src := themeSetting(c.root); src == "ROTA_THEME" || src == "NO_COLOR" {
				c.say("saved; "+src+" still decides here ("+name+")", true)
				return
			}
			c.say("theme: "+c.pal.name, false)
		})
		return
	}
	if r.key == voiceKeyRow {
		c.input = &lineInput{prompt: "ElevenLabs key (empty removes it): ", secret: true, done: func(key string) {
			save := func() {
				if c.sayErr(c.t.SetVoiceKey(key)) {
					return
				}
				c.reread()
				if strings.TrimSpace(key) == "" {
					c.say("ElevenLabs key removed: the pages use the browser's own voice and dictation", false)
				} else {
					c.say("ElevenLabs key saved: the pages use it for what it may do", false)
				}
			}
			if strings.TrimSpace(key) == "" {
				if c.t.VoiceKey() != "" {
					c.ask("Remove the ElevenLabs key?", save)
				}
				return
			}
			save()
		}}
		return
	}
	if c.sayErr(c.t.SetSetting(r.key, v)) {
		return
	}
	c.reread()
	c.say(r.key+" = "+or(v, "the default")+" · it applies within seconds", false)
}

// voiceKeyRow is the ElevenLabs key on the Settings tab: kept in the team's secrets, not in the
// config file.
const voiceKeyRow = "elevenlabs_key"

// ---- the theme ----
//
// Which palette: what was asked for (ROTA_THEME, then the choice saved from Settings, then
// NO_COLOR), else what the terminal says about its background, then COLORFGBG, then dark.

var themeNames = []string{"auto", "dark", "light", "mono"}

func themeFile(root string) string { return filepath.Join(root, ".theme") }

func themeSetting(root string) (name, source string) {
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("ROTA_THEME"))); v != "auto" && contains(themeNames, v) {
		return v, "ROTA_THEME"
	}
	if v := strings.TrimSpace(readFile(themeFile(root))); v != "auto" && contains(themeNames, v) {
		return v, "your choice"
	}
	if os.Getenv("NO_COLOR") != "" {
		return "mono", "NO_COLOR"
	}
	return "auto", ""
}

func saveTheme(root, name string) error {
	if name != "auto" {
		return os.WriteFile(themeFile(root), []byte(name+"\n"), 0o644)
	}
	if err := os.Remove(themeFile(root)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// resolveTheme picks the palette; bg is the terminal's background luminance, when it said.
func resolveTheme(root string, bg float64, haveBg bool) (palette, string) {
	name, source := themeSetting(root)
	switch {
	case name != "auto":
		return paletteNamed(name), source
	case haveBg && bg > 0.3:
		return paletteNamed("light"), "the terminal's light background"
	case haveBg:
		return paletteNamed("dark"), "the terminal's dark background"
	}
	if v := os.Getenv("COLORFGBG"); v != "" { // "15;0": light text on black
		parts := strings.Split(v, ";")
		if n, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			if n == 7 || n == 15 || n >= 250 {
				return paletteNamed("light"), "COLORFGBG"
			}
			return paletteNamed("dark"), "COLORFGBG"
		}
	}
	return paletteNamed("dark"), "the default (the terminal did not say)"
}

// statusPalette is the palette for rota status on a terminal, asking the terminal for its
// background the way the console does.
func statusPalette(root string) palette {
	var bg float64
	var ok bool
	if name, _ := themeSetting(root); name == "auto" && IsTerminal(os.Stdin) {
		if t, err := newTerminal(); err == nil && t.raw() == nil {
			bg, ok = t.background(250 * time.Millisecond)
			t.restore()
		}
	}
	p, _ := resolveTheme(root, bg, ok)
	return p
}

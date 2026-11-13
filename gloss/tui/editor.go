//go:build darwin || freebsd || netbsd || openbsd || dragonfly || linux

package tui

import (
	"strings"
	"time"
	"unicode"

	"rota/gloss/review"
)

// editor is the note being written, at the bottom of the screen. It is saved as a draft a moment
// after each change, one save at a time, and saved once more when it closes.
type editor struct {
	mode  string // new, reply, edit
	at    review.NoteInput
	note  string // the note a reply answers, or the draft edited
	id    string // the draft, once saved
	label string
	text  []rune
	pos   int
	top   int // the first line shown
	saved string
	state string

	saving  int  // saves on their way
	failed  bool // the last save failed
	warned  bool // a close found unsaved text: the next one discards it
	closing bool // closes once saved
	timer   *time.Timer
}

func (m *model) open(ed *editor) {
	if m.ed != nil {
		return
	}
	if ed.state == "" {
		ed.state = "a draft, saved as you type: only you see it until you send"
	}
	m.ed = ed
}

func (m *model) edit(k key) {
	ed := m.ed
	if ed.closing {
		return // the last save is on its way
	}
	before := string(ed.text)
	switch {
	case k.Name == "esc" || k.Name == "ctrl+s" || k.Name == "ctrl+c":
		m.closeEditor()
		return
	case k.Name == "enter":
		ed.insert("\n")
	case k.Name == "tab":
		ed.insert("\t")
	case k.Name == "paste":
		ed.insert(strings.ReplaceAll(strings.ReplaceAll(k.Text, "\r\n", "\n"), "\r", "\n"))
	case k.Rune != 0:
		ed.insert(string(k.Rune))
	case k.Name == "backspace" || k.Name == "ctrl+h":
		if ed.pos > 0 {
			ed.text = append(ed.text[:ed.pos-1], ed.text[ed.pos:]...)
			ed.pos--
		}
	case k.Name == "del" || k.Name == "ctrl+d":
		if ed.pos < len(ed.text) {
			ed.text = append(ed.text[:ed.pos], ed.text[ed.pos+1:]...)
		}
	case k.Name == "left" || k.Name == "ctrl+b":
		ed.pos = max(0, ed.pos-1)
	case k.Name == "right" || k.Name == "ctrl+f":
		ed.pos = min(len(ed.text), ed.pos+1)
	case k.Name == "up" || k.Name == "ctrl+p":
		ed.vertical(-1, m.editWidth())
	case k.Name == "down" || k.Name == "ctrl+n":
		ed.vertical(1, m.editWidth())
	case k.Name == "home" || k.Name == "ctrl+a":
		ed.pos = ed.lineStart()
	case k.Name == "end" || k.Name == "ctrl+e":
		ed.pos = ed.lineEnd()
	case k.Name == "ctrl+u":
		s := ed.lineStart()
		ed.text = append(ed.text[:s], ed.text[ed.pos:]...)
		ed.pos = s
	case k.Name == "ctrl+k":
		e := ed.lineEnd()
		ed.text = append(ed.text[:ed.pos], ed.text[e:]...)
	case k.Name == "ctrl+w":
		s := ed.pos
		for s > 0 && unicode.IsSpace(ed.text[s-1]) {
			s--
		}
		for s > 0 && !unicode.IsSpace(ed.text[s-1]) {
			s--
		}
		ed.text = append(ed.text[:s], ed.text[ed.pos:]...)
		ed.pos = s
	}
	if string(ed.text) != before {
		ed.warned = false
		ed.state = "typing…"
		if ed.timer != nil {
			ed.timer.Stop()
		}
		ed.timer = time.AfterFunc(800*time.Millisecond, func() {
			select {
			case m.events <- saveDue{ed}:
			default:
			}
		})
	}
}

func (ed *editor) insert(s string) {
	r := []rune(s)
	ed.text = append(ed.text[:ed.pos], append(r, ed.text[ed.pos:]...)...)
	ed.pos += len(r)
}

func (ed *editor) lineStart() int {
	i := ed.pos
	for i > 0 && ed.text[i-1] != '\n' {
		i--
	}
	return i
}

func (ed *editor) lineEnd() int {
	i := ed.pos
	for i < len(ed.text) && ed.text[i] != '\n' {
		i++
	}
	return i
}

// lines are where each line on screen starts, the text wrapped at width.
func (ed *editor) lines(width int) []int {
	starts := []int{0}
	col := 0
	for i, r := range ed.text {
		if r == '\n' {
			starts = append(starts, i+1)
			col = 0
			continue
		}
		if col == width {
			starts = append(starts, i)
			col = 0
		}
		col++
	}
	return starts
}

// lineText is line i of starts, without its newline.
func (ed *editor) lineText(starts []int, i int) string {
	end := len(ed.text)
	if i+1 < len(starts) {
		end = starts[i+1]
		if end > 0 && ed.text[end-1] == '\n' {
			end--
		}
	}
	return strings.ReplaceAll(string(ed.text[starts[i]:end]), "\t", " ")
}

// cursor is the line and column of pos.
func (ed *editor) cursor(starts []int) (int, int) {
	row := 0
	for i, s := range starts {
		if s <= ed.pos {
			row = i
		}
	}
	return row, ed.pos - starts[row]
}

func (ed *editor) vertical(d, width int) {
	starts := ed.lines(width)
	row, col := ed.cursor(starts)
	nr := row + d
	if nr < 0 || nr >= len(starts) {
		return
	}
	end := len(ed.text)
	if nr+1 < len(starts) {
		end = starts[nr+1]
		if ed.text[end-1] == '\n' {
			end--
		}
	}
	ed.pos = min(starts[nr]+col, end)
}

// save sends the editor's text as a draft, unless it is saved already, empty, or a save is on its
// way (settle saves again when that one lands).
func (m *model) save(_ bool) {
	ed := m.ed
	if ed == nil || ed.saving > 0 {
		return
	}
	if ed.timer != nil {
		ed.timer.Stop()
	}
	text := string(ed.text)
	if text == ed.saved || strings.TrimSpace(text) == "" {
		return
	}
	j := job{text: text, ed: ed}
	switch {
	case ed.id != "":
		j.kind, j.note = "edit", ed.id
	case ed.mode == "reply":
		j.kind, j.note = "reply", ed.note
	default:
		j.kind, j.at = "draft", ed.at
	}
	ed.saving++
	ed.state = "saving…"
	m.enqueue(j)
}

// settle runs when a save lands: it saves what was typed since, or closes the editor if asked.
func (m *model) settle(ed *editor) {
	if m.ed != ed || ed.saving > 0 {
		return
	}
	text := string(ed.text)
	if strings.TrimSpace(text) != "" && text != ed.saved {
		m.save(false)
		return
	}
	if !ed.closing {
		return
	}
	if strings.TrimSpace(text) == "" && ed.id != "" {
		m.enqueue(job{kind: "delete", note: ed.id, silent: true})
	}
	m.closed(ed)
}

// closeEditor saves and closes; a note that cannot be saved keeps the editor open once.
func (m *model) closeEditor() {
	ed := m.ed
	if ed.timer != nil {
		ed.timer.Stop()
	}
	text := string(ed.text)
	if ed.warned && strings.TrimSpace(text) != "" && text != ed.saved {
		m.closed(ed)
		m.say("the unsaved note was discarded", true)
		return
	}
	ed.closing = true
	m.settle(ed)
}

func (m *model) closed(ed *editor) {
	if ed.timer != nil {
		ed.timer.Stop()
	}
	m.ed = nil
	if ed.id != "" && strings.TrimSpace(string(ed.text)) != "" {
		m.focus = ed.id
	}
	m.layout(true)
}

func (m *model) editWidth() int { return max(10, m.w-2) }

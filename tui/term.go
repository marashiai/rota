package tui

import (
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

// The terminal UI talks to the terminal directly: raw mode through termios, the alternate
// screen, SGR mouse reports and bracketed paste. No library, like the rest of rota.

// IsTerminal says whether f is a terminal.
func IsTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), ioctlGetTermios)
	return err == nil
}

type terminal struct {
	in, out int
	saved   *unix.Termios
	pending []byte // typed while the background was being asked for
}

func newTerminal() (*terminal, error) {
	t := &terminal{in: int(os.Stdin.Fd()), out: int(os.Stdout.Fd())}
	st, err := unix.IoctlGetTermios(t.in, ioctlGetTermios)
	if err != nil {
		return nil, err
	}
	saved := *st
	t.saved = &saved
	return t, nil
}

func openTerminal() (*terminal, error) {
	t, err := newTerminal()
	if err != nil {
		return nil, err
	}
	return t, t.enter()
}

// raw puts the terminal in raw mode: every key as it is pressed, nothing echoed.
func (t *terminal) raw() error {
	raw := *t.saved
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN], raw.Cc[unix.VTIME] = 1, 0
	return unix.IoctlSetTermios(t.in, ioctlSetTermios, &raw)
}

func (t *terminal) restore() { unix.IoctlSetTermios(t.in, ioctlSetTermios, t.saved) }

// enter switches to raw mode and the alternate screen, with the mouse and paste reported
// and the window titled rota (the previous title is kept and given back by leave).
func (t *terminal) enter() error {
	if err := t.raw(); err != nil {
		return err
	}
	t.write("\x1b[22;0t\x1b]2;rota\x07\x1b[?1049h\x1b[?25l\x1b[?7l\x1b[?1000h\x1b[?1006h\x1b[?2004h\x1b[2J")
	return nil
}

// leave restores the screen, the cursor, the title and the mode the terminal had before.
func (t *terminal) leave() {
	t.write("\x1b[?2004l\x1b[?1006l\x1b[?1000l\x1b[?7h\x1b[0m\x1b[0 q\x1b[?25h\x1b[?1049l\x1b[23;0t")
	t.restore()
}

var (
	bgReply = regexp.MustCompile(`\x1b\]11;rgba?:([0-9a-fA-F]{1,4})/([0-9a-fA-F]{1,4})/([0-9a-fA-F]{1,4})`)
	daReply = regexp.MustCompile(`\x1b\[\?[0-9;]*c`)
	replies = regexp.MustCompile(`\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b\[\?[0-9;]*c`)
)

// background asks the terminal for its background color (OSC 11) and then for its device
// attributes, which every terminal answers, so one that does not know OSC 11 takes no
// wait. It returns the color's relative luminance; both answers are consumed. Call it in
// raw mode.
func (t *terminal) background(timeout time.Duration) (float64, bool) {
	t.write("\x1b]11;?\x1b\\\x1b[c")
	deadline := time.Now().Add(timeout)
	var buf []byte
	tmp := make([]byte, 512)
	for !daReply.Match(buf) {
		left := time.Until(deadline)
		if left <= 0 {
			break
		}
		fds := []unix.PollFd{{Fd: int32(t.in), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, int(left/time.Millisecond)+1)
		if err == unix.EINTR {
			continue
		}
		if err != nil || n == 0 {
			break
		}
		m, err := unix.Read(t.in, tmp)
		if m <= 0 && err != unix.EINTR {
			break
		}
		if m > 0 {
			buf = append(buf, tmp[:m]...)
		}
	}
	t.pending = replies.ReplaceAll(buf, nil)
	return parseBackground(buf)
}

// parseBackground reads an OSC 11 answer, "rgb:RRRR/GGGG/BBBB", as relative luminance.
func parseBackground(b []byte) (float64, bool) {
	m := bgReply.FindSubmatch(b)
	if m == nil {
		return 0, false
	}
	var ch [3]float64
	for i := range ch {
		v, _ := strconv.ParseUint(string(m[i+1]), 16, 32)
		ch[i] = float64(v) / float64(uint64(1)<<(4*len(m[i+1]))-1)
	}
	return luminance(ch[0], ch[1], ch[2]), true
}

// luminance is WCAG relative luminance of an sRGB color with channels in 0..1.
func luminance(r, g, b float64) float64 {
	lin := func(c float64) float64 {
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

func (t *terminal) write(s string) {
	for b := []byte(s); len(b) > 0; {
		n, err := unix.Write(t.out, b)
		if err != nil && err != unix.EINTR && err != unix.EAGAIN {
			return
		}
		if n > 0 {
			b = b[n:]
		}
	}
}

// termWidth is the width of the terminal f writes to, or 100.
func termWidth(f *os.File) int {
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 {
		return 100
	}
	return int(ws.Col)
}

func (t *terminal) size() (int, int) {
	ws, err := unix.IoctlGetWinsize(t.out, unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 || ws.Row == 0 {
		return 80, 24
	}
	return int(ws.Col), int(ws.Row)
}

// readKeys delivers key presses until stop is closed. It polls, so it lets go of the
// terminal within a tenth of a second when the UI hands it to an editor or to claude.
func (t *terminal) readKeys(stop <-chan struct{}, out chan<- key) {
	buf := make([]byte, 4096)
	pending := t.pending
	t.pending = nil
	for {
		select {
		case <-stop:
			return
		default:
		}
		wait := 100
		if len(pending) > 0 {
			wait = 30 // an escape on its own is the Esc key unless more follows at once
		}
		fds := []unix.PollFd{{Fd: int32(t.in), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, wait)
		if err != nil && err != unix.EINTR {
			return
		}
		final := false
		if n > 0 {
			m, err := unix.Read(t.in, buf)
			if m <= 0 && err != unix.EINTR && err != unix.EAGAIN {
				return
			}
			if m > 0 {
				pending = append(pending, buf[:m]...)
			}
		} else if len(pending) > 0 {
			final = true
		}
		var keys []key
		keys, pending = parseKeys(pending, final)
		for _, k := range keys {
			select {
			case out <- k:
			case <-stop:
				return
			}
		}
	}
}

// key is one input event: a named key, a typed rune, a mouse report or a paste.
type key struct {
	name  string // up down left right home end pgup pgdn enter tab backtab esc backspace delete ctrl+<x> wheelup wheeldown click paste
	r     rune
	x, y  int    // mouse position, 0-based
	paste string // pasted text
}

// parseKeys decodes as many complete events as b holds and returns the incomplete rest.
// With final set, an incomplete escape sequence is taken as the Esc key.
func parseKeys(b []byte, final bool) ([]key, []byte) {
	var keys []key
	for len(b) > 0 {
		c := b[0]
		switch {
		case c == 0x1b:
			k, n, ok := parseEscape(b, final)
			if !ok {
				return keys, b
			}
			if k.name != "" || k.r != 0 {
				keys = append(keys, k)
			}
			b = b[n:]
			continue
		case c == '\r' || c == '\n':
			keys = append(keys, key{name: "enter"})
		case c == '\t':
			keys = append(keys, key{name: "tab"})
		case c == 0x7f || c == 0x08:
			keys = append(keys, key{name: "backspace"})
		case c < 0x20:
			keys = append(keys, key{name: "ctrl+" + string(rune('a'+c-1))})
		default:
			if !utf8.FullRune(b) && !final {
				return keys, b
			}
			r, n := utf8.DecodeRune(b)
			keys = append(keys, key{r: r})
			b = b[n:]
			continue
		}
		b = b[1:]
	}
	return keys, nil
}

func parseEscape(b []byte, final bool) (key, int, bool) {
	if len(b) == 1 {
		if final {
			return key{name: "esc"}, 1, true
		}
		return key{}, 0, false
	}
	switch b[1] {
	case ']': // an operating system command: the terminal's answer to a question
		end := len(b)
		for i := 2; i < len(b); i++ {
			if b[i] == 0x07 {
				end = i + 1
				break
			}
			if b[i] == 0x1b && i+1 < len(b) && b[i+1] == '\\' {
				end = i + 2
				break
			}
		}
		if end == len(b) && b[len(b)-1] != 0x07 && !strings.HasSuffix(string(b), "\x1b\\") && !final {
			return key{}, 0, false
		}
		if bgReply.Match(b[:end]) {
			return key{name: "background", paste: string(b[:end])}, end, true
		}
		return key{}, end, true
	case '[':
		if len(b) >= 6 && string(b[:6]) == "\x1b[200~" { // bracketed paste
			end := strings.Index(string(b[6:]), "\x1b[201~")
			if end < 0 {
				if final {
					return key{name: "paste", paste: string(b[6:])}, len(b), true
				}
				return key{}, 0, false
			}
			return key{name: "paste", paste: string(b[6 : 6+end])}, 6 + end + 6, true
		}
		j := 2
		for j < len(b) && (b[j] < 0x40 || b[j] > 0x7e) {
			j++
		}
		if j >= len(b) {
			if final {
				return key{name: "esc"}, 1, true
			}
			return key{}, 0, false
		}
		params, fin := string(b[2:j]), b[j]
		if strings.HasPrefix(params, "<") && (fin == 'M' || fin == 'm') { // SGR mouse
			f := strings.Split(params[1:], ";")
			if len(f) == 3 {
				btn, _ := strconv.Atoi(f[0])
				x, _ := strconv.Atoi(f[1])
				y, _ := strconv.Atoi(f[2])
				k := key{x: x - 1, y: y - 1}
				switch {
				case btn&64 != 0 && btn&1 == 0:
					k.name = "wheelup"
				case btn&64 != 0:
					k.name = "wheeldown"
				case btn&3 == 0 && btn&32 == 0 && fin == 'M':
					k.name = "click"
				}
				return k, j + 1, true
			}
			return key{}, j + 1, true
		}
		first, _, _ := strings.Cut(params, ";")
		names := map[byte]string{'A': "up", 'B': "down", 'C': "right", 'D': "left", 'H': "home", 'F': "end", 'Z': "backtab"}
		if fin == '~' {
			tilde := map[string]string{"1": "home", "7": "home", "4": "end", "8": "end", "3": "delete", "5": "pgup", "6": "pgdn", "15": "f5"}
			return key{name: tilde[first]}, j + 1, true
		}
		return key{name: names[fin]}, j + 1, true
	case 'O':
		if len(b) < 3 {
			if final {
				return key{name: "esc"}, 1, true
			}
			return key{}, 0, false
		}
		names := map[byte]string{'A': "up", 'B': "down", 'C': "right", 'D': "left", 'H': "home", 'F': "end"}
		return key{name: names[b[2]]}, 3, true
	case 0x1b:
		return key{name: "esc"}, 1, true
	}
	// Alt+key arrives as Esc and then the key: take it as the key itself
	n := firstKeyLen(b[1:])
	if ks, _ := parseKeys(b[1:1+n], true); len(ks) > 0 {
		return ks[0], 1 + n, true
	}
	return key{name: "esc"}, 1, true
}

func firstKeyLen(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	if b[0] == 0x1b {
		if _, n, ok := parseEscape(b, true); ok {
			return n
		}
		return 1
	}
	if b[0] < 0x80 {
		return 1
	}
	_, n := utf8.DecodeRune(b)
	return n
}

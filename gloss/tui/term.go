//go:build darwin || freebsd || netbsd || openbsd || dragonfly || linux

package tui

import (
	"os"
	"strings"
	"syscall"
	"unicode/utf8"
	"unsafe"
)

// The terminal is driven directly: raw mode through termios, the alternate screen, SGR mouse
// reports and bracketed paste. No library.

type terminal struct {
	in, out *os.File
	saved   syscall.Termios
}

func ioctl(fd uintptr, req uintptr, arg unsafe.Pointer) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg)); e != 0 {
		return e
	}
	return nil
}

// IsTerminal says whether f is a terminal.
func IsTerminal(f *os.File) bool {
	var t syscall.Termios
	return ioctl(f.Fd(), ioctlGet, unsafe.Pointer(&t)) == nil
}

func openTerminal(in, out *os.File) (*terminal, error) {
	t := &terminal{in: in, out: out}
	if err := ioctl(in.Fd(), ioctlGet, unsafe.Pointer(&t.saved)); err != nil {
		return nil, err
	}
	raw := t.saved
	raw.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	raw.Oflag &^= syscall.OPOST
	raw.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	raw.Cflag &^= syscall.CSIZE | syscall.PARENB
	raw.Cflag |= syscall.CS8
	// reads return after a tenth of a second without input, so the reader can stop
	raw.Cc[syscall.VMIN], raw.Cc[syscall.VTIME] = 0, 1
	if err := ioctl(in.Fd(), ioctlSet, unsafe.Pointer(&raw)); err != nil {
		return nil, err
	}
	// alternate screen, cursor hidden, no wrap, mouse (SGR) and bracketed paste on
	t.out.WriteString("\x1b[?1049h\x1b[?25l\x1b[?7l\x1b[?1000h\x1b[?1006h\x1b[?2004h\x1b[2J")
	return t, nil
}

func (t *terminal) close() {
	t.out.WriteString("\x1b[?2004l\x1b[?1006l\x1b[?1000l\x1b[?7h\x1b[0m\x1b[?25h\x1b[?1049l")
	ioctl(t.in.Fd(), ioctlSet, unsafe.Pointer(&t.saved))
}

func (t *terminal) size() (w, h int) {
	var ws struct{ Row, Col, X, Y uint16 }
	if ioctl(t.out.Fd(), syscall.TIOCGWINSZ, unsafe.Pointer(&ws)) != nil || ws.Col == 0 {
		return 80, 24
	}
	return int(ws.Col), int(ws.Row)
}

// key is one key press: a named key, or a rune; Text holds a paste.
type key struct {
	Name string // up, down, left, right, home, end, pgup, pgdn, del, esc, enter, tab, shift+tab, backspace, ctrl+<letter>, paste, wheelup, wheeldown
	Rune rune
	Text string
}

// parseKeys reads every key in one read from the terminal; rest is an incomplete tail.
func parseKeys(b []byte) (keys []key, rest []byte) {
	for len(b) > 0 {
		k, n := parseKey(b)
		if n == 0 {
			return keys, b
		}
		if k.Name != "" || k.Rune != 0 {
			keys = append(keys, k)
		}
		b = b[n:]
	}
	return keys, nil
}

func parseKey(b []byte) (key, int) {
	c := b[0]
	switch {
	case c == 0x1b:
		if len(b) > 1 && (b[1] == '[' || b[1] == 'O') {
			return parseCSI(b)
		}
		return key{Name: "esc"}, 1 // a lone Esc, or Alt+key: Esc, then the key
	case c == '\r' || c == '\n':
		return key{Name: "enter"}, 1
	case c == '\t':
		return key{Name: "tab"}, 1
	case c == 0x7f || c == 0x08:
		return key{Name: "backspace"}, 1
	case c == 0:
		return key{}, 1
	case c < 0x20:
		return key{Name: "ctrl+" + string(rune('a'+c-1))}, 1
	}
	if !utf8.FullRune(b) {
		return key{}, 0
	}
	r, n := utf8.DecodeRune(b)
	return key{Rune: r}, n
}

func parseCSI(b []byte) (key, int) {
	i := 2 // the final byte is 0x40–0x7e, after the parameters
	for i < len(b) && (b[i] < 0x40 || b[i] > 0x7e) {
		i++
	}
	if i >= len(b) {
		return key{}, 0
	}
	seq, n := string(b[2:i+1]), i+1
	switch seq {
	case "A":
		return key{Name: "up"}, n
	case "B":
		return key{Name: "down"}, n
	case "C":
		return key{Name: "right"}, n
	case "D":
		return key{Name: "left"}, n
	case "H", "1~", "7~":
		return key{Name: "home"}, n
	case "F", "4~", "8~":
		return key{Name: "end"}, n
	case "5~":
		return key{Name: "pgup"}, n
	case "6~":
		return key{Name: "pgdn"}, n
	case "3~":
		return key{Name: "del"}, n
	case "Z":
		return key{Name: "shift+tab"}, n
	case "200~": // a paste, up to ESC[201~
		end := strings.Index(string(b[n:]), "\x1b[201~")
		if end < 0 {
			return key{}, 0
		}
		return key{Name: "paste", Text: string(b[n : n+end])}, n + end + 6
	}
	if strings.HasPrefix(seq, "<64;") {
		return key{Name: "wheelup"}, n
	}
	if strings.HasPrefix(seq, "<65;") {
		return key{Name: "wheeldown"}, n
	}
	return key{}, n // other mouse reports and sequences: ignored
}

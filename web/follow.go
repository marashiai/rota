package web

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"rota/core"
)

// settingsCheck is how often the pages look whether web or web_port changed: a change applies
// within it. Tests shorten it.
var settingsCheck = 2 * time.Second

// offTailnetRetry is how often the pages try again to leave the tailnet when Tailscale did not
// confirm they had; tests shorten it.
var offTailnetRetry = 30 * time.Second

// Follow serves the pages where rota.conf says, and moves them within seconds when web or
// web_port changes: to this machine only (local), to the tailnet too (tailscale), to another
// port, or nowhere (off). Pages Tailscale serves over HTTPS leave the tailnet once web is not
// tailscale, whatever port they are on and whichever rota put them there: local means this
// machine only. Until Tailscale confirms they left, it is asked again, and pages it still serves
// and will not stop serving are not served at all. always serves the pages on this machine at
// least, as rota web does while web is off. say tells where the pages are whenever that
// changes. It returns when ctx ends, with the pages stopped.
func Follow(ctx context.Context, root, conf string, always bool, say func(string)) {
	type place struct {
		web  string
		port int
	}
	var (
		at      place
		c       *core.Config // as last read
		edited  time.Time
		first   = true
		stop    context.CancelFunc
		stopped chan struct{}
		leave   = map[int]bool{} // ports Tailscale may still serve on the tailnet, which it must not
		refused = map[int]string{}
		tried   time.Time
		warned  string
		told    string // what say said last
		serving bool
	)
	tell := func(s string) {
		if s != told {
			told = s
			say(s)
		}
	}
	end := func() {
		if stop != nil {
			stop()
			<-stopped // the port is free, and nothing of the old pages is left on the tailnet
			stop, serving = nil, false
		}
	}
	defer end()
	for {
		if read, m, ok := stableConfig(root, conf); ok && (first || !m.Equal(edited)) {
			edited = m
			want := place{read.Web, read.WebPort}
			if always && want.web == "off" {
				want.web = "local"
			}
			if first || want != at {
				end()
				if at.web == "tailscale" && !first {
					leave[at.port] = true
				}
				if want.web != "tailscale" { // also a proxy an earlier setting or rota left on this port
					leave[want.port] = true
				} else {
					delete(leave, want.port)
					delete(refused, want.port)
				}
				at, c, first, tried = want, read, false, time.Time{}
			}
		}
		if c != nil && len(leave) > 0 && (tried.IsZero() || time.Since(tried) >= offTailnetRetry) { // at once after a change, then again until confirmed
			leaveTailnet(leave, refused, &warned)
			tried = time.Now()
		}
		why, held := refused[at.port]
		held = held && at.web != "tailscale" // the pages on this port must not be on the tailnet, and Tailscale will not stop serving them
		switch {
		case c == nil:
		case held:
			end() // also pages served while Tailscale did not answer, now that it does and refuses
			tell("pages off until Tailscale stops serving them on the tailnet: " + why)
		case serving:
		case at.web == "off":
			tell("pages off: web is off")
		default:
			sctx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			stop, stopped, serving = cancel, done, true
			go func(p place) {
				defer close(done)
				serveRetrying(sctx, root, conf, p.port, p.web == "tailscale", say)
			}(at)
			tell("pages at " + c.WebURL("/") + tailnetPages(c, at.web == "tailscale"))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(settingsCheck):
		}
	}
}

// stableConfig reads rota.conf, and when it last changed: ok only when it could be read and did
// not change while it was, so a file half written is read again rather than taken for defaults.
func stableConfig(root, conf string) (*core.Config, time.Time, bool) {
	c, err := core.LoadConfig(root, conf)
	if err != nil {
		return nil, time.Time{}, false
	}
	before := modTime(c)
	c, err = core.LoadConfig(root, conf)
	if err != nil || !modTime(c).Equal(before) {
		return nil, time.Time{}, false
	}
	return c, before, true
}

// modTime is when rota.conf last changed; one that cannot be read has none.
func modTime(c *core.Config) time.Time {
	fi, err := os.Stat(c.Path(c.File))
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// leaveTailnet asks Tailscale to stop serving the pages from each port in leave, and forgets
// those it confirms. refused keeps why Tailscale did not, for the ports it answered about; one
// it could not be asked about serves nothing now, and is asked again, quietly.
func leaveTailnet(leave map[int]bool, refused map[int]string, warned *string) {
	for port := range leave {
		err := core.StopTailscaleHTTPS(port)
		switch {
		case err == nil:
			delete(leave, port)
			delete(refused, port)
		case errors.Is(err, core.ErrTailscaleUnreachable):
			delete(refused, port)
		default:
			refused[port] = err.Error()
			if err.Error() != *warned {
				*warned = err.Error()
				fmt.Fprintln(os.Stderr, "warn: the pages are not served while Tailscale still serves them on the tailnet; asked again every 30 s:", err)
			}
		}
	}
}

// serveRetrying serves the pages until ctx ends. While another rota serves them on the port, as
// rota web or the menu bar's does, it says so once and takes over when that one stops.
func serveRetrying(ctx context.Context, root, conf string, port int, tailnet bool, say func(string)) {
	for warned := false; ctx.Err() == nil; {
		err := Serve(ctx, root, conf, port, tailnet)
		if err == nil || !errors.Is(err, syscall.EADDRINUSE) {
			if err != nil {
				fmt.Fprintln(os.Stderr, "warn: web:", err)
			}
			return
		}
		if !warned {
			say("pages are served by another rota for now")
			warned = true
		}
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}
}

// tailnetPages is where the human's other devices open the pages, when they are on the tailnet.
func tailnetPages(c *core.Config, tailnet bool) string {
	switch {
	case !tailnet:
		return ""
	case core.TailscaleIP() == "":
		return ", and at this machine's Tailscale address once it has one"
	}
	return ", and on your Tailscale devices at " + c.PagesURL("/")
}

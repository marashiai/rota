package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeTailscale puts a tailscale command on PATH that acts as the macOS app's binary does:
// without a terminal's variables or TAILSCALE_BE_CLI it takes itself for the app, says it could
// not start, and exits 0.
func fakeTailscale(t *testing.T, script string) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tailscale"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	forget := func() { tailnet.Lock(); tailnet.at = time.Time{}; tailnet.Unlock() }
	forget()
	t.Cleanup(forget)
}

const macApp = `if [ -z "$TAILSCALE_BE_CLI" ]; then echo "The Tailscale GUI failed to start: (Tailscale.CLIError error 3.)"; exit 0; fi
case "$1" in
status) echo '{"Self":{"DNSName":"mac.tail1.ts.net."},"CertDomains":["mac.tail1.ts.net"]}' ;;
serve) echo '{"Web":{"mac.tail1.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:7900"}}}}}' ;;
esac
`

// An rota opened from Spotlight has no terminal's variables: it still gets this machine's name
// from Tailscale, and with it the pages over HTTPS.
func TestTailscaleAnswersAnRotaOpenedFromSpotlight(t *testing.T) {
	t.Setenv("TERM", "")
	t.Setenv("SHLVL", "")
	t.Setenv("TERM_PROGRAM", "")
	t.Setenv("TAILSCALE_BE_CLI", "") // only rota's own may make the fake answer
	fakeTailscale(t, macApp)
	if n := TailscaleName(); n != "mac.tail1.ts.net" {
		t.Fatalf("name %q", n)
	}
	if u := TailscaleHTTPS(7900); u != "https://mac.tail1.ts.net" {
		t.Fatalf("pages over HTTPS at %q", u)
	}
}

// When Tailscale answers something other than its status, the warning says what it answered,
// not that MagicDNS is off.
func TestAWarningSaysWhatTailscaleAnswered(t *testing.T) {
	fakeTailscale(t, `echo "The Tailscale GUI failed to start: (Tailscale.CLIError error 3.)"`)
	if n := TailscaleName(); n != "" {
		t.Fatalf("name %q from an answer that is not a status", n)
	}
	err := ServeTailscaleHTTPS(context.Background(), 7900)
	if err == nil || !strings.Contains(err.Error(), "The Tailscale GUI failed to start") || strings.Contains(err.Error(), "MagicDNS") {
		t.Fatalf("warning: %v", err)
	}
	fakeTailscale(t, `echo "no permission" >&2; exit 1`)
	if err := ServeTailscaleHTTPS(context.Background(), 7900); err == nil || !strings.Contains(err.Error(), "exit status 1: no permission") {
		t.Fatalf("warning: %v", err)
	}
	fakeTailscale(t, `echo "not logged in"; exit 1`)
	if err := ServeTailscaleHTTPS(context.Background(), 7900); err == nil || !strings.Contains(err.Error(), "exit status 1: not logged in") {
		t.Fatalf("warning: %v", err)
	}
}

// Setting up HTTPS asks Tailscale as a command too, not as the app.
func TestServingOverHTTPSAsksTailscaleAsACommand(t *testing.T) {
	t.Setenv("TAILSCALE_BE_CLI", "")
	set := filepath.Join(t.TempDir(), "set")
	fakeTailscale(t, `if [ -z "$TAILSCALE_BE_CLI" ]; then echo "The Tailscale GUI failed to start"; exit 0; fi
case "$1 $2" in
"status --json") echo '{"Self":{"DNSName":"mac.tail1.ts.net."},"CertDomains":["mac.tail1.ts.net"]}' ;;
"serve status") echo '{}' ;;
"serve --bg") echo "$3 $4" > `+set+` ;;
esac
`)
	if err := ServeTailscaleHTTPS(context.Background(), 7900); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(set); err != nil || strings.TrimSpace(string(b)) != "--https=443 http://127.0.0.1:7900" {
		t.Fatalf("tailscale serve was not run as a command: %q %v", b, err)
	}
}

// Tailscale stops serving the pages over HTTPS when they leave the tailnet, and only when what it
// serves is the pages from that port, read afresh: whatever else it serves is left as it is, and
// it says so. A Tailscale that does not answer is no confirmation; one that still serves them
// after it was told to stop is not either.
func TestTailscaleStopsServingOnlyThePages(t *testing.T) {
	dir := t.TempDir()
	log, state := filepath.Join(dir, "calls"), filepath.Join(dir, "served")
	fake := func(status string) string {
		return `case "$1" in
status) ` + status + ` ;;
serve) if [ "$2" = status ]; then read -r l < ` + state + `; echo "$l"; else echo "$@" >> ` + log + `; [ -f ` + dir + `/stuck ] || echo '{}' > ` + state + `; fi ;;
esac
`
	}
	answer := `echo '{"Self":{"DNSName":"mac.tail1.ts.net."},"CertDomains":["mac.tail1.ts.net"]}'`
	serving := func(handlers string) {
		os.WriteFile(state, []byte(`{"TCP":{"443":{"HTTPS":true}},"Web":{"mac.tail1.ts.net:443":{"Handlers":{`+handlers+`}}}}`), 0o644) // as Tailscale answers
		os.Remove(log)
	}
	calls := func() string { b, _ := os.ReadFile(log); return strings.TrimSpace(string(b)) }
	t.Setenv("TAILSCALE_BE_CLI", "1")
	fakeTailscale(t, fake(answer))
	serving(`"/":{"Proxy":"http://127.0.0.1:7900"}`)
	if err := StopTailscaleHTTPS(7901); err != nil || calls() != "" {
		t.Fatalf("the pages on another port are not these: %v %q", err, calls())
	}
	if err := StopTailscaleHTTPS(7900); err != nil || calls() != "serve --https=443 off" {
		t.Fatalf("the pages leave the tailnet: %v %q", err, calls())
	}
	serving(`"/":{"Proxy":"http://127.0.0.1:3000"}`)
	if err := StopTailscaleHTTPS(7900); err != nil || calls() != "" {
		t.Fatalf("something else Tailscale serves stays: %v %q", err, calls())
	}
	serving(`"/":{"Proxy":"http://127.0.0.1:7900"},"/grafana":{"Proxy":"http://127.0.0.1:3000"}`)
	if err := StopTailscaleHTTPS(7900); err == nil || calls() != "" {
		t.Fatalf("beside something else, the pages are not turned off with it: %v %q", err, calls())
	}
	os.WriteFile(state, []byte(`{"TCP":{"443":{"TCPForward":"127.0.0.1:9999"}},"Web":{"mac.tail1.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:7900"}}}}}`), 0o644)
	os.Remove(log)
	if err := StopTailscaleHTTPS(7900); err == nil || calls() != "" {
		t.Fatalf("a TCP forward on 443 is someone else's: %v %q", err, calls())
	}
	serving(`"/":{"Proxy":"http://127.0.0.1:7900"}`)
	write(t, filepath.Join(dir, "stuck"), "")
	if err := StopTailscaleHTTPS(7900); err == nil || !strings.Contains(err.Error(), "still serves") {
		t.Fatalf("still served after it was told to stop: %v", err)
	}
	fakeTailscale(t, fake(`echo "failed to connect to local tailscaled" >&2; exit 1`))
	if err := StopTailscaleHTTPS(7900); !errors.Is(err, ErrTailscaleUnreachable) {
		t.Fatalf("a Tailscale that does not answer confirms nothing: %v", err)
	}
	fakeTailscale(t, `case "$1" in status) `+answer+` ;; serve) echo "timed out" >&2; exit 1 ;; esac`)
	if err := StopTailscaleHTTPS(7900); err == nil || errors.Is(err, ErrTailscaleUnreachable) {
		t.Fatalf("a Tailscale that answers, but not what it serves, may serve the pages: %v", err)
	}
}

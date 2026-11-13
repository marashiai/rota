package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"rota/gloss/review"
	"rota/gloss/web"
)

// serve runs the review page on this machine only, and opens the pull request in a browser.
func serve(ctx context.Context, a args, out io.Writer) error {
	token, err := review.Token()
	if err != nil {
		return err
	}
	path := "/"
	if len(a.pos) > 0 {
		repo := a.flags["repo"]
		if repo == "" {
			repo = review.RepoFromGit("")
		}
		ref, err := review.ParseRef(a.pos[0], repo)
		if err != nil {
			return err
		}
		path = fmt.Sprintf("/%s/%s/%d/", ref.Owner, ref.Name, ref.Number)
	}
	addr := a.flags["addr"]
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	if host, _, err := net.SplitHostPort(addr); err != nil {
		return fmt.Errorf("--addr %s: %v", addr, err)
	} else if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("--addr %s: gloss web serves this machine only; give a loopback address such as 127.0.0.1:7391", addr)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	hook := a.flags["on-send"]
	if hook == "" {
		hook = os.Getenv("GLOSS_ON_SEND")
	}
	h := web.New(review.NewClient(token), web.Options{OnSend: onSend(hook)})
	srv := &http.Server{Handler: localOnly(h, port), ReadHeaderTimeout: 10 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, path)
	fmt.Fprintf(out, "gloss: %s\n(Ctrl+C stops it)\n", url)
	if !a.has("no-open") {
		openBrowser(url)
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	if err := srv.Serve(ln); err != http.ErrServerClosed {
		return err
	}
	return nil
}

// localOnly refuses requests whose Host is not this machine's address, so a web page cannot
// reach the server through a name that resolves to it.
func localOnly(h http.Handler, port int) http.Handler {
	ok := map[string]bool{}
	for _, host := range []string{"127.0.0.1", "localhost", "[::1]"} {
		ok[fmt.Sprintf("%s:%d", host, port)] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ok[r.Host] {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// onSend runs the hook command after a send: GLOSS_PR, GLOSS_URL and GLOSS_SENT in its
// environment, and the same as JSON on its standard input.
func onSend(hook string) func(context.Context, *review.Review, int) error {
	if strings.TrimSpace(hook) == "" {
		return nil
	}
	return func(ctx context.Context, rv *review.Review, sent int) error {
		approved := ""
		if rv.Approved() {
			approved = "1"
		}
		in, _ := json.Marshal(map[string]any{"pr": rv.PR, "url": rv.URL, "sent": sent, "approved": rv.Approved(), "reviewer": rv.Viewer, "author": rv.Author})
		cmd := exec.CommandContext(ctx, "sh", "-c", hook)
		cmd.Env = append(os.Environ(), "GLOSS_PR="+rv.PR, "GLOSS_URL="+rv.URL, fmt.Sprintf("GLOSS_SENT=%d", sent), "GLOSS_APPROVED="+approved)
		cmd.Stdin = bytes.NewReader(in)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
		}
		return nil
	}
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Start()
}

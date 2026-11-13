package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Usage is what Claude itself reports about the subscription's rate windows.
type Usage struct {
	Session    float64 // percent of the 5-hour window used
	Weekly     float64 // percent of the 7-day window used, all models
	Scoped     float64 // highest model-scoped weekly percent
	ScopedName string  // which model that is
	ResetsAt   time.Time
	Read       time.Time // when Claude reported it
}

func (u *Usage) String() string {
	s := fmt.Sprintf("session %.0f%%, weekly %.0f%%", u.Session, u.Weekly)
	if u.ScopedName != "" {
		s += fmt.Sprintf(" (%s %.0f%%)", u.ScopedName, u.Scoped)
	}
	if !u.ResetsAt.IsZero() {
		s += ", week resets " + u.ResetsAt.Local().Format("Mon 15:04")
	}
	if !u.Read.IsZero() {
		s += ", read " + u.Read.Local().Format("15:04")
	}
	return s
}

// usageToken finds Claude Code's OAuth token: env, credentials file, then the macOS keychain.
func usageToken(c *Config) string {
	if t := os.Getenv(usageTokenEnv); t != "" {
		return t
	}
	file := usageCredFile
	if strings.HasPrefix(file, "~/") {
		home, _ := os.UserHomeDir()
		file = filepath.Join(home, file[2:])
	}
	if b, err := os.ReadFile(file); err == nil {
		if t := tokenFromJSON(b); t != "" {
			return t
		}
	}
	if runtime.GOOS == "darwin" && usageKeychain != "" {
		if b, err := probe(c, "", os.Environ(), "security", "find-generic-password", "-s", usageKeychain, "-w"); err == nil {
			return tokenFromJSON(b)
		}
	}
	return ""
}

func tokenFromJSON(b []byte) string {
	var v struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	json.Unmarshal(b, &v)
	return v.ClaudeAiOauth.AccessToken
}

func usageEnabled(c *Config) bool {
	switch c.Usage {
	case "on":
		return true
	case "off":
		return false
	}
	return usageToken(c) != ""
}

// ReadUsage is what Claude reports about the subscription's windows now, a reading up to a
// minute old reused.
func (c *Config) ReadUsage() (*Usage, error) {
	if !usageEnabled(c) {
		return nil, fmt.Errorf("usage is off")
	}
	return fetchUsage(c, 60, 0)
}

type usageCache struct {
	Time  time.Time     `json:"time"`           // of the last reading
	Usage Usage         `json:"usage"`          // the last reading
	Asked time.Time     `json:"asked"`          // when the endpoint was last asked, whatever it answered
	Err   string        `json:"err,omitempty"`  // its refusal then, if it refused
	Wait  time.Duration `json:"wait,omitempty"` // how long it is not asked after a refusal
}

// measure, as fetchUsage's maxAge, is the reading after a shift: fresh even within usageFloor,
// though never while the endpoint is refusing.
const measure = -1

// refused is the endpoint's "rate limited", with how long it asked to wait, if it said.
type refused struct {
	err   error
	after time.Duration
}

func (r refused) Error() string { return r.err.Error() }

// Claude's usage endpoint answers "rate limited" (HTTP 429) when it is asked too often, and its
// limit is not published. So it is asked only when a reading is needed: before a shift starts,
// after it ends (to measure it), and while it runs, rarely while the team pool has room and more
// often as it runs low. Whoever asks, the organizer or any other rota on this machine, the
// answers are shared through one file, and the endpoint is asked at most once a usageFloor.
// After a refusal it is not asked again for as long as it says (Retry-After), or else for twice
// the last wait (from two minutes up to half an hour); every reading after that halves the wait.

// fetchUsage asks Claude for the rate windows, reusing a reading younger than maxAge seconds
// (0: as fresh as usageFloor allows; measure: fresh), and retrying an endpoint that fails
// (HTTP 5xx) retries times. Within the floor, or while the endpoint is refusing, the answer is
// the last reading, or the last refusal.
func fetchUsage(c *Config, maxAge, retries int) (*Usage, error) {
	last := cachedUsage(c)
	if last == nil {
		last = &usageCache{}
	}
	answered := !last.Time.IsZero() && !last.Time.Before(last.Asked) // the last request was answered
	gap := max(last.Wait, usageFloor)
	switch {
	case !answered && time.Since(last.Asked) < gap: // refused, or another rota is asking now
		return nil, fmt.Errorf("%s (asked again from %s)", or(last.Err, "usage endpoint: no answer yet"), last.Asked.Add(gap).Format("15:04:05"))
	case !answered || maxAge == measure: // a shift is measured fresh while Claude answers
	case maxAge > 0 && time.Since(last.Time) < time.Duration(maxAge)*time.Second, time.Since(last.Asked) < gap:
		u := last.Usage
		return &u, nil
	}
	next := usageCache{Time: last.Time, Usage: last.Usage, Asked: time.Now(), Wait: last.Wait}
	saveUsage(c, next) // asked: no other rota asks again meanwhile
	u, err := fetchUsageRemote(c, retries)
	var r refused
	switch {
	case errors.As(err, &r) && r.after > 0: // as long as Claude says
		next.Err, next.Wait = err.Error(), r.after
	case errors.As(err, &r):
		next.Err, next.Wait = err.Error(), min(max(2*last.Wait, 2*time.Minute), 30*time.Minute)
	case err != nil:
		next.Err = err.Error()
	default:
		u.Read = time.Now()
		next.Time, next.Usage, next.Wait = u.Read, *u, last.Wait/2
		if next.Wait < usageFloor {
			next.Wait = 0
		}
	}
	saveUsage(c, next)
	return u, err
}

// saveUsage writes the shared reading whole: another rota never reads half of it.
func saveUsage(c *Config, uc usageCache) {
	path := c.Path(stateFile + ".usage")
	if b, err := json.Marshal(uc); err == nil && os.WriteFile(path+".tmp", b, 0o600) == nil {
		os.Rename(path+".tmp", path)
	}
}

func cachedUsage(c *Config) *usageCache {
	var cached usageCache
	if b, err := os.ReadFile(c.Path(stateFile + ".usage")); err != nil || json.Unmarshal(b, &cached) != nil {
		return nil
	}
	return &cached
}

// lastUsage is the last reading of Claude's windows, however old, without asking Claude; nil
// with usage off or before the first reading.
func (c *Config) lastUsage() *Usage {
	cached := cachedUsage(c)
	if !usageEnabled(c) || cached == nil || cached.Time.IsZero() {
		return nil
	}
	u := cached.Usage
	if u.Read.IsZero() {
		u.Read = cached.Time
	}
	return &u
}

func fetchUsageRemote(c *Config, retries int) (*Usage, error) {
	tok := usageToken(c)
	if tok == "" {
		return nil, fmt.Errorf("no Claude OAuth token: set %s, or log in with claude so %s exists", usageTokenEnv, usageCredFile)
	}
	req, err := http.NewRequest("GET", usageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("Content-Type", "application/json")
	var body []byte
	for attempt, wait := 0, 5*time.Second; ; attempt, wait = attempt+1, wait*3 {
		resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
		if err != nil {
			return nil, err
		}
		body, _ = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode == 200 {
			break
		}
		err = fmt.Errorf("usage endpoint: HTTP %d: %s", resp.StatusCode, strings.Join(strings.Fields(tail(string(body), 200)), " "))
		if resp.StatusCode == http.StatusTooManyRequests { // not asked again soon: fetchUsage waits
			ra := resp.Header.Get("Retry-After") // seconds, or a date
			secs, _ := strconv.Atoi(ra)
			after := time.Duration(secs) * time.Second
			if at, perr := http.ParseTime(ra); perr == nil {
				after = time.Until(at)
			}
			return nil, refused{err, after}
		}
		if resp.StatusCode < 500 || attempt >= retries {
			return nil, err
		}
		fmt.Fprintf(os.Stderr, "warn: usage endpoint HTTP %d, retrying in %s\n", resp.StatusCode, wait)
		time.Sleep(wait)
	}
	return parseUsage(body)
}

func parseUsage(body []byte) (*Usage, error) {
	type window struct {
		Utilization *float64 `json:"utilization"`
		ResetsAt    string   `json:"resets_at"`
	}
	var v struct {
		FiveHour *window `json:"five_hour"`
		SevenDay *window `json:"seven_day"`
		Limits   []struct {
			Kind    string  `json:"kind"`
			Percent float64 `json:"percent"`
			Scope   *struct {
				Model *struct {
					DisplayName string `json:"display_name"`
				} `json:"model"`
			} `json:"scope"`
		} `json:"limits"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("usage endpoint: %w", err)
	}
	u := &Usage{}
	resets := ""
	if v.FiveHour != nil && v.FiveHour.Utilization != nil {
		u.Session = *v.FiveHour.Utilization
	}
	if v.SevenDay != nil && v.SevenDay.Utilization != nil {
		u.Weekly, resets = *v.SevenDay.Utilization, v.SevenDay.ResetsAt
	}
	for _, l := range v.Limits { // the model-scoped weekly windows; the highest counts
		if l.Kind == "weekly_scoped" && l.Percent >= u.Scoped {
			u.Scoped = l.Percent
			if l.Scope != nil && l.Scope.Model != nil {
				u.ScopedName = l.Scope.Model.DisplayName
			}
		}
	}
	if resets != "" {
		u.ResetsAt, _ = time.Parse(time.RFC3339Nano, resets)
	}
	return u, nil
}

package core

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Claude's own usage limit (its 5-hour window, or its week), reached during a shift: Claude stops
// the shift and says when the limit resets. The shift is interrupted then, as by an update: what
// it left running goes on, and it resumes where it left off once the limit resets. Until then no
// shift starts, as each would meet the limit too.

// limitFile holds when Claude's usage limit resets, while it is reached.
const limitFile = ".claude-limit.json"

type claudeLimit struct {
	Until time.Time `json:"until"`
	Said  string    `json:"said"` // as Claude said it
}

// limitText finds Claude saying a usage limit is reached, as a shift's last words: "You've hit
// your 5-hour limit · resets 9:40pm (Europe/Amsterdam)".
var (
	limitText = regexp.MustCompile(`(?i)\b(hit|reached)\b[^.\n]{0,40}\blimit\b`)
	resetText = regexp.MustCompile(`(?i)\bresets?\s+(?:at\s+)?(?:(mon|tue|wed|thu|fri|sat|sun)[a-z]*\s+)?(\d{1,2})(?::(\d{2}))?\s*([ap]m)?(?:\s*\(([^)]+)\))?`)
)

// usageLimit says whether a shift ended because Claude's usage limit was reached, and when it
// resets: from what Claude said, else limitRetry from now.
func usageLimit(output string, now time.Time) (until time.Time, said string, ok bool) {
	cr, ok := finalResult(output)
	if !ok || !cr.IsError || !limitText.MatchString(cr.Result) {
		return time.Time{}, "", false
	}
	said = strings.TrimSpace(cr.Result)
	return resetTime(said, now), said, true
}

// limitRetry is how long rota waits for a limit that does not say when it resets.
const limitRetry = 30 * time.Minute

// resetTime is the next moment Claude's words name, "resets 9:40pm (Europe/Amsterdam)" or
// "resets Fri 8am", a minute after it; else limitRetry from now.
func resetTime(said string, now time.Time) time.Time {
	m := resetText.FindStringSubmatch(said)
	if m == nil {
		return now.Add(limitRetry)
	}
	loc := time.Local
	if m[5] != "" {
		if l, err := time.LoadLocation(strings.TrimSpace(m[5])); err == nil {
			loc = l
		}
	}
	hour, _ := strconv.Atoi(m[2])
	min, _ := strconv.Atoi(m[3])
	switch strings.ToLower(m[4]) {
	case "pm":
		if hour < 12 {
			hour += 12
		}
	case "am":
		if hour == 12 {
			hour = 0
		}
	}
	if hour > 23 || min > 59 {
		return now.Add(limitRetry)
	}
	t := now.In(loc)
	at := time.Date(t.Year(), t.Month(), t.Day(), hour, min, 0, 0, loc)
	if m[1] != "" {
		want := map[string]time.Weekday{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}[strings.ToLower(m[1])[:3]]
		at = at.AddDate(0, 0, (int(want)-int(at.Weekday())+7)%7)
	}
	for !at.After(now) {
		if m[1] != "" {
			at = at.AddDate(0, 0, 7)
		} else {
			at = at.AddDate(0, 0, 1)
		}
	}
	return at.Add(time.Minute)
}

// holdForLimit keeps every shift from starting until Claude's usage limit resets.
func (c *Config) holdForLimit(until time.Time, said string) error {
	b, _ := json.Marshal(claudeLimit{until, said})
	tmp := c.Path(limitFile) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.Path(limitFile))
}

// limitHold is when Claude's usage limit resets, while no shift starts for it; zero once it has.
func (c *Config) limitHold(now time.Time) time.Time {
	var l claudeLimit
	b, err := os.ReadFile(c.Path(limitFile))
	if err != nil || json.Unmarshal(b, &l) != nil || !now.Before(l.Until) {
		return time.Time{}
	}
	return l.Until
}

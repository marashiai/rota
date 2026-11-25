package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// A shift goes on while it makes real progress: every run_minutes rota checks what it did since it
// began or was last checked, with triage_model, and it goes on for another run_minutes, checked
// again, or ends when it is stuck, loops or waits on nothing. A shift the human is talking with
// goes on unchecked, and one that cannot be checked goes on: only one known not to work ends.

// runMinute is a minute of run_minutes; a test makes it shorter.
var runMinute = time.Minute

// activity is what a running shift did, step by step, as rota's check reads it.
type activity struct {
	mu    sync.Mutex
	steps []step
	open  map[string]step // tool calls not yet answered, by id
}

type step struct {
	at   time.Time
	text string
}

// see takes one line claude printed.
func (a *activity) see(line string, now time.Time) {
	var ev struct {
		Type    string `json:"type"`
		Message struct {
			Content []struct {
				Type    string          `json:"type"`
				ID      string          `json:"id"`
				Name    string          `json:"name"`
				Text    string          `json:"text"`
				Input   json.RawMessage `json:"input"`
				ToolUse string          `json:"tool_use_id"`
				Content json.RawMessage `json:"content"`
				IsError bool            `json:"is_error"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal([]byte(line), &ev) != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.open == nil {
		a.open = map[string]step{}
	}
	for _, c := range ev.Message.Content {
		switch {
		case ev.Type == "assistant" && c.Type == "text" && strings.TrimSpace(c.Text) != "":
			a.steps = append(a.steps, step{now, "said: " + oneLine(c.Text, 300)})
		case ev.Type == "assistant" && c.Type == "tool_use":
			s := step{now, c.Name + ": " + oneLine(toolInput(c.Input), 200)}
			a.steps = append(a.steps, s)
			a.open[c.ID] = s
		case ev.Type == "user" && c.Type == "tool_result":
			delete(a.open, c.ToolUse)
			what := "result"
			if c.IsError {
				what = "error"
			}
			a.steps = append(a.steps, step{now, what + ": " + oneLine(toolResultText(c.Content), 160)})
		}
	}
	if len(a.steps) > 4000 {
		a.steps = a.steps[len(a.steps)-2000:]
	}
}

// since is what the shift did from then on, and what it still waits on, for the check: its last
// steps when there are many.
func (a *activity) since(from time.Time, now time.Time) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var lines []string
	for _, s := range a.steps {
		if !s.at.Before(from) {
			lines = append(lines, s.at.Format("15:04")+" "+s.text)
		}
	}
	text := strings.Join(lines, "\n")
	if len(text) > 14000 {
		text = "…\n" + text[len(text)-14000:]
	}
	if text == "" {
		text = "(nothing)"
	}
	for _, s := range a.open {
		text += fmt.Sprintf("\nStill running, for %d minutes: %s", int(now.Sub(s.at).Minutes()), s.text)
	}
	return text
}

func toolInput(raw json.RawMessage) string {
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil {
		return string(raw)
	}
	for _, k := range []string{"command", "file_path", "pattern", "url", "description"} {
		if v, ok := in[k].(string); ok && v != "" {
			return v
		}
	}
	return string(raw)
}

func toolResultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b []string
		for _, p := range parts {
			b = append(b, p.Text)
		}
		return strings.Join(b, " ")
	}
	return string(raw)
}

func oneLine(s string, n int) string { return FirstLine(strings.Join(strings.Fields(s), " "), n) }

// checkShift asks triage_model whether a shift works on: real progress goes on, stuck, looping or
// idle ends. It goes on when it cannot be checked.
func checkShift(ctx context.Context, c *Config, agent string, minutes int, did string) (goOn bool, why string) {
	if c.TriageModel == "" || c.TriageModel == "off" {
		return true, "not checked, as triage_model is off"
	}
	tctx, cancel := context.WithTimeout(ctx, triageTimeout)
	defer cancel()
	cmd := command(tctx, c, c.Root, noMemory(os.Environ()), c.ClaudeBin, "-p", "--output-format", "json",
		"--model", c.TriageModel, "--tools", "")
	cmd.Stdin = strings.NewReader(checkPrompt(agent, minutes, did))
	var out bytes.Buffer
	cmd.Stdout = &out
	runTracked(cmd)
	cr, ok := finalResult(out.String())
	if !ok {
		return true, "could not be checked (no answer)"
	}
	text := cr.Result
	i, j := strings.Index(text, "{"), strings.LastIndex(text, "}")
	var v struct {
		GoOn bool   `json:"go_on"`
		Why  string `json:"why"`
	}
	if i < 0 || j <= i || json.Unmarshal([]byte(text[i:j+1]), &v) != nil {
		return true, "could not be checked (an unreadable answer)"
	}
	return v.GoOn, strings.TrimSuffix(sentence(v.Why), ".")
}

func checkPrompt(agent string, minutes int, did string) string {
	return fmt.Sprintf(`You check a running shift of %s, an AI agent of a software team that works on its own. Its shift has run %d minutes, and rota decides now whether it goes on: it goes on while the agent does legitimate work, and ends when it does not.

Legitimate work makes real progress on the agent's task: reading, writing or testing code, running or waiting on a long job it started and still needs (a test run, a recording, a build), reviewing, opening or updating pull requests, writing to teammates or the human as its work needs, talking with the human in the team chat (rota say, rota listen) or waiting for their answer (rota wait). Not legitimate: stuck, repeating the same failing step without changing anything, going round in a loop, idle or waiting on nothing, or work that serves no task.

What it did since it began or was last checked, step by step (time, then the step):

%s

Answer with JSON only, no other text: {"go_on": true or false, "why": "one sentence for the human: what it is doing, and why it goes on or ends"}`, agent, minutes, did)
}

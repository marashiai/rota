package core

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Event is one thing a shift did, read from claude's stream-json output, one JSON object per
// line. The front ends render events their own way; the organizer's live line is a Summary.
type Event struct {
	Kind    string // start, text, tool, output, inbox, end, or note: a line that is not an event (a failure message)
	Text    string // text: the agent's words, markdown; output: what the tool printed; inbox: the notes that reached the running shift; end: the final message
	Tool    string // tool: its name
	ID      string // tool: the call's id; output: the id of the call it answers
	Arg     string // tool: the argument a human wants to see, on one line
	Model   string // start
	Tools   int    // start: how many tools it has
	Error   bool   // output: the tool failed; end: the shift ended in an error
	Subtype string // end: how it ended
	Turns   int    // end
}

type streamEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Model   string `json:"model"`
	Message struct {
		Content []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			Name      string          `json:"name"`
			ID        string          `json:"id"`          // a tool call's
			ToolUseID string          `json:"tool_use_id"` // the call a result answers
			Input     json.RawMessage `json:"input"`
			Content   json.RawMessage `json:"content"` // a tool result: a string, or parts
			IsError   bool            `json:"is_error"`
		} `json:"content"`
	} `json:"message"`
	Tools   []string `json:"tools"`
	Result  string   `json:"result"`
	Turns   int      `json:"num_turns"`
	IsError bool     `json:"is_error"`
}

// ParseLine reads the events of one line of a shift's stream; a line can hold several.
func ParseLine(line string) []Event {
	line = strings.TrimSpace(line)
	var e streamEvent
	if line == "" {
		return nil
	}
	if !strings.HasPrefix(line, "{") || json.Unmarshal([]byte(line), &e) != nil {
		return []Event{{Kind: "note", Text: line, Error: true}}
	}
	var out []Event
	switch e.Type {
	case "system":
		if e.Subtype == "init" {
			out = append(out, Event{Kind: "start", Model: e.Model, Tools: len(e.Tools)})
		}
	case "assistant":
		for _, c := range e.Message.Content {
			switch {
			case c.Type == "text" && strings.TrimSpace(c.Text) != "":
				out = append(out, Event{Kind: "text", Text: c.Text})
			case c.Type == "tool_use":
				out = append(out, Event{Kind: "tool", Tool: c.Name, ID: c.ID, Arg: toolArg(c.Input)})
			}
		}
	case "user":
		for _, c := range e.Message.Content {
			if c.Type == "tool_result" {
				out = append(out, Event{Kind: "output", ID: c.ToolUseID, Text: strings.TrimRight(resultText(c.Content), "\n"), Error: c.IsError})
			} else if notes, ok := strings.CutPrefix(c.Text, noteHeader); c.Type == "text" && ok { // claude's echo: it has taken them
				out = append(out, Event{Kind: "inbox", Text: strings.TrimSpace(notes)})
			}
		}
	case "result":
		out = append(out, Event{Kind: "end", Text: e.Result, Error: e.IsError, Subtype: e.Subtype, Turns: e.Turns})
	}
	return out
}

// ReadEvents reads a shift's transcript, and whether it has ended.
func ReadEvents(path string) (events []Event, ended bool) {
	b, _ := os.ReadFile(path)
	for _, line := range strings.Split(string(b), "\n") {
		for _, e := range ParseLine(line) {
			events = append(events, e)
			ended = ended || e.Kind == "end"
		}
	}
	return events, ended
}

// Summary is the event on one short line, for the live output; "" for events not worth a
// line at that level: tools shows the tool calls, all the agent's words too.
func (e Event) Summary(level string) string {
	switch {
	case level == "off":
	case e.Kind == "start":
		return fmt.Sprintf("claude started, %d tools", e.Tools)
	case e.Kind == "tool":
		return FirstLine(e.Tool+" "+e.Arg, 120)
	case e.Kind == "inbox": // the notes, without what a talk's first words tell the shift to do
		notes, _, _ := strings.Cut(e.Text, talkBegins)
		return FirstLine("took a note: "+strings.Join(strings.Fields(notes), " "), 120)
	case e.Kind == "text" && level == "all":
		return `"` + FirstLine(e.Text, 100) + `"`
	case e.Kind == "end":
		return "claude finished"
	}
	return ""
}

// resultText is a tool result's text, whether claude sent a string or a list of parts.
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return string(raw)
	}
	var texts []string
	for _, p := range parts {
		if p.Type == "text" {
			texts = append(texts, p.Text)
		} else {
			texts = append(texts, "["+p.Type+"]")
		}
	}
	return strings.Join(texts, "\n")
}

// toolArg is the argument of a tool call a human wants to see.
func toolArg(raw json.RawMessage) string {
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil {
		return ""
	}
	for _, k := range []string{"command", "file_path", "pattern", "path", "url", "query", "description", "prompt", "skill", "subject"} {
		if v, ok := in[k].(string); ok && v != "" {
			return FirstLine(v, 400)
		}
	}
	b, _ := json.Marshal(in)
	return FirstLine(string(b), 400)
}

// FirstLine is s up to its first line break (marked …), at most n bytes.
func FirstLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

// claudeResult is the final event, with the tokens the shift used.
type claudeResult struct {
	Result  string `json:"result"`
	Turns   int    `json:"num_turns"`
	Session string `json:"session_id"`
	IsError bool   `json:"is_error"`
	Subtype string `json:"subtype"`
	Usage   struct {
		In         int `json:"input_tokens"`
		Out        int `json:"output_tokens"`
		CacheRead  int `json:"cache_read_input_tokens"`
		CacheWrite int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

// finalResult reads how a shift's output ended: the last result event's answer and status,
// with the turns and tokens of every result. A background job that ends after the agent's
// answer wakes claude again, and each wake ends with a result that counts only itself.
func finalResult(output string) (claudeResult, bool) {
	var res claudeResult
	found := false
	turns, in, out, cacheRead, cacheWrite := 0, 0, 0, 0, 0
	for _, line := range strings.Split(output, "\n") {
		var probe struct {
			Type string `json:"type"`
		}
		if line = strings.TrimSpace(line); !strings.HasPrefix(line, "{") || json.Unmarshal([]byte(line), &probe) != nil || probe.Type != "result" {
			continue
		}
		var r claudeResult
		if json.Unmarshal([]byte(line), &r) != nil {
			continue
		}
		res, found = r, true
		turns, in, out = turns+r.Turns, in+r.Usage.In, out+r.Usage.Out
		cacheRead, cacheWrite = cacheRead+r.Usage.CacheRead, cacheWrite+r.Usage.CacheWrite
	}
	res.Turns, res.Usage.In, res.Usage.Out, res.Usage.CacheRead, res.Usage.CacheWrite = turns, in, out, cacheRead, cacheWrite
	return res, found
}

// assistantTurns counts the assistant messages of a stream that may have been cut off.
func assistantTurns(output string) int {
	n := 0
	for _, line := range strings.Split(output, "\n") {
		var e struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(line), &e) == nil && e.Type == "assistant" {
			n++
		}
	}
	return n
}

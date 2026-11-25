package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// verdict is the organizer's judgment of a shift that did not finish ok.
type verdict struct {
	Retry  bool   `json:"retry"`
	Reason string `json:"reason"` // one sentence for the human, in the journal and spend.csv
	Note   string `json:"note"`   // one sentence of advice for the agent's next shift (its inbox)
}

// judge asks Claude whether running the failed shift again would help, from what the agent
// was doing when it stopped: no tools, a small model. When it cannot judge
// (triage off, the organizer is shutting down, claude unavailable) the answer is to retry.
func judge(ctx context.Context, c *Config, a *Agent, r Run, res Result, attempt int) verdict {
	if c.TriageModel == "" || c.TriageModel == "off" {
		return verdict{Retry: true, Reason: "Triage is off, retrying."}
	}
	if errors.Is(context.Cause(ctx), errOperatorCancel) {
		return verdict{Retry: true, Reason: "Stopped by the operator, retrying."}
	}
	if ctx.Err() != nil {
		return verdict{Retry: true, Reason: "The organizer was stopped, retrying."}
	}
	tctx, cancel := context.WithTimeout(context.Background(), triageTimeout)
	defer cancel()
	cmd := command(tctx, c, c.Root, noMemory(os.Environ()), c.ClaudeBin, "-p", "--output-format", "json",
		"--model", c.TriageModel, "--tools", "")
	cmd.Stdin = strings.NewReader(triagePrompt(c, a, r, res, attempt))
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := runTracked(cmd)
	cr, ok := finalResult(out.String())
	if !ok {
		fmt.Fprintf(os.Stderr, "warn: triage of %s gave no result (%v): %s\n", a.Name, err, tail(errOut.String(), 300))
		return verdict{Retry: true, Reason: "Could not be judged (claude gave no answer), retrying."}
	}
	v, perr := parseVerdict(cr.Result)
	if perr != nil {
		fmt.Fprintf(os.Stderr, "warn: triage of %s: %v\n", a.Name, perr)
		return verdict{Retry: true, Reason: "Could not be judged (unreadable answer), retrying."}
	}
	return v
}

func parseVerdict(text string) (verdict, error) {
	i, j := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if i < 0 || j <= i {
		return verdict{}, fmt.Errorf("no JSON object in %q", tail(text, 200))
	}
	var v verdict
	if err := json.Unmarshal([]byte(text[i:j+1]), &v); err != nil {
		return verdict{}, err
	}
	v.Reason, v.Note = sentence(v.Reason), sentence(v.Note)
	if v.Reason == "" {
		v.Reason = "No reason given."
	}
	return v, nil
}

// sentence trims and makes sure a non-empty line ends with a period.
func sentence(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if s != "" && !strings.ContainsAny(s[len(s)-1:], ".!?") {
		s += "."
	}
	return s
}

func triagePrompt(c *Config, a *Agent, r Run, res Result, attempt int) string {
	outcome := r.Status
	switch {
	case r.Status == "timeout":
		outcome = "ended by rota's check of its progress, which found: " + res.Checked
	case r.Status == "stopped":
		outcome = "stopped by the human operator"
	case r.Status == "no-json":
		outcome = "claude exited without a result"
	case strings.HasPrefix(r.Status, "error:"):
		outcome = "claude ended with " + strings.TrimPrefix(r.Status, "error:")
	case r.Status == "failed":
		outcome = "it could not start"
	case r.Status == "lost" || r.Status == "busy":
		outcome = "the host running it was " + r.Status
	}
	var b strings.Builder
	fmt.Fprintf(&b, "You are the organizer of a small team of AI agents that maintain one developer's software. "+
		"The agent %q just had a shift that did not finish: %s. This was attempt %d of at most %d. "+
		"The shift ran %d minutes and made %d turns.\n\n",
		a.Name, outcome, attempt, c.MaxAttempts, r.Seconds/60, r.Turns)
	fmt.Fprintf(&b, "The agent's role:\n%s\n\n", strings.TrimSpace(a.Role))
	fmt.Fprintf(&b, "What it did, as far as it got (the last part):\n%s\n\n", shiftTail(res, 6000))
	b.WriteString("Decide whether running the same shift again after a pause is likely to succeed, or whether it would repeat the failure and waste the team's share of Claude. " +
		"Retrying makes sense when the failure looks transient: an interruption, a crash, a network or provider problem, a host that went away. " +
		"Retrying is pointless when the agent ran out of time or turns on work that would take as long again, was going in circles, " +
		"or was blocked by something only the human can change (a login, a permission, a missing repository, a decision).\n\n" +
		"Answer with one JSON object and nothing else:\n" +
		`{"retry": true or false, "reason": "one sentence for the human", "note": "one sentence of advice for the agent's next shift, or an empty string"}` + "\n")
	return b.String()
}

// shiftTail is the readable end of a shift: the error when claude never started, else its
// tool calls and the agent's words.
func shiftTail(res Result, n int) string {
	if !res.Started {
		return tail(res.Output, n)
	}
	var lines []string
	for _, line := range strings.Split(res.Output, "\n") {
		for _, e := range ParseLine(line) {
			if s := e.Summary("all"); s != "" {
				lines = append(lines, s)
			}
		}
	}
	if len(lines) == 0 {
		return "(nothing was recorded)"
	}
	return tail(strings.Join(lines, "\n"), n)
}

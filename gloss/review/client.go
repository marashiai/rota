// Package review reads and writes the notes of a GitHub pull request, commit by commit.
//
// Every note lives on GitHub; gloss keeps only which ones the reviewer has read (Reads). A note on a hunk is a review comment on the commit it was
// written on; a draft is a comment in the reviewer's pending review; a note on the whole change
// is a section of a review's body or a PR comment. Notes written by a named author (an agent)
// carry a hidden marker, so a reviewer and its agents may share one GitHub account.
package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Client talks to GitHub's API with a token.
type Client struct {
	Token string
	API   string // https://api.github.com unless set (tests)
	HTTP  *http.Client

	mu    sync.Mutex
	diffs map[string][]File // by repo, base and commit: a commit's diff never changes
}

// NewClient returns a client for api.github.com.
func NewClient(token string) *Client {
	return &Client{Token: token, API: "https://api.github.com", HTTP: &http.Client{Timeout: time.Minute}}
}

// Token finds a GitHub token: GH_TOKEN, then GITHUB_TOKEN, then the gh CLI's login.
func Token() (string, error) {
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v, nil
		}
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		return "", errors.New("no GitHub token: set GH_TOKEN or log in with gh auth login")
	}
	return string(bytes.TrimSpace(out)), nil
}

// APIError is an error GitHub returned.
type APIError struct {
	Status   int
	Messages []string
}

func (e *APIError) Error() string {
	msg := strings.Join(e.Messages, "; ")
	if e.Status != 0 {
		return fmt.Sprintf("GitHub %d: %s", e.Status, msg)
	}
	return "GitHub: " + msg
}

// pendingConflict says GitHub refused a comment because the account already has a pending
// review on the pull request: while a reviewer has drafts, its account can write no other
// review comment there.
func pendingConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "one pending review per pull request")
}

func (c *Client) do(ctx context.Context, method, path, accept string, body any) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.API, "/")+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if accept == "" {
		accept = "application/vnd.github+json"
	}
	req.Header.Set("Accept", accept)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, restError(resp.StatusCode, data)
	}
	return data, nil
}

func restError(status int, data []byte) error {
	var e struct {
		Message string            `json:"message"`
		Errors  []json.RawMessage `json:"errors"`
	}
	apiErr := &APIError{Status: status}
	if json.Unmarshal(data, &e) != nil {
		apiErr.Messages = []string{strings.TrimSpace(string(data))}
		return apiErr
	}
	if e.Message != "" {
		apiErr.Messages = append(apiErr.Messages, e.Message)
	}
	for _, raw := range e.Errors {
		var s string
		var o struct {
			Message string `json:"message"`
		}
		switch {
		case json.Unmarshal(raw, &s) == nil:
			apiErr.Messages = append(apiErr.Messages, s)
		case json.Unmarshal(raw, &o) == nil && o.Message != "":
			apiErr.Messages = append(apiErr.Messages, o.Message)
		}
	}
	return apiErr
}

// rest calls GitHub's REST API, decoding the JSON reply into out when out is not nil.
func (c *Client) rest(ctx context.Context, method, path string, body, out any) error {
	data, err := c.do(ctx, method, path, "", body)
	if err != nil || out == nil {
		return err
	}
	return json.Unmarshal(data, out)
}

// graphql runs a GraphQL query or mutation, decoding its data into out.
func (c *Client) graphql(ctx context.Context, query string, vars map[string]any, out any) error {
	data, err := c.do(ctx, http.MethodPost, "/graphql", "", map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	var resp struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return err
	}
	if len(resp.Errors) > 0 {
		e := &APIError{}
		for _, m := range resp.Errors {
			e.Messages = append(e.Messages, m.Message)
		}
		return e
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(resp.Data, out)
}

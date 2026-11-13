package review

import (
	"context"
	"fmt"
	"strings"
)

// The author's side: an agent (or anyone under a name) posts notes and replies at once, each
// marked with its name. While the reviewer has drafts on the pull request and shares the agent's
// account, GitHub refuses that account any review comment; the note then goes out as a PR comment
// whose marker says where it belongs.

// Post publishes notes under author's name and returns their ids. Notes on lines of one diff go
// out together, as one review of its commit: the head, for the whole change's.
func (c *Client) Post(ctx context.Context, rv *Review, author string, notes []NoteInput) ([]string, error) {
	if strings.TrimSpace(author) == "" {
		return nil, fmt.Errorf("say who writes the notes")
	}
	type item struct {
		a   anchor
		in  NoteInput
		pos int
	}
	var byCommit = map[string][]item{}
	var order []string
	var loose []item // the whole change, whole commits, whole files, places GitHub cannot show
	for _, in := range notes {
		a, err := c.resolve(ctx, rv, in)
		if err != nil {
			return nil, err
		}
		it := item{a: a, in: in}
		if a.Path == "" || a.Line == 0 {
			loose = append(loose, it)
			continue
		}
		pos, ok, err := c.position(ctx, rv, a)
		if err != nil {
			return nil, err
		}
		if !ok {
			loose = append(loose, it)
			continue
		}
		it.pos = pos
		sha := a.on(rv)
		if byCommit[sha] == nil {
			order = append(order, sha)
		}
		byCommit[sha] = append(byCommit[sha], it)
	}

	var ids []string
	for _, sha := range order {
		items := byCommit[sha]
		type comment struct {
			Path     string `json:"path"`
			Position int    `json:"position"`
			Body     string `json:"body"`
		}
		var cs []comment
		for _, it := range items {
			m := it.a.meta()
			m.Author = author
			cs = append(cs, comment{it.a.Path, it.pos, encode(m, it.in.Text)})
		}
		var r struct {
			ID int64 `json:"id"`
		}
		err := c.rest(ctx, "POST", fmt.Sprintf("/repos/%s/pulls/%d/reviews", rv.Ref.Repo(), rv.Ref.Number), map[string]any{
			"commit_id": sha, "event": "COMMENT", "body": marked(Meta{Author: author}, ""), "comments": cs,
		}, &r)
		if pendingConflict(err) {
			loose = append(loose, items...)
			continue
		}
		if err != nil {
			return ids, fmt.Errorf("post notes on %s: %w", sha[:7], err)
		}
		var got []struct {
			NodeID string `json:"node_id"`
		}
		if err := c.rest(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d/reviews/%d/comments?per_page=100", rv.Ref.Repo(), rv.Ref.Number, r.ID), nil, &got); err == nil {
			for _, g := range got {
				ids = append(ids, g.NodeID)
			}
		}
	}
	for _, it := range loose {
		m := it.a.meta()
		m.Author = author
		id, err := c.comment(ctx, rv, encode(m, it.in.Text))
		if err != nil {
			return ids, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// Reply answers the thread holding note noteID under author's name, and returns the reply's id.
func (c *Client) Reply(ctx context.Context, rv *Review, author, noteID, text string) (string, error) {
	if strings.TrimSpace(author) == "" {
		return "", fmt.Errorf("say who writes the reply")
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("a reply needs text")
	}
	t, n := rv.Thread(noteID)
	if t == nil || n.Draft || t.Notes[0].Draft {
		return "", fmt.Errorf("%s has no note %s", rv.PR, noteID)
	}
	if t.ghThread != "" && t.topDB != 0 {
		var r struct {
			NodeID string `json:"node_id"`
		}
		err := c.rest(ctx, "POST", fmt.Sprintf("/repos/%s/pulls/%d/comments/%d/replies", rv.Ref.Repo(), rv.Ref.Number, t.topDB),
			map[string]any{"body": encode(Meta{Author: author}, text)}, &r)
		if err == nil {
			return r.NodeID, nil
		}
		if !pendingConflict(err) {
			return "", fmt.Errorf("reply: %w", err)
		}
	}
	return c.comment(ctx, rv, encode(Meta{Author: author, Re: t.ID}, text))
}

// comment posts a PR comment and returns its id.
func (c *Client) comment(ctx context.Context, rv *Review, body string) (string, error) {
	var r struct {
		NodeID string `json:"node_id"`
	}
	err := c.rest(ctx, "POST", fmt.Sprintf("/repos/%s/issues/%d/comments", rv.Ref.Repo(), rv.Ref.Number), map[string]any{"body": body}, &r)
	if err != nil {
		return "", fmt.Errorf("comment: %w", err)
	}
	return r.NodeID, nil
}

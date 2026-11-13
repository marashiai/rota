package review

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The reviewer's side: every note and reply starts as a draft in the reviewer's pending review,
// which only the reviewer sees, and Send submits them all as one review.

// ensurePending returns the viewer's pending review, starting one when there is none.
func (c *Client) ensurePending(ctx context.Context, rv *Review) (string, error) {
	if rv.pending != "" {
		return rv.pending, nil
	}
	var d struct {
		AddPullRequestReview struct {
			PullRequestReview struct {
				ID string `json:"id"`
			} `json:"pullRequestReview"`
		} `json:"addPullRequestReview"`
	}
	// GitHub will not edit a review body that started empty, so it starts as an empty marker.
	err := c.graphql(ctx, `mutation($pr:ID!,$b:String!){addPullRequestReview(input:{pullRequestId:$pr,body:$b}){pullRequestReview{id}}}`,
		map[string]any{"pr": rv.ID, "b": joinSections(nil)}, &d)
	if err != nil {
		return "", fmt.Errorf("start a review: %w", err)
	}
	rv.pending = d.AddPullRequestReview.PullRequestReview.ID
	return rv.pending, nil
}

// AddDraft adds a note to the viewer's drafts and returns its id.
func (c *Client) AddDraft(ctx context.Context, rv *Review, in NoteInput) (string, error) {
	a, err := c.resolve(ctx, rv, in)
	if err != nil {
		return "", err
	}
	review, err := c.ensurePending(ctx, rv)
	if err != nil {
		return "", err
	}
	body := encode(a.meta(), in.Text)
	if a.Path != "" && a.Line != 0 {
		pos, ok, err := c.position(ctx, rv, a)
		if err != nil {
			return "", err
		}
		if ok {
			// addPullRequestReviewComment is deprecated, but it is the one mutation that puts a
			// draft on a commit other than the head (addPullRequestReviewThread has no commit).
			var d struct {
				AddPullRequestReviewComment struct {
					Comment struct {
						ID string `json:"id"`
					} `json:"comment"`
				} `json:"addPullRequestReviewComment"`
			}
			err := c.graphql(ctx, `mutation($r:ID!,$c:GitObjectID!,$p:String!,$pos:Int!,$b:String!){
				addPullRequestReviewComment(input:{pullRequestReviewId:$r,commitOID:$c,path:$p,position:$pos,body:$b}){comment{id}}}`,
				map[string]any{"r": review, "c": a.on(rv), "p": a.Path, "pos": pos, "b": body}, &d)
			if err == nil {
				return d.AddPullRequestReviewComment.Comment.ID, nil
			}
			if !refused(err) {
				return "", err
			}
		}
	} else if a.Path != "" {
		var d struct {
			AddPullRequestReviewThread struct {
				Thread struct {
					Comments struct {
						Nodes []struct {
							ID string `json:"id"`
						} `json:"nodes"`
					} `json:"comments"`
				} `json:"thread"`
			} `json:"addPullRequestReviewThread"`
		}
		err := c.graphql(ctx, `mutation($r:ID!,$p:String!,$b:String!){
			addPullRequestReviewThread(input:{pullRequestReviewId:$r,path:$p,subjectType:FILE,body:$b}){thread{comments(first:1){nodes{id}}}}}`,
			map[string]any{"r": review, "p": a.Path, "b": body}, &d)
		if err == nil && len(d.AddPullRequestReviewThread.Thread.Comments.Nodes) > 0 {
			return d.AddPullRequestReviewThread.Thread.Comments.Nodes[0].ID, nil
		}
		if err != nil && !refused(err) {
			return "", err
		}
	}
	// the whole change, a whole commit, or a place GitHub refused: a section of the review's body
	return c.addSection(ctx, rv, section{Meta: a.meta(), Text: in.Text})
}

// refused says GitHub turned a request down, as opposed to the request not reaching it.
func refused(err error) bool {
	var e *APIError
	return errors.As(err, &e)
}

func (c *Client) addSection(ctx context.Context, rv *Review, s section) (string, error) {
	review, err := c.ensurePending(ctx, rv)
	if err != nil {
		return "", err
	}
	var id string
	err = c.changeBody(ctx, rv, func(now []section) ([]section, error) {
		id = fmt.Sprintf("%s/%d", review, len(now))
		return append(now, s), nil
	})
	return id, err
}

// changeBody changes the pending review's body as it is on GitHub now, not as it was read, so a
// draft saved meanwhile from another window is kept.
func (c *Client) changeBody(ctx context.Context, rv *Review, change func([]section) ([]section, error)) error {
	var d struct {
		Node *struct {
			Body  string `json:"body"`
			State string `json:"state"`
		} `json:"node"`
	}
	if err := c.graphql(ctx, `query($r:ID!){node(id:$r){... on PullRequestReview{body state}}}`, map[string]any{"r": rv.pending}, &d); err != nil {
		return fmt.Errorf("read the drafts: %w", err)
	}
	if d.Node == nil || d.Node.State != "PENDING" {
		return fmt.Errorf("your drafts on %s were sent or dropped elsewhere: reload", rv.PR)
	}
	sections, err := change(splitSections(d.Node.Body))
	if err != nil {
		return err
	}
	err = c.graphql(ctx, `mutation($r:ID!,$b:String!){updatePullRequestReview(input:{pullRequestReviewId:$r,body:$b}){clientMutationId}}`,
		map[string]any{"r": rv.pending, "b": joinSections(sections)}, nil)
	if err != nil {
		return fmt.Errorf("save the draft: %w", err)
	}
	rv.sections = sections
	return nil
}

// sameSection checks that section i is still the one read, before it is changed.
func sameSection(rv *Review, now []section, i int) error {
	if i >= len(now) || now[i] != rv.sections[i] {
		return fmt.Errorf("this draft on %s changed in another window: reload", rv.PR)
	}
	return nil
}

// DraftReply adds a reply to the thread holding note noteID to the viewer's drafts.
func (c *Client) DraftReply(ctx context.Context, rv *Review, noteID, text string) (string, error) {
	t, _ := rv.Thread(noteID)
	if t == nil {
		return "", fmt.Errorf("%s has no note %s", rv.PR, noteID)
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("a reply needs text")
	}
	review, err := c.ensurePending(ctx, rv)
	if err != nil {
		return "", err
	}
	if t.ghThread != "" {
		var d struct {
			AddPullRequestReviewThreadReply struct {
				Comment struct {
					ID string `json:"id"`
				} `json:"comment"`
			} `json:"addPullRequestReviewThreadReply"`
		}
		err := c.graphql(ctx, `mutation($r:ID!,$t:ID!,$b:String!){
			addPullRequestReviewThreadReply(input:{pullRequestReviewId:$r,pullRequestReviewThreadId:$t,body:$b}){comment{id}}}`,
			map[string]any{"r": review, "t": t.ghThread, "b": strings.TrimSpace(text)}, &d)
		if err != nil {
			return "", fmt.Errorf("save the reply: %w", err)
		}
		return d.AddPullRequestReviewThreadReply.Comment.ID, nil
	}
	return c.addSection(ctx, rv, section{Meta: Meta{Re: t.ID}, Text: text})
}

// EditDraft replaces the text of one of the viewer's drafts.
func (c *Client) EditDraft(ctx context.Context, rv *Review, noteID, text string) error {
	n, i, err := draft(rv, noteID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		return c.DeleteDraft(ctx, rv, noteID)
	}
	if n.kind == inSection {
		return c.changeBody(ctx, rv, func(now []section) ([]section, error) {
			if err := sameSection(rv, now, i); err != nil {
				return nil, err
			}
			now[i].Text = text
			return now, nil
		})
	}
	err = c.graphql(ctx, `mutation($id:ID!,$b:String!){updatePullRequestReviewComment(input:{pullRequestReviewCommentId:$id,body:$b}){clientMutationId}}`,
		map[string]any{"id": n.ID, "b": encode(n.meta, text)}, nil)
	if err != nil {
		return fmt.Errorf("save the draft: %w", err)
	}
	n.Text = strings.TrimSpace(text)
	return nil
}

// DeleteDraft removes one of the viewer's drafts. With the last one, the pending review goes
// too: while it exists, an agent sharing the viewer's account can write no review comment.
func (c *Client) DeleteDraft(ctx context.Context, rv *Review, noteID string) error {
	n, i, err := draft(rv, noteID)
	if err != nil {
		return err
	}
	last := rv.Drafts() == 1
	if n.kind == inSection {
		err = c.changeBody(ctx, rv, func(now []section) ([]section, error) {
			if err := sameSection(rv, now, i); err != nil {
				return nil, err
			}
			return append(now[:i], now[i+1:]...), nil
		})
	} else if err = c.graphql(ctx, `mutation($id:ID!){deletePullRequestReviewComment(input:{id:$id}){clientMutationId}}`, map[string]any{"id": n.ID}, nil); err != nil {
		err = fmt.Errorf("delete the draft: %w", err)
	}
	if err != nil || !last {
		return err
	}
	if err := c.graphql(ctx, `mutation($r:ID!){deletePullRequestReview(input:{pullRequestReviewId:$r}){clientMutationId}}`, map[string]any{"r": rv.pending}, nil); err == nil {
		rv.pending, rv.sections = "", nil
	}
	return nil
}

// draft finds one of the viewer's drafts, with its section's index when it is one.
func draft(rv *Review, noteID string) (*Note, int, error) {
	_, n := rv.Thread(noteID)
	if n == nil || !n.Draft {
		return nil, 0, fmt.Errorf("%s has no draft %s", rv.PR, noteID)
	}
	if n.kind != inSection {
		return n, 0, nil
	}
	i, err := strconv.Atoi(noteID[strings.LastIndex(noteID, "/")+1:])
	if err != nil || i < 0 || i >= len(rv.sections) {
		return nil, 0, fmt.Errorf("%s has no draft %s", rv.PR, noteID)
	}
	return n, i, nil
}

// Send submits the viewer's drafts as one review and returns how many notes it sent. With no
// drafts it sends nothing, and drops an empty pending review.
func (c *Client) Send(ctx context.Context, rv *Review) (int, error) {
	if rv.pending == "" {
		return 0, nil
	}
	n := rv.Drafts()
	if n == 0 {
		err := c.graphql(ctx, `mutation($r:ID!){deletePullRequestReview(input:{pullRequestReviewId:$r}){clientMutationId}}`, map[string]any{"r": rv.pending}, nil)
		rv.pending, rv.sections = "", nil
		return 0, err
	}
	if err := c.dropApproval(ctx, rv); err != nil {
		return 0, err
	}
	if err := c.submit(ctx, rv, "COMMENT"); err != nil {
		return 0, fmt.Errorf("send: %w", err)
	}
	rv.pending, rv.sections = "", nil
	rv.Approval = nil // the reviewer's review that does not approve takes an approval back
	return n, nil
}

// Approve sends the viewer's drafts, if any, in one review that approves the pull request, and
// returns how many notes it sent. The approval is a section of the review's body, since GitHub
// lets no one approve their own pull request; on someone else's, the review approves on GitHub
// too.
func (c *Client) Approve(ctx context.Context, rv *Review) (int, error) {
	if rv.State != "OPEN" {
		return 0, fmt.Errorf("%s is %s", rv.PR, strings.ToLower(rv.State))
	}
	n := rv.Drafts()
	if _, err := c.addSection(ctx, rv, section{Meta: Meta{Approval: true}, Text: "Approved."}); err != nil {
		return 0, err
	}
	event := "COMMENT"
	if rv.Author != rv.Viewer {
		event = "APPROVE"
	}
	err := c.submit(ctx, rv, event) // GitHub refusing APPROVE refuses the approval: no marker stands in for it
	if err != nil {
		c.changeBody(ctx, rv, func(now []section) ([]section, error) { return withoutApproval(now), nil }) // a later Send does not approve
		return 0, fmt.Errorf("approve: %w", err)
	}
	rv.pending, rv.sections = "", nil
	rv.Approval = &Approval{By: rv.Viewer, At: time.Now()}
	return n, nil
}

// dropApproval takes an approval out of the pending review as it is on GitHub now: one an Approve
// that failed left, or one another window is adding, would else go out with a plain Send.
func (c *Client) dropApproval(ctx context.Context, rv *Review) error {
	var d struct {
		Node *struct {
			Body string `json:"body"`
		} `json:"node"`
	}
	if err := c.graphql(ctx, `query($r:ID!){node(id:$r){... on PullRequestReview{body}}}`, map[string]any{"r": rv.pending}, &d); err != nil {
		return fmt.Errorf("read the drafts: %w", err)
	}
	if d.Node == nil || !slices.ContainsFunc(splitSections(d.Node.Body), func(s section) bool { return s.Meta.Approval }) {
		return nil
	}
	return c.changeBody(ctx, rv, func(now []section) ([]section, error) { return withoutApproval(now), nil })
}

func (c *Client) submit(ctx context.Context, rv *Review, event string) error {
	return c.graphql(ctx, `mutation($r:ID!,$e:PullRequestReviewEvent!){submitPullRequestReview(input:{pullRequestReviewId:$r,event:$e}){clientMutationId}}`,
		map[string]any{"r": rv.pending, "e": event}, nil)
}

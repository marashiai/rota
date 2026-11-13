package tui

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"rota/core"

	"rota/gloss/review"
	glosstui "rota/gloss/tui"
)

// reviewsPage lists the team's open pull requests in the order to review them, each indented
// under the one it waits for; ⏎ or v reviews one in gloss, which takes the
// terminal until it is quit. r and m narrow the list to a repository and to a mark, in turn, and
// L to the pull requests that carry every label ticked, on themselves or on an open issue they
// are for. What narrows it is kept until the human changes it, and the pages share it.
func reviewsPage() *browser {
	const name, empty = "Pull requests", "No open pull requests, as GitHub last listed them."
	var repo, mark core.Filter // what narrows the list; Value "" for all
	var labels []string
	var kept core.ListFilter // as last read or chosen
	var b *browser
	narrowed := func(b *browser) { // the list again from the top, its name saying what narrows it
		b.name, b.empty, b.loaded, b.sel, b.focus = name, empty, time.Time{}, 0, false
		var by []string
		for _, s := range append([]string{repo.Label, mark.Value}, labels...) { // a mark by its emoji: the count stays in view
			if s != "" {
				by = append(by, s)
			}
		}
		if len(by) > 0 {
			b.name += " · " + strings.Join(by, " · ")
			b.empty = "No open pull request matches " + strings.Join(by, " and ") + "; r, m and L change what narrows the list."
		}
	}
	// sync takes what narrows the list as last chosen, here or in the pages
	sync := func(c *console) {
		f := c.t.ListFilter(core.ReviewsTab)
		if reflect.DeepEqual(f, kept) {
			return
		}
		kept = f
		repos, marks := core.Filters(c.t.ReviewQueue().PRs)
		find := func(fs []core.Filter, v string) core.Filter {
			if v == "" {
				return core.Filter{}
			}
			if i := slices.IndexFunc(fs, func(x core.Filter) bool { return x.Value == v }); i >= 0 {
				return fs[i]
			}
			return core.Filter{Value: v, Label: v} // chosen, though no pull request has it any more
		}
		repo, mark, labels = find(repos, f.Repo), find(marks, f.Mark), f.Labels
		narrowed(b)
	}
	// keep has the choice kept, for the pages and the next console
	keep := func(c *console) {
		kept = core.ListFilter{Repo: repo.Value, Mark: mark.Value, Labels: labels}.Clean()
		if err := c.t.SetListFilter(core.ReviewsTab, kept); err != nil {
			c.say("what narrows the list is not kept: "+err.Error(), true)
		}
	}
	b = &browser{name: name, empty: empty,
		items: func(c *console) []item {
			sync(c)
			var out []item
			for _, pr := range c.t.ReviewQueue().Only(repo.Value, mark.Value, labels...).PRs {
				i := strings.LastIndex(pr.Name, "#")
				label := pr.Name[:i] + sty(c.pal.Muted(), pr.Name[i:]) + " " + pr.Title
				for j := len(pr.Marks) - 1; j >= 0; j-- {
					label = pr.Marks[j].Emoji + " " + label
				}
				if pr.Depth > 0 {
					label = strings.Repeat("  ", pr.Depth-1) + sty(c.pal.Muted(), "└ ") + label
				}
				out = append(out, item{pr.Ref(), label})
			}
			return out
		},
		title: func(c *console, id string) string { return id + sty(c.pal.Muted(), "  ⏎ or v reviews it") },
		doc: func(c *console, id string, w int) doc {
			q := c.t.ReviewQueue()
			var pr core.OpenPR
			for _, p := range q.PRs {
				if p.Ref() == id {
					pr = p
				}
			}
			text := "# " + pr.Title + "\n\n"
			for _, m := range pr.Marks {
				text += m.Emoji + " " + m.Text + "\n\n"
			}
			if pr.Why != "" {
				text += "**Why here:** " + pr.Why + "\n\n"
			}
			if len(pr.Waits) > 0 {
				text += "**Waits for:** " + strings.Join(pr.Waits, ", ") + "\n\n"
			}
			text += core.QueueText(q) + "\n\n**⏎** or **v** reviews it commit by commit, with notes beside the code.\n\n" +
				"Your notes stay drafts on GitHub, seen only by you, until you send them (**S** in the review); " +
				"then the agent that wrote the pull request is told, and answers there."
			return doc{key: id + strconv.Itoa(w) + c.pal.name + text, build: func() ([]string, []int) { return c.pal.markdown(text, w) }}
		},
		own: func(c *console, b *browser, k key) bool {
			if k.r == 'r' || k.r == 'm' || k.r == 'L' {
				sync(c) // a choice made in the pages since the list was read: change that one
			}
			if k.r == 'r' || k.r == 'm' {
				repos, marks := core.Filters(c.t.ReviewQueue().PRs)
				if k.r == 'r' {
					repo = nextFilter(repos, repo)
				} else {
					mark = nextFilter(marks, mark)
				}
				keep(c)
				narrowed(b)
				return true
			}
			if k.r == 'L' { // each label with how many of the list, by repository and state, carry it
				var values, names []string
				for _, f := range core.LabelFilters(c.t.ReviewQueue().Only(repo.Value, mark.Value).PRs, nil) {
					values, names = append(values, f.Value), append(names, f.Label+" ("+strconv.Itoa(f.N)+")")
				}
				ticked := slices.Clone(labels) // as the list spells them: a label's case may differ between repositories
				for i, l := range ticked {
					if j := slices.IndexFunc(values, func(v string) bool { return strings.EqualFold(v, l) }); j >= 0 {
						ticked[i] = values[j]
					} else { // ticked, though none of them carries it any more
						values, names = append(values, l), append(names, l+" (0)")
					}
				}
				if len(values) == 0 {
					c.say("None of these pull requests or their open issues has a label.", false)
					return true
				}
				c.pickSome("Show those with every label ticked", values, names, ticked, func(on []string) {
					sync(c) // what the pages chose while the picker was open stays
					labels = on
					keep(c)
					narrowed(b)
				})
				return true
			}
			if len(b.list) == 0 || !(k.r == 'v' || k.name == "enter" && !b.focus) {
				return false
			}
			c.review(b.list[b.sel].id)
			return true
		},
		ownHints: []hint{{"⏎ v", "review"}, {"r", "repository"}, {"m", "state"}, {"L", "labels"}},
	}
	return b
}

// nextFilter is the choice after cur, and after the last none: the whole list.
func nextFilter(choices []core.Filter, cur core.Filter) core.Filter {
	i := slices.IndexFunc(choices, func(f core.Filter) bool { return f.Value == cur.Value })
	if cur.Value == "" {
		i = -1
	}
	if i+1 < len(choices) {
		return choices[i+1]
	}
	return core.Filter{}
}

// review shows a pull request in gloss with the console out of the way; a send tells the agent
// that wrote it. Its notes as gloss read them when it opened, the human has seen.
func (c *console) review(ref string) {
	r, err := review.ParseRef(ref, "")
	if err != nil {
		c.sayErr(err)
		return
	}
	client, err := core.ReviewClient()
	if err != nil {
		c.sayErr(err)
		return
	}
	opened := time.Now()
	err = c.suspend(func() error {
		return glosstui.Run(c.ctx, client, r, glosstui.Options{OnSend: func(ctx context.Context, rv *review.Review, sent int) error {
			t, err := core.Open(c.root, c.conf)
			if err != nil {
				return err
			}
			approved, err := core.Approved(ctx, client, rv)
			return errors.Join(err, t.ReviewSent(ctx, rv.Ref.Repo(), rv.Ref.Number, sent, approved))
		}})
	})
	if err != nil {
		c.sayErr(err)
	} else {
		c.t.SeenReview(r.Repo(), r.Number, opened)
	}
	c.reread()
}

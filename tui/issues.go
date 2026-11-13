package tui

import (
	"os/exec"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"rota/core"
)

// issuesPage lists the team's open issues, newest first unless o chose another order, each with
// where it stands; ⏎ or v opens one on GitHub. r and m narrow the list to a repository and to a
// mark, in turn, and L to the issues that carry every label ticked. What narrows and orders it is
// kept until the human changes it, and the pages share it.
func issuesPage() *browser {
	const name, empty = "Issues", "No open issues, as GitHub last listed them."
	var repo, mark, order core.Filter // what narrows and orders the list; Value "" for all, and newest first
	var labels []string
	var kept core.ListFilter // as last read or chosen
	var b *browser
	narrowed := func(b *browser) { // the list again from the top, its name saying what narrows it
		b.name, b.empty, b.loaded, b.sel, b.focus = name, empty, time.Time{}, 0, false
		var by []string
		for _, s := range append([]string{repo.Label, mark.Value}, labels...) {
			if s != "" {
				by = append(by, s)
			}
		}
		if order.Value != core.OrderNewest {
			b.name += " · " + strings.ToLower(order.Label)
		}
		if len(by) > 0 {
			b.name += " · " + strings.Join(by, " · ")
			b.empty = "No open issue matches " + strings.Join(by, " and ") + "; r, m and L change what narrows the list."
		}
	}
	find := func(fs []core.Filter, v string) core.Filter {
		if i := slices.IndexFunc(fs, func(x core.Filter) bool { return x.Value == v }); i >= 0 {
			return fs[i]
		}
		if v == "" {
			return core.Filter{}
		}
		return core.Filter{Value: v, Label: v} // chosen, though no issue has it any more
	}
	// sync takes what narrows the list as last chosen, here or in the pages
	sync := func(c *console) {
		f := c.t.ListFilter(core.IssuesTab)
		if reflect.DeepEqual(f, kept) {
			return
		}
		kept = f
		repos, marks := core.Filters(c.t.Issues(f.Order))
		repo, mark, order, labels = find(repos, f.Repo), find(marks, f.Mark), find(core.IssueOrders, f.Order), f.Labels
		narrowed(b)
	}
	keep := func(c *console) {
		kept = core.ListFilter{Repo: repo.Value, Mark: mark.Value, Labels: labels, Order: order.Value}.Clean()
		if err := c.t.SetListFilter(core.IssuesTab, kept); err != nil {
			c.say("what narrows the list is not kept: "+err.Error(), true)
		}
	}
	issue := func(c *console, ref string) (core.OpenIssue, bool) {
		for _, i := range c.t.Issues(order.Value) {
			if i.Ref() == ref {
				return i, true
			}
		}
		return core.OpenIssue{}, false
	}
	b = &browser{name: name, empty: empty,
		items: func(c *console) []item {
			sync(c)
			var out []item
			for _, i := range core.OnlyIssues(c.t.Issues(order.Value), repo.Value, mark.Value, labels...) {
				at := strings.LastIndex(i.Name, "#")
				label := i.Name[:at] + sty(c.pal.Muted(), i.Name[at:]) + " " + i.Title
				for j := len(i.Marks) - 1; j >= 0; j-- {
					label = i.Marks[j].Emoji + " " + label
				}
				out = append(out, item{i.Ref(), label})
			}
			return out
		},
		title: func(c *console, id string) string { return id + sty(c.pal.Muted(), "  ⏎ or v opens it on GitHub") },
		doc: func(c *console, id string, w int) doc {
			i, _ := issue(c, id)
			text := "# " + i.Title + "\n\n"
			for _, m := range i.Marks {
				text += m.Emoji + " " + m.Text + "\n\n"
			}
			if !i.Created.IsZero() {
				text += "**Opened:** " + i.Created.Local().Format("Mon 2 Jan 2006, 15:04") + "\n\n"
			}
			if len(i.Labels) > 0 {
				text += "**Labels:** " + strings.Join(i.Labels, ", ") + "\n\n"
			}
			if len(i.PRs) > 0 {
				text += "**Pull requests for it:** " + strings.Join(i.PRs, ", ") + " (the Reviews tab has them)\n\n"
			}
			text += i.URL() + "\n\n**⏎** or **v** opens it on GitHub."
			return doc{key: id + strconv.Itoa(w) + c.pal.name + text, build: func() ([]string, []int) { return c.pal.markdown(text, w) }}
		},
		own: func(c *console, b *browser, k key) bool {
			if k.r == 'r' || k.r == 'm' || k.r == 'L' || k.r == 'o' {
				sync(c) // a choice made in the pages since the list was read: change that one
			}
			switch k.r {
			case 'r', 'm':
				repos, marks := core.Filters(c.t.Issues(order.Value))
				if k.r == 'r' {
					repo = nextFilter(repos, repo)
				} else {
					mark = nextFilter(marks, mark)
				}
				keep(c)
				narrowed(b)
				return true
			case 'o':
				i := slices.IndexFunc(core.IssueOrders, func(f core.Filter) bool { return f.Value == order.Value })
				order = core.IssueOrders[(i+1)%len(core.IssueOrders)]
				keep(c)
				narrowed(b)
				c.say("issues: "+strings.ToLower(order.Label), false)
				return true
			case 'L':
				var values, names []string
				for _, f := range core.LabelFilters(core.OnlyIssues(c.t.Issues(order.Value), repo.Value, mark.Value), nil) {
					values, names = append(values, f.Value), append(names, f.Label+" ("+strconv.Itoa(f.N)+")")
				}
				ticked := slices.Clone(labels) // as the list spells them: a label's case may differ between repositories
				for i, l := range ticked {
					if j := slices.IndexFunc(values, func(v string) bool { return strings.EqualFold(v, l) }); j >= 0 {
						ticked[i] = values[j]
					} else {
						values, names = append(values, l), append(names, l+" (0)")
					}
				}
				if len(values) == 0 {
					c.say("None of these issues has a label.", false)
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
			if i, ok := issue(c, b.list[b.sel].id); ok {
				openURL(c, i.URL())
			}
			return true
		},
		ownHints: []hint{{"⏎ v", "open"}, {"r", "repository"}, {"m", "state"}, {"L", "labels"}, {"o", "order"}},
	}
	return b
}

// openURL opens an address in this machine's browser.
func openURL(c *console, url string) {
	if err := startBrowser(url); err != nil {
		c.say(err.Error(), true)
		return
	}
	c.say("opened "+url, false)
}

// startBrowser opens url in this machine's browser; tests open none.
var startBrowser = func(url string) error {
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	cmd := exec.Command(opener, url)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // the launcher ends at once: reaped
	return nil
}

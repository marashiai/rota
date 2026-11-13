package core

import (
	"fmt"
	"strings"
)

// Every percent the front ends show is of the team pool: the part of Claude's weekly window the
// human gave the agents (team_pct), or a day's seventh of it with pacing daily, so a share of the
// pool reads as the agent's part of what the team may use. spend.csv and the schedule keep percents
// of the weekly window, as Claude reports them; Claude's own readings of the account are shown as
// Claude reports them.

// PoolPct is the team pool of one pacing period, in percent of the weekly window.
func (c *Config) PoolPct() float64 {
	if c.Pacing == "weekly" {
		return c.TeamPct
	}
	return c.TeamPct / 7
}

// OfPool is pct of the weekly window as a percent of the period's pool; 0 without a pool.
func (c *Config) OfPool(pct float64) float64 { return ofPool(pct, c.PoolPct()) }

// OfWeekPool is pct of the weekly window as a percent of the week's pool; 0 without a pool.
func (c *Config) OfWeekPool(pct float64) float64 { return ofPool(pct, c.TeamPct) }

// Of is pct of the weekly window as a percent of the period's pool.
func (p Pool) Of(pct float64) float64 { return ofPool(pct, p.Quota) }

// OfWeek is pct of the weekly window as a percent of the week's pool.
func (p Pool) OfWeek(pct float64) float64 { return ofPool(pct, p.TeamPct) }

func ofPool(pct, pool float64) float64 {
	if pool <= 0 {
		return 0
	}
	return 100 * pct / pool
}

// SpendHeading is a spend.csv column's heading, as the front ends show it: what a shift used is
// a percent of the pool, and the account's readings after it are Claude's.
func SpendHeading(col string) string {
	switch col {
	case "weekly_pct_used":
		return "% of the pool"
	case "weekly_pct_after":
		return "account weekly % after"
	case "session_pct_after":
		return "account session % after"
	}
	return strings.ReplaceAll(col, "_", " ")
}

// Cell is column i of the shift's spend.csv row, v, as the front ends show it: what it used as a
// percent of the pool.
func (r Run) Cell(c *Config, i int, v string) string {
	if SpendColumns[i] == "weekly_pct_used" && r.HasPct {
		return fmt.Sprintf("%.1f", c.OfPool(r.PctUsed))
	}
	return v
}

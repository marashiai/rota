package core

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The Team looks at what is in rota's scope itself. The team folder it reads with Claude's file
// tools, its working directory; the team's work on GitHub with the tools rota serves it over MCP,
// from a process of its own (rota team-tools) that the Team's Claude starts. Each tool checks
// that what it reads is within the scope before it runs: what changes anything the Team proposes,
// for the human to apply.

// githubIn is what the github tool is asked: a gh command.
type githubIn struct {
	Args []string `json:"args" jsonschema:"gh's arguments after gh, each a word of its own, such as [\"pr\", \"view\", \"671\", \"--repo\", \"owner/name\", \"--json\", \"state,labels,mergedAt\"]"`
}

// toolOutMax is the most a tool gives back of what gh printed.
const toolOutMax = 60000

// teamToolsMCP is the MCP configuration that starts the Team's tools: this program, as rota
// team-tools on the team.
func (t *Team) teamToolsMCP() (string, error) {
	exe, err := selfExe()
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"rota": map[string]any{
		"command": exe, "args": []string{"team-tools", "-root", t.Root, "-config", t.File}}}})
	return string(b), err
}

// teamToolNames are the tools the Team may use without asking anyone: they only read.
var teamToolNames = []string{"mcp__rota__github"}

// TeamTools serves the Team's tools on stdin and stdout until its Claude closes them.
func (t *Team) TeamTools(ctx context.Context) error {
	return t.serveTeamTools(ctx, &mcp.StdioTransport{})
}

func (t *Team) serveTeamTools(ctx context.Context, over mcp.Transport) error {
	s := mcp.NewServer(&mcp.Implementation{Name: "rota", Version: "1"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "github", Description: "Reads the team's work on GitHub, live, with gh: the pull requests, issues, releases, " +
		"workflow runs and labels of the team's repositories (" + strings.Join(t.teamRepos(), ", ") + "), as gh pr view, list, diff, checks; " +
		"gh issue view, list; gh release, run and workflow view, list; gh label list; gh search prs, issues, commits with --repo; " +
		"and gh api GET under repos/<owner>/<name>/ for their work. Name the repository with --repo; give flags in their long form. " +
		"It reads only: a change is a proposal for the human."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in githubIn) (*mcp.CallToolResult, any, error) {
			text, failed := t.githubRead(ctx, in.Args)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: failed}, nil, nil
		})
	return s.Run(ctx, over)
}

// teamGHEnv is the environment of the gh the Team runs: what gh needs to reach github.com as the
// human (their home, with gh's login, and the path), and nothing that prompts, pages, opens, tells
// GitHub's analytics or caches outside the team folder; no other variable of this machine, such as
// a token, so that none is there for the Team to read.
func (t *Team) teamGHEnv() []string {
	env := []string{"GH_HOST=github.com", "GH_PAGER=", "PAGER=", "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1",
		"GH_NO_EXTENSION_UPDATE_NOTIFIER=1", "GH_SPINNER_DISABLED=1", "GH_TELEMETRY=false", "NO_COLOR=1", "XDG_CACHE_HOME=" + t.Path(".cache")}
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); slices.Contains([]string{"HOME", "PATH", "USER", "LOGNAME", "TMPDIR", "LANG", "GH_CONFIG_DIR", "XDG_CONFIG_HOME"}, k) {
			env = append(env, kv)
		}
	}
	return env
}

// githubRead runs a gh command that reads the team's work, and gives what it printed, or why not.
func (t *Team) githubRead(ctx context.Context, args []string) (string, bool) {
	args, err := t.ghScope(args, false)
	if err != nil {
		return err.Error(), true
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var out string
	if err := t.ghWith(ctx, t.teamGHEnv(), &out, args...); err != nil {
		return err.Error(), true
	}
	if len(out) > toolOutMax {
		out = out[:toolOutMax] + "\n… (cut here: ask for less, with --json and its fields, or --limit)"
	}
	return or(strings.TrimRight(out, "\n"), "(gh printed nothing)"), false
}

// githubRun runs a gh command that changes the team's work, as the human applied it, and gives
// what gh printed.
func (t *Team) githubRun(args []string) (string, error) {
	args, err := t.ghScope(args, true)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second) // within the pages' write deadline
	defer cancel()
	var out string
	if err := t.ghWith(ctx, t.teamGHEnv(), &out, args...); err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

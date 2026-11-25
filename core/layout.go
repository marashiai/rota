package core

import (
	"regexp"
	"time"
)

// The team folder's layout and the fixed parts of a shift. They are not settings: nobody
// running a team expects to move them.
const (
	agentsDir    = "agents"
	secretsDir   = "secrets"
	commonEnv    = "common.env" // in secrets/, put in every agent's environment
	workDir      = "work"       // work/<agent>
	reposDir     = ".repos"     // .repos/<owner>/<name>: rota's own clone, where it rebases pull requests stacked on a merged one
	memoryDir    = "memory"     // memory/<agent>/: what its Claude Code remembers from shift to shift, kept here and not in ~/.claude
	journalDir   = "journal"
	inboxDir     = "inbox"
	reportsDir   = "reports"        // reports/<date>.md: what each shift says the human should know, newest first; reports/<agent>/: what the agent wrote for the human
	evidenceDir  = "evidence"       // evidence/<repo>-<branch>/: screenshots, test output and reviews a pull request points to
	questionsDir = "questions"      // questions/<id>.json: what agents asked the human, while open
	closedDir    = "closed"         // questions/closed/<id>.json: answered, dismissed or withdrawn
	sentDir      = "sent"           // sent/<id>.json: a message the human sent an agent, and its answers
	chatFile     = "chat.jsonl"     // the team chat: the human, the Team and the agents at work, a line each
	mailFile     = ".mail.json"     // the human's marks on their mail: read, starred, snoozed, archived, moved; and how they like it
	resumeDir    = ".resume"        // .resume/<agent>.json: an interrupted shift's session, resumed by its next shift
	steerDir     = ".steer"         // .steer/<agent>: while its shift runs and takes notes, the organizer's pid
	talkDir      = ".talk"          // .talk/<agent>.json: its running shift's talk with the human in the chat
	heldDir      = ".paused-agents" // .paused-agents/<agent>: the human paused it, until resumed
	diskDir      = ".disk"          // .disk/<agent>.json: the size of its work folder, as measured after its last shift
	openFile     = ".open.json"     // the team's open pull requests and issues, as GitHub last listed them
	queueFile    = ".queue.json"    // the order in which the human reviews the open pull requests, as an agent gave it last
	planFile     = ".plan.json"     // the order in which the team works on the open issues, as an agent gave it last
	seenFile     = ".seen.json"     // when the human last opened each pull request's review, by owner/name#n
	spendFile    = "spend.csv"
	lockFile     = ".rota.lock"  // one shift at a time
	briefFile    = "brief.md"    // the shift brief; without it, the built-in one
	logDir       = "logs"        // logs/<date>/<HHMM>-<agent>.jsonl: claude's event stream
	stateFile    = ".state.json" // the organizer's snapshot
	hostsFile    = "hosts.json"  // paired hosts: name, token hash, last seen
	pairFile     = ".pair-code"
	workerFile   = ".worker.json" // this machine's token when it is a host

	journalDays     = 14    // days of journal listed in a shift's prompt
	journalMaxChars = 20000 // journal text inlined for an agent on another host
	journalHeader   = "## {time} {agent} on {host} | {pct} of the team pool | {turns} turns | {minutes} min | {status} | log {log}"
	commitMessage   = "{agent}: shift {date} {time}"
	PairMinutes     = 10 // how long a pairing code stays valid
	usageRetries    = 3  // on HTTP 429 or 5xx: wait 5s, 15s, 45s
	usageReuse      = 20 // seconds a usage reading is reused before a shift
	holdMinutes     = 5  // how soon the organizer looks again while Claude's usage cannot be read
	usageTokenEnv   = "CLAUDE_CODE_OAUTH_TOKEN"
	usageCredFile   = "~/.claude/.credentials.json"
	usageKeychain   = "Claude Code-credentials" // macOS
)

// Timeouts and addresses that tests shorten or replace.
var (
	usageURL      = "https://api.anthropic.com/api/oauth/usage"
	killGrace     = 5 * time.Second  // between SIGTERM and SIGKILL when a shift is stopped
	probeTimeout  = 10 * time.Second // keychain, gh, claude auth status, git, the login shell
	syncTimeout   = 10 * time.Minute // cloning and pulling an agent's repositories
	diskTimeout   = 3 * time.Minute  // measuring an agent's work folder after its shift
	triageTimeout = 3 * time.Minute  // one judgment of a failed shift
	reportTimeout = 3 * time.Minute  // one rewrite of the human's report
	waitSlice     = 2 * time.Second  // the organizer's wait between looks, checked this often: a setting saved applies within it
	usageStale    = time.Hour        // the organizer reads usage at least this often, for the status
	usageFloor    = time.Minute      // Claude's usage endpoint is never asked more often than this
)

// noteLine is a teammate addressed in a shift's final message: "@qa: the tests are flaky", or
// about one pull request or issue, "@developer shop-backend#72: not ready: no review".
// Groups: the agent, the repository and number if any, the message.
// withdrawLine withdraws one of the agent's questions: "@human withdraw <id>".
var withdrawLine = regexp.MustCompile(`(?mi)^\s*(?:[-*]\s*)?@human\s+withdraw:?\s+(\S+)\s*$`)

// replyLine answers the human's message <id> in a final message: "@human re <id>: <answer>".
var replyLine = regexp.MustCompile(`(?mi)^\s*(?:[-*]\s*)?@human\s+re\s+([A-Za-z0-9_-]+):\s*(.+)$`)

var noteLine = regexp.MustCompile(`(?m)^\s*(?:[-*]\s*)?@([A-Za-z0-9_-]+)(?:\s+([A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)?)#(\d+))?:\s*(.+)$`)

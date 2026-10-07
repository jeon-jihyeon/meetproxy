package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/triage"
)

// Set by ldflags in release builds
var version = "dev"

// The command set the plugin mod speaks
// Raised whenever a command the mod calls changes so an older mod stops instead of misreading
const protocol = 9

const (
	exitFailed = 1
	exitUsage  = 2
	// Results locate and inbox list print without --limit
	defaultLimit = 10
	// Width of the command column in the usage text
	usageColumn = 32
)

// Wrapped by any error that a corrected command line fixes so it exits with exitUsage
var errUsage = errors.New("usage error")

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage())
		os.Exit(exitUsage)
	}
	cmd, args := os.Args[1], os.Args[2:]
	if cmd == "hook" {
		// Exit 0 so a broken hook never blocks the session
		if err := runHook(os.Getenv("CLAUDE_PLUGIN_DATA"), args, time.Now(), os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "meetproxy hook:", err)
		}
		return
	}
	code, err := run(cmd, args, os.Getenv("CLAUDE_CODE_SESSION_ID"), time.Now(), os.Stdin, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "meetproxy:", err)
	}
	os.Exit(code)
}

// A command line of the CLI
type command struct {
	// Words that name it such as inbox add
	name string
	// Positional arguments as the usage text shows them
	args string
	// Least and most positional arguments
	// A negative most takes any number
	least, most int
	// Flags it reads besides --data and --root
	flags []string
	// Fails without a session id
	session bool
	// Runs without a data directory
	noData bool
	help   string
	run    func(c cli, args []string, f flags) (int, error)
}

type family struct {
	name     string
	commands []command
}

// The one table usage, arity, flag and session checks come from
var families = []family{
	{"setup", []command{
		{name: "version", noData: true, help: "print the binary version", run: printVersion},
		{name: "protocol", noData: true, help: "print the command set the plugin mod expects", run: printProtocol},
		{name: "tick", help: "print the protocol, the waiting requests and Slack as JSON", run: func(c cli, _ []string, _ flags) (int, error) { return exitCode(c.tick()) }},
		{name: "pause", help: "stop queueing and taking requests", run: func(c cli, _ []string, _ flags) (int, error) { return exitCode(c.pause(true)) }},
		{name: "resume", help: "take requests again", run: func(c cli, _ []string, _ flags) (int, error) { return exitCode(c.pause(false)) }},
		{name: "status", help: "print the state of every source, the inbox, Slack and the hooks as JSON", run: func(c cli, _ []string, _ flags) (int, error) { return exitCode(c.status()) }},
		{name: "tidy", help: "expire old requests and prune relays, the map and markers", run: func(c cli, _ []string, _ flags) (int, error) { return exitCode(c.tidy(false)) }},
		{name: "tidy daily", help: "tidy only once a day", run: func(c cli, _ []string, _ flags) (int, error) { return exitCode(c.tidy(true)) }},
	}},
	{"sources", []command{
		{
			name: "lease hold", args: "<source>", least: 1, most: 1, flags: []string{"session", "ttl"}, session: true,
			help: "hold or renew the lease of a source, exit 1 while another session holds it",
			run:  func(c cli, a []string, f flags) (int, error) { return c.leaseHold(a[0], f.ttl) },
		},
		{
			name: "lease drop", args: "<source>", least: 1, most: 1, flags: []string{"session"}, session: true,
			help: "give up the lease of a source",
			run:  func(c cli, a []string, _ flags) (int, error) { return exitCode(c.leaseDrop(a[0])) },
		},
		{
			name: "health", args: "<source>", least: 1, most: 1, help: "record what a check of a source found, {ok, error, found} on stdin",
			run: func(c cli, a []string, _ flags) (int, error) { return exitCode(c.recordHealth(a[0])) },
		},
	}},
	{"slack", []command{
		{
			name: "slack token", help: "store the user token on stdin once Slack accepts it, print user, team, host and missing scopes",
			run: func(c cli, _ []string, _ flags) (int, error) { return exitCode(c.slackToken()) },
		},
		{name: "slack whoami", help: "check the token with Slack", run: func(c cli, _ []string, _ flags) (int, error) { return exitCode(c.slackWhoami()) }},
		{
			name: "slack mentions", flags: []string{"after"}, help: "print messages that mention the user after --after oldest first",
			run: func(c cli, _ []string, f flags) (int, error) { return exitCode(c.slackMentions(f.after)) },
		},
		{
			name: "slack history", args: "<channel>", least: 1, most: 1, flags: []string{"oldest"}, help: "print the messages of a channel after --oldest",
			run: func(c cli, a []string, f flags) (int, error) { return exitCode(c.slackHistory(a[0], f.oldest)) },
		},
		{
			name: "slack covered", args: "<link>", least: 1, most: 1, flags: []string{"ts"},
			help: "print whether the user or meetproxy replied in the thread after --ts",
			run:  func(c cli, a []string, f flags) (int, error) { return exitCode(c.slackCovered(a[0], f.ts)) },
		},
		{
			name: "slack replies", args: "<link>", least: 1, most: 1, flags: []string{"after"},
			help: "print the replies in the thread of a link after --after that others wrote",
			run:  func(c cli, a []string, f flags) (int, error) { return exitCode(c.slackReplies(a[0], f.after)) },
		},
		{
			name: "slack dms", flags: []string{"after"}, help: "print direct messages to the user after --after that do not mention them",
			run: func(c cli, _ []string, f flags) (int, error) { return exitCode(c.slackDMs(f.after)) },
		},
		{
			name: "slack react", args: "<link>", least: 1, most: 1, flags: []string{"react"}, help: "add the reaction --react to the message of a link",
			run: func(c cli, a []string, f flags) (int, error) { return exitCode(c.slackReact(a[0], f.react)) },
		},
		{
			name: "slack update", args: "<reply link>", least: 1, most: 1, help: "replace a reply of the ledger with the text on stdin",
			run: func(c cli, a []string, _ flags) (int, error) { return exitCode(c.slackEdit(a[0], true)) },
		},
		{
			name: "slack delete", args: "<reply link>", least: 1, most: 1, help: "delete a reply of the ledger",
			run: func(c cli, a []string, _ flags) (int, error) { return exitCode(c.slackEdit(a[0], false)) },
		},
		{
			name: "slack read", args: "<link>", least: 1, most: 1, flags: []string{"limit"}, help: "print the text of the thread a link points at",
			run: func(c cli, a []string, f flags) (int, error) { return exitCode(c.slackRead(a[0], f.limit)) },
		},
		{
			name: "slack post", args: "<link>", least: 1, most: 1, flags: []string{"session"},
			help: "post the text on stdin in the thread of a link after the can-post check and print its permalink",
			run:  func(c cli, a []string, _ flags) (int, error) { return c.slackPost(a[0]) },
		},
		{name: "slack manifest", help: "print the app manifest and the link that creates the app", run: func(c cli, _ []string, _ flags) (int, error) { return exitCode(c.slackManifest()) }},
		{
			name: "slack setup", flags: []string{"answer"}, help: "record the answer to setting up a token, now, later or keep",
			run: func(c cli, _ []string, f flags) (int, error) { return exitCode(c.slackSetup(f.answer)) },
		},
	}},
	{"relay", []command{
		{
			name: "open", args: "<origin>", least: 1, most: 1, flags: []string{"target", "session"}, session: true,
			help: "open a relay, posts may go to the origin and the target",
			run:  func(c cli, a []string, f flags) (int, error) { return exitCode(c.open(a[0], f.target)) },
		},
		{
			name: "close", flags: []string{"topic", "keywords", "paths", "session"}, session: true,
			help: "close the relay and record evidence paths, the observed ones without --paths",
			run: func(c cli, _ []string, f flags) (int, error) {
				return exitCode(c.close(f.topic, split(f.keywords), split(f.paths)))
			},
		},
		{
			name: "can-post", args: "<link>", least: 1, most: 1, flags: []string{"session"},
			help: "check a post against the open relay of this session and the allow list",
			run:  func(c cli, a []string, _ flags) (int, error) { return c.canPost(a[0]) },
		},
		{
			name: "locate", args: "<term...>", least: 1, most: -1, flags: []string{"limit"}, help: "look up location map candidates",
			run: func(c cli, a []string, f flags) (int, error) { return exitCode(c.locate(a, f.limit)) },
		},
	}},
	{"destinations", []command{
		{
			name: "dest", args: "<location>", least: 1, most: 1, help: "check whether a destination is allowed",
			run: func(c cli, a []string, _ flags) (int, error) { return c.dest(a[0]) },
		},
		{
			name: "allow", args: "<pattern>", least: 1, most: 1, help: "allow a destination such as github:owner/*",
			run: allow,
		},
		{name: "allowed", help: "list the allowed patterns", run: func(c cli, _ []string, _ flags) (int, error) { return exitCode(c.allowed()) }},
	}},
	{"inbox", []command{
		{
			name: "inbox add", args: "<link>", least: 1, most: 1,
			flags: []string{
				"thread", "source", "from", "author", "channel", "summary", "reason", "ts", "key", "delegation", "target", "self",
				"depth", "digest", "followup", "correction",
			},
			help: "queue a request, one per --thread", run: addRequest,
		},
		{
			name: "inbox take", args: "<id>", least: 1, most: 1, flags: []string{"session"}, session: true,
			help: "take a request and print link, task, target, approve, depth and flags",
			run:  func(c cli, a []string, _ flags) (int, error) { return exitCode(c.inboxTake(a[0])) },
		},
		{
			name: "inbox hold", args: "<id>", least: 1, most: 1, flags: []string{"session", "until"}, session: true,
			help: "put a request off until the user takes it or until --until, a time, a duration or tomorrow",
			run:  func(c cli, a []string, f flags) (int, error) { return exitCode(c.hold(a[0], f.until)) },
		},
		{
			name: "inbox question", args: "<id>", least: 1, most: 1, flags: []string{"session"}, session: true,
			help: "wait for the requester to answer a question asked back",
			run:  func(c cli, a []string, _ flags) (int, error) { return exitCode(c.question(a[0])) },
		},
		{
			name: "inbox ignore", args: "<link>", least: 1, most: 1, flags: []string{"from", "delegation", "reason", "ts", "key"},
			help: "record a message that was not queued and move the cursor past --ts",
			run:  func(c cli, a []string, f flags) (int, error) { return exitCode(c.inboxIgnore(a[0], f)) },
		},
		{
			name: "inbox acked", args: "<id>", least: 1, most: 1, flags: []string{"react"},
			help: "record that --react eyes or done was added to the request link",
			run:  func(c cli, a []string, f flags) (int, error) { return exitCode(c.inboxAcked(a[0], f.react)) },
		},
		{
			name: "inbox done", args: "<id>", least: 1, most: 1, flags: []string{"session"}, session: true,
			help: "drop a request without an answer",
			run:  func(c cli, a []string, _ flags) (int, error) { return exitCode(c.done(a[0])) },
		},
		{
			name: "inbox list", flags: []string{"limit"}, help: "list the requests not done, newest first",
			run: func(c cli, _ []string, f flags) (int, error) { return exitCode(c.inboxList(f.limit)) },
		},
		{
			name: "inbox cursor", flags: []string{"key"}, help: "print the unix time to read Slack after",
			run: func(c cli, _ []string, f flags) (int, error) { return exitCode(c.inboxCursor(f.key)) },
		},
		{
			name: "inbox advance", args: "<ts>", least: 1, most: 1, flags: []string{"key"}, help: "move the cursor past checked messages",
			run: func(c cli, a []string, f flags) (int, error) { return exitCode(c.inboxAdvance(f.key, a[0])) },
		},
	}},
	{"delegations", []command{
		{
			name: "delegation", help: "list the delegations as JSON, the default one last",
			run: func(c cli, _ []string, _ flags) (int, error) { return exitCode(c.delegations()) },
		},
		{
			name: "delegation put", help: "add or replace the delegation given as JSON on stdin",
			run: func(c cli, _ []string, _ flags) (int, error) { return exitCode(c.delegationPut()) },
		},
		{
			name: "delegation remove", args: "<id>", least: 1, most: 1, help: "remove a delegation",
			run: func(c cli, a []string, _ flags) (int, error) { return exitCode(c.delegationRemove(a[0])) },
		},
		{
			name: "delegation match", args: "<mention | review-request | dm | own-pr | id>", least: 1, most: 1, flags: []string{"followup"},
			help: "print what the matching delegation does with a message on stdin, nothing when none matches, --followup yes picks by id",
			run: func(c cli, a []string, f flags) (int, error) {
				return exitCode(c.delegationMatch(a[0], f.followup == "yes"))
			},
		},
	}},
	{"posts", []command{
		{
			name: "posts add", flags: []string{"session"}, session: true,
			help: "record a reply posted in the scope of the session, {reply, body, kind} on stdin",
			run:  func(c cli, _ []string, _ flags) (int, error) { return exitCode(c.postsAdd()) },
		},
		{
			name: "posts list", flags: []string{"limit"}, help: "list the replies meetproxy posted as JSON, newest first",
			run: func(c cli, _ []string, f flags) (int, error) { return exitCode(c.postsList(f.limit)) },
		},
		{
			name: "posts find", args: "<reply link>", least: 1, most: 1, help: "print the ledger record of a reply, exit 1 when there is none",
			run: func(c cli, a []string, _ flags) (int, error) { return exitCode(c.postsFind(a[0])) },
		},
		{
			name: "posts retract", args: "<reply link>", least: 1, most: 1, help: "mark a reply of the ledger as retracted",
			run: func(c cli, a []string, _ flags) (int, error) { return exitCode(c.postsRetract(a[0])) },
		},
		{
			name: "watch seen", args: "<id> <ts>", least: 2, most: 2, help: "move the watch of a request past the reply at ts",
			run: func(c cli, a []string, _ flags) (int, error) { return exitCode(c.watchSeen(a[0], a[1])) },
		},
		{
			name: "correct", args: "<relay id>", least: 1, most: 1, help: "halve how much the answer of a relay counts in the location map",
			run: func(c cli, a []string, _ flags) (int, error) { return exitCode(c.correct(a[0])) },
		},
	}},
	{"triage", []command{
		{
			name: "triage", args: "[claude | codex]", most: 1, help: "show or set what sorts mentions",
			run: func(c cli, a []string, _ flags) (int, error) { return exitCode(c.triage(a)) },
		},
		{
			name: "triage command", args: "<command line>", least: 1, most: -1, help: "sort mentions with a command line",
			run: func(c cli, a []string, _ flags) (int, error) { return exitCode(c.triageCommand(a)) },
		},
		{
			name: "triage prompt", help: "build the prompt for the input JSON on stdin",
			run: func(c cli, _ []string, _ flags) (int, error) { return exitCode(c.triagePrompt()) },
		},
		{
			name: "triage parse", help: "read a verdict from the model reply on stdin",
			run: func(c cli, _ []string, _ flags) (int, error) { return exitCode(c.triageParse()) },
		},
		{
			name: "triage run", help: "run the engine on the input JSON on stdin",
			run: func(c cli, _ []string, _ flags) (int, error) { return exitCode(c.triageRun()) },
		},
	}},
	{"work map", []command{
		{
			name: "map refresh", help: "learn places, skills and past requests from the transcripts",
			run: func(c cli, _ []string, _ flags) (int, error) { return exitCode(c.mapRefresh(false)) },
		},
		{
			name: "map refresh daily", help: "refresh only once a day",
			run: func(c cli, _ []string, _ flags) (int, error) { return exitCode(c.mapRefresh(true)) },
		},
		{
			name: "map show", help: "print the places, skills and output formats the map knows",
			run: func(c cli, _ []string, _ flags) (int, error) { return exitCode(c.mapShow()) },
		},
		{
			name: "map format", args: "<kind>", least: 1, most: 1, flags: []string{"place"},
			help: "print the guides, skills and examples for one kind of output, only those that apply in --place",
			run:  func(c cli, a []string, f flags) (int, error) { return exitCode(c.mapFormat(a[0], f.place)) },
		},
	}},
}

// Hooks run on their own path since they read the data directory from the environment and never fail
const hookUsage = `hooks
  hook path                       PostToolUse path collection
  hook guard                      PreToolUse posting guard
  hook stop                       Stop end of the scope a close kept for the turn
  hook end                        SessionEnd return of the session's takes`

func usage() string {
	var b strings.Builder
	b.WriteString("usage: meetproxy <command> [args] [flags]\n")
	b.WriteString("every command but version and protocol needs --data <dir> or --root <plugin dir>\n")
	for _, fam := range families {
		fmt.Fprintf(&b, "\n%s\n", fam.name)
		for _, c := range fam.commands {
			line := strings.TrimSpace(c.name + " " + c.args)
			for _, f := range c.flags {
				line += " --" + f
			}
			if len(line) >= usageColumn {
				// The help goes under the column past the two space indent
				line += "\n" + strings.Repeat(" ", usageColumn+2)
			}
			fmt.Fprintf(&b, "  %-*s%s\n", usageColumn, line, c.help)
		}
	}
	b.WriteString("\n" + hookUsage + "\n\nflags\n")
	var f flags
	f.set().VisitAll(func(fl *flag.Flag) { fmt.Fprintf(&b, "  --%-12s%s\n", fl.Name, fl.Usage) })
	return strings.TrimSuffix(b.String(), "\n")
}

// Every flag any command takes
// A command names the ones it reads so any other fails
type flags struct {
	data, root, session                 string
	target, topic, from, ts, reason     string
	place, key, delegation, self        string
	keywords, paths, after, oldest      string
	answer, until, react, depth, digest string
	followup, correction                string
	thread, source, author, channel     string
	summary                             string
	limit                               int
	ttl                                 time.Duration
}

func (f *flags) set() *flag.FlagSet {
	fs := flag.NewFlagSet("meetproxy", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&f.data, "data", "", "plugin data directory")
	fs.StringVar(&f.root, "root", "", "plugin directory that names the data directory when --data is empty")
	fs.StringVar(&f.session, "session", f.session, "session id, CLAUDE_CODE_SESSION_ID by default")
	fs.StringVar(&f.target, "target", "", "what the task works on such as a pull request link")
	fs.StringVar(&f.topic, "topic", "", "one line request topic")
	fs.StringVar(&f.keywords, "keywords", "", "comma separated keywords")
	fs.StringVar(&f.paths, "paths", "", "comma separated evidence paths")
	fs.IntVar(&f.limit, "limit", defaultLimit, "number of results, every one with 0")
	fs.StringVar(&f.from, "from", "", "requester id")
	fs.StringVar(&f.ts, "ts", "", "unix seconds of the request")
	fs.StringVar(&f.reason, "reason", "", "one line reason")
	fs.StringVar(&f.place, "place", "", "absolute root of the place")
	fs.StringVar(&f.key, "key", "mention", "cursor key, a delegation id for channel delegations")
	fs.StringVar(&f.delegation, "delegation", "", "delegation that caught the message, the default one when empty")
	fs.StringVar(&f.self, "self", "", "yes when the user wrote the request")
	fs.StringVar(&f.after, "after", "", "unix seconds to read Slack mentions after")
	fs.StringVar(&f.oldest, "oldest", "", "Slack ts to read a channel after")
	fs.StringVar(&f.answer, "answer", "", "now, later or keep")
	fs.StringVar(&f.until, "until", "", "RFC 3339 time, duration such as 1h, or tomorrow")
	fs.StringVar(&f.react, "react", "", "reaction name")
	fs.StringVar(&f.depth, "depth", "", "quick or deep")
	fs.StringVar(&f.digest, "digest", "", "digest duplicates of the request share")
	fs.StringVar(&f.followup, "followup", "", "yes for a reply in a thread meetproxy answered")
	fs.StringVar(&f.correction, "correction", "", "yes when the reply says the earlier answer was wrong")
	fs.DurationVar(&f.ttl, "ttl", defaultLeaseTTL, "how long a lease lasts")
	fs.StringVar(&f.thread, "thread", "", "key of the thread the request sits in")
	fs.StringVar(&f.source, "source", "", "slack or github")
	fs.StringVar(&f.author, "author", "", "requester display name")
	fs.StringVar(&f.channel, "channel", "", "Slack channel or GitHub repository")
	fs.StringVar(&f.summary, "summary", "", "text of the request, of which the first line is kept")
	return fs
}

func run(cmd string, args []string, session string, now time.Time, in io.Reader, out io.Writer) (int, error) {
	f := flags{session: session}
	fs := f.set()
	if err := fs.Parse(reorder(cmd, args)); err != nil {
		return exitUsage, err
	}
	words := append([]string{cmd}, fs.Args()...)
	c, ok := lookup(words)
	if !ok {
		return exitUsage, fmt.Errorf("unknown command %q\n%s", strings.Join(words, " "), usage())
	}
	var given []string
	fs.Visit(func(fl *flag.Flag) { given = append(given, fl.Name) })
	rest := words[len(strings.Fields(c.name)):]
	if err := c.check(rest, given, f.session); err != nil {
		return exitUsage, err
	}
	if !c.noData {
		var err error
		if f.data, err = f.dataDir(); err != nil {
			return exitUsage, err
		}
	}
	return c.run(cli{data: f.data, session: f.session, now: now, in: in, out: out}, rest, f)
}

// Usage errors of the positional arguments, the flags given and the session id
func (c command) check(rest, given []string, session string) error {
	if len(rest) < c.least || (c.most >= 0 && len(rest) > c.most) {
		return fmt.Errorf("%w: run it as %s", errUsage, strings.TrimSpace(c.name+" "+c.args))
	}
	for _, name := range given {
		if name != "data" && name != "root" && !slices.Contains(c.flags, name) {
			return fmt.Errorf("%w: %s does not take --%s", errUsage, c.name, name)
		}
	}
	if c.session && session == "" {
		return fmt.Errorf("%w: %s needs a session id", errUsage, c.name)
	}
	return nil
}

// --data or the data directory Claude Code gives the plugin at --root
func (f flags) dataDir() (string, error) {
	if f.data != "" {
		return f.data, nil
	}
	if f.root == "" {
		return "", errors.New("--data or --root is required")
	}
	config, err := configDir()
	if err != nil {
		return "", err
	}
	return pluginData(f.root, config)
}

// The command whose name is the longest run of leading words
func lookup(words []string) (command, bool) {
	var best command
	size := 0
	for _, fam := range families {
		for _, c := range fam.commands {
			name := strings.Fields(c.name)
			if len(name) > size && len(name) <= len(words) && slices.Equal(name, words[:len(name)]) {
				best, size = c, len(name)
			}
		}
	}
	return best, size > 0
}

func exitCode(err error) (int, error) {
	switch {
	case err == nil:
		return 0, nil
	case errors.Is(err, errUsage):
		return exitUsage, err
	default:
		return exitFailed, err
	}
}

// Moves flags ahead of positional arguments since every flag takes a value
// Words after `--` or after the command word of triage command stay positional as they are
func reorder(cmd string, args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			// Kept so flag parsing stops before words that start with a dash
			return append(append(flags, a), append(pos, args[i+1:]...)...)
		case cmd == "triage" && len(pos) == 0 && a == string(triage.EngineCommand):
			return append(flags, args[i:]...)
		case len(a) > 1 && a[0] == '-':
			flags = append(flags, a)
			if !strings.Contains(a, "=") && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
		default:
			pos = append(pos, a)
		}
	}
	return append(flags, pos...)
}

func split(s string) []string {
	var out []string
	for v := range strings.SplitSeq(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

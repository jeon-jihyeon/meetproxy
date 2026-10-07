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
)

// Set by ldflags in release builds
var version = "dev"

// The command set the plugin mod speaks
// Raised whenever a command the mod calls changes so an older mod stops instead of misreading
const protocol = 9

const (
	exitFailed   = 1
	exitUsage    = 2
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
	code, err := run(cmd, args, os.Getenv("CLAUDE_CODE_SESSION_ID"), time.Now(), os.Stdin, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "meetproxy:", err)
	}
	os.Exit(code)
}

// A command line of the CLI
type command struct {
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
			name: "slack react", args: "<link>", least: 1, most: 1, flags: []string{"react"}, help: "add the reaction --react to the message of a link",
			run: func(c cli, a []string, f flags) (int, error) { return exitCode(c.slackReact(a[0], f.react)) },
		},
		{
			name: "slack update", args: "<reply link>", least: 1, most: 1, help: "replace a reply with the text on stdin",
			run: func(c cli, a []string, _ flags) (int, error) { return exitCode(c.slackEdit(a[0], true)) },
		},
		{
			name: "slack delete", args: "<reply link>", least: 1, most: 1, help: "delete a reply",
			run: func(c cli, a []string, _ flags) (int, error) { return exitCode(c.slackEdit(a[0], false)) },
		},
		{
			name: "slack read", args: "<link>", least: 1, most: 1, flags: []string{"limit"}, help: "print the text of the thread a link points at",
			run: func(c cli, a []string, f flags) (int, error) { return exitCode(c.slackRead(a[0], f.limit)) },
		},
		{
			name: "slack post", args: "<link>", least: 1, most: 1, flags: []string{"session"},
			help: "post the text on stdin in the thread of a link and print its permalink",
			run:  func(c cli, a []string, _ flags) (int, error) { return c.slackPost(a[0]) },
		},
		{name: "slack manifest", help: "print the app manifest and the link that creates the app", run: func(c cli, _ []string, _ flags) (int, error) { return exitCode(c.slackManifest()) }},
		{
			name: "slack setup", flags: []string{"answer"}, help: "record the answer to setting up a token, now, later or keep",
			run: func(c cli, _ []string, f flags) (int, error) { return exitCode(c.slackSetup(f.answer)) },
		},
	}},
}

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
	var f flags
	f.set().VisitAll(func(fl *flag.Flag) { fmt.Fprintf(&b, "  --%-12s%s\n", fl.Name, fl.Usage) })
	return strings.TrimSuffix(b.String(), "\n")
}

// Every flag any command takes
// A command names the ones it reads so any other fails
type flags struct {
	data, root, session string
	topic               string

	keywords, paths, after, oldest string
	answer, react                  string

	thread, channel string

	limit int
}

func (f *flags) set() *flag.FlagSet {
	fs := flag.NewFlagSet("meetproxy", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&f.data, "data", "", "plugin data directory")
	fs.StringVar(&f.root, "root", "", "plugin directory that names the data directory when --data is empty")
	fs.StringVar(&f.session, "session", f.session, "session id, CLAUDE_CODE_SESSION_ID by default")

	fs.StringVar(&f.topic, "topic", "", "one line request topic")
	fs.StringVar(&f.keywords, "keywords", "", "comma separated keywords")
	fs.StringVar(&f.paths, "paths", "", "comma separated evidence paths")
	fs.IntVar(&f.limit, "limit", defaultLimit, "number of results, every one with 0")

	fs.StringVar(&f.after, "after", "", "unix seconds to read Slack mentions after")
	fs.StringVar(&f.oldest, "oldest", "", "Slack ts to read a channel after")
	fs.StringVar(&f.answer, "answer", "", "now, later or keep")

	fs.StringVar(&f.react, "react", "", "reaction name")

	fs.StringVar(&f.thread, "thread", "", "key of the thread the request sits in")

	fs.StringVar(&f.channel, "channel", "", "Slack channel or GitHub repository")

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

func reorder(_ string, args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			// Kept so flag parsing stops before words that start with a dash
			return append(append(flags, a), append(pos, args[i+1:]...)...)
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

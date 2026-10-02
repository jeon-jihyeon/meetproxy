package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const usage = `usage: meetproxy <command> --data <dir> [args]
  version                       print the binary version
  open <origin>                 open a relay
  locate <term...>              look up location map candidates
  dest <location>               check whether a destination is allowed
  allow <pattern>               allow a destination such as github:owner/*
  close --topic --keywords --paths
                                close the relay and record evidence paths
  hook path                     PostToolUse path collection
  hook guard                    PreToolUse posting guard`

const exitUsage = 2

// Set by ldflags in release builds
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(exitUsage)
	}
	cmd, args := os.Args[1], os.Args[2:]
	if cmd == "version" {
		fmt.Println(version)
		return
	}
	if cmd == "hook" {
		// Exit 0 so a broken hook never blocks the session
		if err := runHook(os.Getenv("CLAUDE_PLUGIN_DATA"), args, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "meetproxy hook:", err)
		}
		return
	}
	code, err := run(cmd, args, os.Getenv("CLAUDE_CODE_SESSION_ID"), time.Now(), os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "meetproxy:", err)
	}
	os.Exit(code)
}

func run(cmd string, args []string, session string, now time.Time, out io.Writer) (int, error) {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	data := fs.String("data", "", "plugin data directory")
	fs.StringVar(&session, "session", session, "session id")
	topic := fs.String("topic", "", "one line request topic")
	keywords := fs.String("keywords", "", "comma separated keywords")
	paths := fs.String("paths", "", "comma separated evidence paths, observed paths when empty")
	limit := fs.Int("limit", 10, "number of candidates")
	if err := fs.Parse(reorder(args)); err != nil {
		return exitUsage, err
	}
	if *data == "" {
		return exitUsage, errors.New("--data is required")
	}
	c := cli{data: *data, session: session, now: now, out: out}
	rest := fs.Args()
	if n, ok := singleArg[cmd]; ok && len(rest) != n {
		return exitUsage, fmt.Errorf("%s takes %d argument", cmd, n)
	}
	if needsSession[cmd] && session == "" {
		return exitUsage, fmt.Errorf("%s needs a session id", cmd)
	}

	switch cmd {
	case "open":
		return exitCode(c.open(rest[0]))
	case "locate":
		return exitCode(c.locate(rest, *limit))
	case "dest":
		allowed, err := c.dest(rest[0])
		if err != nil || !allowed {
			return 1, err
		}
		return 0, nil
	case "allow":
		return exitCode(c.allow(rest[0]))
	case "close":
		if *topic == "" {
			return exitUsage, errors.New("close needs --topic")
		}
		return exitCode(c.close(*topic, split(*keywords), split(*paths)))
	default:
		return exitUsage, fmt.Errorf("unknown command %q\n%s", cmd, usage)
	}
}

var (
	singleArg    = map[string]int{"open": 1, "dest": 1, "allow": 1}
	needsSession = map[string]bool{"open": true, "close": true}
)

func exitCode(err error) (int, error) {
	if err != nil {
		return 1, err
	}
	return 0, nil
}

// Moves flags ahead of positional arguments since every flag takes a value
func reorder(args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) > 1 && a[0] == '-' {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		pos = append(pos, a)
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

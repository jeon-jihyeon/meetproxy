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
`

const exitUsage = 2

// Set by ldflags in release builds
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(exitUsage)
	}
	cmd, args := os.Args[1], os.Args[2:]
	if cmd == "version" {
		fmt.Println(version)
		return
	}
	err := run(cmd, args, os.Getenv("CLAUDE_CODE_SESSION_ID"), time.Now(), os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "meetproxy:", err)
	}
	os.Exit(exitUsage)
}

func run(cmd string, args []string, session string, _ time.Time, _ io.Writer) error {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	data := fs.String("data", "", "plugin data directory")
	fs.StringVar(&session, "session", session, "session id")

	if err := fs.Parse(reorder(args)); err != nil {
		return err
	}
	if *data == "" {
		return errors.New("--data is required")
	}

	rest := fs.Args()
	if n, ok := singleArg[cmd]; ok && len(rest) != n {
		return fmt.Errorf("%s takes %d argument", cmd, n)
	}
	if needsSession[cmd] && session == "" {
		return fmt.Errorf("%s needs a session id", cmd)
	}

	return fmt.Errorf("unknown command %q\n%s", cmd, usage)

}

var (
	singleArg    = map[string]int{"open": 1, "dest": 1, "allow": 1}
	needsSession = map[string]bool{"open": true, "close": true}
)

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

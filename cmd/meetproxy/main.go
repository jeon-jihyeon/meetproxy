package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

var version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := execute(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "meetproxy:", err)
		return 1
	}
	return 0
}

func execute(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: meetproxy <control|listen|version> [command] [flags]")
	}
	switch args[0] {
	case "control":
		return runControl(ctx, args[1:], in, out)
	case "listen":
		return runListen(ctx, args[1:], out)
	case "version":
		if len(args) != 1 {
			return errors.New("version takes no arguments")
		}
		_, err := fmt.Fprintln(out, version)
		return err
	default:
		return errors.New("unknown meetproxy command")
	}
}

package main

import (
	"fmt"
	"io"

	"time"
)

type cli struct {
	data    string
	session string
	now     time.Time
	out     io.Writer
}

func printVersion(c cli, _ []string, _ flags) (int, error) {
	_, err := fmt.Fprintln(c.out, version)
	return exitCode(err)
}

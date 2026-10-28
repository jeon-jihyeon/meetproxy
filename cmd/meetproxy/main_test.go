package main

import (
	"io"

	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func run(cmd string, args []string, session string, now time.Time, in io.Reader, out io.Writer) (int, error) {
	c := cli{session: session, now: now, in: in, out: out}
	return c.exec(cmd, args)
}

type step struct {
	cmd   string
	args  []string
	stdin string
}

func TestReorder(t *testing.T) {
	t.Parallel()
	type args struct {
		cmd  string
		args []string
	}
	tcs := []struct {
		name string
		args args
		want []string
	}{
		{"moves a double dash flag", args{"open", []string{"x", "--data", "d"}}, []string{"--data", "d", "x"}},
		{"moves a single dash flag", args{"open", []string{"x", "-data", "d"}}, []string{"-data", "d", "x"}},
		{"keeps an equals flag whole", args{"open", []string{"--data=d", "x"}}, []string{"--data=d", "x"}},
		{"keeps a lone dash as an argument", args{"open", []string{"-", "x"}}, []string{"-", "x"}},
		{
			"keeps every word after a double dash", args{"open", []string{"x", "--data", "d", "--", "-p", "--limit", "5"}},
			[]string{"--data", "d", "--", "x", "-p", "--limit", "5"},
		},
		{"keeps a command word of another command", args{"open", []string{"command", "--data", "d"}}, []string{"--data", "d", "command"}},
		{
			"keeps a flag without a value off the next word",
			args{"login", []string{"--confirm", "x", "--data", "d"}}, []string{"--confirm", "--data", "d", "x"},
		},
		{
			"keeps a single dash flag without a value off the next word",
			args{"login", []string{"-confirm", "x", "--data", "d"}}, []string{"-confirm", "--data", "d", "x"},
		},
		{
			"keeps a flag without a value given with equals",
			args{"login", []string{"x", "--confirm=true"}}, []string{"--confirm=true", "x"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, reorder(tc.args.cmd, tc.args.args))
		})
	}
}

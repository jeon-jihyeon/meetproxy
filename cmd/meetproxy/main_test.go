package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
		{"moves a double dash flag", args{"locate", []string{"x", "--data", "d"}}, []string{"--data", "d", "x"}},
		{"moves a single dash flag", args{"locate", []string{"x", "-data", "d"}}, []string{"-data", "d", "x"}},
		{"keeps an equals flag whole", args{"locate", []string{"--data=d", "x"}}, []string{"--data=d", "x"}},
		{"keeps a lone dash as an argument", args{"locate", []string{"-", "x"}}, []string{"-", "x"}},
		{
			"keeps every word after a double dash", args{"locate", []string{"x", "--data", "d", "--", "-p", "--limit", "5"}},
			[]string{"--data", "d", "--", "x", "-p", "--limit", "5"},
		},
		{"keeps a command word of another command", args{"locate", []string{"command", "--data", "d"}}, []string{"--data", "d", "command"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, reorder(tc.args.cmd, tc.args.args))
		})
	}
}

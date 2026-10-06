package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReorder(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		args []string
		want []string
	}{
		{"moves a double dash flag", []string{"x", "--data", "d"}, []string{"--data", "d", "x"}},
		{"moves a single dash flag", []string{"x", "-data", "d"}, []string{"-data", "d", "x"}},
		{"keeps an equals flag whole", []string{"--data=d", "x"}, []string{"--data=d", "x"}},
		{"keeps a lone dash as an argument", []string{"-", "x"}, []string{"-", "x"}},
		{
			"keeps every word after a double dash", []string{"x", "--data", "d", "--", "-p", "--limit", "5"},
			[]string{"--data", "d", "--", "x", "-p", "--limit", "5"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, reorder(tc.args))
		})
	}
}

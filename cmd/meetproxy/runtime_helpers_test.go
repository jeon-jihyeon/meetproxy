package main

import (
	"bytes"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

var modNow = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

func mustRun(t *testing.T, data, session string, now time.Time, s step) string {
	t.Helper()
	var output bytes.Buffer
	code, err := run(s.cmd, append([]string{"--data", data}, s.args...), session, now, strings.NewReader(s.stdin), &output)
	require.NoError(t, err)
	require.Zero(t, code)
	return strings.TrimSpace(output.String())
}

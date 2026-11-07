package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListenPlistPreservesStaticArguments(t *testing.T) {
	binary, err := os.Executable()
	require.NoError(t, err)
	directory := filepath.Join(t.TempDir(), "not-created")
	args := []string{
		"listen", "plist", "--data", directory, "--token-file", filepath.Join(directory, "app-token"),
		"--bot-user", "U1", "--handler", binary, "--executable", binary,
		"--handler-arg=--handler=<&", "--handler-arg=two words",
	}
	var output bytes.Buffer
	require.NoError(t, execute(context.Background(), args, nil, &output))
	var plist struct {
		Version string `xml:"version,attr"`
		Dict    struct {
			Arguments []string `xml:"array>string"`
		} `xml:"dict"`
	}
	require.NoError(t, xml.Unmarshal(output.Bytes(), &plist))
	assert.Equal(t, "1.0", plist.Version)
	assert.Contains(t, plist.Dict.Arguments, "--handler-arg=--handler=<&")
	assert.Contains(t, plist.Dict.Arguments, "--handler-arg=two words")
	assert.Contains(t, plist.Dict.Arguments, "--handler="+binary)
	assert.Equal(t, []string{binary, "listen"}, plist.Dict.Arguments[:2])
	assert.Contains(t, output.String(), "<key>KeepAlive</key><false/>")
	assert.NotContains(t, strings.Join(plist.Dict.Arguments, " "), "--executable")
	_, err = os.Stat(directory)
	assert.True(t, os.IsNotExist(err))
}

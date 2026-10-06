package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Creates a plugin directory whose manifest holds the given JSON
func pluginDir(t *testing.T, parent, manifest string) string {
	t.Helper()
	root := filepath.Join(parent, "plugin")
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude-plugin"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude-plugin", "plugin.json"), []byte(manifest), 0o644))
	return root
}

// Creates a config directory with the given marketplace and install records
func configWith(t *testing.T, known, installed string) string {
	t.Helper()
	config := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(config, "plugins"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(config, "plugins", "known_marketplaces.json"), []byte(known), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(config, "plugins", "installed_plugins.json"), []byte(installed), 0o644))
	return config
}

func TestPluginData(t *testing.T) {
	t.Parallel()
	market := t.TempDir()
	fromMarket := pluginDir(t, market, `{"name":"meetproxy"}`)
	installs := `{"plugins":{"meetproxy@local":[]}}`
	config := configWith(t, `{"local":{"installLocation":"`+market+`"}}`, installs)
	broken := configWith(t, `{`, installs)
	installed := filepath.Join(config, "plugins", "cache", "jeon.tools", "meetproxy", "0.2.0")

	type args struct {
		root   string
		config string
	}
	type want struct {
		dir    string
		failed bool
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"an installed copy uses name and marketplace", args{installed, config}, want{filepath.Join(config, "plugins", "data", "meetproxy-jeon-tools"), false}},
		{
			"a plugin read from a directory marketplace uses that marketplace", args{fromMarket, config},
			want{filepath.Join(config, "plugins", "data", "meetproxy-local"), false},
		},
		{
			"a plugin dir uses inline", args{pluginDir(t, t.TempDir(), `{"name":"meetproxy"}`), config},
			want{filepath.Join(config, "plugins", "data", "meetproxy-inline"), false},
		},
		{
			"unreadable marketplaces fall back to inline", args{fromMarket, broken},
			want{filepath.Join(broken, "plugins", "data", "meetproxy-inline"), false},
		},
		{"a directory without a manifest fails", args{t.TempDir(), config}, want{"", true}},
		{"a manifest that is no JSON fails", args{pluginDir(t, t.TempDir(), `{`), config}, want{"", true}},
		{"a manifest without a name fails", args{pluginDir(t, t.TempDir(), `{}`), config}, want{"", true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, err := pluginData(tc.args.root, tc.args.config)
			assert.Equal(t, tc.want, want{dir, err != nil})
		})
	}
}

// Not parallel since it sets the environment
func TestConfigDir(t *testing.T) {
	type args struct {
		config string
		home   string
	}
	type want struct {
		dir    string
		failed bool
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"CLAUDE_CONFIG_DIR wins", args{"/c", "/h"}, want{"/c", false}},
		{"the home directory holds .claude otherwise", args{"", "/h"}, want{filepath.Join("/h", ".claude"), false}},
		{"no home directory fails", args{"", ""}, want{"", true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", tc.args.config)
			t.Setenv("HOME", tc.args.home)
			dir, err := configDir()
			assert.Equal(t, tc.want, want{dir, err != nil})
		})
	}
}

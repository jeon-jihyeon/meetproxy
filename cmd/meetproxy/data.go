package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jeon-jihyeon/meetproxy/internal/fileio"
)

var unsafeId = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// Directories of a cached copy below plugins/cache: marketplace, name and version
const cacheDepth = 3

// The plugin mod gets no CLAUDE_PLUGIN_DATA so the directory is rebuilt the way Claude Code names it
// 1. A copy at plugins/cache/<marketplace>/<name>/<version> has the id name@marketplace
// 2. A plugin read from an installed directory marketplace has the id name@that marketplace
// 3. A directory loaded with --plugin-dir has the id name@inline
// 4. Every character other than a letter, digit, `_` or `-` in the id becomes `-`
func pluginData(root, config string) (string, error) {
	root = filepath.Clean(root)
	id := ""
	if rel, err := filepath.Rel(filepath.Join(config, "plugins", "cache"), root); err == nil {
		if parts := strings.Split(rel, string(filepath.Separator)); len(parts) == cacheDepth && parts[0] != ".." {
			id = parts[1] + "@" + parts[0]
		}
	}
	if id == "" {
		name, err := pluginName(root)
		if err != nil {
			return "", err
		}
		id = name + "@" + installedFrom(config, root, name)
	}
	return filepath.Join(config, "plugins", "data", unsafeId.ReplaceAllString(id, "-")), nil
}

func pluginName(root string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, ".claude-plugin", "plugin.json"))
	if err != nil {
		return "", fmt.Errorf("%s is not a plugin directory: %w", root, err)
	}
	var m struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(b, &m); err != nil || m.Name == "" {
		return "", fmt.Errorf("%s has no plugin name", root)
	}
	return m.Name, nil
}

// The directory marketplace holding root under which the plugin is installed
// inline when no such marketplace is known
func installedFrom(config, root, name string) string {
	var known map[string]struct {
		InstallLocation string `json:"installLocation"`
	}
	var installed struct {
		Plugins map[string]json.RawMessage `json:"plugins"`
	}
	if !readJSON(filepath.Join(config, "plugins", "known_marketplaces.json"), &known) ||
		!readJSON(filepath.Join(config, "plugins", "installed_plugins.json"), &installed) {
		return "inline"
	}
	for market, m := range known {
		rel, err := filepath.Rel(filepath.Clean(m.InstallLocation), root)
		if m.InstallLocation == "" || err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		if _, ok := installed.Plugins[name+"@"+market]; ok {
			return market
		}
	}
	return "inline"
}

func readJSON(file string, v any) bool {
	found, err := fileio.ReadJSON(file, v)
	return found && err == nil
}

func configDir() (string, error) {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude"), nil
}

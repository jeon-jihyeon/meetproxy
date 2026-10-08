package workmap

import (
	"bufio"
	"cmp"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Every place a case happened in
func scanPlaces(cases []Case) []Place {
	counts := map[string]int{}
	for _, c := range cases {
		if c.Root != "" {
			counts[c.Root]++
		}
	}
	out := make([]Place, 0, len(counts))
	for root, n := range counts {
		if _, err := os.Stat(root); err != nil || temporary(root) {
			continue
		}
		out = append(out, Place{Root: root, Name: filepath.Base(root), Cases: n})
	}
	slices.SortFunc(out, func(a, b Place) int { return cmp.Or(cmp.Compare(b.Cases, a.Cases), strings.Compare(a.Root, b.Root)) })
	return out
}

// Skills and commands of the user and of installed plugins
// Each place also keeps its own
func scanMethods(config string, places []Place) []Method {
	var out []Method
	out = append(out, skillsIn(filepath.Join(config, "skills"), "", "")...)
	out = append(out, commandsIn(filepath.Join(config, "commands"), "", "")...)
	var installed struct {
		Plugins map[string][]struct {
			InstallPath string `json:"installPath"`
		} `json:"plugins"`
	}
	if b, err := os.ReadFile(filepath.Join(config, "plugins", "installed_plugins.json")); err == nil && json.Unmarshal(b, &installed) == nil {
		for id, copies := range installed.Plugins {
			if len(copies) == 0 {
				continue
			}
			plugin, _, _ := strings.Cut(id, "@")
			out = append(out, skillsIn(filepath.Join(copies[0].InstallPath, "skills"), plugin+":", "")...)
			out = append(out, commandsIn(filepath.Join(copies[0].InstallPath, "commands"), plugin+":", "")...)
		}
	}
	for _, p := range places {
		out = append(out, skillsIn(filepath.Join(p.Root, ".claude", "skills"), "", p.Root)...)
		out = append(out, commandsIn(filepath.Join(p.Root, ".claude", "commands"), "", p.Root)...)
	}
	slices.SortFunc(out, func(a, b Method) int { return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.Root, b.Root)) })
	return out
}

func skillsIn(dir, prefix, root string) []Method {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Method
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		desc := frontmatter(filepath.Join(dir, e.Name(), "SKILL.md"), "description")
		out = append(out, Method{Name: prefix + e.Name(), Description: clip(desc, 200), Root: root})
	}
	return out
}

func commandsIn(dir, prefix, root string) []Method {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Method
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".md")
		if !ok || e.IsDir() {
			continue
		}
		desc := frontmatter(filepath.Join(dir, e.Name()), "description")
		out = append(out, Method{Name: prefix + name, Description: clip(desc, 200), Root: root})
	}
	return out
}

// One key of the YAML front matter
// 1. A plain or quoted value on the line of the key
// 2. A folded or literal block scalar from the indented lines after it
func frontmatter(file, key string) string {
	f, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	if !sc.Scan() || strings.TrimSpace(sc.Text()) != "---" {
		return ""
	}
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "---" {
			return ""
		}
		v, ok := strings.CutPrefix(line, key+":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if !strings.HasPrefix(v, ">") && !strings.HasPrefix(v, "|") {
			return strings.Trim(v, `"'`)
		}
		return blockScalar(sc, v[0] == '|')
	}
	return ""
}

// Folded lines join with spaces and literal lines keep their breaks
func blockScalar(sc *bufio.Scanner, literal bool) string {
	var lines []string
	for sc.Scan() {
		line := sc.Text()
		if line != "" && line[0] != ' ' && line[0] != '\t' {
			break
		}
		lines = append(lines, strings.TrimSpace(line))
	}
	sep := " "
	if literal {
		sep = "\n"
	}
	return strings.TrimSpace(strings.Join(lines, sep))
}

package guard

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/jeon-jihyeon/meetproxy/internal/dest"
)

var (
	// Subcommands that never post
	ghReads = map[string]bool{
		"search": true, "status": true, "browse": true, "help": true, "version": true, "completion": true,
		"pr list": true, "pr view": true, "pr status": true, "pr diff": true, "pr checks": true, "pr checkout": true,
		"issue list": true, "issue view": true, "issue status": true,
		"release list": true, "release view": true, "release download": true,
		"gist list": true, "gist view": true, "gist clone": true,
		"repo view": true, "repo list": true, "repo clone": true,
		"run list": true, "run view": true, "run watch": true, "run download": true,
		"workflow list": true, "workflow view": true,
		"label list": true, "cache list": true, "secret list": true, "variable list": true, "variable get": true,
		"ruleset list": true, "ruleset view": true, "ruleset check": true,
		"auth status": true, "config get": true, "config list": true, "extension list": true,
	}
	// Command groups whose writes name their repository with -R or a link
	// Any other group such as alias, gist or extension is denied while a relay is open
	ghChecked = map[string]bool{
		"pr": true, "issue": true, "release": true, "label": true, "repo": true,
		"run": true, "workflow": true, "secret": true, "variable": true, "cache": true,
	}
	// Flags whose value is free text so a link inside it is never a target
	// Every other flag is read as taking no value so a link after it is still checked
	ghText = map[string]bool{
		"-b": true, "--body": true, "-t": true, "--title": true, "-n": true, "--notes": true, "--subject": true,
	}
	ownerRepo = regexp.MustCompile(`^[\w.-]+/[\w.-]+$`)
	number    = regexp.MustCompile(`^#?(\d+)$`)
	apiPath   = regexp.MustCompile(`^/?repos/([\w.-]+/[\w.-]+)(?:/(?:issues|pulls)/(\d+))?`)
)

// The words of a gh call split by role
type ghArgs struct {
	// Words that are no flag or flag value
	positional []string
	// Values of -R and --repo
	repos []string
}

func splitGh(args []string) ghArgs {
	var g ghArgs
	for j := 0; j < len(args); j++ {
		w := args[j]
		switch {
		case w == "--":
			g.positional = append(g.positional, args[j+1:]...)
			return g
		case w == "-R" || w == "--repo":
			if j+1 < len(args) {
				g.repos = append(g.repos, args[j+1])
			}
			j++
		case strings.HasPrefix(w, "--repo="):
			g.repos = append(g.repos, strings.TrimPrefix(w, "--repo="))
		case strings.HasPrefix(w, "-R"):
			g.repos = append(g.repos, strings.TrimPrefix(w, "-R"))
		case ghText[w]:
			j++
		case strings.HasPrefix(w, "-"):
		default:
			g.positional = append(g.positional, w)
		}
	}
	return g
}

// Where a gh call posts
// 1. Known reads post nowhere
// 2. Writes of a checked group post to every repository they name
// 3. Anything else fails closed
func ghCall(args, envRepos []string) ([]dest.Location, error) {
	g := splitGh(args)
	g.repos = append(g.repos, envRepos...)
	if len(g.positional) == 0 {
		return nil, nil
	}
	group := g.positional[0]
	sub := ""
	if len(g.positional) > 1 {
		sub = g.positional[1]
	}
	switch {
	case group == "api":
		return apiCall(args, g)
	case ghReads[group] || ghReads[group+" "+sub]:
		return nil, nil
	case ghChecked[group] && sub == "":
		return nil, nil
	case !ghChecked[group]:
		return nil, unknown("gh " + group + " is not checked")
	}
	locs := g.targets(group+" "+sub, g.positional[2:])
	if len(locs) == 0 {
		return nil, unknown("gh " + group + " " + sub + " names no repository, add -R OWNER/REPO")
	}
	return locs, nil
}

// Every repository a write names
// 1. Each -R or GH_REPO repository with each pull request or issue number given
// 2. Each link to GitHub
// 3. OWNER/REPO positionals of commands that take one such as issue transfer
func (g ghArgs) targets(cmd string, rest []string) []dest.Location {
	var locs []dest.Location
	numbers := []int{}
	numbered := strings.HasPrefix(cmd, "pr ") || strings.HasPrefix(cmd, "issue ")
	positionalRepo := cmd == "issue transfer" || strings.HasPrefix(cmd, "repo ")
	for _, w := range rest {
		if m := number.FindStringSubmatch(w); m != nil && numbered {
			n, _ := strconv.Atoi(m[1])
			numbers = append(numbers, n)
			continue
		}
		if loc, ok := dest.Parse(w); ok && loc.Source == dest.GitHub {
			locs = append(locs, loc)
			numbers = append(numbers, loc.Number)
			continue
		}
		if positionalRepo && ownerRepo.MatchString(w) {
			locs = append(locs, repoAt(w, 0)...)
		}
	}
	if len(numbers) == 0 {
		numbers = []int{0}
	}
	for _, r := range g.repos {
		for _, n := range numbers {
			locs = append(locs, repoAt(r, n)...)
		}
	}
	return locs
}

// A repository written as OWNER/REPO, HOST/OWNER/REPO or a link
// A value that names no repository still yields a location so it is denied instead of skipped
func repoAt(v string, n int) []dest.Location {
	if loc, ok := dest.Parse(v); ok && loc.Source == dest.GitHub {
		loc.Number = n
		return []dest.Location{loc}
	}
	parts := strings.Split(strings.Trim(v, "/"), "/")
	if len(parts) >= 2 {
		v = parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	return []dest.Location{{Source: dest.GitHub, Name: v, Number: n}}
}

// A write through gh api posts to the repository of its endpoint
func apiCall(args []string, g ghArgs) ([]dest.Location, error) {
	if !apiWrites(args) {
		return nil, nil
	}
	var locs []dest.Location
	for _, w := range g.positional[1:] {
		if m := apiPath.FindStringSubmatch(w); m != nil {
			n, _ := strconv.Atoi(m[2])
			locs = append(locs, dest.Location{Source: dest.GitHub, Name: m[1], Number: n})
		}
	}
	for _, r := range g.repos {
		locs = append(locs, repoAt(r, 0)...)
	}
	if len(locs) == 0 {
		return nil, unknown("gh api writes to an endpoint without a repository")
	}
	return locs, nil
}

func apiWrites(args []string) bool {
	for i, w := range args {
		method := ""
		switch {
		case strings.HasPrefix(w, "--field"), strings.HasPrefix(w, "--raw-field"), strings.HasPrefix(w, "--input"):
			return true
		case strings.HasPrefix(w, "--method="):
			method = strings.TrimPrefix(w, "--method=")
		case w == "--method" || w == "-X":
			if i+1 < len(args) {
				method = args[i+1]
			}
		case strings.HasPrefix(w, "-X"):
			method = strings.TrimPrefix(w, "-X")
		case strings.HasPrefix(w, "-f"), strings.HasPrefix(w, "-F"):
			return true
		}
		if method != "" && !strings.EqualFold(method, "GET") && !strings.EqualFold(method, "HEAD") {
			return true
		}
	}
	return false
}

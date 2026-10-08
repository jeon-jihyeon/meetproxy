package guard

import (
	"regexp"
	"slices"
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
	// The only calls without a subcommand that pass
	ghInfo = map[string]bool{"--version": true, "--help": true, "-h": true}
	// Writes the guard leaves to the user even where posts may go
	ghActs = map[string]string{
		"pr merge": ActMerge, "pr close": ActClose, "issue close": ActClose, "repo archive": ActClose,
		"repo delete": ActDelete, "release delete": ActDelete, "label delete": ActDelete, "issue delete": ActDelete,
	}
	// OWNER/REPO or HOST/OWNER/REPO
	ownerRepo = regexp.MustCompile(`^(?:[\w.-]+/)?[\w.-]+/[\w.-]+$`)
	number    = regexp.MustCompile(`^#?(\d+)$`)
	apiPath   = regexp.MustCompile(`^/?repos/([\w.-]+/[\w.-]+)(?:/(?:issues|pulls)/(\d+))?`)
	// A repository link on a host other than github.com such as GitHub Enterprise
	hostLink = regexp.MustCompile(`^https?://([\w.-]+)/([\w.-]+/[\w.-]+)(?:/(?:pull|issues)/(\d+))?(?:[/?#]|$)`)
)

// The words of a gh call split by role
type ghArgs struct {
	// Words that are no flag or flag value
	positional []string
	// Values of -R and --repo
	repos []string
	// Flags that take no text such as --approve
	flags []string
	// Values of --hostname and GH_HOST
	hosts []string
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
		case w == "--hostname":
			if j+1 < len(args) {
				g.hosts = append(g.hosts, args[j+1])
			}
			j++
		case strings.HasPrefix(w, "--hostname="):
			g.hosts = append(g.hosts, strings.TrimPrefix(w, "--hostname="))
		case ghText[w]:
			j++
		case strings.HasPrefix(w, "-"):
			g.flags = append(g.flags, w)
		default:
			g.positional = append(g.positional, w)
		}
	}
	return g
}

// Where a gh call posts
// 1. Known reads post nowhere
// 2. Writes of a checked group post to every repository they name
// 3. Writes to a host other than github.com and anything else fail closed
// fed is a gh run by xargs or parallel whose last words come from its input
func ghCall(args, envRepos, envHosts []string, fed bool) ([]Post, error) {
	g := splitGh(args)
	g.repos = append(g.repos, envRepos...)
	g.hosts = append(g.hosts, envHosts...)
	if len(g.positional) == 0 {
		if !fed && len(args) == 1 && ghInfo[args[0]] {
			return nil, nil
		}
		return nil, unknown("gh without a subcommand")
	}
	group, sub := g.positional[0], ""
	if len(g.positional) > 1 {
		sub = g.positional[1]
	}
	read := isRead(group, sub)
	switch {
	case fed && !read:
		return nil, unknown("gh given words by xargs or parallel that may not only read")
	case group == "api":
		return apiCall(args, g)
	case read:
		return nil, nil
	case ghChecked[group] && sub == "":
		return nil, nil
	case !ghChecked[group]:
		return nil, unknown("gh " + group + " is not checked")
	}
	cmd := group + " " + sub
	locs, err := g.targets(cmd, g.positional[2:])
	if err != nil {
		return nil, err
	}
	if len(locs) == 0 {
		return nil, unknown("gh " + cmd + " names no repository, add -R OWNER/REPO")
	}
	return postsAt(locs, g.act(cmd)), nil
}

// The host every repository that names none is on and empty for github.com
// Two other hosts cannot be told apart so they are unknown
func (g ghArgs) host() (string, error) {
	host := ""
	for _, h := range g.hosts {
		h = strings.ToLower(h)
		switch {
		case onGitHub(h) || h == host:
		case host == "":
			host = h
		default:
			return "", unknown("gh on hosts " + host + " and " + h)
		}
	}
	return host, nil
}

func onGitHub(host string) bool { return strings.EqualFold(host, "github.com") }

func isRead(group, sub string) bool {
	return group != "api" && (ghReads[group] || ghReads[group+" "+sub])
}

func (g ghArgs) act(cmd string) string {
	if cmd == "pr review" && slices.ContainsFunc(g.flags, approves) {
		return ActApprove
	}
	return ghActs[cmd]
}

// --approve or a cluster of short flags such as -ab that holds -a before a flag that takes a value
func approves(flag string) bool {
	if flag == "--approve" || (strings.HasPrefix(flag, "--approve=") && flag != "--approve=false") {
		return true
	}
	if strings.HasPrefix(flag, "--") {
		return false
	}
	for _, c := range strings.TrimPrefix(flag, "-") {
		switch c {
		case 'a':
			return true
		case 'b', 'F', 'R':
			return false
		}
	}
	return false
}

// Every repository a write names
// 1. Each -R or GH_REPO repository with each pull request or issue number given
// 2. Each link to a repository, and an error for any other link
// 3. OWNER/REPO and HOST/OWNER/REPO positionals of commands that take one such as issue transfer
func (g ghArgs) targets(cmd string, rest []string) ([]dest.Location, error) {
	host, err := g.host()
	if err != nil {
		return nil, err
	}
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
		if strings.Contains(w, "://") {
			loc, err := linkAt(w)
			if err != nil {
				return nil, err
			}
			locs = append(locs, loc)
			numbers = append(numbers, loc.Number)
			continue
		}
		if positionalRepo && ownerRepo.MatchString(w) {
			locs = append(locs, repoAt(w, 0, host))
		}
	}
	if len(numbers) == 0 {
		numbers = []int{0}
	}
	repos, err := g.repoLocs(numbers)
	return append(locs, repos...), err
}

// Each -R or GH_REPO repository with each number
func (g ghArgs) repoLocs(numbers []int) ([]dest.Location, error) {
	host, err := g.host()
	if err != nil {
		return nil, err
	}
	var locs []dest.Location
	for _, r := range g.repos {
		for _, n := range numbers {
			if !strings.Contains(r, "://") {
				locs = append(locs, repoAt(r, n, host))
				continue
			}
			loc, err := linkAt(r)
			if err != nil {
				return nil, err
			}
			loc.Number = n
			locs = append(locs, loc)
		}
	}
	return locs, nil
}

// A link to a repository, its pull request or its issue on any host
// Any other link is unknown so a write never goes to a place the guard skipped
func linkAt(w string) (dest.Location, error) {
	if loc, ok := dest.Parse(w); ok && loc.Source == dest.GitHub {
		return loc, nil
	}
	m := hostLink.FindStringSubmatch(w)
	if m == nil {
		return dest.Location{}, unknown("a link the guard cannot read")
	}
	n, _ := strconv.Atoi(m[3])
	return dest.Location{Source: dest.GitHub, Name: strings.ToLower(m[1]) + "/" + m[2], Number: n}, nil
}

// A repository written as OWNER/REPO or HOST/OWNER/REPO
// 1. github.com is dropped and another host stays first in the name
// 2. A repository that names no host is on the host of --hostname or GH_HOST
// 3. A value that names no repository still yields a location so it is denied instead of skipped
func repoAt(v string, n int, host string) dest.Location {
	parts := strings.Split(strings.Trim(v, "/"), "/")
	switch {
	case len(parts) > 2 && onGitHub(parts[0]):
		parts = parts[1:]
	case len(parts) > 2:
		parts[0] = strings.ToLower(parts[0])
	case host != "":
		parts = append([]string{host}, parts...)
	}
	return dest.Location{Source: dest.GitHub, Name: strings.Join(parts, "/"), Number: n}
}

// A write through gh api posts to the repository of its endpoint
func apiCall(args []string, g ghArgs) ([]Post, error) {
	method, writes := apiWrites(args)
	if !writes {
		return nil, nil
	}
	locs, err := g.repoLocs([]int{0})
	if err != nil {
		return nil, err
	}
	host, _ := g.host()
	merges := false
	for _, w := range g.positional[1:] {
		if m := apiPath.FindStringSubmatch(w); m != nil {
			n, _ := strconv.Atoi(m[2])
			locs = append(locs, repoAt(m[1], n, host))
			merges = merges || strings.Contains(w, "/merge")
		}
	}
	if len(locs) == 0 {
		return nil, unknown("gh api writes to an endpoint without a repository")
	}
	return postsAt(locs, apiAct(args, method, merges)), nil
}

// The act of a gh api write from its method, its endpoint and its fields
// Fields are matched in every word so a field joined to its flag still counts
func apiAct(args []string, method string, merges bool) string {
	switch {
	case strings.EqualFold(method, "DELETE"):
		return ActDelete
	case merges:
		return ActMerge
	}
	for _, w := range args {
		switch v := strings.ToLower(w); {
		case strings.Contains(v, "event=approve"):
			return ActApprove
		case strings.Contains(v, "state=closed"):
			return ActClose
		}
	}
	return ActComment
}

// The method a gh api call names and whether it writes
func apiWrites(args []string) (string, bool) {
	writes := false
	method := ""
	for i, w := range args {
		switch {
		case strings.HasPrefix(w, "--field"), strings.HasPrefix(w, "--raw-field"), strings.HasPrefix(w, "--input"):
			writes = true
		case strings.HasPrefix(w, "--method="):
			method = strings.TrimPrefix(w, "--method=")
		case w == "--method" || w == "-X":
			if i+1 < len(args) {
				method = args[i+1]
			}
		case strings.HasPrefix(w, "-X"):
			method = strings.TrimPrefix(w, "-X")
		case strings.HasPrefix(w, "-f"), strings.HasPrefix(w, "-F"):
			writes = true
		}
	}
	if method != "" && !strings.EqualFold(method, "GET") && !strings.EqualFold(method, "HEAD") {
		writes = true
	}
	return method, writes
}

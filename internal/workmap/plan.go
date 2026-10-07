package workmap

import (
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Where and how the map suggests answering a request
// The JSON key of Root stays place for the plugin hook that reads a plan
type Plan struct {
	// Root of the place
	// Empty unless the request names it or past answers agree on it
	Root string `json:"place,omitempty"`
	Name string `json:"name,omitempty"`
	// Places past cases point at when they do not agree
	// A model picks one of them
	Candidates []Candidate `json:"candidates,omitempty"`
	// Skills and starting files from past requests answered there
	Skills []string `json:"skills,omitempty"`
	Files  []string `json:"files,omitempty"`
}

type Candidate struct {
	Name string `json:"name"`
	Root string `json:"root"`
	// Requests the user once answered there
	// They hint best at what the place is for
	Examples []string `json:"examples,omitempty"`
}

const (
	nearest  = 10
	nearBest = 0.6
	// Below this share of the votes the place is left to a model among the candidates
	agree = 0.6
)

// Directory names that are also everyday words so a request using the word rarely means the place
var commonNames = map[string]bool{
	"api": true, "app": true, "apps": true, "backend": true, "bin": true, "client": true, "code": true,
	"config": true, "core": true, "data": true, "dev": true, "doc": true, "docs": true, "frontend": true,
	"home": true, "lib": true, "main": true, "notes": true, "pkg": true, "private": true, "project": true,
	"projects": true, "public": true, "repo": true, "repos": true, "scripts": true, "server": true,
	"service": true, "src": true, "temp": true, "test": true, "tests": true, "tmp": true, "tools": true,
	"user": true, "users": true, "web": true, "work": true, "workspace": true,
}

// Skills come from the cases like the request and from skill descriptions that share words with it
func (s Store) Plan(text string) (Plan, error) {
	query := tokens(text)
	if len(query) == 0 {
		return Plan{}, nil
	}
	cases, err := s.Cases()
	if err != nil {
		return Plan{}, err
	}
	places, err := s.Places()
	if err != nil {
		return Plan{}, err
	}
	methods, err := s.Methods()
	if err != nil {
		return Plan{}, err
	}
	scored := score(cases, query)
	root := overridden(text, places)
	if root == "" {
		if root, err = s.place(strings.ToLower(text), query, places, scored); err != nil {
			return Plan{}, err
		}
	}
	if root == "" {
		return Plan{Candidates: candidates(scored, 3), Skills: describedSkills(methods, query, "")}, nil
	}
	skills, files := map[string]int{}, map[string]int{}
	for _, sc := range scored {
		if sc.c.Root != root {
			continue
		}
		for _, k := range sc.c.Skills {
			skills[k]++
		}
		for _, f := range sc.c.Files {
			if _, err := os.Stat(f); err == nil && !strings.Contains(f, "/.claude/") {
				files[f]++
			}
		}
	}
	return Plan{
		Root: root, Name: filepath.Base(root), Skills: merge(describedSkills(methods, query, root), top(skills, 3), 3),
		Files: top(files, 5),
	}, nil
}

var override = regexp.MustCompile(`(?i)\[(?:place|repo)=([\w.-]+)\]`)

// The root of a known place the request names as [place=<name>] or [repo=<name>]
// It only routes the request so an unknown name is ignored rather than trusted
func overridden(text string, places []Place) string {
	m := override.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	name := strings.ToLower(m[1])
	for _, p := range places {
		if strings.ToLower(p.Name) == name || slices.Contains(p.Aliases, name) {
			return p.Root
		}
	}
	return ""
}

// The root of the place decided in layers where the first that decides wins
//  1. A place the request names by its directory, remote, module or package as a whole word
//  2. The place the location map saw the most answers on the words of the request rest on
//  3. Votes of past cases like the request scored like BM25 over the user's own prompts
//     Only cases scoring near the best vote so many weak matches cannot outvote a strong one
//  4. A place named by a common word when nothing else decides
func (s Store) place(lower string, query []string, places []Place, scored []scoredCase) (string, error) {
	if root := namedPlace(lower, places, false); root != "" {
		return root, nil
	}
	root, err := s.locations.Route(query)
	if root != "" || err != nil {
		return root, err
	}
	if root := vote(scored); root != "" {
		return root, nil
	}
	return namedPlace(lower, places, true), nil
}

// The root of the place the request names by its longest name
// 1. A name matches as a whole word that a Korean particle may follow
// 2. Names that are common words count only when common is set
// Empty when none is named
func namedPlace(lower string, places []Place, common bool) string {
	best, length := "", 0
	for _, p := range places {
		for _, a := range append([]string{strings.ToLower(p.Name)}, p.Aliases...) {
			if commonNames[a] == common && len(a) > length && wholeWord(lower, a) {
				best, length = p.Root, len(a)
			}
		}
	}
	return best
}

// Whether lower holds word with no letter, digit, hyphen or underscore right before it
// Right after it only a letter outside ASCII such as a Korean particle may follow
func wholeWord(lower, word string) bool {
	for i := 0; word != ""; {
		at := strings.Index(lower[i:], word)
		if at < 0 {
			return false
		}
		at += i
		before, _ := utf8.DecodeLastRuneInString(lower[:at])
		after, _ := utf8.DecodeRuneInString(lower[at+len(word):])
		if (at == 0 || !wordRune(before)) && (at+len(word) == len(lower) || after > unicode.MaxASCII || !wordRune(after)) {
			return true
		}
		i = at + 1
	}
	return false
}

func wordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' }

// The place most votes of cases near the best agree on
// A single case is a coincidence so a place needs two cases near the best to win
func vote(scored []scoredCase) string {
	if len(scored) == 0 {
		return ""
	}
	votes, counts, total := map[string]float64{}, map[string]int{}, 0.0
	for _, sc := range scored {
		if sc.score >= nearBest*scored[0].score {
			votes[sc.c.Root] += sc.score
			counts[sc.c.Root]++
			total += sc.score
		}
	}
	best := ""
	for root, v := range votes {
		if best == "" || v > votes[best] || (v == votes[best] && root < best) {
			best = root
		}
	}
	if counts[best] < 2 || votes[best]/total < agree {
		return ""
	}
	return best
}

// The places of the best cases in order
// Each keeps a few of its matching requests
func candidates(scored []scoredCase, n int) []Candidate {
	var out []Candidate
	at := map[string]int{}
	for _, sc := range scored {
		i, ok := at[sc.c.Root]
		if !ok {
			if len(out) == n {
				continue
			}
			at[sc.c.Root] = len(out)
			out = append(out, Candidate{Name: filepath.Base(sc.c.Root), Root: sc.c.Root})
			i = len(out) - 1
		}
		if len(out[i].Examples) < 2 {
			out[i].Examples = append(out[i].Examples, clip(sc.c.Prompt, 120))
		}
	}
	return out
}

// Skills whose name or description shares at least two words with the request
// A skill kept in another place does not apply here
func describedSkills(methods []Method, query []string, root string) []string {
	counts := map[string]int{}
	for _, m := range methods {
		if m.Root != "" && m.Root != root {
			continue
		}
		doc := tokens(m.Name + " " + m.Description)
		n := 0
		for _, q := range query {
			if hasToken(doc, q) {
				n++
			}
		}
		if n >= 2 {
			counts[m.Name] = n
		}
	}
	return top(counts, 3)
}

func merge(first, then []string, n int) []string {
	out := slices.Clone(first)
	for _, v := range then {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	if len(out) > n {
		out = out[:n]
	}
	return out
}

type scoredCase struct {
	c     Case
	score float64
}

func score(cases []Case, query []string) []scoredCase {
	df := map[string]int{}
	for _, c := range cases {
		for _, q := range query {
			if hasToken(c.Tokens, q) {
				df[q]++
			}
		}
	}
	n := float64(len(cases))
	var out []scoredCase
	for _, c := range cases {
		total := 0.0
		for _, q := range query {
			if hasToken(c.Tokens, q) {
				total += math.Log(1 + (n-float64(df[q])+0.5)/(float64(df[q])+0.5))
			}
		}
		if total > 0 {
			out = append(out, scoredCase{c, total})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	if len(out) > nearest {
		out = out[:nearest]
	}
	return out
}

// Lower case words of two or more letters
func tokens(s string) []string {
	words := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-'
	})
	seen := map[string]bool{}
	var out []string
	for _, w := range words {
		w = strings.Trim(w, "-_")
		if len([]rune(w)) >= 2 && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

// One word starting with the other so a Korean particle after a word still matches
func hasToken(doc []string, q string) bool {
	for _, d := range doc {
		if strings.HasPrefix(q, d) || strings.HasPrefix(d, q) {
			return true
		}
	}
	return false
}

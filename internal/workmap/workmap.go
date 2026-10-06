// Package workmap learns where on this machine and with which skills the user answers each kind of request and how they write each kind of output
package workmap

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/fileio"
	"github.com/jeon-jihyeon/meetproxy/internal/locmap"
)

// One request the user typed and what answering it touched
// The JSON key of Root stays place so cases written before keep reading
type Case struct {
	Prompt string `json:"prompt"`
	// Words of the prompt a plan matches requests against
	Tokens []string `json:"tokens,omitempty"`
	// Root of the repository or the directory the session worked in
	Root   string    `json:"place"`
	Skills []string  `json:"skills,omitempty"`
	Files  []string  `json:"files,omitempty"`
	At     time.Time `json:"at"`
	// Transcript the prompt was typed in
	Source string `json:"source,omitempty"`
	// Byte offset of the prompt line in the transcript
	Offset int64 `json:"offset,omitempty"`
}

// A place the user works in and the names a message may call it by
type Place struct {
	Root    string   `json:"root"`
	Name    string   `json:"name"`
	Aliases []string `json:"aliases,omitempty"`
	Cases   int      `json:"cases"`
}

// A skill or command the user can run
type Method struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Set for one that only exists in the place of this root
	Root string `json:"root,omitempty"`
}

// How far each transcript was read
type state struct {
	Offsets map[string]int64 `json:"offsets"`
	// The place a transcript started in so later prompts typed in a scratch folder keep it
	Homes       map[string]string `json:"homes"`
	RefreshedAt time.Time         `json:"refreshed_at"`
}

type Store struct {
	dir string
	// Places past answers rested on by request topic
	locations locmap.Map
}

func New(dataDir string) Store {
	return Store{dir: filepath.Join(dataDir, "workmap"), locations: locmap.New(dataDir)}
}

var ErrBusy = errors.New("another refresh is running")

const (
	// Cases kept however old they are
	keptCases = 5000
	// Age under which every case is kept
	keptCaseAge = 180 * 24 * time.Hour
	// A tool use still without a result this long after its transcript last changed never gets one
	resultWait = 24 * time.Hour
)

// Reads the transcripts written since the last refresh and rescans places and methods
// 1. Daily skips when the last refresh happened on the same day in the location of now
// 2. The state is read under the lock so a refresh never reads from offsets another one already moved
// 3. Cases and formats are written before the state and a retry replaces what it reads again
// 4. Offsets and homes are kept only for transcripts that still exist
func (s Store) Refresh(config string, daily bool, now time.Time) (bool, error) {
	unlock, ok, err := fileio.TryLock(filepath.Join(s.dir, "refresh.lock"))
	if err != nil {
		return false, err
	}
	if !ok {
		return false, ErrBusy
	}
	defer unlock()
	st, err := s.state()
	if err != nil {
		return false, err
	}
	ay, am, ad := st.RefreshedAt.In(now.Location()).Date()
	by, bm, bd := now.Date()
	if daily && ay == by && am == bm && ad == bd {
		return false, nil
	}
	var formats []Format
	known, err := fileio.ReadJSON(filepath.Join(s.dir, "formats.json"), &formats)
	if err != nil {
		return false, err
	}
	// A map from before formats read its transcripts without keeping examples so they are read again
	// Reading again replaces the cases it covers
	if !known {
		st = state{}
	}
	reads, err := readTranscripts(config, st, now)
	if err != nil {
		return false, err
	}
	old, err := s.Cases()
	if err != nil {
		return false, err
	}
	if err := s.save(config, kept(updated(old, reads), now), formats, reads); err != nil {
		return false, err
	}
	next := state{Offsets: map[string]int64{}, Homes: map[string]string{}, RefreshedAt: now.UTC()}
	for _, r := range reads {
		next.Offsets[r.file] = r.to
		if r.home != "" {
			next.Homes[r.file] = r.home
		}
	}
	return true, fileio.WriteJSON(filepath.Join(s.dir, "state.json"), next)
}

// Writes the cases and the places, methods and formats scanned from them
func (s Store) save(config string, cases []Case, formats []Format, reads []read) error {
	if err := writeCases(filepath.Join(s.dir, "cases.jsonl"), cases); err != nil {
		return err
	}
	places := scanPlaces(cases)
	if err := fileio.WriteJSON(filepath.Join(s.dir, "places.json"), places); err != nil {
		return err
	}
	methods := scanMethods(config, places)
	if err := fileio.WriteJSON(filepath.Join(s.dir, "methods.json"), methods); err != nil {
		return err
	}
	return fileio.WriteJSON(filepath.Join(s.dir, "formats.json"), formatsOf(config, places, methods, formats, reads))
}

// Cases written before they kept their words get them from their prompt
func (s Store) Cases() ([]Case, error) {
	f, err := os.Open(filepath.Join(s.dir, "cases.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []Case
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<22)
	for sc.Scan() {
		var c Case
		if json.Unmarshal(sc.Bytes(), &c) != nil {
			continue
		}
		if c.Tokens == nil {
			c.Tokens = tokens(c.Prompt)
		}
		out = append(out, c)
	}
	return out, sc.Err()
}

func (s Store) Places() ([]Place, error) {
	var out []Place
	_, err := fileio.ReadJSON(filepath.Join(s.dir, "places.json"), &out)
	return out, err
}

func (s Store) Methods() ([]Method, error) {
	var out []Method
	_, err := fileio.ReadJSON(filepath.Join(s.dir, "methods.json"), &out)
	return out, err
}

func (s Store) state() (state, error) {
	var st state
	_, err := fileio.ReadJSON(filepath.Join(s.dir, "state.json"), &st)
	return st, err
}

// The cases one transcript holds from the offset it was read from
type read struct {
	file string
	from int64
	// Where the next refresh reads on from
	to int64
	// The place the transcript started in
	home    string
	cases   []Case
	outputs []output
}

// Transcripts only grow so each one is read on from where the last refresh stopped
// 1. A request still being answered when a refresh runs keeps what was written so far
// 2. A transcript rewritten shorter is read again from the start
// 3. A transcript removed while it is read is left out
// 4. The next read starts at the earliest tool use still waiting for its result so its output is not lost
func readTranscripts(config string, st state, now time.Time) ([]read, error) {
	files, err := filepath.Glob(filepath.Join(config, "projects", "*", "*.jsonl"))
	if err != nil {
		return nil, err
	}
	out := make([]read, 0, len(files))
	for _, file := range files {
		info, err := os.Stat(file)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		from, home := st.Offsets[file], st.Homes[file]
		if info.Size() < from {
			from, home = 0, ""
		}
		b := caseBuilder{source: file, home: home, pending: map[string][]output{}}
		end, err := b.read(file, from)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if waiting, ok := b.waiting(); ok && now.Sub(info.ModTime()) < resultWait {
			end = waiting
		}
		out = append(out, read{file, from, end, b.home, b.done(), b.outputs})
	}
	return out, nil
}

// The offset each read transcript was read from
func rereads(reads []read) map[string]int64 {
	from := make(map[string]int64, len(reads))
	for _, r := range reads {
		from[r.file] = r.from
	}
	return from
}

// Whether a read covers again what was found at offset of source so the read replaces it
func covered(from map[string]int64, source string, offset int64) bool {
	at, ok := from[source]
	return ok && offset >= at
}

// The old cases with those a read covers again replaced by what it found
func updated(old []Case, reads []read) []Case {
	from := rereads(reads)
	// A case kept before cases knew their transcript has no source
	// It is the same as a case read again with its prompt and time
	again := map[string]bool{}
	for _, r := range reads {
		for _, c := range r.cases {
			again[c.Prompt+"\x00"+c.At.String()] = true
		}
	}
	var out []Case
	for _, c := range old {
		if !covered(from, c.Source, c.Offset) && (c.Source != "" || !again[c.Prompt+"\x00"+c.At.String()]) {
			out = append(out, c)
		}
	}
	for _, r := range reads {
		out = append(out, r.cases...)
	}
	return out
}

// The cases younger than keptCaseAge or among the newest keptCases whichever keeps more in their order
func kept(cases []Case, now time.Time) []Case {
	if len(cases) <= keptCases {
		return cases
	}
	times := make([]time.Time, len(cases))
	for i, c := range cases {
		times[i] = c.At
	}
	slices.SortFunc(times, func(a, b time.Time) int { return b.Compare(a) })
	cutoff := now.Add(-keptCaseAge)
	if times[keptCases-1].Before(cutoff) {
		cutoff = times[keptCases-1]
	}
	return slices.DeleteFunc(cases, func(c Case) bool { return c.At.Before(cutoff) })
}

func writeCases(file string, cases []Case) error {
	var b []byte
	for _, c := range cases {
		line, err := json.Marshal(c)
		if err != nil {
			return err
		}
		b = append(append(b, line...), '\n')
	}
	return fileio.WriteAtomic(file, b, 0o644)
}

// At most n values with the most frequent first
func top(counts map[string]int, n int) []string {
	var keys []string
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	if len(keys) > n {
		keys = keys[:n]
	}
	return keys
}

// At most n runes of s on one line
func clip(s string, n int) string { return cut(strings.Join(strings.Fields(s), " "), n) }

// At most n runes of s with its line breaks kept
func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

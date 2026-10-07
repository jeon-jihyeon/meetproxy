package workmap_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/meetproxy/internal/workmap"
)

const scratch = "/private/tmp/claude-1/x/scratchpad"

// A machine with two places and a config with a skill of the user
type machine struct {
	config, svc, wiki string
}

func newMachine(t *testing.T) machine {
	t.Helper()
	base := t.TempDir()
	m := machine{config: newConfig(t), svc: filepath.Join(base, "svc"), wiki: filepath.Join(base, "wiki")}
	require.NoError(t, os.MkdirAll(filepath.Join(m.svc, ".git"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(m.svc, "go.mod"), []byte("module github.com/acme/adserver\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(m.svc, "alloc.go"), nil, 0o644))
	require.NoError(t, os.MkdirAll(m.wiki, 0o755))
	return m
}

func newConfig(t *testing.T) string {
	t.Helper()
	config := t.TempDir()
	writeFile(t, filepath.Join(config, "skills", "incident-triage", "SKILL.md"),
		"---\nname: incident-triage\ndescription: 에러 급증과 지연 원인 파악\n---\nbody\n")
	return config
}

func writeFile(t *testing.T, file, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
	require.NoError(t, os.WriteFile(file, []byte(content), 0o644))
}

func line(t *testing.T, v map[string]any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b) + "\n"
}

func prompt(cwd, text string, extra map[string]any) map[string]any {
	v := map[string]any{"type": "user", "cwd": cwd, "message": map[string]any{"content": text}, "timestamp": "2030-01-01T00:00:00Z"}
	for k, x := range extra {
		v[k] = x
	}
	return v
}

func uses(cwd string, tools ...map[string]any) map[string]any {
	content := make([]any, 0, len(tools))
	for _, x := range tools {
		content = append(content, x)
	}
	return map[string]any{"type": "assistant", "cwd": cwd, "message": map[string]any{"content": content}}
}

func tool(name string, input map[string]any) map[string]any {
	return map[string]any{"type": "tool_use", "name": name, "input": input}
}

// Appends to a transcript of config or replaces it when truncate is set
func transcript(t *testing.T, config, name string, truncate bool, lines ...string) {
	t.Helper()
	dir := filepath.Join(config, "projects", "-p")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	flags := os.O_APPEND | os.O_CREATE | os.O_WRONLY
	if truncate {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(filepath.Join(dir, name+".jsonl"), flags, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString(strings.Join(lines, ""))
	require.NoError(t, errors.Join(err, f.Close()))
}

type got struct {
	prompt string
	root   string
	skills []string
	files  []string
}

func view(cs []workmap.Case) []got {
	var out []got
	for _, c := range cs {
		out = append(out, got{c.Prompt, c.Root, c.Skills, c.Files})
	}
	return out
}

func prompts(cs []workmap.Case) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Prompt)
	}
	return out
}

func TestRefresh(t *testing.T) {
	t.Parallel()
	m := newMachine(t)
	alloc := filepath.Join(m.svc, "alloc.go")
	slack := "mcp__plugin_slack_slack__slack_read_thread"
	command := func(name, args string) string {
		return line(t, prompt(m.svc, "<command-name>/"+name+"</command-name><command-args>"+args+"</command-args>", nil))
	}

	tcs := []struct {
		name  string
		lines []string
		want  []got
	}{
		{
			"a typed prompt keeps its skills, tools and files",
			[]string{
				line(t, prompt(m.svc, "할당 우선순위 로직 설명", map[string]any{"promptSource": "typed"})),
				line(t, uses(m.svc, tool("Read", map[string]any{"file_path": alloc}),
					tool("Skill", map[string]any{"skill": "go-review"}), tool(slack, map[string]any{}))),
			},
			[]got{{"할당 우선순위 로직 설명", m.svc, []string{"go-review"}, []string{alloc}}},
		},
		{
			"a path input counts as a file",
			[]string{line(t, prompt(m.svc, "검색 범위 확인", nil)), line(t, uses(m.svc, tool("Grep", map[string]any{"path": m.svc})))},
			[]got{{"검색 범위 확인", m.svc, nil, []string{m.svc}}},
		},
		{
			"the skill an answer is attributed to counts",
			[]string{
				line(t, prompt(m.svc, "배포 절차 확인", nil)),
				line(t, map[string]any{"type": "assistant", "attributionSkill": "deploy", "message": map[string]any{"content": []any{}}}),
			},
			[]got{{"배포 절차 확인", m.svc, []string{"deploy"}, nil}},
		},
		{
			"prompts the user did not type are skipped",
			[]string{
				line(t, prompt(m.svc, "injected by a hook", map[string]any{"promptSource": "system"})),
				line(t, prompt(m.svc, "This session is being continued", map[string]any{"isCompactSummary": true})),
				line(t, prompt(m.svc, "Caveat from the harness", map[string]any{"isMeta": true})),
			},
			nil,
		},
		{
			"built-in, meetproxy and argument-less commands are skipped",
			[]string{command("compact", "keep it"), command("meetproxy:handle", "abc --auto"), command("review", "")},
			nil,
		},
		{
			"a slash command is its arguments with its name as the skill",
			[]string{command("incident-triage", "5xx 급증 원인")},
			[]got{{"5xx 급증 원인", m.svc, []string{"incident-triage"}, nil}},
		},
		{
			"markup and short prompts are skipped",
			[]string{
				line(t, prompt(m.svc, "<local-command-stdout>done</local-command-stdout>", nil)),
				line(t, prompt(m.svc, "ok", nil)),
				line(t, prompt(m.svc, "  네  ", nil)),
			},
			nil,
		},
		{
			"a scratch folder belongs to the place the transcript started in",
			[]string{line(t, prompt(m.svc, "첫 질문 정리", nil)), line(t, prompt(scratch, "스크래치에서 이어간 질문", nil))},
			[]got{{"첫 질문 정리", m.svc, nil, nil}, {"스크래치에서 이어간 질문", m.svc, nil, nil}},
		},
		{
			"a transcript only in a scratch folder has no place",
			[]string{line(t, prompt(scratch, "어디서 한 질문", nil))},
			nil,
		},
		{
			"a line still being written waits",
			[]string{line(t, prompt(m.wiki, "완성된 질문 하나", nil)), `{"type":"user","cwd":"`},
			[]got{{"완성된 질문 하나", m.wiki, nil, nil}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			config := newConfig(t)
			transcript(t, config, "a", false, tc.lines...)
			s := workmap.New(t.TempDir())
			ran, err := s.Refresh(config, false, time.Now())
			require.NoError(t, err)
			cases, err := s.Cases()
			require.NoError(t, err)
			assert.True(t, ran)
			assert.Equal(t, tc.want, view(cases))
		})
	}
}

// Steps share one store since each refresh reads on from the last one
// A table of independent cases cannot show that so they run in order
func TestRefreshIncremental(t *testing.T) {
	t.Parallel()
	m := newMachine(t)
	s := workmap.New(t.TempDir())
	day := time.Date(2030, 1, 1, 12, 0, 0, 0, time.Local)

	type want struct {
		ran     bool
		prompts []string
		roots   []string
	}
	steps := []struct {
		name     string
		lines    []string
		truncate bool
		daily    bool
		at       time.Time
		want     want
	}{
		{
			"the first refresh reads the transcript", []string{line(t, prompt(m.svc, "할당 우선순위 로직 설명", nil))}, false, true, day,
			want{true, []string{"할당 우선순위 로직 설명"}, []string{m.svc}},
		},
		{
			"a daily refresh on the same day is skipped", []string{line(t, prompt(m.wiki, "온콜 런북 정리", nil))}, false, true, day.Add(time.Hour),
			want{false, []string{"할당 우선순위 로직 설명"}, []string{m.svc}},
		},
		{
			"the next day reads only the new lines and keeps the place a scratch prompt started in",
			[]string{line(t, prompt(scratch, "스크래치에서 이어간 질문", nil)), `{"type":"user",`}, false, true, day.AddDate(0, 0, 1),
			want{
				true, []string{"할당 우선순위 로직 설명", "온콜 런북 정리", "스크래치에서 이어간 질문"},
				[]string{m.svc, m.wiki, m.svc},
			},
		},
		{
			"a line finished later is read once",
			[]string{`"cwd":"` + m.svc + `","message":{"content":"마저 쓴 질문"}}` + "\n"}, false, false, day.AddDate(0, 0, 1),
			want{
				true, []string{"할당 우선순위 로직 설명", "온콜 런북 정리", "스크래치에서 이어간 질문", "마저 쓴 질문"},
				[]string{m.svc, m.wiki, m.svc, m.svc},
			},
		},
		{
			"a transcript rewritten shorter replaces its cases", []string{line(t, prompt(m.wiki, "남은 질문", nil))}, true, false, day.AddDate(0, 0, 1),
			want{true, []string{"남은 질문"}, []string{m.wiki}},
		},
	}
	for _, st := range steps {
		transcript(t, m.config, "a", st.truncate, st.lines...)
		ran, err := s.Refresh(m.config, st.daily, st.at)
		require.NoError(t, err, st.name)
		cases, err := s.Cases()
		require.NoError(t, err, st.name)
		roots := []string{}
		for _, c := range cases {
			roots = append(roots, c.Root)
		}
		assert.Equal(t, st.want, want{ran, prompts(cases), roots}, st.name)
	}
}

// A refresh that stops after writing the cases reads the same lines again
func TestRefreshRetry(t *testing.T) {
	t.Parallel()
	m := newMachine(t)
	data := t.TempDir()
	s := workmap.New(data)
	transcript(t, m.config, "a", false, line(t, prompt(m.svc, "할당 우선순위 로직 설명", nil)), line(t, prompt(scratch, "스크래치 질문", nil)))
	_, err := s.Refresh(m.config, false, time.Now())
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(data, "workmap", "state.json")))

	_, err = s.Refresh(m.config, false, time.Now())
	require.NoError(t, err)
	cases, err := s.Cases()
	require.NoError(t, err)
	assert.Equal(t, []got{
		{"할당 우선순위 로직 설명", m.svc, nil, nil},
		{"스크래치 질문", m.svc, nil, nil},
	}, view(cases))
}

// Old cases beyond the newest ones leave and a removed transcript leaves the state but not its cases
func TestRefreshPrunes(t *testing.T) {
	t.Parallel()
	m := newMachine(t)
	now := time.Date(2030, 6, 1, 0, 0, 0, 0, time.UTC)
	typed := func(cwd, text string, year int) string {
		at := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
		return line(t, map[string]any{"type": "user", "cwd": cwd, "message": map[string]any{"content": text}, "timestamp": at})
	}
	recent := make([]string, 5000)
	for i := range recent {
		recent[i] = typed(m.svc, fmt.Sprintf("최근 질문 %d", i), 2030)
	}

	type want struct {
		cases   int
		first   string
		offsets []string
	}
	tcs := []struct {
		name string
		// A case before the 5000 recent ones and the one of the removed transcript
		text string
		year int
		want want
	}{
		{"an old case beyond the newest ones leaves", "오래된 질문", 2020, want{5001, "최근 질문 0", []string{"a.jsonl"}}},
		{"a young case beyond the newest ones stays", "젊은 질문", 2030, want{5002, "젊은 질문", []string{"a.jsonl"}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			config := newConfig(t)
			data := t.TempDir()
			transcript(t, config, "a", false, append([]string{typed(m.svc, tc.text, tc.year)}, recent...)...)
			transcript(t, config, "b", false, typed(m.wiki, "지워질 대화", 2030))
			s := workmap.New(data)
			_, err := s.Refresh(config, false, now)
			require.NoError(t, err)
			require.NoError(t, os.Remove(filepath.Join(config, "projects", "-p", "b.jsonl")))
			_, err = s.Refresh(config, false, now)
			require.NoError(t, err)

			cases, err := s.Cases()
			require.NoError(t, err)
			b, err := os.ReadFile(filepath.Join(data, "workmap", "state.json"))
			require.NoError(t, err)
			var st struct {
				Offsets map[string]int64 `json:"offsets"`
			}
			require.NoError(t, json.Unmarshal(b, &st))
			var offsets []string
			for file := range st.Offsets {
				offsets = append(offsets, filepath.Base(file))
			}
			require.NotEmpty(t, cases)
			assert.Equal(t, tc.want, want{len(cases), cases[0].Prompt, offsets})
		})
	}
}

// Every session start asks for a daily refresh at once and only one of them runs it
func TestRefreshConcurrent(t *testing.T) {
	t.Parallel()
	m := newMachine(t)
	s := workmap.New(t.TempDir())
	lines := make([]string, 500)
	for i := range lines {
		lines[i] = line(t, prompt(m.svc, fmt.Sprintf("할당 우선순위 질문 %d", i), nil))
	}
	transcript(t, m.config, "a", false, lines...)
	now := time.Now()

	start := make(chan struct{})
	results := make(chan bool, 16)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ran, err := s.Refresh(m.config, true, now)
			if err != nil && !errors.Is(err, workmap.ErrBusy) {
				t.Error(err)
			}
			results <- ran
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	runs := 0
	for ran := range results {
		if ran {
			runs++
		}
	}
	cases, err := s.Cases()
	require.NoError(t, err)
	assert.Equal(t, 1, runs)
	assert.Len(t, cases, len(lines))
}

func TestRefreshLock(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		// Lock operation another process left on the lock file
		op   int
		want error
	}{
		{"a refresh holding the lock makes another busy", syscall.LOCK_EX, workmap.ErrBusy},
		{"a lock file left by a crashed refresh does not block", syscall.LOCK_UN, nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			file := filepath.Join(data, "workmap", "refresh.lock")
			writeFile(t, file, "")
			f, err := os.Open(file)
			require.NoError(t, err)
			defer func() { _ = f.Close() }()
			require.NoError(t, syscall.Flock(int(f.Fd()), tc.op))

			_, err = workmap.New(data).Refresh(newConfig(t), false, time.Now())
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

func TestPlaces(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name      string
		gitConfig string
		want      []string
	}{
		{
			"the origin remote names the repository over an earlier remote",
			"[remote \"upstream\"]\n\turl = https://github.com/acme/upstream-name.git\n" +
				"[remote \"origin\"]\n\turl = git@github.com:me/origin-name.git\n",
			[]string{"origin-name"},
		},
		{
			"the first remote names it without an origin",
			"[submodule \"lib\"]\n\turl = https://github.com/acme/lib-name.git\n" +
				"[remote \"upstream\"]\n\turl = https://github.com/acme/upstream-name\n",
			[]string{"upstream-name"},
		},
		{"a url with a trailing slash still names it", "[remote \"origin\"]\n\turl = https://github.com/acme/slash-name/\n", []string{"slash-name"}},
		{"a submodule url alone names nothing", "[submodule \"lib\"]\n\turl = https://github.com/acme/lib-name.git\n", nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := filepath.Join(t.TempDir(), "repo")
			writeFile(t, filepath.Join(root, ".git", "config"), tc.gitConfig)
			config := newConfig(t)
			transcript(t, config, "a", false, line(t, prompt(root, "저장소 이름 확인", nil)))
			s := workmap.New(t.TempDir())
			_, err := s.Refresh(config, false, time.Now())
			require.NoError(t, err)
			places, err := s.Places()
			require.NoError(t, err)
			assert.Equal(t, []workmap.Place{{Root: root, Name: "repo", Aliases: tc.want, Cases: 1}}, places)
		})
	}
}

func TestPlacesOrder(t *testing.T) {
	t.Parallel()
	m := newMachine(t)
	transcript(t, m.config, "a", false,
		line(t, prompt(m.wiki, "온콜 런북 정리", nil)), line(t, prompt(m.svc, "할당 우선순위 로직", nil)),
		line(t, prompt(m.wiki, "런북 링크 정리", nil)), line(t, prompt(m.svc, "할당 실패 원인", nil)))
	s := workmap.New(t.TempDir())
	_, err := s.Refresh(m.config, false, time.Now())
	require.NoError(t, err)
	places, err := s.Places()
	require.NoError(t, err)
	assert.Equal(t, []workmap.Place{
		{Root: m.svc, Name: "svc", Aliases: []string{"adserver"}, Cases: 2},
		{Root: m.wiki, Name: "wiki", Cases: 2},
	}, places)
}

func TestMethods(t *testing.T) {
	t.Parallel()
	m := newMachine(t)
	plugin := t.TempDir()
	writeFile(t, filepath.Join(m.config, "plugins", "installed_plugins.json"),
		`{"plugins":{"slack@market":[{"installPath":"`+plugin+`"}]}}`)
	writeFile(t, filepath.Join(plugin, "skills", "standup", "SKILL.md"), "---\ndescription: >\n  Daily standup\n  from activity\n---\n")
	writeFile(t, filepath.Join(plugin, "commands", "digest.md"), "---\ndescription: |-\n  Channel digest\n  per day\nname: digest\n---\n")
	writeFile(t, filepath.Join(m.config, "commands", "deploy.md"), "---\ndescription: \"Deploy notice\"\n---\n")
	writeFile(t, filepath.Join(m.svc, ".claude", "skills", "deploy", "SKILL.md"), "---\ndescription: Deploy svc\n---\n")
	writeFile(t, filepath.Join(m.svc, ".claude", "commands", "notes.txt"), "not a command")
	transcript(t, m.config, "a", false, line(t, prompt(m.svc, "할당 우선순위 로직", nil)))
	s := workmap.New(t.TempDir())
	_, err := s.Refresh(m.config, false, time.Now())
	require.NoError(t, err)

	methods, err := s.Methods()
	require.NoError(t, err)
	assert.Equal(t, []workmap.Method{
		{Name: "deploy", Description: "Deploy notice"},
		{Name: "deploy", Description: "Deploy svc", Root: m.svc},
		{Name: "incident-triage", Description: "에러 급증과 지연 원인 파악"},
		{Name: "slack:digest", Description: "Channel digest per day"},
		{Name: "slack:standup", Description: "Daily standup from activity"},
	}, methods)
}

func TestPlan(t *testing.T) {
	t.Parallel()
	m := newMachine(t)
	docs := filepath.Join(filepath.Dir(m.svc), "docs")
	require.NoError(t, os.MkdirAll(docs, 0o755))
	transcript(t, m.config, "a", false,
		line(t, prompt(m.svc, "할당 우선순위 로직 설명", nil)),
		line(t, uses(m.svc, tool("Read", map[string]any{"file_path": filepath.Join(m.svc, "alloc.go")}))),
		line(t, prompt(m.svc, "할당 실패 원인", nil)),
		line(t, prompt(m.svc, "우선순위 계산 위치", nil)),
		line(t, prompt(m.svc, "할당 우선순위 변경 이력", nil)),
		line(t, prompt(m.wiki, "온콜 런북 정리", nil)),
		line(t, prompt(m.wiki, "할당 런북 링크", nil)),
		line(t, prompt(docs, "문서 목차 정리", nil)),
	)
	data := t.TempDir()
	// An answer the location map recorded in the wiki
	writeFile(t, filepath.Join(data, "map.jsonl"), line(t, map[string]any{
		"relay_id": "r1", "topic": "정산 리포트 위치", "keywords": []string{"정산"},
		"paths": []any{map[string]any{"repo": "wiki", "root": m.wiki, "rel": "."}},
	}))
	s := workmap.New(data)
	_, err := s.Refresh(m.config, false, time.Now())
	require.NoError(t, err)

	type want struct {
		name       string
		root       string
		candidates []string
		skills     []string
		files      []string
	}
	alloc := []string{filepath.Join(m.svc, "alloc.go")}
	tcs := []struct {
		name string
		text string
		want want
	}{
		{"a place named by its module wins", "adserver 에러 봐줘", want{"svc", m.svc, nil, nil, nil}},
		{"a name followed by a Korean particle names the place", "svc에서 에러 봐줘", want{"svc", m.svc, nil, nil, nil}},
		{"a name inside another word names nothing", "myadserver 에러 봐줘", want{"", "", nil, nil, nil}},
		{"a named place wins over the location map", "svc 정산 봐줘", want{"svc", m.svc, nil, nil, nil}},
		{"the location map picks the place its answers rest on", "정산 리포트 봐줘", want{"wiki", m.wiki, nil, nil, nil}},
		{"two agreeing cases pick the place and its files", "할당 우선순위가 왜 바뀌나", want{"svc", m.svc, nil, nil, alloc}},
		{"a common word names a place only when nothing else decides", "update the docs", want{"docs", docs, nil, nil, nil}},
		{"agreeing cases win over a common word", "할당 우선순위 docs", want{"svc", m.svc, nil, nil, alloc}},
		{"the location map wins over a common word", "정산 docs", want{"wiki", m.wiki, nil, nil, nil}},
		{"a single case leaves candidates", "온콜 담당 누구", want{"", "", []string{"wiki"}, nil, nil}},
		{"a skill matches by its description", "지연 원인 파악 부탁", want{"", "", []string{"svc"}, []string{"incident-triage"}, nil}},
		{"nothing matches", "점심 메뉴", want{"", "", nil, nil, nil}},
		{"a known place given as place= routes there", "[place=wiki] 할당 우선순위가 왜 바뀌나", want{"wiki", m.wiki, nil, nil, nil}},
		{"a known place given as repo= routes there", "[repo=svc] 온콜 담당 누구", want{"svc", m.svc, nil, nil, nil}},
		{"an unknown place given is ignored", "[place=elsewhere] 할당 우선순위가 왜 바뀌나", want{"svc", m.svc, nil, nil, alloc}},
		{"a request without words plans nothing", "?? !!", want{"", "", nil, nil, nil}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, err := s.Plan(tc.text)
			require.NoError(t, err)
			var names []string
			for _, c := range p.Candidates {
				names = append(names, c.Name)
			}
			assert.Equal(t, tc.want, want{p.Name, p.Root, names, p.Skills, p.Files})
		})
	}
}

// A map from before formats is read again from the start to find examples
// Cases it kept before they knew their transcript are not doubled and those of a deleted transcript stay
func TestRefreshUpgrade(t *testing.T) {
	t.Parallel()
	m := newMachine(t)
	data := t.TempDir()
	at := "2030-01-01T00:00:00Z"
	transcript(t, m.config, "a", false, line(t, prompt(m.svc, "할당 우선순위 로직 설명", nil)),
		line(t, map[string]any{"type": "assistant", "cwd": m.svc, "timestamp": at, "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_use", "id": "u1", "name": "Bash", "input": map[string]any{"command": "git commit -m one"}},
		}}}),
		line(t, map[string]any{"type": "user", "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "u1"}}}}))
	legacy := line(t, map[string]any{"prompt": "할당 우선순위 로직 설명", "place": m.svc, "at": at}) +
		line(t, map[string]any{"prompt": "지워진 대화의 질문", "place": m.wiki, "at": at})
	writeFile(t, filepath.Join(data, "workmap", "cases.jsonl"), legacy)
	file := filepath.Join(m.config, "projects", "-p", "a.jsonl")
	info, err := os.Stat(file)
	require.NoError(t, err)
	writeFile(t, filepath.Join(data, "workmap", "state.json"), fmt.Sprintf(`{"offsets":{%q:%d}}`, file, info.Size()))
	s := workmap.New(data)

	_, err = s.Refresh(m.config, false, time.Now())
	require.NoError(t, err)
	cases, err := s.Cases()
	require.NoError(t, err)
	formats, err := s.Formats()
	require.NoError(t, err)
	require.NotEmpty(t, formats)
	assert.Equal(t, []string{"지워진 대화의 질문", "할당 우선순위 로직 설명"}, prompts(cases))
	assert.Len(t, formats[0].Examples, 1)
}

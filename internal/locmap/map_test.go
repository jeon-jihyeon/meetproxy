package locmap_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/meetproxy/internal/locmap"
)

func TestMapLocate(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	for _, rel := range []string{"alloc.go", "pipeline.go", "point.go", "mongo.go", "svc.go"} {
		require.NoError(t, os.WriteFile(filepath.Join(repo, rel), nil, 0o644))
	}

	type spec struct {
		relayId  string
		topic    string
		keywords []string
		rels     []string
	}
	specs := []spec{
		{"r1", "할당 안 됨 원인", []string{"할당", "allocation"}, []string{"alloc.go", "pipeline.go"}},
		{"r2", "할당 파이프라인 지연", []string{"할당", "pipeline"}, []string{"pipeline.go", "deleted.go"}},
		{"r3", "포인트 적립 누락", []string{"포인트", "go"}, []string{"point.go"}},
		{"r4", "몽고 조회 지연", []string{"mongo"}, []string{"point.go"}},
		{"r4", "몽고 조회 지연", []string{"mongo"}, []string{"mongo.go"}},
		{"", "정산 초기값", []string{"정산"}, []string{"alloc.go"}},
		{"", "정산 초기값", []string{"정산"}, []string{"point.go"}},
		{"r5", "pointsvc 장애", []string{"pointsvc"}, []string{"svc.go"}},
	}

	type args struct {
		terms []string
		limit int
	}
	tcs := []struct {
		name string
		args args
		want []string
	}{
		{"matches a term with a particle and sorts by score", args{[]string{"할당이", "pipeline"}, 10}, []string{"pipeline.go", "alloc.go"}},
		{"drops missing paths", args{[]string{"파이프라인"}, 10}, []string{"pipeline.go"}},
		{"applies the limit", args{[]string{"할당"}, 1}, []string{"pipeline.go"}},
		{"a keyword does not match inside a longer term", args{[]string{"mongodb"}, 10}, []string{}},
		{"a term does not match inside a topic word or keyword", args{[]string{"svc"}, 10}, []string{}},
		{"a keyword matches a term with a particle", args{[]string{"pointsvc의"}, 10}, []string{"svc.go"}},
		{"a term matches a whole topic word", args{[]string{"장애"}, 10}, []string{"svc.go"}},
		{"uses the last record per relay id", args{[]string{"몽고"}, 10}, []string{"mongo.go"}},
		{"keeps entries without a relay id apart", args{[]string{"정산"}, 10}, []string{"alloc.go", "point.go"}},
		{"no match", args{[]string{"배포"}, 10}, []string{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := locmap.New(t.TempDir())
			for _, s := range specs {
				ps := make([]locmap.Path, 0, len(s.rels))
				for _, rel := range s.rels {
					ps = append(ps, locmap.Path{Name: "svc", Root: repo, Rel: rel})
				}
				e := locmap.Entry{RelayId: s.relayId, Topic: s.topic, Keywords: s.keywords, Paths: ps, RecordedAt: time.Now()}
				require.NoError(t, m.Add(e))
			}
			cs, err := m.Locate(tc.args.terms, tc.args.limit)
			require.NoError(t, err)
			got := []string{}
			for _, c := range cs {
				got = append(got, c.Rel)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestMapLocate_CorruptLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "map.jsonl"), []byte("{\"relay_id\":\"r1\"}\n{broken\n"), 0o644))

	_, err := locmap.New(dir).Locate([]string{"x"}, 10)

	assert.ErrorContains(t, err, "line 2 is corrupt")
}

func TestMapRoute(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	m := locmap.New(t.TempDir())
	path := func(name, rel string) locmap.Path {
		return locmap.Path{Name: name, Root: filepath.Join(base, name), Rel: rel}
	}
	entries := []locmap.Entry{
		{RelayId: "r1", Topic: "할당 지연", Keywords: []string{"할당"}, Paths: []locmap.Path{path("svc", "alloc.go")}},
		{RelayId: "r2", Topic: "할당 오류", Keywords: []string{"할당"}, Paths: []locmap.Path{path("svc", "pipeline.go")}},
		{RelayId: "r3", Topic: "할당 화면", Keywords: []string{"화면"}, Paths: []locmap.Path{path("web", "page.tsx")}},
		{RelayId: "r4", Topic: "캐시 만료", Keywords: []string{"캐시"}, Paths: []locmap.Path{path("web", "page.tsx")}},
		{RelayId: "r5", Topic: "캐시 갱신", Keywords: []string{"캐시"}, Paths: []locmap.Path{
			path("worker", "alloc.go"), path("worker", "pipeline.go"),
		}},
	}
	for _, e := range entries {
		require.NoError(t, m.Add(e))
	}

	tcs := []struct {
		name  string
		terms []string
		want  string
	}{
		{"picks the place with more matching answers", []string{"할당"}, filepath.Join(base, "svc")},
		{"matches a keyword", []string{"화면"}, filepath.Join(base, "web")},
		{"counts an answer once however many files it has", []string{"캐시"}, filepath.Join(base, "web")},
		{"returns nothing without a match", []string{"배포"}, ""},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := m.Route(tc.terms)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestMapCompact(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(repo, "a.go"), nil, 0o600))
	entry := func(id, rel string, n int) locmap.Entry {
		return locmap.Entry{RelayId: id, Topic: "topic", Keywords: []string{strings.Repeat("k", n)}, Paths: []locmap.Path{{Root: repo, Rel: rel}}}
	}
	type want struct {
		wrote bool
		lines int
	}
	tcs := []struct {
		name    string
		entries []locmap.Entry
		want    want
	}{
		{"a small map is left alone", []locmap.Entry{entry("r1", "a.go", 10), entry("r1", "a.go", 10)}, want{false, 2}},
		{
			"a large map keeps the last record of each relay that still exists",
			append(slices.Repeat([]locmap.Entry{entry("r1", "a.go", 1000)}, 300), entry("r2", "gone.go", 10), entry("", "a.go", 10)),
			want{true, 2},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			m := locmap.New(data)
			for _, e := range tc.entries {
				require.NoError(t, m.Add(e))
			}

			wrote, err := m.Compact()

			require.NoError(t, err)
			b, err := os.ReadFile(filepath.Join(data, "map.jsonl"))
			require.NoError(t, err)
			found, err := m.Locate([]string{"topic"}, 0)
			require.NoError(t, err)
			assert.Equal(t, tc.want, want{wrote, strings.Count(string(b), "\n")})
			assert.Len(t, found, 1)
		})
	}
}

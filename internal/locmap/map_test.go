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

// A corrected answer counts half so the next one of the same words ranks first
func TestMapCorrect(t *testing.T) {
	t.Parallel()
	now := time.Now()
	repo := t.TempDir()
	for _, rel := range []string{"retry.go", "config.go"} {
		require.NoError(t, os.WriteFile(filepath.Join(repo, rel), nil, 0o644))
	}
	tcs := []struct {
		name    string
		correct []string
		want    []string
		weight  float64
		failed  bool
	}{
		{"ties sort by path", nil, []string{"config.go", "retry.go"}, 0, false},
		{"a corrected relay drops behind", []string{"r1"}, []string{"retry.go", "config.go"}, 0.5, false},
		{"each correction halves again", []string{"r1", "r1"}, []string{"retry.go", "config.go"}, 0.25, false},
		{"a relay the map never recorded cannot be corrected", []string{"nope"}, []string{"config.go", "retry.go"}, 0, true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := locmap.New(t.TempDir())
			for _, e := range []struct{ relay, rel string }{{"r1", "config.go"}, {"r2", "retry.go"}} {
				require.NoError(t, m.Add(locmap.Entry{RelayId: e.relay, Topic: "retry", Keywords: []string{"retry"}, Paths: []locmap.Path{{Root: repo, Rel: e.rel}}, RecordedAt: now}))
			}
			var e locmap.Entry
			var err error
			for _, r := range tc.correct {
				e, err = m.Correct(r, now)
			}

			cs, lerr := m.Locate([]string{"retry"}, 0)
			require.NoError(t, lerr)

			rels := []string{}
			for _, c := range cs {
				rels = append(rels, c.Rel)
			}
			assert.Equal(t, tc.failed, err != nil)
			assert.Equal(t, tc.want, rels)
			assert.Equal(t, tc.weight, e.Weight)
		})
	}
}

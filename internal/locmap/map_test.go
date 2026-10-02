package locmap_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/meetproxy/internal/locmap"
)

func TestMapLocate(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	for _, rel := range []string{"alloc.go", "pipeline.go", "point.go", "mongo.go"} {
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
		{"short keyword does not match inside a word", args{[]string{"mongodb"}, 10}, []string{"mongo.go"}},
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
					ps = append(ps, locmap.Path{Repo: "svc", Root: repo, Rel: rel})
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

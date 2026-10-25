package github_test

import "github.com/jeon-jihyeon/meetproxy/internal/messageurl"

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSnapshot(t *testing.T) {
	t.Parallel()
	c, _ := client(map[string]string{
		"GET repos/o/svc/issues/7": `{"id":7,"body":"root","updated_at":"2030-01-01T00:01:00Z",` +
			`"reactions":{"eyes":1}}`,
		"GET repos/o/svc/issues/7/comments": `[{"id":8,"body":"already fixed","updated_at":"2030-01-01T00:02:00Z"}]`,
	})
	got, err := c.Snapshot(messageurl.Link{Repo: "o/svc", Number: 7, Comment: "8"})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "root", got[0].Body)
	assert.JSONEq(t, `{"eyes":1}`, string(got[0].Reactions))
	assert.Equal(t, "already fixed", got[1].Body)
	assert.Equal(t, "2030-01-01T00:02:00Z", got[1].UpdatedAt.Format("2006-01-02T15:04:05Z"))
}

func TestSnapshotIncomplete(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name    string
		answers map[string]string
	}{
		{"read error", map[string]string{"GET repos/o/svc/issues/7": `{"id":7}`}},
		{"partial comments", map[string]string{
			"GET repos/o/svc/issues/7": `{"id":7,"comments":2}`, "GET repos/o/svc/issues/7/comments": `[{"id":8}]`,
		}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, _ := client(tc.answers)
			got, err := c.Snapshot(messageurl.Link{Repo: "o/svc", Number: 7})
			assert.Error(t, err)
			assert.Nil(t, got)
		})
	}
}

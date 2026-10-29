package resume_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/meetproxy/internal/resume"
)

var now = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

func put(t *testing.T, s resume.Store, session string, ttl time.Duration) string {
	t.Helper()
	token, err := s.Put(resume.Continuation{Session: session, Chain: "post", State: json.RawMessage(`{"text":"hi"}`)}, ttl, now)
	require.NoError(t, err)
	return token
}

func TestPut_TokenShapeAndFileName(t *testing.T) {
	data := t.TempDir()
	s := resume.New(data)
	token := put(t, s, "S1", 0)
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{32}$`), token)
	entries, err := os.ReadDir(filepath.Join(data, "mod", "resume"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	name := entries[0].Name()
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{64}\.json$`), name)
	assert.NotContains(t, name, token)
	b, err := os.ReadFile(filepath.Join(data, "mod", "resume", name))
	require.NoError(t, err)
	assert.NotContains(t, string(b), token)
	info, err := os.Stat(filepath.Join(data, "mod", "resume", name))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestTake_SingleUse(t *testing.T) {
	s := resume.New(t.TempDir())
	token := put(t, s, "S1", 0)
	got, err := s.Take(token, "S1", now)
	require.NoError(t, err)
	assert.Equal(t, "post", got.Chain)
	assert.JSONEq(t, `{"text":"hi"}`, string(got.State))
	_, err = s.Take(token, "S1", now)
	assert.ErrorIs(t, err, resume.ErrUnknown)
}

func TestTake_OtherSessionLeavesIt(t *testing.T) {
	s := resume.New(t.TempDir())
	token := put(t, s, "S1", 0)
	_, err := s.Take(token, "S2", now)
	assert.ErrorIs(t, err, resume.ErrUnknown)
	_, err = s.Take(token, "S1", now)
	assert.NoError(t, err)
}

func TestTake_Expired(t *testing.T) {
	s := resume.New(t.TempDir())
	token := put(t, s, "S1", 5*time.Minute)
	_, err := s.Take(token, "S1", now.Add(5*time.Minute))
	assert.ErrorIs(t, err, resume.ErrUnknown)
	long := put(t, s, "S1", 0)
	_, err = s.Take(long, "S1", now.Add(23*time.Hour))
	assert.NoError(t, err)
}

// An expired token of the session comes back with ErrExpired so its chain can end what it left outside
// Another session's expired token stays for that session's end or a prune
func TestTake_ExpiredComesBackToEnd(t *testing.T) {
	data := t.TempDir()
	s := resume.New(data)
	mine := put(t, s, "S1", 5*time.Minute)
	other := put(t, s, "S2", 5*time.Minute)
	later := now.Add(5 * time.Minute)

	got, err := s.Take(mine, "S1", later)
	assert.ErrorIs(t, err, resume.ErrExpired)
	assert.ErrorIs(t, err, resume.ErrUnknown)
	assert.Equal(t, "post", got.Chain)
	_, err = s.Take(other, "S1", later)
	assert.ErrorIs(t, err, resume.ErrUnknown)
	assert.NotErrorIs(t, err, resume.ErrExpired)

	dropped, err := s.DropSession("S2")
	require.NoError(t, err)
	require.Len(t, dropped, 1)
	assert.Equal(t, "S2", dropped[0].Session)
	_, err = s.Take(mine, "S1", later)
	assert.NotErrorIs(t, err, resume.ErrExpired, "an expired token runs its end once")
}

func TestTake_BadToken(t *testing.T) {
	s := resume.New(t.TempDir())
	for _, token := range []string{"", "../x", strings.Repeat("A", 32), strings.Repeat("0", 31)} {
		_, err := s.Take(token, "S1", now)
		assert.ErrorIs(t, err, resume.ErrUnknown, token)
	}
}

func TestDropSession(t *testing.T) {
	s := resume.New(t.TempDir())
	mine := put(t, s, "S1", 0)
	other := put(t, s, "S2", 0)
	assert.False(t, s.Holds("S3"))
	assert.True(t, s.Holds("S1"))
	dropped, err := s.DropSession("S1")
	require.NoError(t, err)
	require.Len(t, dropped, 1)
	assert.Equal(t, "S1", dropped[0].Session)
	assert.False(t, s.Holds("S1"))
	_, err = s.Take(mine, "S1", now)
	assert.ErrorIs(t, err, resume.ErrUnknown)
	_, err = s.Take(other, "S2", now)
	assert.NoError(t, err)
}

func TestPrune(t *testing.T) {
	data := t.TempDir()
	s := resume.New(data)
	put(t, s, "S1", 5*time.Minute)
	kept := put(t, s, "S1", 0)
	require.NoError(t, os.WriteFile(filepath.Join(data, "mod", "resume", strings.Repeat("a", 64)+".json"), []byte("{"), 0o600))
	dropped, err := s.Prune(now.Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, dropped, 1, "the expired one comes back so its chain can end, the unreadable one cannot")
	assert.Equal(t, "S1", dropped[0].Session)
	entries, err := os.ReadDir(filepath.Join(data, "mod", "resume"))
	require.NoError(t, err)
	assert.Len(t, entries, 1)
	_, err = s.Take(kept, "S1", now.Add(time.Hour))
	assert.NoError(t, err)
}

func TestPrune_NoDirectory(t *testing.T) {
	pruned, err := resume.New(t.TempDir()).Prune(now)
	assert.NoError(t, err)
	assert.Empty(t, pruned)
	dropped, err := resume.New(t.TempDir()).DropSession("S1")
	assert.NoError(t, err)
	assert.Empty(t, dropped)
}

// An expired continuation still counts until something removes it, so its chain's end is never skipped
func TestHolds_CountsAnExpiredOne(t *testing.T) {
	s := resume.New(t.TempDir())
	put(t, s, "S1", time.Minute)
	assert.True(t, s.Holds("S1"))
	assert.False(t, s.Holds("S2"))
}

func TestPruneSession(t *testing.T) {
	s := resume.New(t.TempDir())
	put(t, s, "S1", time.Minute)
	live := put(t, s, "S1", 0)
	other := put(t, s, "S2", time.Minute)
	later := now.Add(time.Hour)

	dropped, err := s.PruneSession("S1", later)

	require.NoError(t, err)
	require.Len(t, dropped, 1)
	assert.Equal(t, "S1", dropped[0].Session)
	_, err = s.Take(live, "S1", later)
	assert.NoError(t, err, "a live one stays")
	_, err = s.Take(other, "S2", later)
	assert.ErrorIs(t, err, resume.ErrExpired, "another session's stays")
}

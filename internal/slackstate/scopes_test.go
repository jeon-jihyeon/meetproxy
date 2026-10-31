package slackstate_test

import (
	"slices"
	"testing"

	"github.com/jeon-jihyeon/meetproxy/internal/slackstate"
	"github.com/stretchr/testify/assert"
)

func TestScopesMissing(t *testing.T) {
	t.Parallel()
	required := []string{"search:read", "channels:history", "groups:history", "im:history", "mpim:history",
		"users:read", "chat:write", "reactions:write", "channels:read", "groups:read", "im:read", "mpim:read"}
	reordered := slices.Clone(required)
	slices.Reverse(reordered)
	tcs := []struct {
		name             string
		granted, missing []string
	}{
		{"none", nil, required},
		{"complete", required, []string{}},
		{"complete in another order", reordered, []string{}},
		{"partial in another order", []string{"chat:write", "search:read"}, []string{
			"channels:history", "groups:history", "im:history", "mpim:history", "users:read",
			"reactions:write", "channels:read", "groups:read", "im:read", "mpim:read"}},
		{"duplicate", []string{"search:read", "search:read"}, required[1:]},
		{"unknown", []string{"other:read"}, required},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			granted := slices.Clone(tc.granted)
			scopes := slackstate.RestoreScopes(granted)
			for i := range granted {
				granted[i] = "other:read"
			}
			missing := scopes.Missing()
			assert.Equal(t, tc.missing, missing)
			for i := range missing {
				missing[i] = "other:read"
			}
			assert.Equal(t, tc.missing, scopes.Missing())
		})
	}
	assert.Equal(t, required, (slackstate.Scopes{}).Missing())
}

func TestRequiredScopes(t *testing.T) {
	t.Parallel()
	got := slackstate.RequiredScopes()
	required := slices.Clone(got)
	got[0] = "other:read"
	assert.Equal(t, required, slackstate.RequiredScopes())
}

package slackstate

import "slices"

type Scopes struct {
	granted []string
}

func RestoreScopes(granted []string) Scopes {
	return Scopes{granted: slices.Clone(granted)}
}

func RequiredScopes() []string {
	return []string{
		"search:read", "channels:history", "groups:history", "im:history", "mpim:history", "users:read", "chat:write",
		"reactions:write", "channels:read", "groups:read", "im:read", "mpim:read",
	}
}

func (s Scopes) Missing() []string {
	missing := []string{}
	for _, scope := range RequiredScopes() {
		if !slices.Contains(s.granted, scope) {
			missing = append(missing, scope)
		}
	}
	return missing
}

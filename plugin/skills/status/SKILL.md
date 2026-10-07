---
name: status
description: Show what meetproxy is doing, the sources it reads, the Slack token, the inbox, open relays, hook failures and how much state it keeps, with the fix for each problem.
disable-model-invocation: true
allowed-tools: Bash(meetproxy status), Bash(meetproxy status *)
---

# status

1. Run `meetproxy status --data "${CLAUDE_PLUGIN_DATA}"`. It prints one JSON object
2. Show it as one short table of two columns, Item and State, one row each
   - meetproxy: `version`, `protocol` and paused when `paused`
   - Slack: token or connector by `slack.token`, `slack.user` at `slack.host`, `slack.missing` scopes, `slack.setup.answer`
   - one row per entry of `sources`: ok with `found` or the `error`, and how long ago `at` was
   - inbox: `waiting`, `taken`, `held` and `corrupt`
   - relays open: `relays_open`
   - hook failures: how many `hooks` holds and the newest one's hook and error
   - kept: `bytes.map`, `bytes.closed` and `bytes.observed` in KB
3. Under the table, one line per problem with its fix and nothing when there is none
   - Slack read through the connector: `/meetproxy:slack setup` sets up a token so sessions stop showing busy every minute
   - missing scopes: add them under User Token Scopes of the Slack app, reinstall it and run `/meetproxy:slack setup`
   - a source error: `/meetproxy:delegate` checks what that source needs
   - corrupt requests: they were moved to `inbox/corrupt` under the data directory and nothing else needs them
   - hook failures: the error says what broke, and the hooks never block the session for it
   - paused: `/meetproxy:pause resume`

---
name: status
description: Show what meetproxy is doing, the sources it reads, the Slack token, the inbox, the replies it posted, what it ignored, open relays, hook failures and how much state it keeps, with the fix for each problem. Run with retract and a number to correct or remove one of the replies it lists.
disable-model-invocation: true
argument-hint: "[retract <n>]"
allowed-tools: Bash(meetproxy status), Bash(meetproxy status *), Bash(meetproxy posts list *), mcp__meetproxy__retract
---

# status

## No argument

1. Run `meetproxy status --data "${CLAUDE_PLUGIN_DATA}"`. It prints one JSON object
2. Show it as one short table of two columns, Item and State, one row each
   - meetproxy: `version`, `protocol` and paused when `paused`
   - Slack: token or connector by `slack.token`, `slack.user` at `slack.host`, `slack.missing` scopes, `slack.setup.answer`
   - one row per entry of `sources`: ok with `found` or the `error`, and how long ago `at` was
   - inbox: `waiting`, `taken`, `held`, `question` and `corrupt`
   - held: one row per entry of `held` with its id, name and `until` in local time, or until you run it when there is none
   - posts: `history.posts` replies in 30 days, `history.retracted` of them retracted, `history.watching` threads read for follow-ups
   - ignored: `history.ignored` messages read and not queued in 3 days
   - relays open: `relays_open`
   - hook failures: how many `hooks` holds and the newest one's hook and error
   - kept: `bytes.map`, `bytes.closed` and `bytes.observed` in KB
3. Then run `meetproxy posts list --data "${CLAUDE_PLUGIN_DATA}" --limit 5` and list the replies numbered from 1, one line each: when, `mode`, `kind`, the `reply` link and retracted when `retracted_at` is set. Never quote `body`
4. Under that, one line per problem with its fix and nothing when there is none
   - Slack read through the connector: `/meetproxy:slack setup` sets up a token so sessions stop showing busy every minute
   - missing scopes: add them under User Token Scopes of the Slack app, reinstall it and run `/meetproxy:slack setup`
   - a source error: `/meetproxy:delegate` checks what that source needs
   - corrupt requests: they were moved to `inbox/corrupt` under the data directory and nothing else needs them
   - hook failures: the error says what broke, and the hooks never block the session for it
   - paused: `/meetproxy:pause resume`
   - a wrong reply: `/meetproxy:status retract <n>` with its number

## retract <n>

1. Run `meetproxy posts list --data "${CLAUDE_PLUGIN_DATA}" --limit 5` and take the reply numbered n as in step 3 above. A number outside the list or a retracted reply: say so and stop
2. Show its link and ask once with AskUserQuestion
   1. Correct it: then ask for the corrected text, or take the text typed under Other
   2. Delete it
   3. Cancel
3. Call `mcp__meetproxy__retract` with `reply` and, to correct it, `text`. It edits or deletes the reply where the source allows it, and posts a correction under it otherwise
4. Report its answer in one line

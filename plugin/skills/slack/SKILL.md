---
name: slack
description: Set up a personal Slack token so meetproxy reads Slack through its binary instead of the Slack connector, whose calls mark the session busy every minute. meetproxy runs it with setup once when a session reads Slack through the connector. Run it with no argument to check the token.
argument-hint: "[setup]"
disable-model-invocation: true
allowed-tools: Bash(meetproxy slack manifest), Bash(meetproxy slack manifest *), Bash(meetproxy slack setup *), Bash(meetproxy slack whoami), Bash(meetproxy slack whoami *), mcp__meetproxy__slack_token
---

# slack

The token is a secret. Never ask for it in the conversation, never echo it and never pass it as an argument. It only travels through the `slack_token` tool dialog or the user's own terminal.

## No argument

Run `meetproxy slack whoami --data "${CLAUDE_PLUGIN_DATA}"`.

- It prints the user, team and host: report them in one line and stop
- It says there is no token: continue as for setup

## setup

1. Ask once with AskUserQuestion: "Slack reads through the connector mark the session busy every minute. Set up a personal Slack token?"
   1. Set up now
   2. Later
   3. Keep the connector
2. Record the answer with `meetproxy slack setup --data "${CLAUDE_PLUGIN_DATA}" --answer <now | later | keep>`
   - Later asks again in a week and Keep the connector never asks again
   - For either, say in one line that `/meetproxy:slack setup` sets it up any time, and stop
   - When the question cannot be asked, as in a non-interactive session, record nothing and stop
3. For Set up now, run `meetproxy slack manifest --data "${CLAUDE_PLUGIN_DATA}"` and show its `url` as a link
4. Walk the user through it, one short line per step
   1. Open the link and pick the workspace
   2. Create the app from the manifest it fills in
   3. Install to Workspace and Allow
   4. Under OAuth & Permissions copy the User OAuth Token, the one that starts with `xoxp-`
5. Once they have it, call `mcp__meetproxy__slack_token`. It opens a dialog where the user pastes the token under Other and answers with the team, the user and the scopes the token lacks
6. When the tool is not there, fails, or says the user pastes the token in their own terminal, give them this command with both paths written out in full, since their terminal has neither variable, and wait until they say it ran
   `pbpaste | "${CLAUDE_PLUGIN_ROOT}/bin/meetproxy" slack token --data "${CLAUDE_PLUGIN_DATA}"`
   Then run `meetproxy slack whoami --data "${CLAUDE_PLUGIN_DATA}"` to confirm
7. Report the team, the user and the missing scopes in one line
   - Each missing scope turns off what needs it, such as `search:read` for mentions or `chat:write` for replies
   - The fix is to add it under User Token Scopes of the app, reinstall it and run `/meetproxy:slack setup` again
   - From the next minute on Slack is read with the token and the connector is no longer called by meetproxy

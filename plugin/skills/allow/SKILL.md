---
name: allow
description: Add or check a destination that meetproxy may post to. Run with a pattern such as github:owner/*, slack:C0123 or https:api.example.com, or with no argument to list the current patterns.
disable-model-invocation: true
argument-hint: "<github:owner/*, slack:CHANNEL, https:host or a link>"
allowed-tools: Bash(meetproxy allow *), Bash(meetproxy allowed), Bash(meetproxy allowed *), Bash(meetproxy dest *)
---

# allow

Manage the destinations meetproxy may post to.

1. With an argument, run `meetproxy allow --data "${CLAUDE_PLUGIN_DATA}" "$ARGUMENTS"`
   - Turn a link into `github:owner/repo` or `slack:CHANNEL` first
   - `github:owner/*` allows every repository of an owner, and `github:host/owner/*` one on GitHub Enterprise
   - `https:host` allows HTTP writes such as `curl -d` to that host, and `*` in it matches any text such as `https:*.example.com`
2. Then run `meetproxy dest --data "${CLAUDE_PLUGIN_DATA}" "$ARGUMENTS"` and report the result in one line
3. Without an argument, run `meetproxy allowed --data "${CLAUDE_PLUGIN_DATA}"` and list the patterns it prints one per line

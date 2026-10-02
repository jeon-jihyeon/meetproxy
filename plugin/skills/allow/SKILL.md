---
name: allow
description: Add or check a destination that meetproxy may post to. Run with a pattern such as github:owner/* or slack:C0123, or with no argument to list the current patterns.
disable-model-invocation: true
argument-hint: "<github:owner/* or slack:CHANNEL or a link>"
allowed-tools: Bash(meetproxy *)
---

# allow

Manage the destinations meetproxy may post to.

1. With an argument, run `meetproxy allow --data "${CLAUDE_PLUGIN_DATA}" "$ARGUMENTS"`
   - Turn a link into `github:owner/repo` or `slack:CHANNEL` first
   - `github:owner/*` allows every repository of an owner
2. Then run `meetproxy dest --data "${CLAUDE_PLUGIN_DATA}" "$ARGUMENTS"` and report the result in one line
3. Without an argument, read `${CLAUDE_PLUGIN_DATA}/dest.json` and list the patterns

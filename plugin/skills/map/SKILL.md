---
name: map
description: Refresh the meetproxy work map now, or show what it knows. Run with no argument to refresh, or with show to list its places, skills and output formats.
disable-model-invocation: true
argument-hint: "[show]"
allowed-tools: Bash(meetproxy map refresh *), Bash(meetproxy map show *)
---

# map

The work map refreshes itself with the first session of each day. This refreshes it on request.

1. With `show`, run `meetproxy map show --data "${CLAUDE_PLUGIN_DATA}"` and list the places, the skill count and each output kind with its counts of guides, skills and examples
2. Otherwise run `meetproxy map refresh --data "${CLAUDE_PLUGIN_DATA}"`
   - Empty output with no error means another refresh is running; say so
   - Then run `meetproxy map show --data "${CLAUDE_PLUGIN_DATA}"` and report the place count and the top three places in one line

---
name: pause
description: Stop meetproxy from queuing or taking any request in every session, or start it again with resume.
argument-hint: "[resume]"
disable-model-invocation: true
allowed-tools: Bash(meetproxy pause), Bash(meetproxy pause *), Bash(meetproxy resume), Bash(meetproxy resume *)
---

# pause

1. With `resume`, run `meetproxy resume --data "${CLAUDE_PLUGIN_DATA}"`
2. Otherwise run `meetproxy pause --data "${CLAUDE_PLUGIN_DATA}"`
3. Report the result in one line. While paused, requests already queued wait and nothing new is read

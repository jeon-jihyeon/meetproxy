---
name: triage
description: Show or choose what sorts messages from every connected source into ignore, handle and ask. Run with claude, codex or command followed by a command line, or with no argument to show the current one.
argument-hint: "[claude | codex | command <command line>]"
disable-model-invocation: true
allowed-tools: Bash(meetproxy *)
---

# triage

1. Run `meetproxy triage --data "${CLAUDE_PLUGIN_DATA}" $ARGUMENTS` and report the result in one line.
2. When the argument is empty, explain the choices in one line each.
   - `claude`: the default, the session's own model with no setup
   - `codex`: `codex exec` in a read-only sandbox, needs a Codex login
   - `command <command line>`: any command that reads `{"text","channel","from","workspace","linked","knowledge","places"}` as JSON on stdin and prints `{"verdict":"ignore|handle|ask","reason":"...","place":"..."}`
     - `linked` holds the text of messages the message links to, `knowledge` the approved nodloop notes and `places` the work map candidates as `{"name","examples"}`
     - `place` names the candidate that fits best, or is empty
     - Every word after `command` is the command line, so `--data` comes before it
3. A failing engine never posts. Its mentions wait for the user as `ask`.

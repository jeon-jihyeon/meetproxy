<div align="center">
  <h1>meetproxy</h1>
</div>

<div align="center">
  <h3>meetproxy, not meatproxy.</h3>
  <p>Stop being a meat proxy.</p>
</div>

<div align="center">
  <a href="https://github.com/jeon-jihyeon/meetproxy/releases"><img src="https://img.shields.io/github/v/release/jeon-jihyeon/meetproxy?display_name=tag" alt="release"></a>
  <a href="https://github.com/jeon-jihyeon/meetproxy/actions/workflows/test.yml"><img src="https://img.shields.io/github/actions/workflow/status/jeon-jihyeon/meetproxy/test.yml?branch=main&label=test" alt="test"></a>
  <img src="https://img.shields.io/badge/license-MIT-green" alt="license">
  <img src="https://img.shields.io/badge/API%20key-none-lightgrey" alt="API key">
</div>

<br>

meetproxy is a Claude Code plugin that carries requests between people and your AI session so you don't have to. A teammate asks in Slack or on a PR, meetproxy brings the request into the session you already have open, and the refined answer goes back where it was asked. Every answer leaves a note of the code it came from, so the next similar request starts in the right place.

## Why meetproxy?

A meat proxy is a person who sits between a coworker and an AI and only carries text: the question goes in by copy paste, the answer comes out the same way, unread.

- **The word** — `meat` is old hacker slang for the human attached to a machine, as in [meatware](http://catb.org/jargon/html/M/meatware.html), and a `proxy` forwards traffic without changing it
- **The meme** — The earliest AI use we found is [Meat-based LLM proxies](https://not-an-llm.com/meat-based-llm-proxies) in March 2026. It spread with Niklas Gruhn's [Don't be a meat proxy](https://gruhn.me/blog/2026-08-03/) and its [Hacker News thread](https://news.ycombinator.com/item?id=49151933) in August 2026
- **The cost** — Generating an answer got cheap but checking it did not, so the reader [pays for the verification](https://agentpatterns.ai/patterns/anti-patterns/meat-proxy/) the carrier skipped

meetproxy was built to remove that role. The carrying is automated, the judgment a relay should add still happens in your session, and every post says it was written by Claude. The requester meets the answer directly. That is the `meet` in the name.

## Quickstart

```
/plugin marketplace add jeon-jihyeon/meetproxy
/plugin install meetproxy@meetproxy
```

Hand it a request link. It replies where the request came from, and `/meetproxy:allow` adds other places it may post to.

```
/meetproxy:relay https://github.com/your-org/your-repo/pull/42#discussion_r123
```

The plugin fetches its binary from the matching GitHub release on first run. No API key is needed because the work happens in your own Claude Code session.

## How it works

A person in the middle makes three calls before pasting anything. meetproxy makes them in the session instead.

- **Ask back** — When a request is missing what it takes to start, it asks the requester, never you
- **Point to where to look** — It looks up the location map for files that answered similar requests and hands them over as starting points
- **Refine for the reader** — It writes the conclusion first with one piece of evidence the reader can check, and marks the post as written by Claude

## What's inside

- **[relay skill](plugin/skills/relay/SKILL.md)** — The flow from a request link to a posted answer
- **[map skill](plugin/skills/map/SKILL.md)** — Refreshes the work map at once or shows what it knows
- **[allow skill](plugin/skills/allow/SKILL.md)** — Places to post besides where a request came from
- **Posting guard** — A PreToolUse hook on Bash and the Slack and GitHub MCP tools that blocks posts anywhere but where the request came from, the pull request or issue it works on and the allow list while a relay is open. Every repository a `gh` write names must pass, including commands chained, wrapped or run through `bash -c`, and a `gh` command it cannot judge is denied. Commands that do not post are never blocked
- **Work map** — Built from your own Claude Code transcripts the first time a session starts, then refreshed once a day by the first session of the day in a separate process that never touches a session. It knows the places you work in, the skills and commands you have, and which place, skills and files answered each request you typed, and `meetproxy map plan` matches a request to them by the place it names and the past requests that agree. It also learns how you write each kind of output, such as commits, pull requests, review replies, Linear issues and Slack messages: the sections of your CLAUDE.md, rules and memory files whose heading names that kind, by file and line only, and your three newest outputs of each kind that went through, kept on your machine. The relay skill runs `meetproxy map format <kind>` before it posts. Only derived requests, places, skill names, file paths and those examples are kept
- **Location map** — A PostToolUse hook that records the paths read while handling a request, in a repository or in the session directory, and `meetproxy locate` to find them for the next one
- **[Launcher](plugin/bin/meetproxy)** — Runs the binary for the plugin version from a cache, a checksum verified release, or `go install`

## Features

- **Your session, your context** — Requests land in the Claude Code session you already have open, with your files, rules and history, instead of a fresh cloud sandbox
- **Gets better with use** — Each answer records the code it rests on, so the next similar request starts from the right files, and each post follows the way you already write that kind of output
- **Safe by default** — Request text is treated as untrusted, posts anywhere but where the request came from and the allow list are denied by a hook, and the guard fails closed
- **Honest posts** — Every post says it was written by Claude, because hidden AI use costs more trust than disclosed use
- **Small and local** — One Go binary on the standard library, with data kept in the plugin data directory

---

## Resources

- [Claude Code plugins](https://code.claude.com/docs/en/plugins) — how plugins, skills and hooks are installed and loaded
- [Claude Code hooks](https://code.claude.com/docs/en/hooks) — the PreToolUse, PostToolUse and SessionStart events the guard, the location map and the work map refresh use
- [Releases](https://github.com/jeon-jihyeon/meetproxy/releases) — binaries for darwin and linux on amd64 and arm64
- [License](LICENSE) — MIT

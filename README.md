<div align="center">
  <h1>meetproxy</h1>
</div>

<div align="center">
  <h3>Stop being a meat proxy.</h3>
</div>

<div align="center">
  <a href="https://github.com/jeon-jihyeon/meetproxy/releases"><img src="https://img.shields.io/github/v/release/jeon-jihyeon/meetproxy?display_name=tag" alt="release"></a>
  <a href="https://github.com/jeon-jihyeon/meetproxy/actions/workflows/test.yml"><img src="https://img.shields.io/github/actions/workflow/status/jeon-jihyeon/meetproxy/test.yml?branch=main&label=test" alt="test"></a>
  <img src="https://img.shields.io/badge/license-MIT-green" alt="license">
  <img src="https://img.shields.io/badge/API%20key-none-lightgrey" alt="API key">
</div>

<br>

meetproxy is a Claude Code plugin that carries requests between people and your AI session so you don't have to. A teammate asks in Slack or on a PR, meetproxy brings the request into the session you already have open, and the refined answer goes back where it was asked. Every answer leaves a note of the code it came from, so the next similar request starts in the right place.

> [!TIP]
> Not sure what a meat proxy is? Read [Don't be a meat proxy](https://gruhn.me/blog/2026-08-03/), the post that named the habit of pasting AI output back and forth without adding anything.

## Quickstart

```
/plugin marketplace add jeon-jihyeon/meetproxy
/plugin install meetproxy@meetproxy
```

Allow where meetproxy may post, then hand it a request link.

```
/meetproxy:allow github:your-org/*
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
- **[allow skill](plugin/skills/allow/SKILL.md)** — The repositories and channels meetproxy may post to
- **Posting guard** — A PreToolUse hook that blocks posts outside the allow list while a relay is open, including chained `gh` commands and direct API calls
- **Location map** — A PostToolUse hook that records the paths read while handling a request, and `meetproxy locate` to find them for the next one
- **[Launcher](plugin/bin/meetproxy)** — Runs the binary for the plugin version from a cache, a checksum verified release, or `go install`

## Why use meetproxy?

- **Your session, your context** — Requests land in the Claude Code session you already have open, with your files, rules and history, instead of a fresh cloud sandbox
- **Gets better with use** — Each answer records the code it rests on, so the next similar request starts from the right files
- **Safe by default** — Request text is treated as untrusted, posts outside the allow list are denied by a hook, and the guard fails closed
- **Honest posts** — Every post says it was written by Claude, because hidden AI use costs more trust than disclosed use
- **Small and local** — One Go binary on the standard library, with data kept in the plugin data directory

---

## Resources

- [Claude Code plugins](https://code.claude.com/docs/en/plugins) — how plugins, skills and hooks are installed and loaded
- [Claude Code hooks](https://code.claude.com/docs/en/hooks) — the PreToolUse and PostToolUse events the guard and the location map use
- [Releases](https://github.com/jeon-jihyeon/meetproxy/releases) — binaries for darwin and linux on amd64 and arm64
- [License](LICENSE) — MIT

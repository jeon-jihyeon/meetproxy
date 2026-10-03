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

## Why meetproxy?

A meat proxy is a person who sits between a coworker and an AI and only carries text: the question goes in by copy paste, the answer comes out the same way, unread.

- **The word** — `meat` is old hacker slang for the human attached to a machine, as in [meatware](http://catb.org/jargon/html/M/meatware.html), and a `proxy` forwards traffic without changing it
- **The meme** — The earliest AI use we found is [Meat-based LLM proxies](https://not-an-llm.com/meat-based-llm-proxies) in March 2026. It spread with Niklas Gruhn's [Don't be a meat proxy](https://gruhn.me/blog/2026-08-03/) and its [Hacker News thread](https://news.ycombinator.com/item?id=49151933) in August 2026
- **The cost** — Generating an answer got cheap but checking it did not, so the reader [pays for the verification](https://agentpatterns.ai/patterns/anti-patterns/meat-proxy/) the carrier skipped

## Quickstart

```
/plugin marketplace add jeon-jihyeon/meetproxy
/plugin install meetproxy@meetproxy
```

The plugin fetches its binary from the matching GitHub release on first run. No API key is needed because the work happens in your own Claude Code session.

## How it works

A person in the middle makes three calls before pasting anything. meetproxy makes them in the session instead.

## What's inside

- **[Launcher](plugin/bin/meetproxy)** — Runs the binary for the plugin version from a cache, a checksum verified release, or `go install`

## Features

- **Small and local** — One Go binary on the standard library, with data kept in the plugin data directory

---

## Resources

- [Claude Code plugins](https://code.claude.com/docs/en/plugins) — how plugins, skills and hooks are installed and loaded
- [Releases](https://github.com/jeon-jihyeon/meetproxy/releases) — binaries for darwin and linux on amd64 and arm64
- [License](LICENSE) — MIT

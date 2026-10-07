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

That is all for GitHub once `gh` is logged in. For Slack, install the Slack plugin, and set up a personal token so Slack is read in the background, both covered under [Requirements](#requirements).

## Requirements

| Need | Why | Check |
|---|---|---|
| macOS or Linux on amd64 or arm64 | the platforms releases are built for | `uname -sm` |
| `curl`, `tar` and `sha256sum` or `shasum` | the launcher downloads the release and checks it against `checksums.txt`. With Go installed it falls back to `go install` | `command -v curl tar shasum` |
| `gh` logged in | GitHub mentions, review requests, comments on your pull requests, and posting there. The default scopes of `gh auth login`, `repo` and `read:org`, cover it, since `repo` reads notifications and writes comments | `gh auth status` |

## How it works

| Source | Receives | Sends | Connected by |
|---|---|---|---|
| GitHub | mentions in comments, review requests, and comments of others on your own pull requests | a comment, or a reply in the review thread | a logged in `gh` |

A person in the middle makes three calls before pasting anything. meetproxy makes them in the session with you.

## What's inside

- **[Launcher](plugin/bin/meetproxy)** — Runs the binary for the plugin version from a cache, a checksum verified release, or `go install`

## Data and privacy

### What is read

- GitHub notifications you participate in, the pull requests and issues they point at, and comments on your own pull requests

### What is kept

Everything lives in `${CLAUDE_PLUGIN_DATA}`, which is `~/.claude/plugins/data/meetproxy-meetproxy` for an install from this marketplace. Directories are 0700 and files 0600. `meetproxy tidy` runs once a day from session start and removes what the table says.

| Path | Holds | Kept |
|---|---|---|
| `slack/token` | the personal Slack token, mode 0600 | until you remove it |
| `slack/auth.json`, `slack/users.json`, `slack/setup.json` | who the token acts for, users looked up, your answer to the token question | users for 24 hours, the rest until replaced |

Outside the data directory:

- `~/.meetproxy/bin/v<version>/meetproxy` holds the downloaded binary of each version

### What leaves your machine

- Reads from the Slack API with your token or through the Slack connector, and from the GitHub API through `gh`
- The launcher downloads the release from GitHub

There is no meetproxy server and no telemetry.

### How to wipe it

Close every session first, then remove the data directory, the binaries and the mod store.

```sh
rm -rf ~/.claude/plugins/data/meetproxy-meetproxy ~/.meetproxy
rm -f ~/.claude/plugins/store/meetproxy_meetproxy-*.json
```

With a Slack token, also remove the Slack app you created from your workspace's app settings, which revokes the token. With `CLAUDE_CONFIG_DIR` set, use it in place of `~/.claude`.

## Uninstall

```
/plugin uninstall meetproxy@meetproxy
/plugin marketplace remove meetproxy
```

```sh
rm -rf ~/.claude/plugins/data/meetproxy-meetproxy ~/.meetproxy
rm -f ~/.claude/plugins/store/meetproxy_meetproxy-*.json
```

## Troubleshooting

### The binary did not download

The launcher looks for a dev build, then `~/.meetproxy/bin/v<version>/meetproxy`, then the GitHub release checked against `checksums.txt`, then `go install`, then another `meetproxy` on `PATH`. It prints what failed to stderr.

- No `curl`, or no `sha256sum` and no `shasum`: install them, or install Go so it builds the same version
- `checksum ... does not match checksums.txt, refusing to run it`: the download was altered or cut off. Run the command again, and report it if it repeats
- Offline or behind a proxy: download `meetproxy_<os>_<arch>.tar.gz` from the release yourself, [verify it](#verifying-releases), and extract `meetproxy` into `~/.meetproxy/bin/v<version>/`

## Verifying releases

From 0.1.9 on, each release carries a build provenance attestation for its archives and `checksums.txt`, made by the release workflow of this repository.

```sh
gh release download v0.1.9 -R jeon-jihyeon/meetproxy -p 'meetproxy_darwin_arm64.tar.gz' -p checksums.txt
gh attestation verify meetproxy_darwin_arm64.tar.gz -R jeon-jihyeon/meetproxy
gh attestation verify checksums.txt -R jeon-jihyeon/meetproxy
```

The launcher checks every download against `checksums.txt` on its own, so the attestation is for when you want to know the release was built from this repository.

---

## Resources

- [Changelog](CHANGELOG.md) — what changed in each version
- [Contributing](.github/CONTRIBUTING.md) and [security policy](.github/SECURITY.md)
- [Claude Code plugins](https://code.claude.com/docs/en/plugins) — how plugins, skills and hooks are installed and loaded
- [Releases](https://github.com/jeon-jihyeon/meetproxy/releases) — binaries for darwin and linux on amd64 and arm64
- [License](LICENSE) — MIT

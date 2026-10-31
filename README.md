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

A *meat proxy* is someone who just shuttles text between a coworker and an AI: paste the question in, paste the answer back, never read it. The term [took off in 2026](https://gruhn.me/blog/2026-08-03/), and the problem is real. Generating answers got cheap, but [checking them didn't](https://agentpatterns.ai/patterns/anti-patterns/meat-proxy/).

## Quick start

```
/plugin marketplace add jeon-jihyeon/meetproxy
/plugin install meetproxy@meetproxy
```

That's it for GitHub, as long as `gh` is logged in. For Slack, add the official Slack plugin. A personal token is optional, but it lets meetproxy check Slack in the background:

No API key needed. All the work happens in your own Claude Code sessions.

## Requirements

- Claude Code 2.1.287 or later
- macOS or Linux, amd64 or arm64
- `tar`, and `shasum` or `sha256sum`
- `gh`, logged in with the default scopes and able to read this repository. The launcher downloads the binary through `gh`
- For Slack: the [official Slack plugin](https://github.com/anthropics/claude-plugins-official). A personal token is optional

## How it works

### Computer use

For an app meetproxy doesn't connect, like a database client or a chat app, Claude can drive its window. It needs a computer-use server, and meetproxy doesn't ship one. Claude Code's built-in computer use works, or install cua-driver on macOS and register it:

```sh
/bin/bash -c "$(curl -fsSL https://cua.ai/driver/install.sh)"
cua-driver permissions grant
cua-driver mcp-config --client claude --no-skills
```

Run what the last command prints, then restart the session. Your host's permission settings decide which actions it asks you about first. 

## Safe by default

Anyone who can mention you can write a request, so meetproxy treats request text as untrusted.

- **Your host approves computer use.** When an agent drives an app window, your host, Claude Code or Codex, approves those actions, not meetproxy.

## Privacy

meetproxy runs locally with no telemetry. An opt-in authenticated loopback HTTP interface provides reads and host handoff.

## Uninstall

```
/plugin uninstall meetproxy@meetproxy
/plugin marketplace remove meetproxy
```

To remove all data, close every session first:

```sh
rm -rf ~/.claude/plugins/data/meetproxy-meetproxy ~/.meetproxy
rm -f ~/.claude/plugins/store/meetproxy_meetproxy-*.json
```

## Troubleshooting

- **"meetproxy was updated, run /reload-plugins".** Do exactly that, or restart the session.
- **The binary didn't download.** The launcher prints the reason. If it says to run `gh auth login`, do that and start a new session. Behind a proxy, grab `meetproxy_<os>_<arch>.tar.gz` from [Releases](https://github.com/jeon-jihyeon/meetproxy/releases), [verify it](#verify-a-release), and extract it to `~/.meetproxy/bin/v<version>/`.

## Verify a release

The launcher checks every download against `checksums.txt`. To confirm a release was built from this repo:

```sh
gh release download v0.1.9 -R jeon-jihyeon/meetproxy -p 'meetproxy_darwin_arm64.tar.gz' -p checksums.txt
gh attestation verify meetproxy_darwin_arm64.tar.gz -R jeon-jihyeon/meetproxy
```

## Resources

- [Changelog](CHANGELOG.md)
- [Contributing](.github/CONTRIBUTING.md) and [security policy](.github/SECURITY.md)
- [Claude Code plugins](https://code.claude.com/docs/en/plugins) and [mods](https://code.claude.com/docs/en/plugins/mods/api)
- [MIT License](LICENSE)

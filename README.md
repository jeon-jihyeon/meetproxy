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

meetproxy is a Claude Code plugin that collects what people ask you on Slack and GitHub into one inbox. You can open it from any Claude Code session, without switching windows.

Pick a request and the session you're already in reads the thread, does the work, and drafts a reply. Nothing gets posted until you say so.

## Why meetproxy?

A *meat proxy* is someone who just shuttles text between a coworker and an AI: paste the question in, paste the answer back, never read it. The term [took off in 2026](https://gruhn.me/blog/2026-08-03/), and the problem is real. Generating answers got cheap, but [checking them didn't](https://agentpatterns.ai/patterns/anti-patterns/meat-proxy/).

meetproxy handles the carrying so you can focus on the judgment. You choose what to take on and approve what goes out. Every post says it was written by Claude.

## Quick start

```
/plugin marketplace add jeon-jihyeon/meetproxy
/plugin install meetproxy@meetproxy
```

That's it for GitHub, as long as `gh` is logged in. For Slack, add the official Slack plugin. A personal token is optional, but it lets meetproxy check Slack in the background:

```
/plugin install slack@claude-plugins-official
/reload-plugins
/meetproxy:slack setup
```

Your next session downloads a checksum-verified binary and starts checking for requests once a minute. The status line shows how many are open. When you're ready, run `/meetproxy:inbox`.

No API key needed. All the work happens in your own Claude Code sessions.

## Requirements

- Claude Code 2.1.287 or later
- macOS or Linux, amd64 or arm64
- `curl`, `tar`, and `shasum` or `sha256sum`. If Go is installed, the launcher can build from source instead
- `gh`, logged in with the default scopes
- For Slack: the [official Slack plugin](https://github.com/anthropics/claude-plugins-official). A personal token is optional

Without a token, Slack is only read while `/meetproxy:inbox` runs. Reading it in the background through the connector would make tools like cmux show your session as busy every minute. A token also unlocks direct messages, follow-ups in threads, and editing or deleting replies. `/meetproxy:slack setup` walks you through creating a small Slack app for it, and the token never leaves your machine except to go to `slack.com`.

## How it works

```
GitHub  →  gh, every minute
Slack   →  token, every minute, or the Slack plugin when you open the inbox
               ↓
         meetproxy inbox      one request per thread, FYIs dropped
               ↓
         /meetproxy:inbox     in any session, pick one
               ↓
         Claude does the work
               ↓
         You approve          →  reply posted in the thread
```

A follow-up in the same thread opens the request again.

### Delegate

Mentions, review requests, DMs, and comments on your PRs work out of the box. For anything else, just describe it:

```
/meetproxy:delegate when Datadog posts a Triggered alert in #devops-emergency, investigate it and reply in the thread
/meetproxy:delegate when a message in #ops mentions me, run incident-triage on it
```

meetproxy checks what the task needs, such as logins and tools, before saving it. A delegation can `answer`, `review` a PR, `investigate` an alert, or run any skill you have.

## Commands

| Command | What it does |
|---|---|
| `/meetproxy:inbox` | List requests and take one. Claude runs it when you ask what's waiting |
| `/meetproxy:handle <id>` | Take a specific request |
| `/meetproxy:status [retract <n>]` | Show sources, the inbox, recent replies, and fixes for any problems. `retract` edits or removes a reply |
| `/meetproxy:delegate [sentence \| remove <id>]` | Add, list, or remove delegations |
| `/meetproxy:slack [setup]` | Check or set up the Slack token |
| `/meetproxy:triage [claude \| codex \| command <cmd>]` | Choose what does triage. Defaults to the session's own Claude |
| `/meetproxy:allow [destination]` | Allow replies to go somewhere besides the original thread |
| `/meetproxy:pause [resume]` | Pause or resume everything |
| `/meetproxy:map [show]` | Refresh or show what meetproxy learned about how you write |
| `/meetproxy:relay <link>` | Answer a single Slack or GitHub link without the inbox |
| `/meetproxy:review <link> [--approve]` | Review a PR and reply where you were asked |
| `/meetproxy:investigate <link>` | Investigate an alert with read-only tools |

## Safe by default

Anyone who can mention you can write a request, so meetproxy treats request text as untrusted.

- **You approve every reply.** Nothing is posted without your OK.
- **Replies stay in scope.** While a session handles a request, it can only post to that thread, the PR it's working on, and your [allow list](plugin/skills/allow/SKILL.md).
- **Risky actions stay with you.** Merging, closing, deleting, approving PRs without permission, and changing meetproxy's settings are all blocked.
- **It fails closed.** If the guard can't tell where a command or tool will write, it blocks it. That includes network writes like `curl -X POST` to anything other than the Slack and GitHub APIs.
- **Posts are labeled.** Every reply ends with `_Written by Claude on behalf of the user_`.

The guard doesn't parse scripts run by path, copied `gh` binaries, raw IP hosts, or WebFetch. It also blocks inline interpreters like `python -c`, and PowerShell, which it can't parse. It only applies while a request is being handled, so your normal work isn't affected.

## Privacy

There's no meetproxy server and no telemetry.

- **Stored locally** in `~/.claude/plugins/data/meetproxy-meetproxy`, readable only by you: requests (the first line, not the full message), the replies it posted, your settings, and the Slack token. `meetproxy tidy` clears out old data every day.
- **Read:** your Slack mentions and DMs and GitHub notifications. Your Claude Code transcripts and CLAUDE.md files are read only to learn how you write, and your prompts aren't stored.
- **Sent:** replies and reactions to Slack and GitHub, and message text to whichever triage engine you choose.

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

If you set up a Slack token, also delete the Slack app from your workspace to revoke it.

## Troubleshooting

Start with `/meetproxy:status`. It shows what's broken and how to fix it.

- **Nothing shows up.** At least one session has to be open. Without a Slack token, Slack requests only appear when you run `/meetproxy:inbox`. Check for a pause with `/meetproxy:pause resume`.
- **"meetproxy was updated, run /reload-plugins".** Do exactly that, or restart the session.
- **A post was denied.** The message explains why. Post it yourself, or add the destination with `/meetproxy:allow`.
- **The binary didn't download.** The launcher prints the reason. Behind a proxy, grab `meetproxy_<os>_<arch>.tar.gz` from [Releases](https://github.com/jeon-jihyeon/meetproxy/releases), [verify it](#verify-a-release), and extract it to `~/.meetproxy/bin/v<version>/`.

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

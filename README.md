<div align="center">
  <h1>meetproxy</h1>
</div>

<div align="center">
  <h3>meetproxy, not meatproxy.</h3>
  <p>Stop being a meat proxy.</p>
</div>

## Why meetproxy?

A *meat proxy* is someone who just shuttles text between a coworker and an AI: paste the question in, paste the answer back, never read it. The term [took off in 2026](https://gruhn.me/blog/2026-08-03/), and the problem is real. Generating answers got cheap, but [checking them didn't](https://agentpatterns.ai/patterns/anti-patterns/meat-proxy/).

## Quick start

```sh
gh auth login --hostname github.com
gh auth setup-git --hostname github.com
gh repo clone https://github.com/jeon-jihyeon/meetproxy
```

Register that checkout as a local marketplace, using its absolute path:

```
/plugin marketplace add /path/to/meetproxy
/plugin install meetproxy@meetproxy
```

That's it for GitHub, as long as `gh` is logged in. For Slack, add the official Slack plugin. A personal token is optional, but it lets meetproxy check Slack in the background:

No API key needed. All the work happens in your own Claude Code sessions.

## Requirements

- Claude Code 2.1.287 or later
- macOS or Linux, amd64 or arm64
- `tar`, and `shasum` or `sha256sum`
- `git` and `gh`, with a github.com login that can read this private repository. The launcher downloads the binary through authenticated `gh`
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
- **The binary didn't download.** The launcher prints the reason. If it says to run `gh auth login`, log in to github.com with an account that can read this repository and start a new session. For a manual download, use the authenticated commands below, verify the checksums, and extract the matching `meetproxy_<os>_<arch>.tar.gz` to `~/.meetproxy/bin/v<version>/`.

## Verify a release

The launcher checks every download against `checksums.txt`. Private releases are built locally and don't provide GitHub build provenance or attestations. Checksums detect a changed download; they aren't independent proof of who built it.

In an empty directory, download all four archives and their checksum file through your github.com login:

```sh
gh release download v0.1.18 --repo github.com/jeon-jihyeon/meetproxy --pattern 'meetproxy_*.tar.gz' --pattern checksums.txt
shasum -a 256 -c checksums.txt
```

On Linux, use `sha256sum -c checksums.txt` if `shasum` isn't installed. Extract only the archive for your OS and architecture.

Maintainers use one local release path. Start from a clean checkout of the reviewed final commit, with the plugin manifest version already set. Use GoReleaser 2.19.0, Go 1.26.10 and a public noreply Git identity. Set `tag`, `commit` and `notes` to the reviewed version, full commit id and release-notes file outside the checkout. Keep normal Git hooks and signing enabled.

```sh
set -eu
git tag -a "$tag" -m "$tag"
python3 .github/scripts/verify_release.py source --tag "$tag" --source-digest "$commit"
goreleaser release --clean --skip=publish,announce --release-notes "$notes"
python3 .github/scripts/verify_release.py artifacts --tag "$tag" --source-digest "$commit"
git push origin "$tag"
gh release create "$tag" --repo github.com/jeon-jihyeon/meetproxy --verify-tag --draft --title "$tag" --notes-file "$notes" dist/meetproxy_*.tar.gz dist/checksums.txt
python3 .github/scripts/verify_release.py uploaded --tag "$tag" --source-digest "$commit" --repo jeon-jihyeon/meetproxy
gh release edit "$tag" --repo github.com/jeon-jihyeon/meetproxy --draft=false
python3 .github/scripts/verify_release.py uploaded --tag "$tag" --source-digest "$commit" --repo jeon-jihyeon/meetproxy
```

Stop on any failed command. Publish only after the draft's downloaded bytes match the checked local artifacts. The last command repeats the authenticated download and artifact checks after publication. A failure then needs investigation; don't replace published assets automatically or report success. GitHub Actions isn't required. The optional manual-dispatch workflow only builds and validates artifacts when Actions is enabled; it never uploads or publishes them.

## Resources

- [Changelog](CHANGELOG.md)
- [Contributing](.github/CONTRIBUTING.md) and [security policy](.github/SECURITY.md)
- [Claude Code plugins](https://code.claude.com/docs/en/plugins) and [mods](https://code.claude.com/docs/en/plugins/mods/api)
- [MIT License](LICENSE)

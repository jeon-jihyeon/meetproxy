# Contributing to meetproxy

Thanks for helping. Bug reports, fixes and new sources are all welcome. For a security problem use the private reporting link in [SECURITY.md](SECURITY.md) instead of an issue.

## What you need

- Go at the version in [go.mod](../go.mod)
- Node 22 or later, for the watcher tests that [plugin/mod_test.go](../plugin/mod_test.go) runs with `node --test`. Without node that test is skipped, so CI is where it runs for sure
- Claude Code at the version the README names, for loading the plugin and `claude plugin validate`
- [golangci-lint](https://golangci-lint.run) v2, the version CI pins in [test.yml](workflows/test.yml)

## Dev loop

Load the plugin straight from the source tree.

```sh
claude --plugin-dir ./plugin
```

The launcher at `plugin/bin/meetproxy` sees the source tree and builds `bin/meetproxy` with `go build` whenever a Go file is newer than the last build, so a change shows up on the next command without a release. Hooks reuse the last build and the watcher tick rebuilds within a minute. Run `/reload-plugins` in the session after changing a skill, `hooks.json` or `meetproxy.js`.

Data of a `--plugin-dir` session lives in `~/.claude/plugins/data/meetproxy-inline`, apart from an installed copy.

## Checks

Run these before opening a pull request. CI runs the same on Linux and macOS.

```sh
gofmt -l .
go vet ./...
golangci-lint run ./...
go test -race ./...
claude plugin validate plugin
claude plugin validate .
```

- `gofmt -l .` prints nothing when every file is formatted
- `go test -race ./...` also runs the node tests of the watcher through `plugin/mod_test.go`, against a binary it builds
- `claude plugin validate` needs no login

## Changes that touch more than one part

- A command the watcher calls changes in a way an older watcher would misread: raise `protocol` in [cmd/meetproxy/main.go](../cmd/meetproxy/main.go) and `PROTOCOL` in [plugin/hooks/meetproxy.js](../plugin/hooks/meetproxy.js) together, and note in [CHANGELOG.md](../CHANGELOG.md) that sessions need `/reload-plugins`
- A new source: see the list of places it touches in the README
- Anything that posts: it must pass the posting guard in [internal/guard](../internal/guard), so add guard tests for the new tool or command

## Commits and pull requests

Commit subjects follow [Conventional Commits](https://www.conventionalcommits.org) as `type(scope): subject`, such as `fix(meetproxy): keep the take of a busy session`.

- `type` is one of `feat`, `fix`, `docs`, `test`, `refactor`, `chore` or `ci`
- Release notes group `feat` under Features and `fix` under Fixes, and leave merge commits out
- Keep one change per pull request and fill in the template

All public text, including commits, comments and docs, is in English.

## Releases

The maintainer bumps `version` in [plugin/.claude-plugin/plugin.json](../plugin/.claude-plugin/plugin.json), updates the changelog and pushes a tag `v<version>`. The release workflow refuses a tag that does not match the manifest, builds with GoReleaser as a draft, attests the archives and the checksums, and then publishes the release.

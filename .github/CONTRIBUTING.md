# Contributing to meetproxy

Thanks for helping. Bug reports, fixes and new sources are all welcome. For a security problem use the private reporting link in [SECURITY.md](SECURITY.md) instead of an issue.

## What you need

- Go at the version in [go.mod](../go.mod)
- Claude Code at the version the README names, for loading the plugin and `claude plugin validate`
- [golangci-lint](https://golangci-lint.run) v2, the version CI pins in [test.yml](workflows/test.yml)

## Dev loop

Load the plugin straight from the source tree.

```sh
claude --plugin-dir ./plugin
```

Data of a `--plugin-dir` session lives in `~/.claude/plugins/data/meetproxy-inline`, apart from an installed copy.

## Checks

Run these before opening a pull request. CI runs the same on Linux, and on macOS too when a maintainer dispatches it with `macos`.

```sh
gofmt -l .
go vet ./...
golangci-lint run ./...
go test -race ./...
claude plugin validate plugin
claude plugin validate .
```

- `gofmt -l .` prints nothing when every file is formatted
- `claude plugin validate` needs no login

## Commits and pull requests

- `type` is one of `feat`, `fix`, `docs`, `test`, `refactor`, `chore` or `ci`
- Release notes group `feat` under Features and `fix` under Fixes, and leave merge commits out
- Keep one change per pull request and fill in the template

All public text, including commits, comments and docs, is in English.

## Releases

Update the manifest version and changelog, then follow [Verify a release](../README.md#verify-a-release) from the reviewed final clean commit. That procedure builds locally, verifies the archives, uploads a draft, compares authenticated downloads, publishes and verifies the downloads again. The optional manual workflow builds and validates artifacts only. Local releases have no GitHub build provenance or attestations.

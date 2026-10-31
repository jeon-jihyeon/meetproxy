# Changelog

All notable changes to meetproxy are listed here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions follow [Semantic Versioning](https://semver.org/).

## [0.1.18] - 2026-11-01

Protocol 16. Run `/reload-plugins` in every open session after upgrading, or restart it.

### Added

- Computer use reads and drives web pages in your own browser through its accessibility tree, with no profile or debugging setup.

### Changed

- `slack_token` open an OS dialog on the user's screen for the token.
- The launcher downloads the release from github.com through your `gh` login, even when `GH_HOST` names another host. Without one it says to run `gh auth login` and no longer tries curl or `go install`.

### Security

- A sign-in window closes only while it still holds one tab on the loopback port of the link the app printed, within 10 minutes of opening, unless the same wait just saw it on that port, so a browser that reused its window id never closes a window of yours.
- On macOS the token and password dialogs come to the front, so a paste goes into the dialog and not the window behind it.

## [0.1.16]

Protocol 14. Restart every open session after upgrading.

### Added

- Computer use for apps meetproxy has no adapter for. The host approves every computer-use action.

### Security

- The CLI checks every `--session` value before it names a file.

## [0.1.15]

Protocol 13. Restart every open session after upgrading.

## [0.1.14] - 2026-10-22

Protocol 10. Sessions on 0.1.13 keep working without `/reload-plugins`.

## [0.1.13] - 2026-10-21

Protocol 10. Sessions on 0.1.12 keep working without `/reload-plugins`.

## [0.1.12] - 2026-10-15

Protocol 10. Sessions on 0.1.11 keep working without `/reload-plugins`. It carries everything 0.1.9 to 0.1.11 shipped, since those releases were withdrawn.

### Changed

- Test fixtures and examples use neutral names only

## [0.1.11] - 2026-10-08 [YANKED]

Protocol 10. Sessions on 0.1.10 keep working without `/reload-plugins`.

## [0.1.10] - 2026-10-08 [YANKED]

Protocol 10. Sessions need `/reload-plugins`.

### Added

- The binary compiles for Windows. Releases and the launcher still cover macOS and Linux only

### Changed

- GitHub is read and written by the binary, the same way Slack is with a token

## [0.1.9] - 2026-10-07 [YANKED]

Protocol 9. Sessions need `/reload-plugins`.

## [0.1.8] - not released, part of 0.1.9

### Added

- README sections on requirements, data and privacy, uninstall, troubleshooting and verifying releases, a flow diagram and a table of every command
- Security policy with private vulnerability reporting, contributing guide, code of conduct, issue and pull request templates
- Dependabot updates for Go modules and GitHub Actions every week
- Build provenance attestations for the release archives and `checksums.txt`, checked with `gh attestation verify`
- CI validates the plugin and the marketplace manifests with `claude plugin validate`

### Changed

- CI runs on Linux and macOS with the race detector and Node 22, and writes a coverage summary
- Every workflow action is pinned by commit SHA and workflows get only the permissions they use
- Releases are created as drafts and published once their attestation exists
- Release notes are grouped into Features, Fixes and Others, without merge commits

## [0.1.7] - not released, part of 0.1.9

Protocol 8.

## [0.1.6] - not released, part of 0.1.9

Protocol 7.

### Changed

- The dev build in the launcher is rebuilt by commands only, so a hook never waits on `go build`

### Fixed

- Lines appended to a file while it is rewritten are no longer lost

### Security

- The data directory is private to the user, 0700 for directories and 0600 for files, and the first tidy fixes files written before

## [0.1.5] - not released, part of 0.1.9

Protocol 6.

## [0.1.4] - 2026-10-06

## [0.1.3] - 2026-10-06

## [0.1.2] - 2026-10-06

### Fixed

- Data files are written atomically under locks, so two sessions never corrupt them

## [0.1.1] - 2026-10-03

## [0.1.0] - 2026-10-02

### Added

- Launcher that runs the binary from a cache, a checksum verified release or `go install`

[0.1.18]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.16...v0.1.18
[0.1.16]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.15...v0.1.16
[0.1.15]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.14...v0.1.15
[0.1.14]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.13...v0.1.14
[0.1.13]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.12...v0.1.13
[0.1.12]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.4...v0.1.12
[0.1.11]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.4...v0.1.12
[0.1.10]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.4...v0.1.12
[0.1.9]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.4...v0.1.12
[0.1.8]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.4...v0.1.12
[0.1.7]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.4...v0.1.12
[0.1.6]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.4...v0.1.12
[0.1.5]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.4...v0.1.12
[0.1.4]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.3...v0.1.4
[0.1.3]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/jeon-jihyeon/meetproxy/releases/tag/v0.1.0

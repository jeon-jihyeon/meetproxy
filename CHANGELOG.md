# Changelog

All notable changes to meetproxy are listed here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions follow [Semantic Versioning](https://semver.org/).

## [0.1.9] - 2026-10-07

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

[0.1.8]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.4...HEAD
[0.1.7]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.4...HEAD
[0.1.6]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.4...HEAD
[0.1.5]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.4...HEAD
[0.1.4]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.3...v0.1.4
[0.1.3]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/jeon-jihyeon/meetproxy/releases/tag/v0.1.0

# Changelog

All notable changes to meetproxy are listed here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions follow [Semantic Versioning](https://semver.org/).

A release that raises the protocol between the binary and the watcher needs `/reload-plugins` in every open session, or a restart of it. Until then the old watcher stops and says so instead of misreading the new binary.

## [0.1.8] - Unreleased

### Added

- README sections on requirements, data and privacy, uninstall, troubleshooting and verifying releases, a flow diagram and a table of every command
- Security policy with private vulnerability reporting, contributing guide, code of conduct, issue and pull request templates
- Dependabot updates for Go modules and GitHub Actions every week
- Build provenance attestations for the release archives and `checksums.txt`, checked with `gh attestation verify`
- CI validates the plugin and the marketplace manifests with `claude plugin validate`

### Changed

- CI runs on Linux and macOS with the race detector and Node 22, so the watcher tests run there too, and writes a coverage summary
- Every workflow action is pinned by commit SHA and workflows get only the permissions they use
- Releases are created as drafts and published once their attestation exists
- Release notes are grouped into Features, Fixes and Others, without merge commits

## [0.1.7] - Unreleased

Protocol 8. Sessions need `/reload-plugins`.

### Added

- Follow-ups: every post is kept in a ledger for 30 days and its thread is read for three days, so a reply there comes back as the same request through triage
- Corrections: a reply that says the answer was wrong halves how much that answer counts in the location map and asks the user, with an option to save a nodloop note
- `/meetproxy:status retract <n>` edits or deletes a reply with a Slack token or on GitHub, and posts a correction under it otherwise
- Questions back to the requester wait for their reply, and the user is asked after three days without one
- Reactions on request links: eyes when a request is queued, a check mark or hooray once it is answered, and an optional handoff note when an automatic attempt gives up
- Requests the user puts off come back at the time they chose, and several requests that need the user come as one question
- Direct messages and comments of others on the user's own pull requests as delegation kinds, with built in delegations for both
- Delegation fields `priority`, `max_per_hour`, `dedupe_minutes` and `handoff`. Channel delegations queue at most 20 requests an hour and drop repeats within 30 minutes by default
- `[deep]`, `[place=<name>]` and `[repo=<name>]` in a request choose a longer answer or name the place to work in
- A busy session leaves a request of its place to an idle session of the same place
- Messages read and not queued are kept for three days without their text, and `/meetproxy:status` counts them
- A request the user or any meetproxy post already answered is not answered again

### Security

- The posting guard also checks Write, Edit and NotebookEdit, so a session never edits meetproxy's own data files while it handles a request
- A Slack channel shared with another organization counts as outside the trust set, and one that cannot be read counts as shared

## [0.1.6] - Unreleased

Protocol 7. Sessions need `/reload-plugins`.

### Added

- Personal Slack token through `/meetproxy:slack setup`. With it the binary reads and posts Slack, so no connector call runs and terminal multiplexers stop showing the session as busy every minute
- `/meetproxy:slack` checks the token and names the scopes it lacks. The token is stored in `slack/token` with mode 0600
- `/meetproxy:status` shows every source, the Slack token, the inbox, open relays, hook failures and how much state is kept
- Source leases, so one session reads each source and another takes over within three minutes when it stops
- `meetproxy tidy`, run once a day from session start, expires old requests and prunes relays, the location map, scope markers, the ledger and the ignored list

### Changed

- The path and guard hooks exit in the launcher before any program starts while no session handles a request
- A long turn renews its takes with a heartbeat, and a take with no sign of work for 20 minutes can be taken over
- The location map is compacted past 256 KB, observed paths are kept 30 days and closed relay records 90 days
- The dev build in the launcher is rebuilt by commands only, so a hook never waits on `go build`

### Fixed

- A request file that cannot be read is moved to `inbox/corrupt` instead of stopping every session
- Lines appended to a file while it is rewritten are no longer lost

### Security

- The data directory is private to the user, 0700 for directories and 0600 for files, and the first tidy fixes files written before
- A Bash command that edits or redirects into the data directory is denied while a request is handled
- `gh` writes to a host other than github.com and repositories named as `OWNER/REPO` or `HOST/OWNER/REPO` positionals are checked

## [0.1.5] - Unreleased

Protocol 6. Sessions need `/reload-plugins`.

### Security

- The guard covers every MCP tool, not only Slack and GitHub ones, and fails closed for a tool that may write somewhere it cannot tell
- The scope of a request lasts from the take until the turn that settled it ends, through a new Stop hook, so neither a take before the relay opens nor a close before the turn ends lets a post go elsewhere
- A request from outside the user's Slack workspace or GitHub organization asks the user first whatever triage said. Guests and restricted Slack accounts are never trusted, and only author ids, never display names, let a delegation post alone
- While a request is handled merging, closing, deleting, approving without a delegation that allows it and changing meetproxy settings are left to the user
- The guard reads commands inside substitutions, `bash -c`, `eval`, `xargs` and wrappers, and denies inline interpreters, commands named by an expansion and direct API hosts it cannot judge
- An allow pattern that covers a whole source such as `slack:*` is refused
- A post whose check fails or times out is denied rather than sent

## [0.1.4] - 2026-10-06

### Added

- Watcher mod that reads Slack and GitHub every minute while a session is open and queues requests that mention the user
- Triage into ignore, handle and ask with Claude, Codex or a command of your own, through `/meetproxy:triage`
- Delegations with when, do and post parts through `/meetproxy:delegate`, and the review and investigate tasks
- `/meetproxy:handle` takes one queued request so only one session answers it, and `/meetproxy:pause` stops and resumes everything

## [0.1.3] - 2026-10-06

### Added

- Work map built from the user's own transcripts: places, skills and the place, skills and files of past requests, refreshed once a day or with `/meetproxy:map`
- Output formats per kind, such as commits, pull requests and Slack messages, from CLAUDE.md, rules, memory files and recent examples, read with `meetproxy map format`

## [0.1.2] - 2026-10-06

### Fixed

- Data files are written atomically under locks, so two sessions never corrupt them

### Security

- The posting guard parses shell commands, including chains and wrappers, and checks every `gh` write it finds

## [0.1.1] - 2026-10-03

### Fixed

- The guard checks every repository a `gh` write names and explains the repository it denied

## [0.1.0] - 2026-10-02

### Added

- `/meetproxy:relay` takes a Slack or GitHub link and posts the refined answer back where it was asked
- Location map that records the files read while answering and `meetproxy locate` to find them for the next request
- Posting guard as a PreToolUse hook, with `/meetproxy:allow` for other destinations
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

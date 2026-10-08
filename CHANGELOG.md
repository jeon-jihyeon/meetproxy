# Changelog

All notable changes to meetproxy are listed here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions follow [Semantic Versioning](https://semver.org/).

A release that raises the protocol between the binary and the watcher needs `/reload-plugins` in every open session, or a restart of it. Until then the old watcher stops and says so instead of misreading the new binary.

## [0.1.10] - 2026-10-08

Protocol 10. Sessions need `/reload-plugins`.

### Added

- Requests you answered by hand, on another machine, or that a coworker answered close on their own
- A session that crashes gives its requests back within three minutes instead of a day
- `/meetproxy:inbox` lists the newest requests first and can filter by source or delegation
- Without a Slack token, a reply to a question in Slack reopens the request when you open the inbox
- Builds for Windows

### Changed

- GitHub is read and written by the binary, the same way Slack is with a token
- A Slack direct message conversation is one request until it is done, and one GitHub review thread is one request
- A new message in a thread updates what the request asks for, so a question on a reviewed pull request is answered, not reviewed again
- Hooks skip at once in every session that is not handling a request, and the minute check starts fewer processes, rereads less and makes fewer Slack and GitHub calls
- A lease handover no longer triages the same messages twice

### Fixed

- Choosing not to send a reply while handling a request keeps the request instead of closing it
- Messages of a delegated channel read through the Slack connector keep their first lines

### Removed

- The location map, `meetproxy locate`, `meetproxy correct` and the PostToolUse path hook
- Stored prompt text and place aliases in the work map

### Security

- While a request is handled, network writes through `curl`, `wget`, httpie or `xh` to hosts other than the Slack and GitHub APIs are denied, and so is the PowerShell tool

## [0.1.9] - 2026-10-07

Protocol 9. Sessions need `/reload-plugins`.

meetproxy now keeps requests in an inbox you take them from, instead of picking a session and answering on its own.

### Added

- `/meetproxy:inbox` reads the sources and lists the requests, open ones first, and takes up the one you pick in the session you are in. Claude runs it when you ask what waits for you
- The status line counts the open requests
- Every message of one Slack thread or one pull request or issue is one request, showing its source, author and the first line of the newest message
- Every reply is shown to you and posted only once you approve it
- A session that ends gives back the requests it took, so they open again at once

### Changed

- The minute check never calls a tool, so terminal multiplexers such as cmux no longer show sessions as running. Without a Slack token, Slack is read through the connector only while `/meetproxy:inbox` runs
- Triage only drops messages that ask you for nothing. Every other message is kept
- `/meetproxy:handle <id>` takes no `--auto` or `--ask` and always asks you what to do first
- A take stays with its session until it is settled or the session ends, and opens again after 24 hours at most. The posting guard lasts as long

### Removed

- Matching a request to a session or a place, and answering without you: the work map plan, the location map routing, `inbox claim`, `inbox ask`, `inbox waiting`, `map plan` and the idle session preference
- Trust checks of the sender and shared channel detection, which only chose between answering alone and asking
- Delegation fields `post`, `workspace`, `priority` and `handoff`, and the handoff note
- `[place=<name>]` and `[repo=<name>]` in a request
- The SessionStart notice of waiting requests, `slack shared` and `slack trusted`

## [0.1.8] - not released, part of 0.1.9

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

## [0.1.7] - not released, part of 0.1.9

Protocol 8.

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

## [0.1.6] - not released, part of 0.1.9

Protocol 7.

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

## [0.1.5] - not released, part of 0.1.9

Protocol 6.

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

[0.1.10]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.9...v0.1.10
[0.1.9]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.4...v0.1.9
[0.1.8]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.4...v0.1.9
[0.1.7]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.4...v0.1.9
[0.1.6]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.4...v0.1.9
[0.1.5]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.4...v0.1.9
[0.1.4]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.3...v0.1.4
[0.1.3]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/jeon-jihyeon/meetproxy/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/jeon-jihyeon/meetproxy/releases/tag/v0.1.0

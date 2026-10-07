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

meetproxy is a Claude Code plugin that carries requests between people and your AI session so you don't have to. A teammate asks in Slack or on GitHub, meetproxy brings the request into the session already working where the answer lives, and the refined answer goes back where it was asked, written the way you write. Every answer leaves a note of the code it came from, so the next similar request starts in the right place.

## Why meetproxy?

A meat proxy is a person who sits between a coworker and an AI and only carries text: the question goes in by copy paste, the answer comes out the same way, unread.

- **The word** — `meat` is old hacker slang for the human attached to a machine, as in [meatware](http://catb.org/jargon/html/M/meatware.html), and a `proxy` forwards traffic without changing it
- **The meme** — The earliest AI use we found is [Meat-based LLM proxies](https://not-an-llm.com/meat-based-llm-proxies) in March 2026. It spread with Niklas Gruhn's [Don't be a meat proxy](https://gruhn.me/blog/2026-08-03/) and its [Hacker News thread](https://news.ycombinator.com/item?id=49151933) in August 2026
- **The cost** — Generating an answer got cheap but checking it did not, so the reader [pays for the verification](https://agentpatterns.ai/patterns/anti-patterns/meat-proxy/) the carrier skipped

meetproxy was built to remove that role. The carrying is automated, the judgment a relay should add still happens in your session, and every post says it was written by Claude. The requester meets the answer directly. That is the `meet` in the name.

## Quickstart

```
/plugin marketplace add jeon-jihyeon/meetproxy
/plugin install meetproxy@meetproxy
```

That is all. The binary is fetched from the matching GitHub release on first run, and no API key is needed because the work happens in your own Claude Code sessions.

From then on, while any session is open, meetproxy checks every connected source each minute for messages that mention you.

| Source | Receives | Sends | Connected by |
|---|---|---|---|
| Slack | mentions, direct messages with a token, and every message of a channel you delegate | a reply in the thread | a personal token set up with `/meetproxy:slack setup`, or the [Slack plugin](https://github.com/anthropics/claude-plugins-official) |
| GitHub | mentions in comments, review requests, and comments of others on your own pull requests | a comment, or a reply in the review thread | a logged in `gh` |

Adding a service touches one entry in the source table of [internal/dest](internal/dest/dest.go), one case in each of the five dispatchers of the [watcher](plugin/hooks/meetproxy.js) (`owner`, `ready`, `receive`, `answered` and `send`), the tool dispatch of the [posting guard](internal/guard/guard.go) and a reference for the [relay skill](plugin/skills/relay/references). Triage sorts each message.

- **ignore** — not a request, nothing is kept
- **handle** — answered where it was asked, at the place on your machine the work map picks, with the skills and starting files you used for similar requests. A session working in that place takes it, otherwise the session reading that source works there by absolute paths. A request need not concern a repository
- **ask** — needs you, such as a deploy approval or a decision. The session stops to ask whether to answer it now, later or not at all, and posts nothing before you choose

A request you wrote yourself is handled without triage. Replies go only where the request came from. A request that is queued gets an :eyes: reaction and one that was answered a :white_check_mark:, or eyes and hooray on GitHub. A reply that already ends with the meetproxy mark, yours or a teammate's, means the request is covered.

Every post is kept in a ledger for 30 days and its thread is read for three days. A reply there comes back as the same request through triage, and one that says the answer was wrong lowers how much that answer counts in the location map and asks you. A question meetproxy asked back waits for the requester and asks you after three days without an answer. `/meetproxy:status retract <n>` corrects or removes a reply it posted.

When a source stops working an idle session asks you once a day with the exact fix, and you can stop reading that source. When several requests need you they come as one question, a busy session leaves a request to an idle session of the same place, and a request you put off comes back at the time you chose. `/meetproxy:relay <link>` still hands over one request by hand, and a request waiting for the place a new session starts in is named when it opens.

With a token meetproxy reads and posts Slack through its own binary, so no tool call runs and no session shows as busy. Without one only the session holding the Slack lease calls the connector, and it is asked once whether to set a token up. The token is kept in the plugin data directory, readable by you alone. `/meetproxy:status` shows every source, the inbox and any hook failure.

Triage uses the session's own Claude by default. `/meetproxy:triage codex` switches to Codex, and `/meetproxy:triage command <cmd>` plugs in any command that reads the message as JSON and prints a verdict. Approved [nodloop](https://github.com/jeon-jihyeon/nodloop) notes for the place go into triage when nodloop is installed. `/meetproxy:pause` stops all of it at once and `/meetproxy:pause resume` starts it again.

### Delegate

Pull requests that come with a mention are reviewed with no setup, and approval stays with requests you made yourself. A review someone requests on GitHub asks you first. For anything else, say what to take over.

```
/meetproxy:delegate when Datadog posts a Triggered alert in #devops-emergency, investigate it and reply in the thread
/meetproxy:delegate when a message in #ops mentions me, run incident-triage on it but ask me first
```

meetproxy turns the sentence into a delegation with when, do and post parts, then checks what that delegation needs before saving it: the sources it reads, a GitHub login for reviews, the tools an investigation can use. It gives the exact command for what only you can do, such as a login.

| Part | Choices |
|---|---|
| When | mentions of you in any source, review requests, or every message of a Slack channel, narrowed by author id or exact display name, words or a pull request link |
| Do | `answer`, `review` a pull request, `investigate` an alert, or any skill you have |
| Post | on its own, or after asking you. Reviews comment unless the delegation lets them approve |

`/meetproxy:delegate` lists what is delegated and `/meetproxy:delegate remove <id>` takes one back.

## How it works

A person in the middle makes three calls before pasting anything. meetproxy makes them in the session instead.

- **Ask back** — When a request is missing what it takes to start, it asks the requester, never you
- **Point to where to look** — It picks the place on your machine from the work map and hands over the files that answered similar requests as starting points
- **Refine for the reader** — It writes the conclusion first with one piece of evidence the reader can check, follows your own rules and recent examples for that kind of output, and marks the post as written by Claude

## What's inside

- **[relay skill](plugin/skills/relay/SKILL.md)** — The flow from a request link to a posted answer
- **[allow skill](plugin/skills/allow/SKILL.md)** — Places to post besides where a request came from
- **[map skill](plugin/skills/map/SKILL.md)** — Refreshes the work map at once or shows what it knows
- **[Watcher](plugin/hooks/meetproxy.js)** — A Claude Code mod that receives from each connected source by the delegations, runs triage, starts the handle skill in a session working at the place the work map picks, and sends replies through its `post` tool
- **[handle skill](plugin/skills/handle/SKILL.md)** — Takes one queued request by id so only one session answers it, opens the relay, then follows the skill its delegation names. Started by the watcher, it posts nothing that needs your decision, and a request it leaves unsettled for an hour comes back to ask you
- **[triage skill](plugin/skills/triage/SKILL.md)** — Chooses Claude, Codex or a command of your own to sort messages
- **[pause skill](plugin/skills/pause/SKILL.md)** — Stops and resumes taking requests in every session
- **[delegate skill](plugin/skills/delegate/SKILL.md)** — Turns a sentence into a delegation with when, do and post parts, and checks and sets up what it needs
- **[review skill](plugin/skills/review/SKILL.md)** and **[investigate skill](plugin/skills/investigate/SKILL.md)** — The tasks a delegation can name besides answering, also runnable by hand with a link
- **Posting guard** — A PreToolUse hook on Bash and the MCP tools that blocks posts anywhere but where the request came from, the pull request or issue it works on and the allow list while a request is handled, from the take until the turn that settled it ends. Every repository a `gh` write names must pass, including commands chained, wrapped or run through `bash -c`, and a `gh` command it cannot judge is denied. Commands that do not post are never blocked
- **Work map** — Built from your own Claude Code transcripts the first time a session starts, then refreshed once a day by the first session of the day in a separate process that never touches a session. Run `/meetproxy:map` to refresh it at once. It knows the places you work in, the skills and commands you have, and which place, skills and files answered each request you typed. A request is matched in layers: a place it names, then past requests that agree, then a model picks among a few candidates. It also learns how you write each kind of output, such as commits, pull requests, review replies, Linear issues and Slack messages: the sections of your CLAUDE.md, rules and memory files whose heading names that kind, by file and line only, and your three newest outputs of each kind that went through, kept on your machine. A session runs `meetproxy map format <kind>` before it writes one. Only derived requests, places, skill names, file paths and those examples are kept
- **Location map** — A PostToolUse hook that records the paths read while handling a request, in a repository or in the session directory, and `meetproxy locate` to find them for the next one
- **[Launcher](plugin/bin/meetproxy)** — Runs the binary for the plugin version from a cache, a checksum verified release, or `go install`

## Features

- **Your session, your context** — Requests land in the Claude Code session you already have open, with your files, rules and history, instead of a fresh cloud sandbox
- **Gets better with use** — Each answer records the code it rests on, so the next similar request starts from the right files, and each post follows the way you already write that kind of output
- **Safe by default** — Request text is treated as untrusted, posts anywhere but where the request came from and the allow list are denied by a hook, nothing that needs your decision is posted without you, and the guard fails closed. A request from outside your Slack workspace or GitHub organization asks you first, and while one is handled merging, closing, deleting, approving without a delegation and changing meetproxy settings are left to you
  - The guard does not parse scripts run by path, copied `gh` binaries, raw IP hosts or WebFetch, and denies inline interpreters it cannot read. The narrow allowed tools of each skill are the main defense and the guard is defense in depth
- **Honest posts** — Every post says it was written by Claude, because hidden AI use costs more trust than disclosed use
- **Small and local** — One Go binary on the standard library, with data kept in the plugin data directory
- **Limits** — Direct messages, Slack follow-ups and channels shared with another organization need a Slack token, since the connector cannot list or tell them. `@here`, `@channel`, user group mentions without a token, GitHub Discussions, edits of a message already read, and Linear, Jira or CI sources are not read yet. A channel delegation queues at most 20 requests an hour and drops repeats within 30 minutes unless it says otherwise

---

## Resources

- [Claude Code plugins](https://code.claude.com/docs/en/plugins) — how plugins, skills and hooks are installed and loaded
- [Claude Code hooks](https://code.claude.com/docs/en/hooks) — the PreToolUse, PostToolUse and SessionStart events the guard, the location map, the waiting notice and the work map refresh use
- [Claude Code mods](https://code.claude.com/docs/en/plugins/mods/api) — the timers, MCP calls and commands the watcher uses, Claude Code 2.1.287 or later
- [Releases](https://github.com/jeon-jihyeon/meetproxy/releases) — binaries for darwin and linux on amd64 and arm64
- [License](LICENSE) — MIT

---
name: delegate
description: Hand meetproxy a kind of request from any connected source to take over, from a sentence such as "when Datadog posts a Triggered alert in #devops-emergency, investigate it". Checks and helps set up what that request needs before saving it. Run with the sentence, with remove and an id, or with nothing to list what is delegated.
argument-hint: "[what to take over | remove <id>]"
disable-model-invocation: true
allowed-tools: Bash(meetproxy delegation), Bash(meetproxy delegation *), Bash(meetproxy slack whoami), Bash(meetproxy slack whoami *), Bash(gh auth status*), Bash(command -v *)
---

# delegate

Each delegation says when a message becomes a request, what the session does and how it posts.

| Field | Values |
|---|---|
| `id` | short lowercase name such as `alerts`, never starting with `default` |
| `when` | `mention` for Slack messages and GitHub comments that mention the user, `review-request` for pull requests that ask the user for a review, `dm` for Slack direct messages that do not mention the user, `own-pr` for comments of others on the user's own pull requests, `channel` for every message of one Slack channel |
| `channel`, `host` | channel id such as `C0123` and workspace host such as `acme.slack.com`, both for `channel` |
| `from` | author ids or exact display names, any of which must match without case, such as `["Datadog"]` |
| `words` | words that must all appear, such as `["Triggered"]` |
| `link` | `github-pr` when the message must link a pull request |
| `do` | `answer`, `review`, `investigate` or the name of another skill such as `incident-triage` |
| `post` | `auto` to post alone, `ask` to ask the user first. `auto` holds only for senders the source vouches for, members of the user's Slack workspace or of the repository's GitHub organization, and for author ids `from` names. Display names never count, so name a bot by its id |
| `approve` | for `review` only: `self` approves only the user's own requests and is the default, `never` comments only, `any` approves anyone's |
| `workspace` | the place to run in, the absolute path of a repository or directory or the name of a place, empty to let the work map or triage pick it |
| `note` | the user's sentence |
| `priority` | a whole number, higher first when several requests are asked about together |
| `max_per_hour` | requests queued per hour at most, 20 by default for `channel` and no limit otherwise |
| `dedupe_minutes` | minutes within which a message with the same text apart from numbers, times and links is a duplicate, 30 by default for `channel` |
| `handoff` | `true` posts a one line note where it was asked when an automatic attempt gives the request to the user |

Five delegations are built in and come after the user's own: `default-review` reviews a mention with a pull request link and approves only the user's own requests, `default-review-request` asks the user before reviewing a requested pull request, `default-dm` answers direct messages after triage, `default-own-pr` answers comments on the user's own pull requests after asking, and `default` answers other mentions after triage.

A request may say `[deep]` for a longer answer with more evidence, and `[place=<name>]` or `[repo=<name>]` to name a place the work map knows. Neither changes whether it is answered alone.

Not read yet: `@here` and `@channel`, user group mentions without a Slack token, GitHub Discussions, edits of a message already read, and Linear, Jira or CI sources. Direct messages and Slack follow-ups are read with a Slack token only, and a channel shared with another organization is told apart only with one.

## No argument

Run `meetproxy delegation --data "${CLAUDE_PLUGIN_DATA}"` and show each delegation in one line, the built in ones last.

## remove <id>

Run `meetproxy delegation remove --data "${CLAUDE_PLUGIN_DATA}" <id>`.

## A sentence

1. Turn it into one delegation.
   - Find a channel id and host with the Slack channel search, or from a link the user gave.
   - Find the author id or exact display name in a recent message of the channel when the sentence names a bot.
   - Ask only for what the sentence leaves open and no default covers.
2. Check what this delegation needs, and only that. Record each as ready or missing with the fix.

   | Needs | Check | Fix |
   |---|---|---|
   | Slack, for `channel` and `dm` delegations and Slack mentions | `meetproxy slack whoami --data "${CLAUDE_PLUGIN_DATA}"`, and without a token the Slack user profile tool | No token and no Slack tools: `/meetproxy:slack setup`, or `/plugin install slack@claude-plugins-official` then `/reload-plugins`. An auth error of the tools: log in from `/mcp` |
   | A Slack token, for any Slack delegation | `meetproxy slack whoami --data "${CLAUDE_PLUGIN_DATA}"` names the user | Optional. Without it Slack is read through the connector by the session holding the Slack lease, which shows as busy every minute. `/meetproxy:slack setup` sets one up |
   | GitHub, for `review`, `review-request`, `own-pr` and GitHub mentions | `command -v gh` and `gh auth status` | install gh, then the user runs `! gh auth login` |
   | Evidence, for `investigate` | the tools this session has for metrics, logs and the cluster, such as a Datadog MCP, `kubectl` or a cloud CLI | name what is missing and what the verdict will rest on without it |
   | The skill, for any other `do` | the skill is listed in this session | name it as missing |
   | A session to run in | a fixed `workspace` is best served by a session opened in it | say that the session holding the source works there by absolute paths otherwise |

   Replies go where the request came from and reviews go on the pull request, so neither needs an allow list entry. Only the sources a delegation reads have to be connected.

3. Show the delegation in one line per field and then the checks, and ask once with AskUserQuestion.
   1. Save
   2. Change something
   - Run no `meetproxy delegation put` before the user picks 1. A delegation starts reading and posting within a minute.
   - When the question cannot be asked, as in a non-interactive session, stop after showing the delegation and say that running the sentence again in an interactive session saves it.
4. Save it with the delegation as JSON on stdin: `meetproxy delegation put --data "${CLAUDE_PLUGIN_DATA}"`.
5. Report in a few lines.
   - What was saved
   - What the user still has to do, such as a login, with the exact command
   - When it starts: the next check, within a minute, while a session with meetproxy is open

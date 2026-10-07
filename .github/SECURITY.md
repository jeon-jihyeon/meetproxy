# Security policy

## Supported versions

Only the latest minor release line gets security fixes. Today that is 0.1.x, and a fix ships as a new patch release of it.

| Version | Supported |
|---|---|
| latest 0.1.x | yes |
| anything older | no, update with `claude plugin update meetproxy@meetproxy` |

## Reporting a vulnerability

Please do not open a public issue for a security problem.

Report it privately through GitHub private vulnerability reporting:

https://github.com/jeon-jihyeon/meetproxy/security/advisories/new

Include what you can of the following.

- The meetproxy version from `/meetproxy:status` and the Claude Code version from `claude --version`
- What an attacker controls, such as the text of a Slack message or a GitHub comment, and what they gain
- Steps or a tool call that reproduces it, such as a Bash command the posting guard lets through

The conversation stays in the private advisory. Once a fix is released the advisory is published with credit, unless you ask not to be named.

## Scope

In scope are the posting guard, the launcher and its checksum check, the handling of the Slack token, the data directory permissions and anything that lets request text steer a session into posting, merging or changing meetproxy settings.

The guard is defense in depth and its known gaps are listed under Safe by default in the README. A bypass through one of those gaps is still welcome as a report when it is practical to close.

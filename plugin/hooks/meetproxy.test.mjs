// Runs the watcher against fake Slack and GitHub answers and the real binary
// MEETPROXY_BIN names the binary
// The Go test of the plugin sets it

import { test } from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { register, testing } from './meetproxy.js'

const BIN = process.env.MEETPROXY_BIN
const integration = { skip: BIN ? false : 'MEETPROXY_BIN is not set' }
const NOW = Date.parse('2030-01-01T00:10:00Z')
const LEASE = 3 * 60_000

// A session with fake sources
// 1. gh maps an api path to its answer
// 2. slack maps a tool name to a function of its arguments
// 3. fail picks binary calls that fail
// 4. sendFails makes every post fail
function session({ gh = {}, slack = {}, repo = '/tmp/elsewhere', fail = () => false, sendFails = false, now = NOW } = {}) {
  testing.failures.clear()
  testing.sorted.clear()
  for (const k of Object.keys(testing.reached)) delete testing.reached[k]
  const data = mkdtempSync(join(tmpdir(), 'meetproxy-mod-'))
  const store = {}
  const env = { data, now, repo, ran: [], sent: [], logs: [], toasts: [], statuses: [], calls: [], ticks: [], triaged: 0 }
  const bin = (args, stdin) => {
    try {
      return { exitCode: 0, stdout: execFileSync(BIN, [...args, '--data', data], { input: stdin ?? '' }).toString(), stderr: '' }
    } catch (e) {
      return { exitCode: e.status ?? 1, stdout: e.stdout?.toString() ?? '', stderr: e.stderr?.toString() ?? '' }
    }
  }
  env.bin = bin
  env.$ = {
    plugin: { root: '/plugin', name: 'meetproxy' },
    session: { id: async () => 'S1', cwd: async () => repo },
    clock: {
      now: async () => env.now,
      after: (ms, fn) => env.ticks.push(fn),
      every: () => () => {},
    },
    tool: { register: async () => {} },
    store: {
      get: async k => store[k],
      set: async (k, v) => { store[k] = v },
      keys: async () => Object.keys(store),
      delete: async k => { delete store[k] },
    },
    process: {
      run: async (argv, init) => {
        if (argv[0] === 'gh') {
          const args = argv.slice(1)
          if (args.includes('POST')) {
            if (sendFails) return { exitCode: 1, stdout: '', stderr: 'HTTP 404' }
            env.sent.push({ path: args[3], body: JSON.parse(init.stdin).body })
            return { exitCode: 0, stdout: '{}', stderr: '' }
          }
          const path = args.find(a => a.startsWith('repos/') || a.startsWith('https://') || a === 'notifications' || a === 'user')
          const answer = path === 'user' ? 'me' : gh[path]
          if (answer === undefined) return { exitCode: 1, stdout: '', stderr: 'no fake for ' + path }
          return { exitCode: 0, stdout: typeof answer === 'string' ? answer : JSON.stringify(answer), stderr: '' }
        }
        const args = argv.slice(1).filter((a, i, all) => a !== '--root' && all[i - 1] !== '--root')
        env.calls.push(args)
        if (fail(args)) return { exitCode: 1, stdout: '', stderr: 'failed on purpose' }
        return bin(args, init?.stdin)
      },
    },
    mcp: {
      call: async (server, tool, args) => {
        if (tool === 'slack_send_message') {
          if (sendFails) return { isError: true, content: [{ type: 'text', text: 'channel_not_found' }] }
          env.sent.push(args)
          return { content: [] }
        }
        const fn = slack[tool]
        if (!fn) throw new Error('slack is not connected')
        return { content: [{ type: 'text', text: JSON.stringify(fn(args)) }] }
      },
    },
    model: {
      complete: async () => {
        env.triaged++
        return { isAnswered: true, text: '{"verdict":"handle","reason":"code question"}' }
      },
    },
    command: { run: async c => env.ran.push(c) },
    ui: { status: s => env.statuses.push(s), toast: t => env.toasts.push(t), log: m => env.logs.push(m) },
  }
  return env
}

const at = iso => String(Date.parse(iso) / 1000)
const githubCursor = env => env.bin(['inbox', 'cursor', '--key', 'github']).stdout.trim()
const opsDelegation = { id: 'ops', when: 'channel', channel: 'C9', host: 'w.slack.com', words: ['Triggered'], do: 'investigate', post: 'auto', workspace: 'ops' }
const rows = env => env.bin(['inbox', 'list']).stdout.trim().split('\n').filter(Boolean).map(l => l.split('\t'))
const tick = env => JSON.parse(env.bin(['tick', '--cwd', env.repo]).stdout)

// A pull request with one comment that mentions the user and a later one that does not
function githubMention(extra = {}) {
  return {
    notifications: [[{
      reason: 'mention', updated_at: '2030-01-01T00:05:00Z', repository: { full_name: 'o/svc' },
      subject: { type: 'PullRequest', title: 'Fix', url: 'https://api.github.com/repos/o/svc/pulls/7', latest_comment_url: 'x' },
    }]],
    'repos/o/svc/issues/7/comments': [[
      { html_url: 'https://github.com/o/svc/pull/7#issuecomment-1', body: '@me where is retry set?', user: { login: 'kai' }, author_association: 'MEMBER', created_at: '2030-01-01T00:04:00Z' },
      { html_url: 'https://github.com/o/svc/pull/7#issuecomment-2', body: 'thanks', user: { login: 'lee' }, created_at: '2030-01-01T00:05:00Z' },
    ]],
    'repos/o/svc/pulls/7/comments': [[]],
    'repos/o/svc/pulls/7/reviews': [[]],
    ...extra,
  }
}

test('slackLink reads the thread of a reply link', () => {
  const link = 'https://w.slack.com/archives/C1/p1790000000000200?thread_ts=1790000000.000100&cid=C1'
  assert.deepEqual(testing.slackLink(link), { channel: 'C1', ts: '1790000000.000200', thread: '1790000000.000100' })
  assert.equal(testing.slackLink('https://example.com'), undefined)
})

test('githubLink reads a review thread', () => {
  assert.deepEqual(testing.githubLink('https://github.com/o/r/pull/3#discussion_r55'), { owner: 'o', repo: 'r', number: '3', pull: true, discussion: '55' })
  assert.equal(testing.githubLink('https://github.com/o/r/pull/3').discussion, undefined)
  assert.equal(testing.githubLink('https://github.com/o/r/issues/3').pull, false)
})

test('terms drop mentions, links and one letter tokens', () => {
  assert.deepEqual(testing.terms('<@U1> pointsvc 적립 a -x <https://x|y> 적립'), ['pointsvc', '적립'])
})

test('nextPage reads the cursor of a Slack answer', () => {
  const page = { content: [{ type: 'text', text: JSON.stringify({ pagination_info: 'use cursor: `abc=`' }) }] }
  assert.equal(testing.nextPage(page), 'abc=')
  assert.equal(testing.nextPage({ content: [] }), undefined)
})

test('a GitHub mention takes the comment that names the user, not the latest one', integration, async () => {
  const env = session({ gh: githubMention() })
  env.bin(['inbox', 'advance', at('2030-01-01T00:00:00Z'), '--key', 'github'])
  await testing.check(env.$)
  assert.deepEqual(rows(env).map(r => r.slice(1, 4)), [['new', 'svc', 'https://github.com/o/svc/pull/7#issuecomment-1']])
})

test('a request the user already answered is skipped', integration, async () => {
  const mine = { html_url: 'x', body: 'done', user: { login: 'me' }, created_at: '2030-01-01T00:06:00Z' }
  const gh = githubMention()
  gh['repos/o/svc/issues/7/comments'] = [[...gh['repos/o/svc/issues/7/comments'][0], mine]]
  const env = session({ gh })
  env.bin(['inbox', 'advance', at('2030-01-01T00:00:00Z'), '--key', 'github'])
  await testing.check(env.$)
  assert.deepEqual(rows(env), [])
  assert.equal(env.bin(['inbox', 'cursor', '--key', 'github']).stdout.trim(), at('2030-01-01T00:04:00Z'))
})

test('a GitHub mention from outside the organization waits for the user', integration, async () => {
  const gh = githubMention()
  gh['repos/o/svc/issues/7/comments'][0][0].author_association = 'NONE'
  const env = session({ gh })
  env.bin(['inbox', 'advance', at('2030-01-01T00:00:00Z'), '--key', 'github'])
  await testing.check(env.$)
  assert.deepEqual(rows(env).map(r => [r[1], r[4]]), [['ask', 'sender outside the trust set']])
  assert.ok(env.calls.some(a => a[0] === 'inbox' && a[1] === 'add' && a.join(' ').includes('--trusted no')))
})

test('a Slack mention from a member is handled and one from a guest or another team waits', integration, async () => {
  const result = (n, from, ts) => `### Result ${n}\nChannel: #dev (ID: C1)\nFrom: P${n} <p@x.com> (ID: ${from}) \nMessage_ts: ${ts}\nPermalink: [link](https://w.slack.com/archives/C1/p${ts.replace('.', '')})\nText: \n<@ME> question ${n}\n\n---\n`
  const profiles = {
    undefined: 'User ID: ME\nTeam: T1\nEmail: me@x.com\n',
    U2: 'User ID: U2\nTeam: T1\n',
    U3: 'User ID: U3\nTeam: T1\nIs Restricted: true\n',
    U4: 'User ID: U4\nTeam: T9\n',
  }
  const env = session({
    slack: {
      slack_read_user_profile: args => ({ result: profiles[args.user_id] }),
      slack_search_public_and_private: () => ({
        results: result(1, 'U2', '1893456100.000001') + result(2, 'U3', '1893456110.000001') + result(3, 'U4', '1893456120.000001'),
      }),
      slack_read_thread: () => ({ messages: 'THREAD: x' }),
      slack_read_channel: () => ({ messages: '' }),
    },
  })
  env.bin(['inbox', 'advance', '1893456000', '--key', 'mention'])
  await testing.check(env.$)
  assert.deepEqual(rows(env).map(r => [r[3], r[1]]).sort(), [
    ['https://w.slack.com/archives/C1/p1893456100000001', 'new'],
    ['https://w.slack.com/archives/C1/p1893456110000001', 'ask'],
    ['https://w.slack.com/archives/C1/p1893456120000001', 'ask'],
  ])
  assert.equal(await testing.slackTrusted(env.$, 'U2'), true)
  assert.equal(await testing.slackTrusted(env.$, 'U4'), false)
})

test('a Slack profile that cannot be read is not trusted and not remembered', integration, async () => {
  const env = session({ slack: { slack_read_user_profile: args => ({ result: args.user_id ? 'not found' : 'User ID: ME\nTeam: T1\n' }) } })
  assert.equal(await testing.slackTrusted(env.$, 'U2'), false)
  assert.equal(await env.$.store.get('slackTrust:U2'), undefined)
})

test('a request found more than an hour late waits for the user', integration, async () => {
  const env = session({ gh: githubMention(), now: Date.parse('2030-01-01T02:00:00Z') })
  env.bin(['inbox', 'advance', at('2030-01-01T00:00:00Z'), '--key', 'github'])
  await testing.check(env.$)
  assert.deepEqual(rows(env).map(r => [r[1], r[4]]), [['ask', 'came in while no session was open']])
})

// A review requested from the user at 00:03 whose notification moved with later activity
function reviewRequest(updated, extra = {}) {
  return {
    notifications: [[{
      reason: 'review_requested', updated_at: updated, repository: { full_name: 'o/web' },
      subject: { type: 'PullRequest', title: 'Add page', url: 'https://api.github.com/repos/o/web/pulls/3' },
    }]],
    'https://api.github.com/repos/o/web/pulls/3': { html_url: 'https://github.com/o/web/pull/3', body: 'adds', user: { login: 'green' } },
    'repos/o/web/issues/3/timeline': [[
      { event: 'review_requested', requested_reviewer: { login: 'kai' }, created_at: '2030-01-01T00:01:00Z' },
      { event: 'review_requested', requested_reviewer: { login: 'me' }, created_at: '2030-01-01T00:03:00Z' },
      { event: 'commented', created_at: '2030-01-01T00:08:00Z' },
    ]],
    'repos/o/web/issues/3/comments': [[]],
    'repos/o/web/pulls/3/reviews': [[]],
    ...extra,
  }
}

test('a review request asks first', integration, async () => {
  const env = session({ gh: reviewRequest('2030-01-01T00:05:00Z') })
  env.bin(['inbox', 'advance', at('2030-01-01T00:00:00Z'), '--key', 'github'])
  await testing.check(env.$)
  assert.deepEqual(rows(env).map(r => r.slice(1, 4)), [['ask', 'web', 'https://github.com/o/web/pull/3']])
  assert.equal(githubCursor(env), at('2030-01-01T00:03:00Z'))
})

test('a review request done stays done when the pull request moves on', integration, async () => {
  const gh = reviewRequest('2030-01-01T00:05:00Z')
  const env = session({ gh })
  env.bin(['inbox', 'advance', at('2030-01-01T00:00:00Z'), '--key', 'github'])
  await testing.check(env.$)
  const id = rows(env)[0][0]
  assert.equal(env.bin(['inbox', 'done', id, '--session', 'S1']).exitCode, 0)
  // Another session without this session's memory reads the notification after new activity
  gh.notifications[0][0].updated_at = '2030-01-01T00:09:00Z'
  testing.sorted.clear()
  await testing.check(env.$)
  assert.deepEqual(rows(env).map(r => r[1]), ['done'])
})

test('a review the user submitted answers the review request', integration, async () => {
  const mine = { user: { login: 'me' }, state: 'APPROVED', submitted_at: '2030-01-01T00:06:00Z' }
  const env = session({ gh: reviewRequest('2030-01-01T00:06:00Z', { 'repos/o/web/pulls/3/reviews': [[mine]] }) })
  env.bin(['inbox', 'advance', at('2030-01-01T00:00:00Z'), '--key', 'github'])
  await testing.check(env.$)
  assert.deepEqual(rows(env), [])
  assert.equal(githubCursor(env), at('2030-01-01T00:03:00Z'))
})

test('a message that fails to queue stops the source twice and is skipped the third time', integration, async () => {
  const env = session({ gh: githubMention(), fail: args => args[0] === 'inbox' && args[1] === 'add' })
  env.bin(['inbox', 'advance', at('2030-01-01T00:00:00Z'), '--key', 'github'])
  await testing.check(env.$)
  await testing.check(env.$)
  assert.equal(githubCursor(env), at('2030-01-01T00:00:00Z'))
  await testing.check(env.$)
  assert.equal(githubCursor(env), at('2030-01-01T00:04:00Z'))
  assert.equal(env.logs.length, 3)
  assert.equal(testing.failures.size, 0)
})

test('match failures do not use up the tries of queueing', integration, async () => {
  // Two match failures and then one add failure
  const left = { 'delegation match': 2, 'inbox add': 1 }
  const fail = args => {
    const step = args.slice(0, 2).join(' ')
    return left[step] > 0 && left[step]-- > 0
  }
  const env = session({ gh: githubMention(), fail })
  env.bin(['inbox', 'advance', at('2030-01-01T00:00:00Z'), '--key', 'github'])
  await testing.check(env.$)
  await testing.check(env.$)
  await testing.check(env.$)
  assert.deepEqual(rows(env), [])
  assert.equal(githubCursor(env), at('2030-01-01T00:00:00Z'))
  await testing.check(env.$)
  assert.deepEqual(rows(env).map(r => r[3]), ['https://github.com/o/svc/pull/7#issuecomment-1'])
  assert.equal(testing.failures.size, 0)
})

test('a channel delegation reads every page back to the cursor', integration, async () => {
  const block = (ts, body) => `=== Message from Datadog <b@x> (U9) at x === \nMessage TS: ${ts}\n\n${body}\n\n`
  const env = session({
    slack: {
      slack_read_user_profile: () => ({ result: 'User ID: ME\nEmail: me@x.com\n' }),
      slack_read_channel: args => args.cursor
        ? { messages: 'Channel: #ops (C9)\n\n' + block('1893456060.000001', 'Triggered: disk') }
        : { messages: 'Channel: #ops (C9)\n\n' + block('1893456120.000001', 'Triggered: cpu'), pagination_info: 'use cursor: `p2`' },
      slack_read_thread: () => ({ messages: 'THREAD: alert' }),
      slack_search_public_and_private: () => ({ results: '' }),
    },
  })
  env.bin(['delegation', 'put'], JSON.stringify(opsDelegation))
  env.bin(['inbox', 'advance', '1893456000', '--key', 'ops'])
  await testing.check(env.$)
  assert.deepEqual(rows(env).map(r => r[3]).sort(), ['https://w.slack.com/archives/C9/p1893456060000001', 'https://w.slack.com/archives/C9/p1893456120000001'])
})

test('a failing source gives up its lease so another session can read it', integration, async () => {
  const env = session({ gh: { notifications: undefined } })
  await testing.check(env.$)
  assert.equal(testing.reached.github, false)
  assert.equal(await env.$.store.get('lease:github'), undefined)
})

test('a session takes requests of its own place and the lease holder waits before taking what nobody covers', integration, async () => {
  const env = session({ repo: '/tmp/notes' })
  await env.$.store.set('session:S2', { place: '/w/svc', name: 'svc', at: NOW })
  env.bin(['inbox', 'add', 'https://w.slack.com/archives/C1/p1', '--verdict', 'handle', '--trusted', 'yes', '--name', 'svc', '--place', '/w/svc'])
  env.bin(['inbox', 'add', 'https://w.slack.com/archives/C1/p2', '--verdict', 'handle', '--trusted', 'yes', '--name', 'web', '--place', '/w/web'])
  // Same name as this session's place but another root
  env.bin(['inbox', 'add', 'https://w.slack.com/archives/C1/p3', '--verdict', 'handle', '--trusted', 'yes', '--name', 'notes', '--place', '/w/notes'])
  env.bin(['inbox', 'add', 'https://w.slack.com/archives/C1/p4', '--verdict', 'ask', '--name', 'notes', '--place', '/tmp/notes'])
  await env.$.store.set('lease:slack', { session: 'S1', until: NOW + 60_000 })
  await testing.pickUp(env.$, tick(env))
  // S1 works in /tmp/notes and takes that one before any other
  // The notes one at another root is not of its place
  assert.deepEqual(env.ran.map(c => c.args.split(' ')[1]), ['--ask'])
  assert.equal(rows(env).find(r => r[1] === 'taken')?.[3], 'https://w.slack.com/archives/C1/p4')
  assert.deepEqual(await env.$.store.get('session:S1'), { place: '/tmp/notes', name: 'notes', at: NOW })
})

test('the lease holder takes a request of a place whose session went quiet', integration, async () => {
  const env = session({ repo: '/tmp/notes', now: Date.now() + 5 * 60_000 })
  await env.$.store.set('session:OLD', { place: '/w/web', name: 'web', at: env.now - LEASE - 1 })
  env.bin(['inbox', 'add', 'https://w.slack.com/archives/C1/p1', '--verdict', 'handle', '--trusted', 'yes', '--name', 'web', '--place', '/w/web'])
  await env.$.store.set('lease:slack', { session: 'S1', until: env.now + 60_000 })
  await testing.pickUp(env.$, tick(env))
  assert.deepEqual(env.ran.map(c => c.args.split(' ')[1]), ['--auto'])
  assert.equal(await env.$.store.get('session:OLD'), undefined)
})

test('a live session covers a request stored with its name only', integration, async () => {
  const env = session({ repo: '/tmp/notes', now: Date.now() + 5 * 60_000 })
  await env.$.store.set('session:S2', { place: '/w/web', name: 'web', at: env.now })
  env.bin(['inbox', 'add', 'https://w.slack.com/archives/C1/p1', '--verdict', 'handle', '--trusted', 'yes', '--name', 'web'])
  await env.$.store.set('lease:slack', { session: 'S1', until: env.now + 60_000 })
  await testing.pickUp(env.$, tick(env))
  assert.deepEqual(env.ran, [])
})

test('triage picks a place among the work map candidates', integration, async () => {
  const env = session({ gh: githubMention() })
  env.$.model.complete = async ({ prompt }) => ({
    isAnswered: true,
    text: prompt.includes('Places:') ? '{"verdict":"handle","reason":"ops","place":"ops"}' : '{"verdict":"handle","reason":"x"}',
  })
  const real = env.$.process.run
  env.$.process.run = async (argv, init) => {
    if (argv.includes('map') && argv.includes('plan')) {
      const plan = { candidates: [{ name: 'svc', root: '/w/svc', examples: ['alloc'] }, { name: 'ops', root: '/w/ops', examples: ['runbook'] }], skills: ['incident-triage'] }
      return { exitCode: 0, stdout: JSON.stringify(plan), stderr: '' }
    }
    return real(argv, init)
  }
  env.bin(['inbox', 'advance', at('2030-01-01T00:00:00Z'), '--key', 'github'])
  await testing.check(env.$)
  const id = rows(env)[0][0]
  const taken = env.bin(['inbox', 'take', id, '--session', 'S1']).stdout.trim().split('\t')
  assert.deepEqual(taken.slice(4), ['/w/ops', 'incident-triage', '-'])
  assert.equal(rows(env)[0][2], 'ops')
})

test('post checks its input and its destination before sending', integration, async () => {
  const env = session()
  assert.equal(await testing.post(env.$, 'https://github.com/o/svc/pull/7', ''), 'post needs a link and a text')
  assert.equal(await testing.post(env.$, 'https://example.com', 'hi'), 'meetproxy cannot post to https://example.com')
  assert.match(await testing.post(env.$, 'https://github.com/x/y/pull/1', 'hi'), /^denied/)
  env.bin(['open', 'https://github.com/o/svc/pull/7#discussion_r55', '--session', 'S1'])
  assert.equal(await testing.post(env.$, 'https://github.com/o/svc/pull/7#discussion_r55', 'reply'), 'posted to https://github.com/o/svc/pull/7#discussion_r55')
  assert.deepEqual(env.sent, [{ path: 'repos/o/svc/pulls/7/comments/55/replies', body: 'reply' }])
  assert.match(await testing.post(env.$, 'https://w.slack.com/archives/C7/p1790000000000001', 'hi'), /^denied/)
})

test('a post whose check fails is denied and nothing is sent', integration, async () => {
  const env = session()
  const handlers = await started(env, {})
  env.bin(['open', 'https://github.com/o/svc/pull/7', '--session', 'S1'])
  const real = env.$.process.run
  env.$.process.run = async (argv, init) => {
    if (argv.includes('can-post')) throw new Error('timed out')
    return real(argv, init)
  }
  const posted = await handlers['tool.call'](env.$, { tool: 'mcp__meetproxy__post', link: 'https://github.com/o/svc/pull/7', text: 'reply' }, () => 'passed')
  assert.deepEqual(posted, { result: 'denied: post check failed timed out' })
  assert.deepEqual(env.sent, [])
  // A post hook that outran its budget before it answered is denied too
  const next = Object.assign(() => 'ran', { called: false, error: { kind: 'timeout' } })
  assert.deepEqual(handlers['tool.call.catch'](env.$, { tool: 'mcp__meetproxy__post' }, next), { result: 'denied: post check failed timeout' })
  const other = Object.assign(e => e, { called: true })
  assert.deepEqual(handlers['tool.call.catch'](env.$, { tool: 'Bash' }, other), { tool: 'Bash' })
})

test('a request the user wrote in a thread is not taken for their own reply', integration, async () => {
  const ts = '1893456100.000200'
  const link = `https://w.slack.com/archives/C1/p${ts.replace('.', '')}?thread_ts=1893456000.000100&cid=C1`
  const env = session({
    slack: {
      slack_read_user_profile: () => ({ result: 'User ID: ME\nEmail: me@x.com\n' }),
      slack_search_public_and_private: () => ({
        results: `### Result 1 of 1\nChannel: DM (ID: C1)\nFrom: Me <me@x.com> (ID: ME) \nMessage_ts: ${ts}\nPermalink: [link](${link})\nText: \n<@ME> where is retry set\n\n---\n`,
      }),
      // The thread holds the request itself
      // A read that includes its ts would show it as the user's reply
      slack_read_thread: args => ({
        messages: Number(args.oldest) <= Number(ts) ? 'THREAD: x\n\n> Me <me@x.com>: <@ME> where is retry set' : 'THREAD: x',
      }),
      slack_read_channel: () => ({ messages: '' }),
    },
  })
  env.bin(['inbox', 'advance', '1893456000', '--key', 'mention'])
  await testing.check(env.$)
  assert.deepEqual(rows(env).map(r => [r[1], r[3]]), [['new', link]])
})

test('a Slack request the user already replied to is skipped', integration, async () => {
  const ts = '1893456100.000200'
  const link = `https://w.slack.com/archives/C1/p${ts.replace('.', '')}`
  const env = session({
    slack: {
      slack_read_user_profile: () => ({ result: 'User ID: ME\nEmail: me@x.com\n' }),
      slack_search_public_and_private: () => ({
        results: `### Result 1 of 1\nChannel: #dev (ID: C1)\nFrom: Kai <kai@x.com> (ID: U2) \nMessage_ts: ${ts}\nPermalink: [link](${link})\nText: \n<@ME> where is retry set\n\n---\n`,
      }),
      slack_read_thread: () => ({ messages: 'THREAD: <@ME> where is retry set\n\n> Me <me@x.com>: in config.go' }),
      slack_read_channel: () => ({ messages: '' }),
    },
  })
  env.bin(['inbox', 'advance', '1893456000', '--key', 'mention'])
  await testing.check(env.$)
  assert.deepEqual(rows(env), [])
  assert.equal(env.bin(['inbox', 'cursor', '--key', 'mention']).stdout.trim(), ts)
})

test('a session with no place does not count as covering requests with none', integration, async () => {
  const env = session({ repo: '/tmp/notes', now: Date.now() + 5 * 60_000 })
  await env.$.store.set('session:OLD', { place: '', name: '', at: env.now })
  env.bin(['inbox', 'add', 'https://w.slack.com/archives/C1/p1', '--verdict', 'handle', '--trusted', 'yes'])
  await env.$.store.set('lease:slack', { session: 'S1', until: env.now + 5 * 60_000 })
  await testing.pickUp(env.$, tick(env))
  assert.deepEqual(env.ran.map(c => c.args.split(' ')[1]), ['--auto'])
})

test('a source another session leases is left to it', integration, async () => {
  const env = session({ gh: githubMention() })
  env.bin(['inbox', 'advance', at('2030-01-01T00:00:00Z'), '--key', 'github'])
  await env.$.store.set('lease:github', { session: 'S2', until: NOW + 60_000 })
  await testing.check(env.$)
  assert.deepEqual(rows(env), [])
  assert.equal(githubCursor(env), at('2030-01-01T00:00:00Z'))
  assert.deepEqual(await env.$.store.get('lease:github'), { session: 'S2', until: NOW + 60_000 })
})

test('a failing source leaves a lease another session took over', integration, async () => {
  const env = session()
  const taken = { session: 'S2', until: NOW + LEASE }
  const real = env.$.process.run
  env.$.process.run = async (argv, init) => {
    if (!argv.includes('notifications')) return real(argv, init)
    await env.$.store.set('lease:github', taken)
    return { exitCode: 1, stdout: '', stderr: 'timeout' }
  }
  await testing.check(env.$)
  assert.equal(testing.reached.github, false)
  assert.deepEqual(await env.$.store.get('lease:github'), taken)
})

test('a message meetproxy posted only moves the cursor', integration, async () => {
  const gh = githubMention()
  gh['repos/o/svc/issues/7/comments'][0][0].body = '@me retry is in config.go\n\n_Written by Claude on behalf of the user_'
  const env = session({ gh })
  env.bin(['inbox', 'advance', at('2030-01-01T00:00:00Z'), '--key', 'github'])
  await testing.check(env.$)
  assert.deepEqual(rows(env), [])
  assert.equal(githubCursor(env), at('2030-01-01T00:04:00Z'))
  assert.equal(env.triaged, 0)
})

test('a message triage ignores only moves the cursor and is never queued', integration, async () => {
  const env = session({ gh: githubMention() })
  env.$.model.complete = async () => ({ isAnswered: true, text: '{"verdict":"ignore","reason":"thanks"}' })
  env.bin(['inbox', 'advance', at('2030-01-01T00:00:00Z'), '--key', 'github'])
  await testing.check(env.$)
  assert.deepEqual(rows(env), [])
  assert.equal(githubCursor(env), at('2030-01-01T00:04:00Z'))
  assert.equal(env.calls.some(a => a[0] === 'inbox' && a[1] === 'add'), false)
})

test('a failed delegation match keeps the cursor on every try', integration, async () => {
  const env = session({ gh: githubMention(), fail: args => args[0] === 'delegation' && args[1] === 'match' })
  env.bin(['inbox', 'advance', at('2030-01-01T00:00:00Z'), '--key', 'github'])
  for (let i = 0; i < 4; i++) await testing.check(env.$)
  assert.deepEqual(rows(env), [])
  assert.equal(githubCursor(env), at('2030-01-01T00:00:00Z'))
  assert.equal(env.logs.length, 4)
})

test('a message that shows up after the cursor moved past it is still queued once', integration, async () => {
  const env = session({ gh: githubMention() })
  env.bin(['inbox', 'advance', at('2030-01-01T00:06:00Z'), '--key', 'github'])
  await testing.check(env.$)
  await testing.check(env.$)
  // Another session reads the overlap again without this session's memory
  testing.sorted.clear()
  await testing.check(env.$)
  assert.deepEqual(rows(env).map(r => r.slice(1, 4)), [['new', 'svc', 'https://github.com/o/svc/pull/7#issuecomment-1']])
  assert.equal(env.triaged, 2)
  assert.equal(githubCursor(env), at('2030-01-01T00:06:00Z'))
})

test('a mention in the issue itself counts when no comment names the user', integration, async () => {
  const env = session({
    gh: {
      notifications: [[{
        reason: 'mention', updated_at: '2030-01-01T00:05:00Z', repository: { full_name: 'o/svc' },
        subject: { type: 'Issue', title: 'Bug', url: 'https://api.github.com/repos/o/svc/issues/8' },
      }]],
      'repos/o/svc/issues/8/comments': [[]],
      'https://api.github.com/repos/o/svc/issues/8': { html_url: 'https://github.com/o/svc/issues/8', body: '@me can you look', user: { login: 'kai' }, author_association: 'MEMBER', created_at: '2030-01-01T00:03:00Z' },
    },
  })
  env.bin(['inbox', 'advance', at('2030-01-01T00:00:00Z'), '--key', 'github'])
  await testing.check(env.$)
  assert.deepEqual(rows(env).map(r => r.slice(1, 4)), [['new', 'svc', 'https://github.com/o/svc/issues/8']])
})

for (const tc of [
  { name: 'a reply in the review thread answers it', replyTo: 55, want: [] },
  { name: 'a reply in another review thread does not answer it', replyTo: 99, want: ['https://github.com/o/svc/pull/7#discussion_r55'] },
]) {
  test(tc.name, integration, async () => {
    const env = session({
      gh: githubMention({
        'repos/o/svc/issues/7/comments': [[]],
        'repos/o/svc/pulls/7/comments': [[
          { id: 55, html_url: 'https://github.com/o/svc/pull/7#discussion_r55', body: '@me is this safe?', user: { login: 'kai' }, created_at: '2030-01-01T00:04:00Z' },
          { id: 56, in_reply_to_id: tc.replyTo, html_url: 'x', body: 'yes', user: { login: 'me' }, created_at: '2030-01-01T00:06:00Z' },
        ]],
      }),
    })
    env.bin(['inbox', 'advance', at('2030-01-01T00:00:00Z'), '--key', 'github'])
    await testing.check(env.$)
    assert.deepEqual(rows(env).map(r => r[3]), tc.want)
  })
}

test('a channel with more pages than the bound reads the newest ones and says so', integration, async () => {
  let reads = 0
  const env = session({
    slack: {
      slack_read_user_profile: () => ({ result: 'User ID: ME\nEmail: me@x.com\n' }),
      slack_read_channel: () => {
        reads++
        return { messages: `Channel: #ops (C9)\n\n=== Message from Datadog <b@x> (U9) at x === \nMessage TS: 1893456120.000001\n\nTriggered: cpu\n\n`, pagination_info: 'use cursor: `next`' }
      },
      slack_read_thread: () => ({ messages: 'THREAD: alert' }),
      slack_search_public_and_private: () => ({ results: '' }),
    },
  })
  env.bin(['delegation', 'put'], JSON.stringify(opsDelegation))
  env.bin(['inbox', 'advance', '1893456000', '--key', 'ops'])
  await testing.check(env.$)
  assert.equal(reads, 50)
  assert.ok(env.logs.some(l => l.includes('older ones are skipped')))
  assert.deepEqual(rows(env).map(r => r[3]), ['https://w.slack.com/archives/C9/p1893456120000001'])
})

test('a post that fails to send says so', integration, async () => {
  const env = session({ sendFails: true })
  env.bin(['open', 'https://github.com/o/svc/pull/7', '--session', 'S1'])
  assert.match(await testing.post(env.$, 'https://github.com/o/svc/pull/7', 'reply'), /^failed to post: .*HTTP 404/)
  env.bin(['allow', 'slack:C7'])
  assert.match(await testing.post(env.$, 'https://w.slack.com/archives/C7/p1790000000000001', 'hi'), /^failed to post: channel_not_found/)
  assert.deepEqual(env.sent, [])
})

test('a post to an allowed Slack channel goes to the thread of the link', integration, async () => {
  const env = session()
  env.bin(['allow', 'slack:C7'])
  const link = 'https://w.slack.com/archives/C7/p1790000000000200?thread_ts=1790000000.000100&cid=C7'
  assert.equal(await testing.post(env.$, link, 'hi'), 'posted to ' + link)
  assert.deepEqual(env.sent, [{ channel_id: 'C7', thread_ts: '1790000000.000100', message: 'hi' }])
})

// The handlers register gives with the binary answering the given commands with an older protocol
async function started(env, older) {
  const real = env.$.process.run
  env.$.process.run = async (argv, init) => {
    if (argv[1] === 'protocol' && older.protocol) return { exitCode: 0, stdout: '4\n', stderr: '' }
    if (argv[1] === 'tick' && older.tick) return { exitCode: 0, stdout: '{"protocol":4}\n', stderr: '' }
    return real(argv, init)
  }
  const handlers = {}
  register((name, fn) => {
    handlers[name] = fn
    return { catch: c => { handlers[name + '.catch'] = c } }
  })
  await handlers['session.start'](env.$, {}, e => e)
  return handlers
}

for (const tc of [
  { name: 'a binary of another protocol at start schedules no tick', older: { protocol: true }, want: { updated: true, ticks: 0, inbox: false } },
  { name: 'a binary that changed under the session stops the tick', older: { tick: true }, want: { updated: true, ticks: 1, inbox: false } },
  { name: 'the binary speaks the protocol the watcher expects', older: {}, want: { updated: false, ticks: 1, inbox: true } },
]) {
  test(tc.name, integration, async () => {
    const env = session()
    await started(env, tc.older)
    for (const fn of env.ticks) await fn()
    assert.deepEqual({
      updated: env.statuses.includes('meetproxy was updated, run /reload-plugins in this session'),
      ticks: env.ticks.length,
      inbox: env.calls.some(a => a[0] === 'inbox'),
    }, tc.want)
  })
}

test('the post tool goes through the tool call and other tools pass on', integration, async () => {
  const env = session()
  const handlers = await started(env, {})
  env.bin(['open', 'https://github.com/o/svc/pull/7', '--session', 'S1'])
  const other = { tool: 'Bash' }
  assert.equal(await handlers['tool.call'](env.$, other, e => e), other)
  const posted = await handlers['tool.call'](env.$, { tool: 'mcp__meetproxy__post', link: 'https://github.com/o/svc/pull/7', text: 'reply' }, () => 'passed')
  assert.deepEqual(posted, { result: 'posted to https://github.com/o/svc/pull/7' })
  assert.deepEqual(env.sent, [{ path: 'repos/o/svc/issues/7/comments', body: 'reply' }])
})

for (const tc of [
  { name: 'a request taken an hour ago and never settled is asked about', takenAgo: 2 * 3600_000, want: ['--ask'] },
  { name: 'a request another session took recently is left to it', takenAgo: 10 * 60_000, want: [] },
]) {
  test(tc.name, integration, async () => {
    const env = session({ repo: '/tmp/notes' })
    env.bin(['inbox', 'add', 'https://w.slack.com/archives/C1/p1', '--verdict', 'handle', '--trusted', 'yes', '--name', 'notes', '--place', '/tmp/notes'])
    const id = rows(env)[0][0]
    const file = join(env.data, 'inbox', id + '.json')
    const item = JSON.parse(readFileSync(file, 'utf8'))
    writeFileSync(file, JSON.stringify({ ...item, status: 'taken', session_id: 'S2', updated_at: new Date(Date.now() - tc.takenAgo).toISOString() }))
    await testing.pickUp(env.$, tick(env))
    assert.deepEqual(env.ran.map(c => c.args.split(' ')[1]), tc.want)
  })
}

test('a message the overlap keeps finding while idle is sorted once and forgotten once no read finds it', integration, async () => {
  const gh = githubMention()
  const env = session({ gh })
  env.bin(['inbox', 'advance', at('2030-01-01T00:00:00Z'), '--key', 'github'])
  for (let i = 0; i < 4; i++) {
    await testing.check(env.$)
    env.now += 8 * 60_000
  }
  assert.equal(env.triaged, 1)
  assert.equal(testing.sorted.size, 1)
  gh.notifications = [[]]
  env.now += 11 * 60_000
  await testing.check(env.$)
  assert.equal(testing.sorted.size, 0)
})

test('Slack mentions are read past the first page', integration, async () => {
  const result = (n, ts) => `### Result ${n}\nChannel: #dev (ID: C1)\nFrom: Kai <kai@x.com> (ID: U2) \nMessage_ts: ${ts}\nPermalink: [link](https://w.slack.com/archives/C1/p${ts.replace('.', '')})\nText: \n<@ME> question ${n}\n\n---\n`
  const pages = []
  const env = session({
    slack: {
      slack_read_user_profile: () => ({ result: 'User ID: ME\nEmail: me@x.com\n' }),
      slack_search_public_and_private: args => {
        pages.push(args.cursor)
        return args.cursor
          ? { results: result(2, '1893456200.000001') }
          : { results: result(1, '1893456100.000001'), pagination_info: 'use cursor: `p2`' }
      },
      slack_read_thread: () => ({ messages: 'THREAD: x' }),
      slack_read_channel: () => ({ messages: '' }),
    },
  })
  env.bin(['inbox', 'advance', '1893456000', '--key', 'mention'])
  await testing.check(env.$)
  assert.deepEqual(pages, [undefined, 'p2'])
  assert.equal(rows(env).length, 2)
  assert.equal(env.bin(['inbox', 'cursor', '--key', 'mention']).stdout.trim(), '1893456200.000001')
})

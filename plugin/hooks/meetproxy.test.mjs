// Runs the watcher against fake Slack and GitHub answers and the real binary
// MEETPROXY_BIN names the binary
// The Go test of the plugin sets it

import { test } from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { register, testing } from './meetproxy.js'

const BIN = process.env.MEETPROXY_BIN
const integration = { skip: BIN ? false : 'MEETPROXY_BIN is not set' }
const NOW = Date.parse('2030-01-01T00:10:00Z')
const LEASE = 3 * 60_000

// A session with fake sources
// 1. gh maps an api path to its answer
// 2. slack maps a tool name to a function of its arguments and lists the connector's tools when it holds any
// 3. token maps a meetproxy slack command such as mentions to a function of its arguments and stdin
//    It also stores a token so the binary's tick reports one
// 4. fail picks binary calls that fail
// 5. sendFails makes every post fail
function session({ gh = {}, slack = {}, token, repo = '/tmp/elsewhere', fail = () => false, sendFails = false, now = NOW } = {}) {
  testing.failures.clear()
  testing.sorted.clear()
  testing.reset()
  for (const k of Object.keys(testing.reached)) delete testing.reached[k]
  const data = mkdtempSync(join(tmpdir(), 'meetproxy-mod-'))
  const store = {}
  const env = { data, now, repo, ran: [], sent: [], reactions: [], edits: [], asked: [], logs: [], toasts: [], statuses: [], calls: [], ticks: [], mcp: [], ghCalls: [], triaged: 0 }
  env.tools = Object.keys(slack).length ? Object.keys(slack).map(t => ({ name: 'mcp__plugin_slack_slack__' + t, mcp: true })) : []
  if (token) {
    mkdirSync(join(data, 'slack'), { recursive: true })
    writeFileSync(join(data, 'slack', 'token'), 'xoxp-test\n', { mode: 0o600 })
    writeFileSync(join(data, 'slack', 'auth.json'), JSON.stringify({ user: 'ME', team: 'T1', host: 'w.slack.com', scopes: [] }))
  }
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
    tool: { register: async () => {}, list: async () => env.tools },
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
            if (args.includes('content=eyes') || args.includes('content=hooray')) {
              env.reactions.push([args[3], args.at(-1)])
              return { exitCode: 0, stdout: '{}', stderr: '' }
            }
            env.sent.push({ path: args[3], body: JSON.parse(init.stdin).body })
            return { exitCode: 0, stdout: JSON.stringify({ html_url: 'https://github.com/o/svc/pull/7#issuecomment-900' }), stderr: '' }
          }
          if (args.includes('PATCH') || args.includes('DELETE')) {
            env.edits.push([args[2], args[3], init?.stdin ? JSON.parse(init.stdin).body : undefined])
            return { exitCode: 0, stdout: '{}', stderr: '' }
          }
          const path = args.find(a => a.startsWith('repos/') || a.startsWith('https://') || a === 'notifications' || a === 'user')
          env.ghCalls.push(path)
          const answer = path === 'user' ? 'me' : gh[path]
          if (answer === undefined) return { exitCode: 1, stdout: '', stderr: 'no fake for ' + path }
          return { exitCode: 0, stdout: typeof answer === 'string' ? answer : JSON.stringify(answer), stderr: '' }
        }
        const args = argv.slice(1).filter((a, i, all) => a !== '--root' && all[i - 1] !== '--root')
        env.calls.push(args)
        if (fail(args)) return { exitCode: 1, stdout: '', stderr: 'failed on purpose' }
        if (args[0] === 'slack' && token?.[args[1]]) {
          const answer = token[args[1]](args.slice(2), init?.stdin)
          return { exitCode: answer.exitCode ?? 0, stdout: typeof answer.out === 'string' ? answer.out : JSON.stringify(answer.out), stderr: answer.err ?? '' }
        }
        return bin(args, init?.stdin)
      },
    },
    mcp: {
      call: async (server, tool, args) => {
        env.mcp.push(tool)
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
    ui: {
      status: s => env.statuses.push(s),
      toast: t => env.toasts.push(t),
      log: m => env.logs.push(m),
      ask: async (q, o) => {
        env.asked.push({ q, o })
        return typeof env.answer === 'function' ? env.answer(q, o) : env.answer
      },
    },
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
  // The notification was read whole so the cursor moves to its updated_at
  assert.equal(env.bin(['inbox', 'cursor', '--key', 'github']).stdout.trim(), at('2030-01-01T00:05:00Z'))
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
  assert.equal(githubCursor(env), at('2030-01-01T00:05:00Z'))
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
  assert.equal(githubCursor(env), at('2030-01-01T00:06:00Z'))
})

test('a message that fails to queue stops the source twice and is skipped the third time', integration, async () => {
  const env = session({ gh: githubMention(), fail: args => args[0] === 'inbox' && args[1] === 'add' })
  env.bin(['inbox', 'advance', at('2030-01-01T00:00:00Z'), '--key', 'github'])
  await testing.check(env.$)
  await testing.check(env.$)
  assert.equal(githubCursor(env), at('2030-01-01T00:00:00Z'))
  await testing.check(env.$)
  assert.equal(githubCursor(env), at('2030-01-01T00:05:00Z'))
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
  assert.equal(env.bin(['lease', 'hold', 'github', '--session', 'S2']).exitCode, 0)
})

test('a session takes requests of its own place and the lease holder waits before taking what nobody covers', integration, async () => {
  const env = session({ repo: '/tmp/notes' })
  await env.$.store.set('session:S2', { place: '/w/svc', name: 'svc', at: NOW })
  env.bin(['inbox', 'add', 'https://w.slack.com/archives/C1/p1', '--verdict', 'handle', '--trusted', 'yes', '--name', 'svc', '--place', '/w/svc'])
  env.bin(['inbox', 'add', 'https://w.slack.com/archives/C1/p2', '--verdict', 'handle', '--trusted', 'yes', '--name', 'web', '--place', '/w/web'])
  // Same name as this session's place but another root
  env.bin(['inbox', 'add', 'https://w.slack.com/archives/C1/p3', '--verdict', 'handle', '--trusted', 'yes', '--name', 'notes', '--place', '/w/notes'])
  env.bin(['inbox', 'add', 'https://w.slack.com/archives/C1/p4', '--verdict', 'ask', '--name', 'notes', '--place', '/tmp/notes'])
  testing.holding.slack = NOW + 60_000
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
  testing.holding.slack = env.now + 60_000
  await testing.pickUp(env.$, tick(env))
  assert.deepEqual(env.ran.map(c => c.args.split(' ')[1]), ['--auto'])
  assert.equal(await env.$.store.get('session:OLD'), undefined)
})

test('a live session covers a request stored with its name only', integration, async () => {
  const env = session({ repo: '/tmp/notes', now: Date.now() + 5 * 60_000 })
  await env.$.store.set('session:S2', { place: '/w/web', name: 'web', at: env.now })
  env.bin(['inbox', 'add', 'https://w.slack.com/archives/C1/p1', '--verdict', 'handle', '--trusted', 'yes', '--name', 'web'])
  testing.holding.slack = env.now + 60_000
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
  assert.deepEqual(taken.slice(4), ['/w/ops', 'incident-triage', '-', 'quick', '-'])
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
  testing.holding.slack = env.now + 5 * 60_000
  await testing.pickUp(env.$, tick(env))
  assert.deepEqual(env.ran.map(c => c.args.split(' ')[1]), ['--auto'])
})

test('a source another session leases is left to it', integration, async () => {
  const env = session({ gh: githubMention() })
  env.bin(['inbox', 'advance', at('2030-01-01T00:00:00Z'), '--key', 'github'])
  env.bin(['lease', 'hold', 'github', '--session', 'S2'])
  await testing.check(env.$)
  assert.deepEqual(rows(env), [])
  assert.equal(githubCursor(env), at('2030-01-01T00:00:00Z'))
  assert.equal(env.bin(['lease', 'hold', 'github', '--session', 'S3']).exitCode, 1)
  assert.equal(env.calls.some(a => a[0] === 'lease' && a[1] === 'drop'), false)
})

test('a failing source leaves a lease another session took over', integration, async () => {
  const env = session()
  const real = env.$.process.run
  env.$.process.run = async (argv, init) => {
    if (!argv.includes('notifications')) return real(argv, init)
    // S2 takes the lease over while the read of S1 hangs
    env.bin(['lease', 'drop', 'github', '--session', 'S1'])
    env.bin(['lease', 'hold', 'github', '--session', 'S2'])
    return { exitCode: 1, stdout: '', stderr: 'timeout' }
  }
  await testing.check(env.$)
  assert.equal(testing.reached.github, false)
  assert.equal(env.bin(['lease', 'hold', 'github', '--session', 'S3']).exitCode, 1)
})

test('a message meetproxy posted only moves the cursor', integration, async () => {
  const gh = githubMention()
  gh['repos/o/svc/issues/7/comments'][0][0].body = '@me retry is in config.go\n\n_Written by Claude on behalf of the user_'
  const env = session({ gh })
  env.bin(['inbox', 'advance', at('2030-01-01T00:00:00Z'), '--key', 'github'])
  await testing.check(env.$)
  assert.deepEqual(rows(env), [])
  assert.equal(githubCursor(env), at('2030-01-01T00:05:00Z'))
  assert.equal(env.triaged, 0)
})

test('a message triage ignores only moves the cursor and is never queued', integration, async () => {
  const env = session({ gh: githubMention() })
  env.$.model.complete = async () => ({ isAnswered: true, text: '{"verdict":"ignore","reason":"thanks"}' })
  env.bin(['inbox', 'advance', at('2030-01-01T00:00:00Z'), '--key', 'github'])
  await testing.check(env.$)
  assert.deepEqual(rows(env), [])
  assert.equal(githubCursor(env), at('2030-01-01T00:05:00Z'))
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
  const gh = githubMention()
  const env = session({ gh })
  env.bin(['inbox', 'advance', at('2030-01-01T00:06:00Z'), '--key', 'github'])
  await testing.check(env.$)
  await testing.check(env.$)
  // Another session reads the overlap again without this session's memory once the notification moved
  testing.sorted.clear()
  gh.notifications[0][0].updated_at = '2030-01-01T00:07:00Z'
  await testing.check(env.$)
  assert.deepEqual(rows(env).map(r => r.slice(1, 4)), [['new', 'svc', 'https://github.com/o/svc/pull/7#issuecomment-1']])
  assert.equal(env.triaged, 2)
  assert.equal(githubCursor(env), at('2030-01-01T00:07:00Z'))
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
    if (argv[1] === 'tick' && older.tickFails) return { exitCode: 1, stdout: '', stderr: 'meetproxy: inbox is broken\nmore detail' }
    return real(argv, init)
  }
  // Hooks of one event run in the order registered, each one's next running the one after it
  const hooks = {}
  const handlers = {}
  const chain = (fns, i) => ($, e, next) => (i < fns.length ? fns[i]($, e, ee => chain(fns, i + 1)($, ee, next)) : next(e))
  // A hook registered with a matcher on its tool sees only calls of that tool
  register((name, matcher, hook) => {
    const fn = hook ? ($, e, next) => (e.tool === matcher.tool ? hook($, e, next) : next(e)) : matcher
    hooks[name] = [...(hooks[name] ?? []), fn]
    handlers[name] = chain(hooks[name], 0)
    return { catch: c => { handlers[name + '.catch'] ??= c } }
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
    // Activity moves the notification so every check reads it again
    gh.notifications[0][0].updated_at = `2030-01-01T00:0${5 + i}:00Z`
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

// The meetproxy slack commands the watcher ran in order
const slackCalls = env => env.calls.filter(a => a[0] === 'slack').map(a => a[1])

test('with a token Slack is read and answered through the binary and the connector is never called', integration, async () => {
  const link = 'https://w.slack.com/archives/C1/p1893456100000001'
  const posted = []
  const env = session({
    slack: { slack_read_user_profile: () => ({ result: 'User ID: ME\nTeam: T1\n' }) },
    token: {
      mentions: () => ({ out: [{ author: 'Kai', channel: 'C1', from: 'U2', ts: '1893456100.000001', link, text: '<@ME> see https://w.slack.com/archives/C2/p1893456000000001' }] }),
      trusted: () => ({ out: { trusted: true } }),
      covered: () => ({ out: { covered: false } }),
      read: () => ({ out: { text: 'Lee: the context' } }),
      dms: () => ({ out: [] }),
      shared: () => ({ out: { shared: false } }),
      post: (args, stdin) => {
        posted.push([args[0], stdin])
        return { out: 'posted' }
      },
    },
  })
  env.bin(['inbox', 'advance', '1893456000', '--key', 'mention'])
  await testing.check(env.$)
  env.bin(['allow', 'slack:C1'])
  const result = await testing.post(env.$, link, 'hi')
  assert.deepEqual(rows(env).map(r => [r[1], r[3]]), [['new', link]])
  assert.equal(result, 'posted to ' + link)
  assert.deepEqual(posted, [[link, 'hi']])
  assert.deepEqual(env.mcp, [])
  assert.deepEqual(env.ran, [])
  assert.deepEqual(slackCalls(env), ['mentions', 'dms', 'trusted', 'covered', 'read', 'shared', 'post'])
})

test('without a token a session that does not hold the Slack lease never calls the connector', integration, async () => {
  const env = session({
    slack: {
      slack_read_user_profile: () => ({ result: 'User ID: ME\nEmail: me@x.com\n' }),
      slack_search_public_and_private: () => ({ results: '' }),
    },
  })
  env.bin(['lease', 'hold', 'slack', '--session', 'S2'])
  await testing.check(env.$)
  await testing.check(env.$)
  assert.deepEqual(env.mcp, [])
  assert.deepEqual(env.ran, [])
})

for (const tc of [
  { name: 'the Slack lease holder without a token asks once to set one up', answer: undefined, want: [{ command: 'meetproxy:slack', args: 'setup' }] },
  { name: 'a user who keeps the connector is not asked', answer: 'keep', want: [] },
]) {
  test(tc.name, integration, async () => {
    const env = session({
      slack: {
        slack_read_user_profile: () => ({ result: 'User ID: ME\nEmail: me@x.com\n' }),
        slack_search_public_and_private: () => ({ results: '' }),
      },
    })
    if (tc.answer) env.bin(['slack', 'setup', '--answer', tc.answer])
    await testing.check(env.$)
    await testing.check(env.$)
    assert.deepEqual(env.ran, tc.want)
    // The connector's user is checked live once for the lease
    assert.deepEqual(env.mcp.filter(t => t === 'slack_read_user_profile'), ['slack_read_user_profile'])
  })
}

test('a Slack answer in a shape the parser does not know fails the source instead of reading nothing', integration, async () => {
  const env = session({
    slack: {
      slack_read_user_profile: () => ({ result: 'User ID: ME\nEmail: me@x.com\n' }),
      slack_search_public_and_private: () => ({ results: '## Matches\n* a message in a new layout' }),
    },
  })
  await env.$.store.set('slackSetupAsked', NOW)
  await testing.check(env.$)
  assert.ok(env.logs.some(l => l.includes('unrecognized Slack answer format')))
  assert.equal(env.bin(['lease', 'hold', 'slack', '--session', 'S2']).exitCode, 0)
  assert.equal(JSON.parse(env.bin(['status']).stdout).sources.slack.error, 'unrecognized Slack answer format')
})

test('a tick the binary fails names the failure and not an update', integration, async () => {
  const env = session()
  await started(env, { tickFails: true })
  for (const fn of env.ticks) await fn()
  assert.deepEqual(env.statuses, ['meetproxy: inbox is broken, /meetproxy:status'])
})

test('a request whose reply cannot be checked asks the user', integration, async () => {
  const env = session({ gh: githubMention({ 'repos/o/svc/pulls/7/reviews': undefined }) })
  env.bin(['inbox', 'advance', at('2030-01-01T00:00:00Z'), '--key', 'github'])
  await testing.check(env.$)
  assert.deepEqual(rows(env).map(r => [r[1], r[4]]), [['ask', 'could not check whether you already replied']])
})

test('a notification whose updated_at did not change costs no read of its pull request', integration, async () => {
  const env = session({ gh: reviewRequest('2030-01-01T00:05:00Z') })
  env.bin(['inbox', 'advance', at('2030-01-01T00:00:00Z'), '--key', 'github'])
  await testing.check(env.$)
  const first = env.ghCalls.length
  await testing.check(env.$)
  const again = env.ghCalls.slice(first)
  assert.ok(env.ghCalls.slice(0, first).includes('https://api.github.com/repos/o/web/pulls/3'))
  assert.deepEqual(again.filter(p => p !== 'notifications'), [])
  assert.equal(githubCursor(env), at('2030-01-01T00:05:00Z'))
  // A day without the notification forgets it
  env.now += 25 * 60 * 60_000
  await env.$.store.set('ghseen:x', { updated: 'y', at: NOW })
  await testing.check(env.$)
  assert.equal(await env.$.store.get('ghseen:x'), undefined)
})

test('the lease is renewed after each message of a long check', integration, async () => {
  const gh = githubMention()
  gh['repos/o/svc/issues/7/comments'][0][1] = { html_url: 'https://github.com/o/svc/pull/7#issuecomment-2', body: '@me and this?', user: { login: 'lee' }, author_association: 'MEMBER', created_at: '2030-01-01T00:05:00Z' }
  const env = session({ gh })
  env.bin(['inbox', 'advance', at('2030-01-01T00:00:00Z'), '--key', 'github'])
  await testing.check(env.$)
  assert.equal(rows(env).length, 2)
  assert.equal(env.calls.filter(a => a[0] === 'lease' && a[1] === 'hold' && a[2] === 'github').length, 3)
})

test('a tick while a turn runs keeps the session takes alive', integration, async () => {
  const env = session()
  const handlers = await started(env, {})
  await handlers['turn.start'](env.$, { turnId: 't1', text: '' }, e => e)
  await env.ticks[0]()
  await handlers['turn.complete'](env.$, { turnId: 't1', answer: '' }, e => e)
  await env.ticks[0]()
  assert.deepEqual(env.calls.filter(a => a[0] === 'tick').map(a => [a.includes('--session'), a.includes('--busy')]), [[true, true], [true, false]])
})

test('the token tool hands the token to the binary and answers without it', integration, async () => {
  const given = []
  const env = session({
    token: {
      token: (args, stdin) => {
        given.push(stdin)
        return { out: { user: 'U1', team: 'T1', host: 'w.slack.com', missing: ['im:history'] } }
      },
    },
  })
  const handlers = await started(env, {})
  env.answer = 'xoxp-secret-1'
  const r = await handlers['tool.call'](env.$, { tool: 'mcp__meetproxy__slack_token' }, () => 'passed')
  assert.deepEqual(given, ['xoxp-secret-1'])
  assert.equal(r.result, 'stored the token of user U1 in team T1 at w.slack.com, it lacks im:history')
  assert.ok(!JSON.stringify(r).includes('xoxp'))
  env.answer = 'Cancel'
  assert.deepEqual(await handlers['tool.call'](env.$, { tool: 'mcp__meetproxy__slack_token' }, () => 'passed'), { result: 'no token was given' })
})

// Waits until the check holds or a second passes, for work the watcher does not await
async function until(check) {
  for (let i = 0; i < 100 && !check(); i++) await new Promise(r => setTimeout(r, 10))
}

const origin = 'https://github.com/o/svc/pull/7#issuecomment-1'
const comment = (id, login, body, at) => ({ id, html_url: `https://github.com/o/svc/pull/7#issuecomment-${id}`, body, user: { login }, author_association: 'MEMBER', created_at: at })

// A request answered through a relay so the ledger holds the post and the thread is watched
async function answered(env) {
  env.bin(['open', origin, '--session', 'S1'])
  assert.equal(await testing.post(env.$, origin, 'retry is in config.go\n\n_Written by Claude on behalf of the user_'), 'posted to ' + origin)
  env.bin(['close', '--topic', 'retry', '--session', 'S1'])
}

test('post writes the ledger with the permalink of the reply', integration, async () => {
  const env = session()
  await answered(env)
  const ledger = JSON.parse(env.bin(['posts', 'list']).stdout)
  assert.deepEqual(ledger.map(p => [p.origin, p.reply, p.mode, p.kind]), [[origin, 'https://github.com/o/svc/pull/7#issuecomment-900', 'manual', 'answer']])
  assert.deepEqual(tick(env).watch.map(w => w.thread), [origin])
})

test('a follow-up in a watched thread reopens the same request', integration, async () => {
  const env = session({
    gh: {
      notifications: [[]],
      'repos/o/svc/issues/7/comments': [[comment(901, 'kai', 'and where is the timeout?', '2030-01-01T00:04:00Z'), comment(902, 'me', 'mine', '2030-01-01T00:03:00Z')]],
      'repos/o/svc/pulls/7/reviews': [[]],
    },
  })
  await answered(env)
  await testing.check(env.$)
  // The request keeps the link and so the id of the request the post answered
  assert.deepEqual(rows(env).map(r => [r[1], r[3]]), [['new', origin]])
  assert.deepEqual(JSON.parse(env.bin(['posts', 'list']).stdout).map(p => p.request), [rows(env)[0][0]])
  assert.deepEqual(tick(env).watch.map(w => w.seen), [at('2030-01-01T00:04:00Z')])
  // Read once so the next check queues nothing new
  testing.sorted.clear()
  await testing.check(env.$)
  assert.equal(env.triaged, 1)
})

test('a follow-up that says the answer was wrong halves the record and asks the user', integration, async () => {
  const env = session({
    gh: {
      notifications: [[]],
      'repos/o/svc/issues/7/comments': [[comment(901, 'kai', 'that is wrong, it is in retry.go', '2030-01-01T00:04:00Z')]],
      'repos/o/svc/pulls/7/reviews': [[]],
    },
  })
  env.$.model.complete = async () => ({ isAnswered: true, text: '{"verdict":"handle","reason":"fix","correction":true}' })
  await answered(env)
  await testing.check(env.$)
  assert.deepEqual(rows(env).map(r => [r[1], r[4]]), [['ask', 'the requester says the earlier answer was wrong']])
  assert.ok(env.calls.some(a => a[0] === 'correct'))
  const taken = env.bin(['inbox', 'take', rows(env)[0][0], '--session', 'S1']).stdout.trim().split('\t')
  assert.deepEqual(taken.slice(7), ['quick', 'followup,correction'])
})

test('a reply meetproxy posted for anyone covers the request', integration, async () => {
  const gh = githubMention()
  gh['repos/o/svc/issues/7/comments'][0][1].body = 'it is in config.go\n\n_Written by Claude on behalf of the user_'
  const env = session({ gh })
  env.bin(['inbox', 'advance', at('2030-01-01T00:00:00Z'), '--key', 'github'])
  await testing.check(env.$)
  assert.deepEqual(rows(env), [])
  assert.equal(env.triaged, 0)
})

test('comments on the user own pull request come from others only and ask first', integration, async () => {
  const env = session({
    gh: {
      notifications: [[{
        reason: 'author', updated_at: '2030-01-01T00:05:00Z', repository: { full_name: 'o/svc' },
        subject: { type: 'PullRequest', title: 'Fix', url: 'https://api.github.com/repos/o/svc/pulls/7' },
      }]],
      'https://api.github.com/repos/o/svc/pulls/7': { html_url: 'https://github.com/o/svc/pull/7', user: { login: 'me' } },
      'repos/o/svc/issues/7/comments': [[
        comment(1, 'kai', 'why a new retry?', '2030-01-01T00:04:00Z'),
        comment(2, 'me', 'opening note', '2030-01-01T00:03:00Z'),
        { ...comment(3, 'dependabot[bot]', 'bump', '2030-01-01T00:04:20Z'), user: { login: 'dependabot[bot]', type: 'Bot' } },
      ]],
      'repos/o/svc/pulls/7/comments': [[]],
      'repos/o/svc/pulls/7/reviews': [[]],
    },
  })
  env.bin(['inbox', 'advance', at('2030-01-01T00:00:00Z'), '--key', 'github'])
  await testing.check(env.$)
  assert.deepEqual(rows(env).map(r => [r[1], r[3]]), [['ask', 'https://github.com/o/svc/pull/7#issuecomment-1']])
  assert.deepEqual(tick(env).waiting.map(r => r.delegation), ['default-own-pr'])
})

// A session reading Slack with a token where every new timer path has something to do
function tokenSession(extra = {}) {
  const link = 'https://w.slack.com/archives/C1/p1893456100000001'
  const reacted = []
  const env = session({
    slack: { slack_read_user_profile: () => ({ result: 'User ID: ME\nTeam: T1\n' }) },
    token: {
      mentions: () => ({ out: [{ author: 'Kai', channel: 'C1', from: 'U2', ts: '1893456100.000001', link, text: '<@ME> where is retry' }] }),
      // The same message seen by both readers is queued once
      dms: () => ({ out: [{ author: 'Kai', channel: 'C1', from: 'U2', ts: '1893456100.000001', link, text: '<@ME> where is retry' }] }),
      trusted: () => ({ out: { trusted: true } }),
      covered: () => ({ out: { covered: false } }),
      shared: () => ({ out: { shared: true } }),
      replies: () => ({ out: [] }),
      react: args => {
        reacted.push(args)
        return { out: '' }
      },
      ...extra,
    },
  })
  env.bin(['inbox', 'advance', '1893456000', '--key', 'mention'])
  env.bin(['inbox', 'advance', '1893456000', '--key', 'dm'])
  return { env, link, reacted }
}

test('with a token a shared channel asks first, reactions go once and the connector is never called', integration, async () => {
  const { env, link, reacted } = tokenSession()
  await testing.check(env.$)
  assert.deepEqual(rows(env).map(r => [r[1], r[3], r[4]]), [['ask', link, 'shared channel with people outside the workspace']])
  await testing.check(env.$)
  assert.deepEqual(reacted, [[link, '--react', 'eyes']])
  assert.deepEqual(env.mcp, [])
})

test('with a token a Slack follow-up is read through the binary', integration, async () => {
  const thread = 'https://w.slack.com/archives/C1/p1893456100000001'
  const { env } = tokenSession({
    mentions: () => ({ out: [] }),
    dms: () => ({ out: [] }),
    shared: () => ({ out: { shared: false } }),
    post: () => ({ out: thread.replace('p1893456100000001', 'p1893456150000001') + '?thread_ts=1893456100.000001&cid=C1' }),
    replies: args => ({ out: args[0] === thread ? [{ author: 'Kai', channel: 'C1', from: 'U2', ts: '1893456200.000001', link: thread + '?x', text: 'and the timeout?' }] : [] }),
  })
  env.bin(['open', thread, '--session', 'S1'])
  assert.equal(await testing.post(env.$, thread, 'in config.go'), 'posted to ' + thread)
  env.bin(['close', '--topic', 'retry', '--session', 'S1'])
  await testing.check(env.$)
  assert.deepEqual(rows(env).map(r => [r[1], r[3]]), [['new', thread]])
  assert.deepEqual(env.mcp, [])
})

test('a busy session leaves a request to an idle session of its place', integration, async () => {
  for (const tc of [
    { peer: { place: '/tmp/notes', name: 'notes' }, ago: 11 * 60_000, want: [] },
    { peer: { place: '/w/web', name: 'web' }, ago: 2 * 60_000, want: [] },
    { peer: { place: '/w/web', name: 'web' }, ago: 11 * 60_000, want: ['--auto'] },
  ]) {
    const env = session({ repo: '/tmp/notes', now: Date.now() + tc.ago })
    await env.$.store.set('session:S2', { ...tc.peer, at: env.now })
    await env.$.store.set('activity:S2', { in_turn: false })
    env.bin(['inbox', 'add', 'https://w.slack.com/archives/C1/p1', '--verdict', 'handle', '--trusted', 'yes', '--name', 'notes', '--place', '/tmp/notes'])
    testing.busy(true)
    await testing.pickUp(env.$, tick(env))
    assert.deepEqual(env.ran.map(c => c.args.split(' ')[1]), tc.want)
  }
})

test('several requests that need the user are offered in one question', integration, async () => {
  const env = session({ repo: '/tmp/notes' })
  for (const n of [1, 2, 3]) env.bin(['inbox', 'add', 'https://w.slack.com/archives/C1/p' + n, '--verdict', 'ask', '--name', 'notes', '--place', '/tmp/notes'])
  const ids = tick(env).waiting.map(r => r.id)
  env.answer = (q, o) => o.options[1]
  await testing.pickUp(env.$, tick(env))
  await until(() => env.ran.length)
  assert.equal(env.asked.length, 1)
  assert.deepEqual(env.asked[0].o.options, ids.map(id => `${id} · notes · default`))
  assert.equal(env.asked[0].o.multiSelect, true)
  assert.ok(!JSON.stringify(env.asked).includes('slack.com'))
  assert.deepEqual(env.ran.map(c => c.args), [ids[1] + ' --ask'])
  const held = tick(env).waiting.filter(r => r.status === 'held')
  assert.deepEqual(held.map(r => r.id), [ids[0], ids[2]])
  assert.ok(held.every(r => r.until > Date.now() / 1000 + 3600))
})

test('a held request whose time came is asked about again', integration, async () => {
  const env = session({ repo: '/tmp/notes' })
  env.bin(['inbox', 'add', 'https://w.slack.com/archives/C1/p1', '--verdict', 'ask', '--name', 'notes', '--place', '/tmp/notes'])
  const id = rows(env)[0][0]
  assert.equal(env.bin(['inbox', 'hold', id, '--until', '2000-01-01T00:00:00Z', '--session', 'S1']).exitCode, 0)
  assert.equal(tick(env).waiting[0].due, true)
  await testing.pickUp(env.$, tick(env))
  assert.deepEqual(env.ran.map(c => c.args), [id + ' --ask'])
})

test('a source that fails asks once a day with its fix and can be stopped', integration, async () => {
  const env = session({ gh: { notifications: undefined } })
  await testing.check(env.$)
  // A minute passes between ticks so the first question is answered before the next one
  const settle = () => new Promise(r => setTimeout(r, 20))
  await testing.nag(env.$, tick(env))
  await settle()
  await testing.nag(env.$, tick(env))
  await settle()
  assert.equal(env.asked.length, 1)
  assert.match(env.asked[0].q, /gh auth login/)
  assert.deepEqual(env.asked[0].o.options, ['Done', 'Remind tomorrow', 'Stop reading github'])
  env.now += 25 * 60 * 60_000
  env.answer = 'Stop reading github'
  await testing.nag(env.$, tick(env))
  await settle()
  assert.equal(env.asked.length, 2)
  assert.equal(await env.$.store.get('muted:github'), true)
  env.ghCalls.length = 0
  await testing.check(env.$)
  assert.deepEqual(env.ghCalls, [])
})

test('retract refuses a reply outside the ledger and edits one in it', integration, async () => {
  const env = session()
  assert.match(await testing.retract(env.$, 'https://github.com/o/svc/pull/7#issuecomment-5', 'x'), /^denied/)
  await answered(env)
  const reply = 'https://github.com/o/svc/pull/7#issuecomment-900'
  assert.equal(await testing.retract(env.$, reply, 'it is in retry.go'), 'corrected ' + reply)
  assert.deepEqual(env.edits, [['PATCH', 'repos/o/svc/issues/comments/900', 'it is in retry.go\n\n_Written by Claude on behalf of the user_']])
  assert.ok(JSON.parse(env.bin(['posts', 'list']).stdout)[0].retracted_at)
  assert.match(await testing.retract(env.$, reply), /already retracted/)
})

// Brings requests from every connected source to the open session that fits them so nobody carries them
// 1. For each source one session where it works holds its lease and receives from it
// 2. Messages go to the first matching delegation and triage sorts those the default delegation catches
// 3. The work map built from the user's own transcripts names the place, skills and files for the request
//    Triage picks among the map's candidates only when the map cannot decide
// 4. A session working in that place takes it and runs /meetproxy:handle
//    The lease holder also takes those no open session covers and works at the place by its absolute paths
// 5. Requests that need the user start a turn that asks them before anything is posted
// 6. Replies go out through the post tool
//    It sends with the source that owns the link
// 7. Slack is read through the binary with the user's own token when one is set
//    A connector call fires the PreToolUse hooks and terminal multiplexers then show the session as running
//    So without a token only the session holding the Slack lease calls the connector

const TICK_MS = 60_000
const FIRST_MS = 15_000
const LEASE_MS = 3 * TICK_MS
// A request found later than this came in while no session was open and is not answered on its own
const STALE_MS = 60 * 60_000
// Times a message may fail to be queued before it is skipped
const MAX_FAILURES = 3
// How far before the cursor searches read again
// Slack search and GitHub notifications can show a message minutes after its timestamp
const OVERLAP_MS = 5 * 60_000
// How long a sorted message is remembered after the last read that found it
// Longer than the overlap so a message the overlap keeps finding is never sorted twice
const SORTED_MS = 2 * OVERLAP_MS
// What the binary must speak
// A session still running an older watcher stops instead of misreading a newer binary
const PROTOCOL = 7
// Characters of nodloop notes given to triage
const KNOWLEDGE_CHARS = 2000
// Enough for the one line verdict triage answers with
const TRIAGE_TOKENS = 200
const TRIAGE_MS = 30_000
const UPDATED = 'meetproxy was updated, run /reload-plugins in this session'
const NODLOOP = 'plugin:nodloop:nodloop'
const MARK = '_Written by Claude on behalf of the user_'
// Longer than the two minute run timeout of triage run so a slow engine ends there first
const BINARY_MS = 150_000
const SLACK_LINK = /https:\/\/[\w-]+\.slack\.com\/archives\/([A-Z0-9]+)\/p(\d{10})(\d{6})/
const GITHUB_LINK = /https:\/\/github\.com\/([\w.-]+)\/([\w.-]+)\/(pull|issues)\/(\d+)/
// Pages of 100 messages read from one channel in one check
// Far more than a busy channel gets in a day and still a bound on a runaway loop
const MAX_PAGES = 50
// The most results Slack search answers with in one page
const SEARCH_PAGE = 20
// How long a Slack user's trust is remembered
const TRUST_MS = 24 * 60 * 60_000
// How long the user is left alone after being asked about a Slack token or answering later
const SETUP_AGAIN_MS = 7 * 24 * 60 * 60_000
// Profile lines that mark a guest or restricted account
const RESTRICTED = /\b(?:is[ _])?(?:ultra[ _])?(?:restricted|guest)\b[^:\n\\]*:\s*"?(?:true|yes)|"is_(?:ultra_)?restricted"\s*:\s*true|Account Type: [^\n\\]*(?:guest|restricted)/i
// What a Slack connector answer says when nothing matched
const NO_RESULTS = /\bno (?:results|messages|matches)\b|\bnothing found\b|^\s*Channel: [^\n]*\s*$/i
// Characters kept of each Slack message a message links to
const LINKED_CHARS = 1500
const SLACK = 'plugin:slack:slack'
// The tools the Slack connector lists
const SLACK_TOOLS = 'mcp__plugin_slack_slack__'
// Choices of the token question besides the token typed under Other
const TOKEN_CANCEL = 'Cancel'
const TOKEN_TERMINAL = 'Paste it in my terminal'
// Characters of a message kept for triage and the work map
const TEXT_CHARS = 4000
// How long a GitHub notification read whole is remembered by its updated_at
// One that does not change in that time was answered or dropped long ago
const GHSEEN_MS = 24 * 60 * 60_000
// How often a remembered notification that shows up again renews its memory
const GHSEEN_RENEW_MS = 60 * 60_000
// GitHub associations of members of the repository's own organization or its collaborators
const TRUSTED_ASSOCIATIONS = ['OWNER', 'MEMBER', 'COLLABORATOR']

let stop
// What keeps the watcher from working
// Shown until it is fixed
let problem
// Sources this session reached itself
// The ids in the store are shared so they cannot tell whether this session reaches a source
const reached = {}
// Failed attempts of one step for one message keyed by the step and the link
// Each step counts on its own so failures of one never use up the tries of another
const failures = new Map()
// Messages this session already sorted mapped to the last time in ms a read found them
// The overlap reads them again and this keeps them from costing another triage
const sorted = new Map()
// Sources whose lease this session holds mapped to when in ms it runs out
const holding = {}
// Whether a turn of the main loop is running so the tick keeps this session's takes alive
let turning = false
// The last trouble recorded per source so a failure that repeats every check is recorded once
const troubled = {}
// The last tick the binary answered
let lastTick
// Whether the Slack user the connector acts for was checked since this session took the lease
let verified = false

export function register(on) {
  on('session.start', async ($, e, next) => {
    await $.tool.register({
      name: 'post',
      description:
        'Post a reply in the thread of a Slack or GitHub link, such as where a meetproxy request came from. ' +
        'Only where the open request came from, what it works on and the allow list are accepted.',
      inputSchema: {
        type: 'object',
        properties: { link: { type: 'string' }, text: { type: 'string' } },
        required: ['link', 'text'],
      },
    })
    await $.tool.register({
      name: 'slack_token',
      description:
        'Ask the user for their Slack user token in a dialog and store it for meetproxy. ' +
        'The token never reaches the model, only the team and user it acts for and the scopes it lacks.',
      inputSchema: { type: 'object', properties: {} },
    })
    cancel(stop)
    stop = undefined
    const version = await meetproxy($, ['protocol'])
    if (!version.ok) {
      $.ui.status(failed(version))
      return next(e)
    }
    if (Number(version.out) !== PROTOCOL) {
      $.ui.status(UPDATED)
      return next(e)
    }
    let busy = false
    const tick = async () => {
      if (busy) return
      busy = true
      try {
        // The binary can change under a running session so every tick checks again
        const args = ['tick', '--cwd', await $.session.cwd(), '--session', await $.session.id()]
        if (turning) args.push('--busy')
        const r = await meetproxy($, args)
        if (!r.ok) {
          $.ui.status(failed(r))
          return
        }
        const t = parseJSON(r.out)
        if (t?.protocol !== PROTOCOL) {
          $.ui.status(UPDATED)
          return
        }
        lastTick = t
        await check($, t)
        await pickUp($, t)
      } catch (err) {
        $.ui.log('meetproxy: ' + message(err))
      } finally {
        busy = false
      }
    }
    stop = [$.clock.after(FIRST_MS, tick), $.clock.every(TICK_MS, tick)]
    return next(e)
  })

  // A subagent's run raises no turn.start so only the main loop's turns count
  on('turn.start', ($, e, next) => {
    turning = true
    return next(e)
  })
  on('turn.complete', ($, e, next) => {
    if (!e.agentId) turning = false
    return next(e)
  })

  // A post whose check fails or runs out of time is denied rather than sent
  on('tool.call', { tool: 'mcp__meetproxy__post' }, async ($, e) => {
    return post($, e.link, e.text)
      .then(r => ({ result: r }))
      .catch(err => ({ result: 'denied: post check failed ' + message(err) }))
  }).catch(($, e, next) => (next.called ? next(e) : { result: 'denied: post check failed ' + (next.error?.message ?? next.error?.kind ?? '') }))

  // The token goes from the dialog to the binary and only what it acts for comes back
  on('tool.call', { tool: 'mcp__meetproxy__slack_token' }, async $ => ({ result: await askToken($) })).catch(($, e, next) => (next.called ? next(e) : { result: 'no token was stored: ' + (next.error?.message ?? next.error?.kind ?? '') }))
}

// Sources

// Each source is one case in each of these five dispatchers
// 1. owner(link): the source a link belongs to
// 2. ready($, source, t): whether its connection works now
// 3. receive($, source, t): messages from a little before its cursors on
//    Each names its source and the kind its delegation is matched by
//    The kind is mention, review-request or a channel delegation id
//    Each also names the cursor key its position is kept under
//    Beside them the newest position seen per cursor key and what to remember once every message is settled
// 4. answered($, m): whether the user already replied after the message, undefined when that cannot be told
// 5. send($, source, link, reply): a reply in the thread of the link
// The mod validator rejects passing $ through object methods so sources dispatch by switch instead of a table
const SOURCES = ['slack', 'github']

function owner(link) {
  if (slackLink(link)) return 'slack'
  if (githubLink(link)) return 'github'
  return undefined
}

// Slack is ready when a token is set or the connector lists its tools
// Neither costs a connector call
async function ready($, source, t) {
  switch (source) {
    case 'slack':
      return Boolean(await slackRoute($, t))
    case 'github':
      if (reached.github) return true
      try {
        await githubUser($, true)
        reached.github = true
      } catch (err) {
        await trouble($, 'github', err, true)
      }
      return Boolean(reached.github)
  }
  return false
}

async function receive($, source, t) {
  switch (source) {
    case 'slack':
      return slackReceive($, t)
    case 'github':
      return githubReceive($)
  }
  return { messages: [], newest: {}, marks: [] }
}

// Whether the user already replied after the message
// Replies by hand and through meetproxy both count
async function answered($, m) {
  try {
    switch (m.source) {
      case 'slack':
        return await slackAnswered($, m)
      case 'github':
        return await githubAnswered($, m)
    }
  } catch (err) {
    await trouble($, m.source, err)
    return undefined
  }
  return false
}

async function send($, source, link, reply) {
  switch (source) {
    case 'slack':
      return slackSend($, link, reply)
    case 'github':
      return githubSend($, link, reply)
  }
}

// Slack

// How this session reaches Slack
// 1. token: the binary calls Slack with the user's token so no tool call fires any hook
// 2. mcp: the Slack connector, which $.tool.list names without calling it
// 3. undefined: neither
async function slackRoute($, t) {
  t ??= lastTick ?? parseJSON((await meetproxy($, ['tick', '--cwd', await $.session.cwd()])).out)
  if (t?.slack?.token) return 'token'
  const tools = await $.tool.list()
  return tools.some(x => x.name.startsWith(SLACK_TOOLS)) ? 'mcp' : undefined
}

// Runs once this session holds the Slack lease
// 1. The connector's user is checked live once per lease
// 2. A session reading through the connector asks the user once whether to set up a token
async function slackHeld($, t) {
  if ((await slackRoute($, t)) !== 'mcp') return
  if (!verified) {
    await slackUser($, true)
    verified = true
  }
  await offerToken($, t)
}

// Asks again only after the user said later or a week after the last ask went unanswered
async function offerToken($, t) {
  const setup = t?.slack?.setup
  const now = await $.clock.now()
  if (setup?.answer === 'keep' || (setup && now - setup.at * 1000 < SETUP_AGAIN_MS)) return
  const asked = await $.store.get('slackSetupAsked')
  if (asked && now - asked < SETUP_AGAIN_MS) return
  await $.store.set('slackSetupAsked', now)
  // Not awaited since the command waits for the user and the tick goes on meanwhile
  $.command.run({ command: $.plugin.name + ':slack', args: 'setup' }).catch(err => $.ui.log('meetproxy: ' + message(err)))
}

async function slackReceive($, t) {
  const via = await slackRoute($, t)
  const me = await slackUser($, false, t)
  const delegations = parseJSON((await meetproxy($, ['delegation'])).out) ?? []
  const messages = []
  const newest = {}
  for (const d of delegations.filter(d => d.when === 'channel')) {
    const cursor = await meetproxy($, ['inbox', 'cursor', '--key', d.id])
    if (!cursor.ok) continue
    for (const m of await slackChannel($, d, cursor.out, via)) {
      newest[d.id] = later(newest[d.id], m.ts)
      if (Number(m.ts) > Number(cursor.out)) messages.push({ ...m, source: 'slack', kind: d.id, cursor: d.id, self: m.from === me })
    }
  }
  // The mention cursor keeps the key it had before sources
  const cursor = await meetproxy($, ['inbox', 'cursor', '--key', 'mention'])
  if (!cursor.ok) return { messages, newest, marks: [] }
  const after = overlapped(cursor.out)
  for (const m of await slackMentions($, me, after, via)) {
    newest.mention = later(newest.mention, m.ts)
    if (Number(m.ts) > after) messages.push({ ...m, source: 'slack', kind: 'mention', cursor: 'mention', self: m.from === me })
  }
  return { messages, newest, marks: [] }
}

// Results come oldest first and every page is read
// One page could hold only messages the overlap already saw so stopping there would never move the cursor
async function slackMentions($, me, after, via) {
  if (via === 'token') return fromBinary($, await meetproxy($, ['slack', 'mentions', '--after', String(after)]))
  const out = []
  let page
  for (let i = 0; i < MAX_PAGES; i++) {
    const found = await $.mcp.call(SLACK, 'slack_search_public_and_private', {
      keywords: ['<@' + me + '>'],
      after: String(Math.floor(after)),
      sort: 'timestamp',
      sort_dir: 'asc',
      include_bots: false,
      include_context: false,
      limit: SEARCH_PAGE,
      cursor: page,
    })
    out.push(...recognized(found, 'results', searchMessages(found)))
    page = nextPage(found)
    if (!page) break
  }
  return out.sort(byTs)
}

// The reader answers newest first so every page back to the cursor is read before any message is handled
async function slackChannel($, d, oldest, via) {
  if (via === 'token') return fromBinary($, await meetproxy($, ['slack', 'history', d.channel, '--oldest', oldest]))
  const out = []
  let page
  for (let i = 0; i < MAX_PAGES; i++) {
    const found = await $.mcp.call(SLACK, 'slack_read_channel', { channel_id: d.channel, oldest, limit: 100, cursor: page })
    out.push(...recognized(found, 'messages', channelMessages(found, d.channel, d.host)))
    page = nextPage(found)
    if (!page) return out.sort(byTs)
  }
  // Reading back to the cursor is impossible within the bound so the older messages are skipped on purpose
  $.ui.log(`meetproxy: #${d.channel} had more than ${MAX_PAGES * 100} messages since the last check and the older ones are skipped`)
  return out.sort(byTs)
}

// The messages a slack command of the binary printed
// What it says on stderr such as messages it skipped is logged
function fromBinary($, r) {
  if (!r.ok) throw new Error(firstLine(r.err) || 'slack exited ' + r.code)
  if (r.err) $.ui.log('meetproxy: ' + r.err)
  const out = parseJSON(r.out)
  if (!Array.isArray(out)) throw new Error('unrecognized answer of meetproxy slack')
  return out
}

// A connector answer with text but no message in it is a format this parser no longer reads
// Failing makes the source give up its lease and shows in status instead of skipping messages quietly
function recognized(result, field, messages) {
  const md = String(parseJSON(text(result))?.[field] ?? text(result))
  if (messages.length || !md.trim() || NO_RESULTS.test(md)) return messages
  throw new Error('unrecognized Slack answer format')
}

// The concise thread lists each reply on a line that starts with > and names its author by email
// oldest is just past the message so a request the user wrote in the thread does not count as a reply
async function slackAnswered($, m) {
  if ((await slackRoute($)) === 'token') {
    const r = await meetproxy($, ['slack', 'answered', m.link, '--ts', m.ts])
    const got = parseJSON(r.out)
    if (!r.ok || typeof got?.answered !== 'boolean') throw new Error('could not read the thread: ' + firstLine(r.err))
    return got.answered
  }
  // A profile that shows no email leaves nothing to match replies by, as before tokens
  const email = await $.store.get('slackEmail')
  const at = slackLink(m.link)
  if (!email || !at) return false
  const r = await $.mcp.call(SLACK, 'slack_read_thread', {
    channel_id: at.channel,
    message_ts: at.thread,
    oldest: (Number(m.ts) + 0.000001).toFixed(6),
    limit: 50,
    response_format: 'concise',
  })
  const thread = parseJSON(text(r))?.messages ?? text(r)
  return thread.split('\n').some(l => l.startsWith('> ') && l.includes('<' + email + '>:'))
}

async function slackSend($, link, reply) {
  const at = slackLink(link)
  if (!at) throw new Error('not a Slack message link ' + link)
  if ((await slackRoute($)) === 'token') {
    const r = await meetproxy($, ['slack', 'post', link, '--session', await $.session.id()], reply)
    if (!r.ok) throw new Error(r.out === 'denied' ? 'denied' : firstLine(r.err))
    return
  }
  const r = await $.mcp.call(SLACK, 'slack_send_message', { channel_id: at.channel, thread_ts: at.thread, message: reply })
  if (r?.isError) throw new Error(text(r))
}

// The token is typed under Other in the dialog
// It goes to the binary on stdin and never into a result the model reads
async function askToken($) {
  let answer
  try {
    answer = await $.ui.ask('Paste the User OAuth Token that starts with xoxp- under Other. Which token should meetproxy use?', {
      header: 'Slack token',
      options: [TOKEN_CANCEL, TOKEN_TERMINAL],
    })
  } catch (err) {
    return 'the token question was dismissed: ' + message(err)
  }
  if (answer === TOKEN_TERMINAL) return 'the user pastes the token in their own terminal'
  if (answer === TOKEN_CANCEL || !answer?.trim()) return 'no token was given'
  const r = await meetproxy($, ['slack', 'token'], answer.trim())
  if (!r.ok) return 'Slack did not accept the token: ' + firstLine(r.err)
  lastTick = undefined
  const a = parseJSON(r.out) ?? {}
  const lacks = a.missing?.length ? ', it lacks ' + a.missing.join(', ') : ', it has every scope meetproxy needs'
  return `stored the token of user ${a.user} in team ${a.team} at ${a.host}${lacks}`
}

// Text of up to two Slack messages a message links to
// Read only for triage and routing so it is never stored
async function linkedMessages($, s) {
  const found = [...s.matchAll(new RegExp(SLACK_LINK.source, 'g'))].slice(0, 2)
  if (!found.length) return []
  const via = await slackRoute($)
  const out = []
  for (const [link] of found) {
    try {
      const t = (await linkedText($, link, via)).trim()
      if (t) out.push(t.slice(0, LINKED_CHARS))
    } catch (err) {
      await trouble($, 'slack', err)
    }
  }
  return out
}

async function linkedText($, link, via) {
  if (via === 'token') {
    const r = await meetproxy($, ['slack', 'read', link, '--limit', '5'])
    if (!r.ok) throw new Error('could not read ' + link + ': ' + firstLine(r.err))
    return parseJSON(r.out)?.text ?? ''
  }
  if (via !== 'mcp') return ''
  const at = slackLink(link)
  const r = await $.mcp.call(SLACK, 'slack_read_thread', {
    channel_id: at.channel,
    message_ts: at.ts,
    limit: 5,
    response_format: 'concise',
  })
  return parseJSON(text(r))?.messages ?? text(r)
}

// With a token the user the token acts for, which the tick names
// Through the connector the stored one, read live only when asked
async function slackUser($, live, t) {
  const tick = t ?? lastTick
  if (tick?.slack?.token) {
    if (tick.slack.user) return tick.slack.user
    const who = parseJSON((await meetproxy($, ['slack', 'whoami'])).out)
    if (!who?.user) throw new Error('could not read the Slack user id')
    return who.user
  }
  const cached = await $.store.get('slackUser')
  if (cached && !live) return cached
  const r = await $.mcp.call(SLACK, 'slack_read_user_profile', {})
  const id = /User ID: (\w+)/.exec(text(r))?.[1]
  if (!id) throw new Error('could not read the Slack user id')
  await $.store.set('slackUser', id)
  const email = /Email: ([^\s\\]+)/.exec(text(r))?.[1]
  if (email) await $.store.set('slackEmail', email)
  const team = slackTeam(text(r))
  if (team) await $.store.set('slackTeam', team)
  return id
}

// Whether a Slack user is a full member of the user's own workspace
// 1. The profile names the same team as the user's own
// 2. Guests, restricted and deactivated accounts are never trusted
// 3. A profile that cannot be read is not trusted and is read again on the next message
async function slackTrusted($, id) {
  if (!id) return false
  const key = 'slackTrust:' + id
  const now = await $.clock.now()
  const cached = await $.store.get(key)
  if (cached && now - cached.at < TRUST_MS) return cached.trusted
  try {
    const trusted = (await slackRoute($)) === 'token' ? await tokenTrusts($, id) : await connectorTrusts($, id)
    if (trusted === undefined) return false
    await $.store.set(key, { trusted, at: now })
    return trusted
  } catch (err) {
    await trouble($, 'slack', err)
    return false
  }
}

async function tokenTrusts($, id) {
  const r = await meetproxy($, ['slack', 'trusted', id])
  const got = parseJSON(r.out)
  if (!r.ok || typeof got?.trusted !== 'boolean') throw new Error('could not read the Slack profile of ' + id + ': ' + firstLine(r.err))
  return got.trusted
}

// undefined when the profile cannot be read
async function connectorTrusts($, id) {
  await slackUser($)
  const mine = await $.store.get('slackTeam')
  const profile = text(await $.mcp.call(SLACK, 'slack_read_user_profile', { user_id: id }))
  if (!/User ID: /.test(profile)) return undefined
  return Boolean(mine) && slackTeam(profile) === mine && !RESTRICTED.test(profile)
}

// The team id a profile names in any of the shapes the profile tool answers with
function slackTeam(profile) {
  return /Team(?: ID)?: (\w+)/i.exec(profile)?.[1] ?? /"team(?:_id)?"\s*:\s*"(\w+)"/.exec(profile)?.[1]
}

// The cursor of the next page in a Slack tool answer
function nextPage(result) {
  const info = parseJSON(text(result))?.pagination_info ?? ''
  return /cursor: `([^`]+)`/.exec(info)?.[1]
}

// The search tool answers with JSON whose results field is markdown
// Each message is one block
function searchMessages(result) {
  const md = parseJSON(text(result))?.results ?? text(result)
  return md
    .split(/^### Result /m)
    .slice(1)
    .map(block => ({
      author: /^From: ([^<(]*)/m.exec(block)?.[1]?.trim() ?? '',
      channel: /^Channel: .*\(ID: (\w+)\)/m.exec(block)?.[1] ?? '',
      from: /^From: .*\(ID: (\w+)\)/m.exec(block)?.[1] ?? '',
      ts: /^Message_ts: ([\d.]+)/m.exec(block)?.[1] ?? '',
      link: /^Permalink: \[link\]\((\S+)\)/m.exec(block)?.[1] ?? '',
      text: body(block),
    }))
    .filter(m => m.ts && m.link)
    .sort(byTs)
}

// The channel reader answers with one block per message newest first
// Its blocks carry no permalink so one is built from the host of the delegation
function channelMessages(result, channel, host) {
  const md = parseJSON(text(result))?.messages ?? text(result)
  return md
    .split(/^=== Message from /m)
    .slice(1)
    .map(block => {
      const ts = /^Message TS: ([\d.]+)/m.exec(block)?.[1] ?? ''
      const at = block.indexOf('\n\n')
      return {
        author: /^([^<(]*)/.exec(block)?.[1]?.trim() ?? '',
        from: /\((\w+)\) at /.exec(block)?.[1] ?? '',
        channel,
        ts,
        link: ts ? `https://${host}/archives/${channel}/p${ts.replace('.', '')}` : '',
        text: at < 0 ? '' : block.slice(at).trim(),
      }
    })
    .filter(m => m.ts)
    .sort(byTs)
}

function body(block) {
  const at = block.indexOf('Text: \n')
  return at < 0 ? '' : block.slice(at + 'Text: \n'.length).replace(/\n-{3}\s*$/, '').trim()
}

// GitHub

// Notifications that name the user
// 1. Read without marking them read
// 2. One whose updated_at did not change since it was read whole is skipped without reading its subject
async function githubReceive($) {
  const me = await githubUser($)
  const cursor = await meetproxy($, ['inbox', 'cursor', '--key', 'github'])
  if (!cursor.ok) return { messages: [], newest: {}, marks: [] }
  const after = overlapped(cursor.out)
  const since = new Date(after * 1000).toISOString()
  const pages = parseJSON(await gh($, ['api', '--paginate', '--slurp', 'notifications', '-X', 'GET', '-f', 'participating=true', '-f', 'since=' + since])) ?? []
  const now = await $.clock.now()
  const messages = []
  const newest = {}
  const marks = []
  for (const n of pages.flat()) {
    newest.github = later(newest.github, seconds(n.updated_at))
    const key = 'ghseen:' + (n.id ?? n.subject?.url)
    const seen = await $.store.get(key)
    if (seen?.updated === n.updated_at) {
      if (now - seen.at > GHSEEN_RENEW_MS) await $.store.set(key, { ...seen, at: now })
      continue
    }
    marks.push({ key, value: { updated: n.updated_at, at: now } })
    messages.push(...(await githubMessages($, n, me, after)))
  }
  await forgetNotifications($, now)
  return { messages: messages.sort(byTs), newest, marks }
}

async function githubMessages($, n, me, after) {
  const base = { source: 'github', cursor: 'github', channel: n.repository.full_name }
  const out = []
  if (n.reason === 'review_requested') {
    const pr = parseJSON(await gh($, ['api', n.subject.url]))
    if (!pr?.html_url) return out
    const ts = await reviewRequested($, n, me)
    if (Number(ts) > after) {
      const content = `Review requested: ${n.subject.title}\n${pr.body ?? ''}`
      out.push({ ...base, kind: 'review-request', self: false, trusted: associated(pr), link: pr.html_url, text: content.slice(0, TEXT_CHARS), from: pr.user?.login ?? '', author: pr.user?.login ?? '', ts })
    }
  }
  if (n.reason === 'mention' || n.reason === 'team_mention') {
    for (const c of await githubMentions($, n, me, after)) {
      out.push({ ...base, kind: 'mention', self: c.user?.login === me, trusted: associated(c), link: c.html_url, text: (c.body ?? '').slice(0, TEXT_CHARS), from: c.user?.login ?? '', author: c.user?.login ?? '', ts: seconds(c.created_at) })
    }
  }
  return out
}

// Notifications no read found for a day are forgotten so the store does not grow
async function forgetNotifications($, now) {
  for (const k of await $.store.keys()) {
    if (!k.startsWith('ghseen:')) continue
    const seen = await $.store.get(k)
    if (!seen || now - seen.at > GHSEEN_MS) await $.store.delete(k)
  }
}

// The author of a comment, issue or pull request belongs to the repository's organization or collaborates on it
function associated(item) {
  return TRUSTED_ASSOCIATIONS.includes(item?.author_association)
}

// When the review was last requested from the user or a team
// The notification time moves with any activity on the pull request and would reopen a review already done
// A timeline without such an event falls back to the notification time
async function reviewRequested($, n, me) {
  const at = /repos\/([\w.-]+)\/([\w.-]+)\/pulls\/(\d+)$/.exec(n.subject.url ?? '')
  if (at) {
    const events = await githubList($, `repos/${at[1]}/${at[2]}/issues/${at[3]}/timeline`)
    const asked = events.filter(e => e.event === 'review_requested' && (e.requested_reviewer?.login === me || e.requested_team)).at(-1)
    if (asked?.created_at) return seconds(asked.created_at)
  }
  return seconds(n.updated_at)
}

// The comments after the cursor that name the user
// A notification only points at the latest comment
// That one may not be the comment that mentions the user
// The issue or pull request itself counts when it was opened after the cursor and names the user
async function githubMentions($, n, me, after) {
  const at = /repos\/([\w.-]+)\/([\w.-]+)\/(?:issues|pulls)\/(\d+)$/.exec(n.subject.url ?? '')
  if (!at) return []
  const [, owner, repo, number] = at
  const since = new Date(Number(after) * 1000).toISOString()
  const names = new RegExp('@' + me.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '\\b', 'i')
  const fresh = c => names.test(c.body ?? '') && Number(seconds(c.created_at)) > Number(after)
  const comments = [...(await githubList($, `repos/${owner}/${repo}/issues/${number}/comments`, since))]
  if (n.subject.type === 'PullRequest') comments.push(...(await githubList($, `repos/${owner}/${repo}/pulls/${number}/comments`, since)))
  const hits = comments.filter(fresh)
  if (hits.length) return hits
  const item = parseJSON(await gh($, ['api', n.subject.url]))
  return item && fresh(item) ? [item] : []
}

// Without since every item is read
async function githubList($, path, since) {
  const args = ['api', '--paginate', '--slurp', path, '-X', 'GET']
  if (since) args.push('-f', 'since=' + since)
  return (parseJSON(await gh($, args)) ?? []).flat()
}

// Comments the user wrote after the message
// 1. A review thread link only counts replies in that thread
// 2. A review the user submitted on the pull request after the message counts too
async function githubAnswered($, m) {
  const me = await githubUser($)
  const at = githubLink(m.link)
  if (!at) return false
  const since = new Date(Number(m.ts) * 1000).toISOString()
  const after = c => c.user?.login === me && Number(seconds(c.created_at)) > Number(m.ts)
  const base = `repos/${at.owner}/${at.repo}`
  if (at.discussion) {
    const replies = await githubList($, `${base}/pulls/${at.number}/comments`, since)
    return replies.some(c => String(c.in_reply_to_id) === at.discussion && after(c))
  }
  if ((await githubList($, `${base}/issues/${at.number}/comments`, since)).some(after)) return true
  if (!at.pull) return false
  const reviewed = r => r.user?.login === me && Number(seconds(r.submitted_at)) > Number(m.ts)
  return (await githubList($, `${base}/pulls/${at.number}/reviews`)).some(reviewed)
}

// A review comment gets a reply in its thread and anything else a comment on the issue or pull request
async function githubSend($, link, reply) {
  const at = githubLink(link)
  if (!at) throw new Error('not a GitHub issue or pull request link ' + link)
  const base = `repos/${at.owner}/${at.repo}`
  const path = at.discussion ? `${base}/pulls/${at.number}/comments/${at.discussion}/replies` : `${base}/issues/${at.number}/comments`
  await gh($, ['api', '-X', 'POST', path, '--input', '-'], JSON.stringify({ body: reply }))
}

async function githubUser($, live) {
  const cached = await $.store.get('githubUser')
  if (cached && !live) return cached
  const login = (await gh($, ['api', 'user', '-q', '.login'])).trim()
  if (!login) throw new Error('could not read the GitHub login')
  await $.store.set('githubUser', login)
  return login
}

// Receiving

// Each source has its own lease and only a session where that source works may hold it
// So a session that cannot reach a source never keeps the others from reading it
// t is the tick the check runs in and is read again when a test calls the check alone
async function check($, t) {
  t ??= parseJSON((await meetproxy($, ['tick', '--cwd', await $.session.cwd()])).out) ?? {}
  lastTick = t
  let connected = 0
  let engine
  const now = await $.clock.now()
  forgetSorted(now)
  for (const source of SOURCES) {
    if (!(await ready($, source, t))) continue
    connected++
    if (!(await holdLease($, source))) continue
    engine ??= (await meetproxy($, ['triage'])).out
    try {
      if (source === 'slack') await slackHeld($, t)
      const got = await receive($, source, t)
      const found = await settle($, source, got.messages, engine, now)
      if (found < 0) continue
      // Every message up to the newest one seen is settled so the cursors move there and the source is remembered
      for (const [key, ts] of Object.entries(got.newest)) await meetproxy($, ['inbox', 'advance', ts, '--key', key])
      for (const m of got.marks) await $.store.set(m.key, m.value)
      await health($, source, { ok: true, found })
    } catch (err) {
      // Let another session take the source and check again next time
      reached[source] = false
      await dropLease($, source)
      await trouble($, source, err)
    }
  }
  problem = connected ? undefined : 'no source works in this session, /meetproxy:delegate checks Slack and GitHub'
}

// Queues or skips each new message and renews the lease after each so a long check keeps the source
// Returns how many were new or -1 when one stopped the source or the lease went to another session
async function settle($, source, messages, engine, now) {
  let found = 0
  for (const m of messages) {
    const key = m.link + ' ' + m.ts
    const fresh = !sorted.has(key)
    if (fresh && !(await enqueue($, m, engine))) return -1
    sorted.set(key, now)
    if (!fresh) continue
    found++
    if (!(await holdLease($, source))) return -1
  }
  return found
}

// While no newer message moves the cursor the overlap keeps finding the last ones
// So an entry lives on from the last read that found it rather than from the message time
function forgetSorted(now) {
  for (const [key, at] of sorted) {
    if (now - at > SORTED_MS) sorted.delete(key)
  }
}

// The binary checks and writes the lease under one lock so two sessions never both hold it
// Holding again renews it
async function holdLease($, source) {
  const me = await $.session.id()
  const r = await meetproxy($, ['lease', 'hold', source, '--session', me, '--ttl', LEASE_MS / 1000 + 's'])
  if (r.ok) {
    holding[source] = (await $.clock.now()) + LEASE_MS
    return true
  }
  if (source === 'slack') verified = false
  delete holding[source]
  return false
}

// Another session may have taken the lease over meanwhile and keeps it
async function dropLease($, source) {
  if (source === 'slack') verified = false
  delete holding[source]
  await meetproxy($, ['lease', 'drop', source, '--session', await $.session.id()])
}

async function leads($) {
  const now = await $.clock.now()
  return SOURCES.some(source => holding[source] > now)
}

// Queues the message or only moves the cursor past it
// 1. A message meetproxy posted only moves the cursor
// 2. So does one no delegation matches or the user already answered or triage ignores
// 3. One whose reply could not be checked asks the user rather than answer twice
// Returns false to stop the source here so the next check starts again from this message
async function enqueue($, m, engine) {
  const skip = async () => (await meetproxy($, ['inbox', 'advance', m.ts, '--key', m.cursor])).ok
  if (m.text.trim().endsWith(MARK)) return skip()
  const trusted = await senderTrusted($, m)
  const msg = JSON.stringify({ link: m.link, text: m.text, from: m.from, author: m.author, trusted })
  const matched = await meetproxy($, ['delegation', 'match', m.kind], msg)
  // A failed match says nothing about the message so it is retried and never skipped
  if (!matched.ok) {
    countFailure($, 'match', m, matched.err)
    return false
  }
  const d = parseJSON(matched.out)
  const replied = d ? await answered($, m) : false
  if (!d || replied) return settled(m, await skip())
  const linked = await linkedMessages($, m.text)
  const request = [m.text, ...linked].join('\n')
  const plan = parseJSON((await meetproxy($, ['map', 'plan'], request)).out) ?? {}
  let v = await classify($, { ...m, trusted }, d, plan, linked, engine)
  if (v.verdict !== 'handle' && v.verdict !== 'ask') return settled(m, await skip())
  if (v.verdict === 'handle' && replied === undefined) v = { ...v, verdict: 'ask', reason: 'could not check whether you already replied' }
  const at = placeOf(d.workspace, plan, v.place, githubLink(m.link)?.repo)
  const words = terms(request)
  const args = ['inbox', 'add', m.link, '--verdict', v.verdict, '--reason', v.reason ?? '', '--from', m.from]
  args.push('--ts', m.ts, '--key', m.cursor, '--delegation', d.delegation, '--self', m.self ? 'yes' : 'no', '--trusted', trusted ? 'yes' : 'no')
  if (at.name) args.push('--name', at.name)
  if (at.root) args.push('--place', at.root)
  if (plan.skills?.length) args.push('--skills', plan.skills.join(','))
  if (plan.files?.length) args.push('--files', plan.files.join(','))
  if (d.target) args.push('--target', d.target)
  if (words.length) args.push('--keywords', words.join(','))
  const added = await meetproxy($, args)
  if (added.ok) return settled(m, true)
  // A message that keeps failing to queue is skipped so it does not cost a triage every check
  if (countFailure($, 'add', m, added.err) < MAX_FAILURES) return false
  return settled(m, await skip())
}

// Whether the source vouches for the sender as one of the user's own team
// The user is always trusted
async function senderTrusted($, m) {
  if (m.self) return true
  if (m.source === 'slack') return slackTrusted($, m.from)
  return Boolean(m.trusted)
}

// Forgets the failures of a message once it is queued or skipped so the counts never pile up
function settled(m, ok) {
  if (ok) {
    failures.delete('match ' + m.link)
    failures.delete('add ' + m.link)
  }
  return ok
}

// The place a request goes to as its absolute root and its display name
// 1. The place the delegation fixes, by root when it is a path and by name otherwise
// 2. The place the work map decided
// 3. The candidate triage picked, else the first candidate
// 4. The repository of a GitHub link as a name only
function placeOf(fixed, plan, pickedName, repo) {
  if (fixed?.startsWith('/')) return { root: fixed, name: fixed.split('/').filter(Boolean).at(-1) ?? '' }
  if (fixed) return { root: plan.name === fixed ? plan.place : '', name: fixed }
  if (plan.name) return { root: plan.place ?? '', name: plan.name }
  const candidates = plan.candidates ?? []
  const c = candidates.find(c => c.name === pickedName) ?? candidates[0]
  if (c) return { root: c.root, name: c.name }
  return { root: '', name: repo ?? '' }
}

// Whether to handle the message alone or ask the user and which map candidate fits it
// 1. A request the user wrote or a delegation triage does not decide already has its verdict
// 2. Triage still runs to pick a place when the map left candidates
// 3. A delegation that asks first, or a sender outside the trust set, asks whatever triage says
// 4. A request found more than an hour late asks the user
async function classify($, m, d, plan, linked, engine) {
  const candidates = plan.candidates ?? []
  const decided = !d.triage || m.self
  let v = { verdict: d.post === 'ask' ? 'ask' : 'handle', reason: 'delegation ' + d.delegation, place: '' }
  if (!decided || (!plan.name && candidates.length > 1)) {
    const places = candidates.map(c => ({ name: c.name, examples: c.examples }))
    const input = { text: m.text, channel: m.channel, from: m.from, workspace: plan.name ?? '', linked, knowledge: await knowledge($, plan.name), places }
    const t = await triage($, engine, input)
    v = decided ? { ...v, place: t.place } : t
  }
  if (v.verdict === 'handle' && d.post === 'ask') {
    return { ...v, verdict: 'ask', reason: m.trusted ? 'delegation ' + d.delegation + ' asks first' : 'sender outside the trust set' }
  }
  if (v.verdict === 'handle' && (await $.clock.now()) - Number(m.ts) * 1000 > STALE_MS) {
    return { ...v, verdict: 'ask', reason: 'came in while no session was open' }
  }
  return v
}

// Returns the tries of the step so far
function countFailure($, step, m, err) {
  const key = step + ' ' + m.link
  const tries = (failures.get(key) ?? 0) + 1
  failures.set(key, tries)
  $.ui.log(`meetproxy: could not ${step} ${m.link} on try ${tries}: ${err}`)
  return tries
}

// Claude runs here because only the session can call its model
// Other engines run in the binary
async function triage($, engine, input) {
  const stdin = JSON.stringify(input)
  if (!engine.startsWith('claude')) {
    return parseVerdict((await meetproxy($, ['triage', 'run'], stdin)).out)
  }
  const prompt = (await meetproxy($, ['triage', 'prompt'], stdin)).out
  let reply = ''
  try {
    const r = await $.model.complete({ model: 'haiku', prompt, maxTokens: TRIAGE_TOKENS, timeoutMs: TRIAGE_MS })
    reply = typeof r === 'string' ? r : r?.isAnswered ? r.text : ''
  } catch (err) {
    reply = message(err)
  }
  return parseVerdict((await meetproxy($, ['triage', 'parse'], reply)).out)
}

function parseVerdict(out) {
  return parseJSON(out) ?? { verdict: 'ask', reason: 'triage failed' }
}

// Approved nodloop notes for the place of this name when the nodloop plugin is there
// Without nodloop there are none so its absence is no trouble
async function knowledge($, name) {
  if (!name) return []
  try {
    const r = await $.mcp.call(NODLOOP, 'knowledge_for', { producer: 'meetproxy-triage', labels: { repo: [name] } })
    const t = text(r).trim()
    return t ? [t.slice(0, KNOWLEDGE_CHARS)] : []
  } catch (err) {
    $.ui.log('meetproxy: no nodloop notes: ' + message(err), { to: 'debug' })
    return []
  }
}

// Handing out

// Every session says where it works so the lease holder knows what nobody covers
async function introduce($, me, place, name) {
  await $.store.set('session:' + me, { place, name, at: await $.clock.now() })
}

// The places of sessions that spoke within the lease time
// 1. Each place counts by its root and by its name for requests stored with a name only
// 2. A root is an absolute path and a name never holds a slash so the two never collide
// 3. Older records are removed
async function livePlaces($) {
  const now = await $.clock.now()
  const live = new Set()
  for (const k of await $.store.keys()) {
    if (!k.startsWith('session:')) continue
    const s = await $.store.get(k)
    if (!s || now - s.at > LEASE_MS) {
      await $.store.delete(k)
      continue
    }
    if (s.place) live.add(s.place)
    if (s.name) live.add(s.name)
  }
  return live
}

// Which waiting request of the tick this session takes
// 1. One the binary marks as of this session's place, at once
// 2. For the lease holder one no open session covers, after two checks so a session of that place gets it first
//    A request is covered by its root, or by its name when it has no root
async function pickUp($, t) {
  const me = await $.session.id()
  await introduce($, me, t.place, t.name)
  const covered = await livePlaces($)
  const leader = await leads($)
  const now = await $.clock.now()
  const rows = t.waiting ?? []
  const held = rows.filter(r => r.status === 'held' && r.here)
  $.ui.status(problem ?? (held.length ? `${held.length} request(s) put off, run /meetproxy:handle ${held[0].id}` : undefined))
  const open = rows.filter(r => r.status !== 'held')
  const orphan = r => leader && !covered.has(r.place || r.name) && now - r.added * 1000 >= 2 * TICK_MS
  const pick = open.find(r => r.here) ?? open.find(orphan)
  if (!pick) return
  const link = (await meetproxy($, ['inbox', 'claim', pick.id, '--session', me])).out
  if (!link) return
  // An ask request starts a turn that asks the user before anything is posted
  // A take with no heartbeat for twenty minutes comes back from the tick as ask
  if (pick.status === 'ask') $.ui.toast('meetproxy: a request needs you ' + link)
  await $.command.run({ command: $.plugin.name + ':handle', args: pick.id + (pick.status === 'ask' ? ' --ask' : ' --auto') })
}

// Sending

// The same check as the posting guard runs before the source sends
async function post($, link, body) {
  if (typeof link !== 'string' || typeof body !== 'string' || !body.trim()) return 'post needs a link and a text'
  const source = owner(link)
  if (!source) return 'meetproxy cannot post to ' + link
  const me = await $.session.id()
  if (!(await meetproxy($, ['can-post', link, '--session', me])).ok) {
    return `denied: ${link} is not the request's origin, its target or on the allow list`
  }
  try {
    await send($, source, link, body)
    return 'posted to ' + link
  } catch (err) {
    return 'failed to post: ' + message(err)
  }
}

// Helpers

// Where a Slack message sits
// 1. channel: the channel id
// 2. ts: the message ts
// 3. thread: the ts of the thread it sits in
function slackLink(link) {
  const m = SLACK_LINK.exec(link ?? '')
  if (!m) return undefined
  const ts = m[2] + '.' + m[3]
  const thread = /[?&]thread_ts=([\d.]+)/.exec(link)?.[1] ?? ts
  return { channel: m[1], ts, thread }
}

// The issue or pull request and the review thread when the link points at one
function githubLink(link) {
  const m = GITHUB_LINK.exec(link ?? '')
  if (!m) return undefined
  return { owner: m[1], repo: m[2], number: m[4], pull: m[3] === 'pull', discussion: /#discussion_r(\d+)/.exec(link)?.[1] }
}

// The time in seconds a search reads from
function overlapped(cursor) {
  return Number(cursor) - OVERLAP_MS / 1000
}

function cancel(timers) {
  for (const t of timers ?? []) {
    if (typeof t === 'function') t()
    else t?.cancel?.()
  }
}

async function meetproxy($, args, stdin) {
  const argv = [$.plugin.root + '/bin/meetproxy', ...args, '--root', $.plugin.root]
  const r = await $.process.run(argv, { stdin, timeoutMs: BINARY_MS })
  return { ok: r.exitCode === 0, code: r.exitCode, out: r.stdout.trim(), err: r.stderr.trim() }
}

// What a failed binary call shows until a tick works again
function failed(r) {
  return `meetproxy: ${firstLine(r.err) || 'exit ' + r.code}, /meetproxy:status`
}

function firstLine(s) {
  return (s ?? '').split('\n')[0].replace(/^meetproxy: /, '')
}

// Logs a failure and records it once per message so status shows it
// quiet failures such as a source that is not set up go to the debug log alone
async function trouble($, source, err, quiet) {
  const msg = message(err)
  $.ui.log('meetproxy: ' + source + ': ' + msg, quiet ? { to: 'debug' } : undefined)
  if (troubled[source] === msg) return
  troubled[source] = msg
  await health($, source, { ok: false, error: msg, found: 0 })
}

async function health($, source, rec) {
  if (rec.ok) delete troubled[source]
  await meetproxy($, ['health', source], JSON.stringify(rec))
}

async function gh($, args, stdin) {
  const r = await $.process.run(['gh', ...args], { stdin, timeoutMs: 60_000 })
  if (r.exitCode !== 0) throw new Error('gh ' + args.slice(0, 2).join(' ') + ': ' + r.stderr.trim())
  return r.stdout
}

function text(result) {
  return (result?.content ?? []).filter(c => c.type === 'text').map(c => c.text).join('\n')
}

function message(err) {
  return err?.message ?? String(err)
}

function parseJSON(out) {
  try {
    return JSON.parse(out)
  } catch {
    return undefined
  }
}

function seconds(iso) {
  return String(Date.parse(iso) / 1000)
}

function byTs(a, b) {
  return Number(a.ts) - Number(b.ts)
}

// The later of two unix second strings, either of which may be missing
function later(a, b) {
  if (a === undefined) return b
  return Number(b) > Number(a) ? b : a
}

// Words of the message for the location map
// Dropped tokens
// 1. Mentions and links
// 2. One letter tokens
function terms(s) {
  const words = s
    .replace(/<[^>]*>/g, ' ')
    .split(/[^\p{L}\p{N}_-]+/u)
    .filter(w => [...w].length >= 2 && !w.startsWith('-'))
  return [...new Set(words)].slice(0, 20)
}

// Pieces the tests reach without a session
export const testing = {
  slackLink, githubLink, searchMessages, channelMessages, terms, nextPage, check, pickUp, post, slackTrusted, reached, failures, sorted, holding,
  reset() {
    lastTick = undefined
    turning = false
    verified = false
    for (const k of Object.keys(holding)) delete holding[k]
    for (const k of Object.keys(troubled)) delete troubled[k]
  },
}

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
// 8. Every post is kept in a ledger and its thread is read for follow-ups for three days
//    A reply there goes back to the same request through triage
// 9. The lease holder reacts on request links when a request is queued and when it is answered

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
const PROTOCOL = 8
// Enough for the one line verdict triage answers with
const TRIAGE_TOKENS = 200
const TRIAGE_MS = 30_000
const UPDATED = 'meetproxy was updated, run /reload-plugins in this session'
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
// How long a busy session leaves a request of its place to an idle session of the same place
const BUSY_WAIT_MS = 10 * 60_000
// Rows one question offers at most since the dialog holds four options
const BATCH = 4
// How long requests the user did not pick from a batch wait before they are asked again
const BATCH_HOLD = '2h'
// How long a source problem is left alone after the user was asked about it
const NAG_MS = 24 * 60 * 60_000
// How long whether a Slack channel is shared outside the workspace is remembered
const SHARED_MS = 24 * 60 * 60_000
// Reactions per check so a backlog never holds the lease up
const MAX_ACKS = 10
// Slack and GitHub reaction names of each acknowledgement
const SLACK_REACTION = { eyes: 'eyes', done: 'white_check_mark' }
const GITHUB_REACTION = { eyes: 'eyes', done: 'hooray' }
const HANDOFF = 'I could not finish this on my own, so I passed it to the person you asked. They will follow up.\n\n' + MARK
const DONE = 'Done'
const REMIND = 'Remind tomorrow'

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
// Sources whose last check failed for a reason the user can fix
const failing = {}
// Whether a question of this mod waits for the user so two never stack
let prompting = false

export function register(on) {
  on('session.start', async ($, e, next) => {
    await $.tool.register({
      name: 'post',
      description:
        'Post a reply in the thread of a Slack or GitHub link, such as where a meetproxy request came from. ' +
        'Only where the open request came from, what it works on and the allow list are accepted.',
      inputSchema: {
        type: 'object',
        properties: {
          link: { type: 'string' },
          text: { type: 'string' },
          kind: { type: 'string', enum: ['answer', 'question'], description: 'question for a question back to the requester' },
        },
        required: ['link', 'text'],
      },
    })
    await $.tool.register({
      name: 'retract',
      description:
        'Correct or remove a reply meetproxy posted, given its link from the post ledger. ' +
        'With text the reply is replaced by it, without text it is deleted. Replies outside the ledger are refused.',
      inputSchema: {
        type: 'object',
        properties: { reply: { type: 'string' }, text: { type: 'string' } },
        required: ['reply'],
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
        await nag($, t)
      } catch (err) {
        $.ui.log('meetproxy: ' + message(err))
      } finally {
        busy = false
      }
    }
    stop = [$.clock.after(FIRST_MS, tick), $.clock.every(TICK_MS, tick)]
    return next(e)
  })

  // Every session says when the user last typed and whether a turn runs so requests go to an idle session first
  // A failure only costs that preference so the prompt always goes on
  on('prompt.submit', async ($, e, next) => {
    await active($, { prompt_at: await $.clock.now() })
    return next(e)
  }).catch(($, e, next) => next(e))

  // A subagent's run raises no turn.start so only the main loop's turns count
  on('turn.start', async ($, e, next) => {
    turning = true
    await active($, { in_turn: true }).catch(() => {})
    return next(e)
  })
  on('turn.complete', async ($, e, next) => {
    if (!e.agentId) {
      turning = false
      await active($, { in_turn: false }).catch(() => {})
    }
    return next(e)
  })

  // A post whose check fails or runs out of time is denied rather than sent
  on('tool.call', { tool: 'mcp__meetproxy__post' }, async ($, e) => {
    return post($, e.link, e.text, e.kind)
      .then(r => ({ result: r }))
      .catch(err => ({ result: 'denied: post check failed ' + message(err) }))
  }).catch(($, e, next) => (next.called ? next(e) : { result: 'denied: post check failed ' + (next.error?.message ?? next.error?.kind ?? '') }))

  // A retract the ledger does not vouch for or that fails part way is refused rather than guessed
  on('tool.call', { tool: 'mcp__meetproxy__retract' }, async ($, e) => {
    return retract($, e.reply, e.text)
      .then(r => ({ result: r }))
      .catch(err => ({ result: 'denied: retract failed ' + message(err) }))
  }).catch(($, e, next) => (next.called ? next(e) : { result: 'denied: retract failed ' + (next.error?.message ?? next.error?.kind ?? '') }))

  // The token goes from the dialog to the binary and only what it acts for comes back
  on('tool.call', { tool: 'mcp__meetproxy__slack_token' }, async $ => ({ result: await askToken($) })).catch(($, e, next) => (next.called ? next(e) : { result: 'no token was stored: ' + (next.error?.message ?? next.error?.kind ?? '') }))
}

// Sources

// Each source is one case in each of these dispatchers
// 1. owner(link): the source a link belongs to
// 2. ready($, source, t): whether its connection works now
// 3. receive($, source, t): messages from a little before its cursors on
//    Each names its source and the kind its delegation is matched by
//    The kind is mention, review-request, dm, own-pr or a channel delegation id
//    Each also names the cursor key its position is kept under
//    Beside them the newest position seen per cursor key and what to remember once every message is settled
// 4. covered($, m): whether the user or a meetproxy post already replied after the message, undefined when that cannot be told
// 5. send($, source, link, reply): a reply in the thread of the link, resolving to the link of the reply
// 6. replies($, source, w): replies others wrote in a watched thread after what was seen
// 7. react($, source, link, ack): a reaction on the message of the link
// 8. unsay($, source, post, text): the reply of a ledger post replaced by text or removed
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
  if (await $.store.get('muted:' + source)) return false
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

// Whether the message already has an answer after it
// Replies of the user by hand and posts of any meetproxy both count
async function covered($, m) {
  try {
    switch (m.source) {
      case 'slack':
        return await slackCovered($, m)
      case 'github':
        return await githubCovered($, m)
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
  return ''
}

async function replies($, source, w) {
  switch (source) {
    case 'slack':
      return slackReplies($, w)
    case 'github':
      return githubReplies($, w)
  }
  return []
}

async function react($, source, link, ack) {
  switch (source) {
    case 'slack':
      return slackReact($, link, ack)
    case 'github':
      return githubReact($, link, ack)
  }
}

async function unsay($, source, p, text) {
  switch (source) {
    case 'slack':
      return slackUnsay($, p, text)
    case 'github':
      return githubUnsay($, p, text)
  }
  throw new Error('meetproxy cannot retract ' + p.reply)
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
  // The connector lists no direct messages so they are read with a token alone
  // The binary leaves out those that mention the user since the mention search finds them
  if (via !== 'token') return { messages, newest, marks: [] }
  const dm = await meetproxy($, ['inbox', 'cursor', '--key', 'dm'])
  if (!dm.ok) return { messages, newest, marks: [] }
  const since = overlapped(dm.out)
  for (const m of fromBinary($, await meetproxy($, ['slack', 'dms', '--after', String(since)]))) {
    newest.dm = later(newest.dm, m.ts)
    messages.push({ ...m, source: 'slack', kind: 'dm', cursor: 'dm', self: false })
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
async function slackCovered($, m) {
  if ((await slackRoute($)) === 'token') {
    const r = await meetproxy($, ['slack', 'covered', m.link, '--ts', m.ts])
    const got = parseJSON(r.out)
    if (!r.ok || typeof got?.covered !== 'boolean') throw new Error('could not read the thread: ' + firstLine(r.err))
    return got.covered
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
  return thread.includes(MARK) || thread.split('\n').some(l => l.startsWith('> ') && l.includes('<' + email + '>:'))
}

// Resolves to the permalink of the reply, or empty when the connector named none
async function slackSend($, link, reply) {
  const at = slackLink(link)
  if (!at) throw new Error('not a Slack message link ' + link)
  if ((await slackRoute($)) === 'token') {
    const r = await meetproxy($, ['slack', 'post', link, '--session', await $.session.id()], reply)
    if (!r.ok) throw new Error(r.out === 'denied' ? 'denied' : firstLine(r.err))
    return r.out
  }
  const r = await $.mcp.call(SLACK, 'slack_send_message', { channel_id: at.channel, thread_ts: at.thread, message: reply })
  if (r?.isError) throw new Error(text(r))
  return new RegExp(SLACK_LINK.source + '[^\\s)>|]*').exec(text(r))?.[0] ?? ''
}

// Replies in a watched thread are read with a token alone
// The connector's thread answer names no ts per reply so what was seen could not move past them
async function slackReplies($, w) {
  if ((await slackRoute($)) !== 'token') return []
  return fromBinary($, await meetproxy($, ['slack', 'replies', w.thread, '--after', w.seen]))
}

async function slackReact($, link, ack) {
  const at = slackLink(link)
  if ((await slackRoute($)) === 'token') {
    const r = await meetproxy($, ['slack', 'react', link, '--react', SLACK_REACTION[ack]])
    if (!r.ok) throw new Error(firstLine(r.err))
    return
  }
  const r = await $.mcp.call(SLACK, 'slack_add_reaction', { channel_id: at.channel, message_ts: at.ts, emoji: SLACK_REACTION[ack] })
  if (r?.isError) throw new Error(text(r))
}

// With a token the reply is edited or deleted
// The connector edits nothing so a correction goes in the thread instead
async function slackUnsay($, p, replacement) {
  if ((await slackRoute($)) === 'token') {
    const r = await meetproxy($, ['slack', replacement ? 'update' : 'delete', p.reply], replacement ? withMark(replacement) : undefined)
    if (!r.ok) throw new Error(firstLine(r.err))
    return replacement ? 'corrected' : 'deleted'
  }
  const at = slackLink(p.reply)
  const note = 'Correction: ' + (replacement || 'please disregard my earlier reply in this thread.')
  const r = await $.mcp.call(SLACK, 'slack_send_message', { channel_id: at.channel, thread_ts: at.thread, message: withMark(note) })
  if (r?.isError) throw new Error(text(r))
  return 'posted a correction under'
}

// Whether people outside the workspace read the channel
// Only a token can tell so without one a channel counts as internal
async function slackShared($, channel) {
  if (!channel || (await slackRoute($)) !== 'token') return false
  const key = 'slackShared:' + channel
  const now = await $.clock.now()
  const cached = await $.store.get(key)
  if (cached && now - cached.at < SHARED_MS) return cached.shared
  const r = await meetproxy($, ['slack', 'shared', channel])
  const got = parseJSON(r.out)
  if (!r.ok || typeof got?.shared !== 'boolean') throw new Error('could not read the channel ' + channel + ': ' + firstLine(r.err))
  await $.store.set(key, { shared: got.shared, at: now })
  return got.shared
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
  if ((n.reason === 'author' || n.reason === 'comment') && n.subject?.type === 'PullRequest') {
    for (const c of await ownPullComments($, n, me, after)) {
      out.push({ ...base, kind: 'own-pr', self: false, trusted: associated(c), link: c.html_url, text: (c.body ?? '').slice(0, TEXT_CHARS), from: c.user.login, author: c.user.login, ts: seconds(c.created_at) })
    }
  }
  return out
}

// Comments others wrote after the cursor on a pull request the user opened
// A comment naming the user comes as a mention instead so it is left out here
async function ownPullComments($, n, me, after) {
  const at = /repos\/([\w.-]+)\/([\w.-]+)\/pulls\/(\d+)$/.exec(n.subject.url ?? '')
  if (!at) return []
  const pr = parseJSON(await gh($, ['api', n.subject.url]))
  if (pr?.user?.login !== me) return []
  const since = new Date(Number(after) * 1000).toISOString()
  const base = `repos/${at[1]}/${at[2]}`
  const comments = [...(await githubList($, `${base}/issues/${at[3]}/comments`, since)), ...(await githubList($, `${base}/pulls/${at[3]}/comments`, since))]
  const names = new RegExp('@' + me.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '\\b', 'i')
  return comments.filter(c => c.user?.login && c.user.login !== me && !bot(c.user) && !names.test(c.body ?? '') && Number(seconds(c.created_at)) > Number(after))
}

function bot(user) {
  return user?.type === 'Bot' || /\[bot\]$/.test(user?.login ?? '')
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

// Comments after the message that the user wrote or any meetproxy posted
// 1. A review thread link only counts replies in that thread
// 2. A review the user submitted on the pull request after the message counts too
async function githubCovered($, m) {
  const me = await githubUser($)
  const at = githubLink(m.link)
  if (!at) return false
  const since = new Date(Number(m.ts) * 1000).toISOString()
  const after = c => (c.user?.login === me || (c.body ?? '').trim().endsWith(MARK)) && Number(seconds(c.created_at)) > Number(m.ts)
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
  return parseJSON(await gh($, ['api', '-X', 'POST', path, '--input', '-'], JSON.stringify({ body: reply })))?.html_url ?? ''
}

// Comments others wrote in the watched thread after what was seen
// A review thread counts only replies in that thread
async function githubReplies($, w) {
  const me = await githubUser($)
  const at = githubLink(w.thread)
  if (!at) return []
  const since = new Date(Number(w.seen) * 1000).toISOString()
  const base = `repos/${at.owner}/${at.repo}`
  const all = at.discussion
    ? (await githubList($, `${base}/pulls/${at.number}/comments`, since)).filter(c => String(c.in_reply_to_id) === at.discussion)
    : await githubList($, `${base}/issues/${at.number}/comments`, since)
  return all
    .filter(c => c.user?.login && c.user.login !== me && !bot(c.user) && !(c.body ?? '').trim().endsWith(MARK) && Number(seconds(c.created_at)) > Number(w.seen))
    .map(c => ({ link: c.html_url, text: (c.body ?? '').slice(0, TEXT_CHARS), from: c.user.login, author: c.user.login, ts: seconds(c.created_at), trusted: associated(c), channel: `${at.owner}/${at.repo}` }))
    .sort(byTs)
}

// The API path of the comment a link points at, or of the issue or pull request itself
function githubItem(link) {
  const at = githubLink(link)
  if (!at) return undefined
  const base = `repos/${at.owner}/${at.repo}`
  const comment = /#issuecomment-(\d+)/.exec(link)?.[1]
  if (at.discussion) return `${base}/pulls/comments/${at.discussion}`
  if (comment) return `${base}/issues/comments/${comment}`
  return `${base}/issues/${at.number}`
}

async function githubReact($, link, ack) {
  await gh($, ['api', '-X', 'POST', githubItem(link) + '/reactions', '-f', 'content=' + GITHUB_REACTION[ack]])
}

// Only a comment can be edited or deleted so a link to the issue itself is refused
async function githubUnsay($, p, replacement) {
  const path = githubItem(p.reply)
  if (!path || !/\/comments\/\d+$/.test(path)) throw new Error('not a comment link ' + p.reply)
  if (replacement) {
    await gh($, ['api', '-X', 'PATCH', path, '--input', '-'], JSON.stringify({ body: withMark(replacement) }))
    return 'corrected'
  }
  await gh($, ['api', '-X', 'DELETE', path])
  return 'deleted'
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
      const more = await settle($, source, await followups($, source, t), engine, now)
      if (more < 0) continue
      await acknowledge($, source, t)
      delete failing[source]
      await health($, source, { ok: true, found: found + more })
    } catch (err) {
      // Let another session take the source and check again next time
      reached[source] = false
      failing[source] = message(err)
      await dropLease($, source)
      await trouble($, source, err)
    }
  }
  problem = connected ? undefined : 'no source works in this session, /meetproxy:delegate checks Slack and GitHub'
}

// Replies others wrote in the threads this source answered, each as a message of the request it follows
// The link stays the request's own so the reply reopens the same request
async function followups($, source, t) {
  const out = []
  for (const w of (t.watch ?? []).filter(w => owner(w.thread) === source)) {
    for (const r of await replies($, source, w)) {
      out.push({ ...r, source, kind: 'followup', cursor: 'followup', watch: w.request, relay: w.relay, delegation: w.delegation, link: w.origin, reply: r.link, self: false })
    }
  }
  return out.sort(byTs)
}

// Reactions the tick says are owed on links of this source
// A failed reaction is tried again on the next check and never costs the source its lease
async function acknowledge($, source, t) {
  for (const a of (t.acks ?? []).filter(a => owner(a.link) === source).slice(0, MAX_ACKS)) {
    try {
      if (a.react === 'handoff') await send($, source, a.link, HANDOFF)
      else await react($, source, a.link, a.react)
      await meetproxy($, ['inbox', 'acked', a.id, '--react', a.react])
    } catch (err) {
      $.ui.log(`meetproxy: could not react ${a.react} on ${a.link}: ${message(err)}`, { to: 'debug' })
    }
  }
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
// 2. So does one no delegation matches or one the user or a meetproxy post already answered
// 3. One triage ignores is recorded as ignored without its text
// 4. One whose reply could not be checked asks the user rather than answer twice
// 5. A follow-up moves what its watch has seen instead of a cursor
// Returns false to stop the source here so the next check starts again from this message
async function enqueue($, m, engine) {
  const skip = async () => passed($, m, await meetproxy($, ['inbox', 'advance', m.ts, '--key', m.cursor]))
  if (m.text.trim().endsWith(MARK)) return skip()
  const trusted = await senderTrusted($, m)
  const msg = JSON.stringify({ link: m.link, text: m.text, from: m.from, author: m.author, trusted })
  const matched = m.watch
    ? await meetproxy($, ['delegation', 'match', m.delegation || 'default', '--followup', 'yes'], msg)
    : await meetproxy($, ['delegation', 'match', m.kind], msg)
  // A failed match says nothing about the message so it is retried and never skipped
  if (!matched.ok) {
    countFailure($, 'match', m, matched.err)
    return false
  }
  const d = parseJSON(matched.out)
  const replied = d ? await covered($, m) : false
  if (!d || replied) return settled(m, await skip())
  const linked = await linkedMessages($, m.text)
  const request = [m.text, ...linked].join('\n')
  const plan = parseJSON((await meetproxy($, ['map', 'plan'], request)).out) ?? {}
  let v = await classify($, { ...m, trusted }, d, plan, linked, engine)
  if (v.verdict !== 'handle' && v.verdict !== 'ask') {
    const ignored = ['inbox', 'ignore', m.link, '--from', m.from, '--delegation', d.delegation, '--reason', v.reason || 'ignored', '--ts', m.ts, '--key', m.cursor]
    return settled(m, await passed($, m, await meetproxy($, ignored)))
  }
  if (v.verdict === 'handle' && replied === undefined) v = { ...v, verdict: 'ask', reason: 'could not check whether you already replied' }
  if (v.verdict === 'handle' && m.source === 'slack' && (await shared($, m.channel))) v = { ...v, verdict: 'ask', reason: 'shared channel with people outside the workspace' }
  if (v.correction && m.relay) await meetproxy($, ['correct', m.relay])
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
  if (d.depth) args.push('--depth', d.depth)
  if (d.digest) args.push('--digest', d.digest)
  if (m.watch) args.push('--followup', 'yes')
  if (v.correction) args.push('--correction', 'yes')
  const added = await meetproxy($, args)
  if (added.ok) return settled(m, await passed($, m, added))
  // A message that keeps failing to queue is skipped so it does not cost a triage every check
  if (countFailure($, 'add', m, added.err) < MAX_FAILURES) return false
  return settled(m, await skip())
}

// A follow-up settled moves its watch past it so it is read once
async function passed($, m, r) {
  if (!r.ok || !m.watch) return r.ok
  return (await meetproxy($, ['watch', 'seen', m.watch, m.ts])).ok
}

// A channel that cannot be read counts as shared so the user is asked rather than answer outsiders
async function shared($, channel) {
  try {
    return await slackShared($, channel)
  } catch (err) {
    await trouble($, 'slack', err)
    return true
  }
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
// 5. A follow-up always goes through triage and one that says the answer was wrong asks the user
async function classify($, m, d, plan, linked, engine) {
  const candidates = plan.candidates ?? []
  const decided = (!d.triage || m.self) && !m.watch
  let v = { verdict: d.post === 'ask' ? 'ask' : 'handle', reason: 'delegation ' + d.delegation, place: '' }
  if (!decided || (!plan.name && candidates.length > 1)) {
    const places = candidates.map(c => ({ name: c.name, examples: c.examples }))
    const input = { text: m.text, channel: m.channel, from: m.from, workspace: plan.name ?? '', linked, places, followup: Boolean(m.watch) }
    const t = await triage($, engine, input)
    v = decided ? { ...v, place: t.place } : t
  }
  if (v.correction && v.verdict !== 'ignore') {
    return { ...v, verdict: 'ask', reason: 'the requester says the earlier answer was wrong' }
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

// Handing out

// Every session says where it works so the lease holder knows what nobody covers
async function introduce($, me, place, name) {
  await $.store.set('session:' + me, { place, name, at: await $.clock.now() })
}

// What the session is doing as the prompt and turn hooks last saw it
async function active($, change) {
  const key = 'activity:' + (await $.session.id())
  await $.store.set(key, { ...(await $.store.get(key)), ...change })
}

// Sessions that spoke within the lease time and the places they cover
// 1. Each place counts by its root and by its name for requests stored with a name only
// 2. A root is an absolute path and a name never holds a slash so the two never collide
// 3. Older records and their activity are removed
async function liveSessions($) {
  const now = await $.clock.now()
  const places = new Set()
  const sessions = []
  for (const k of await $.store.keys()) {
    if (!k.startsWith('session:')) continue
    const id = k.slice('session:'.length)
    const s = await $.store.get(k)
    if (!s || now - s.at > LEASE_MS) {
      await $.store.delete(k)
      await $.store.delete('activity:' + id)
      continue
    }
    if (s.place) places.add(s.place)
    if (s.name) places.add(s.name)
    sessions.push({ id, place: s.place, name: s.name, busy: Boolean((await $.store.get('activity:' + id))?.in_turn) })
  }
  return { places, sessions }
}

// Which waiting requests of the tick this session takes
// 1. Those the binary marks as of this session's place
// 2. For the lease holder those no open session covers, after two checks so a session of that place gets them first
//    A request is covered by its root, or by its name when it has no root
// 3. A session in a turn leaves a request to an idle session of the same place and takes it itself only after ten minutes with none
// 4. Held requests whose time came are asked about again like ask
// 5. Two or more that need the user are offered in one question, the rest one at a time
// 6. Higher delegation priority first, then the oldest
async function pickUp($, t) {
  const me = await $.session.id()
  await introduce($, me, t.place, t.name)
  const { places, sessions } = await liveSessions($)
  const leader = await leads($)
  const now = await $.clock.now()
  const rows = t.waiting ?? []
  const held = rows.filter(r => r.status === 'held' && !r.due && r.here)
  $.ui.status(problem ?? (held.length ? `${held.length} request(s) put off, run /meetproxy:handle ${held[0].id}` : undefined))
  const open = rows.filter(r => r.status === 'new' || r.status === 'ask' || r.due)
  const orphan = r => leader && !places.has(r.place || r.name) && now - r.added * 1000 >= 2 * TICK_MS
  const idlePeer = r => sessions.some(s => s.id !== me && !s.busy && (r.place ? s.place === r.place : s.name === r.name))
  const waits = r => turning && (idlePeer(r) || now - r.added * 1000 < BUSY_WAIT_MS)
  const mine = open.filter(r => (r.here || orphan(r)) && !waits(r)).sort(byTurn)
  const asks = mine.filter(r => r.status !== 'new')
  if (asks.length >= 2) {
    if (prompting) return
    // Not awaited since the question waits for the user and the tick goes on meanwhile
    prompting = true
    batch($, me, asks).catch(err => $.ui.log('meetproxy: ' + message(err))).finally(() => { prompting = false })
    return
  }
  const pick = mine[0]
  if (!pick) return
  const link = (await meetproxy($, ['inbox', 'claim', pick.id, '--session', me])).out
  if (!link) return
  const asking = pick.status !== 'new'
  // A request for the user starts a turn that asks them before anything is posted
  // A take with no heartbeat for twenty minutes comes back from the tick as ask
  if (asking) $.ui.toast('meetproxy: a request needs you ' + link)
  // Not awaited since the command lasts the whole turn and the tick must go on renewing the take meanwhile
  $.command.run({ command: $.plugin.name + ':handle', args: pick.id + (asking ? ' --ask' : ' --auto') }).catch(err => $.ui.log('meetproxy: ' + message(err)))
}

// The rows of the place first, then priority, then age
function byTurn(a, b) {
  return Number(b.here) - Number(a.here) || (b.priority ?? 0) - (a.priority ?? 0) || a.added - b.added
}

// One question for several requests that need the user
// 1. Labels name only the id, the place and the delegation since request text is untrusted
// 2. The chosen ones are handled in order and the others offered wait two hours
// 3. A dismissed question holds all of them so it is not asked again every minute
async function batch($, me, rows) {
  const offered = rows.slice(0, BATCH)
  const label = r => [r.id, r.name || r.place || '-', r.delegation || 'default'].join(' · ').replace(/,/g, ' ')
  let answer = ''
  try {
    answer = await $.ui.ask(`${rows.length} requests need you. Which should meetproxy bring up now?`, {
      header: 'meetproxy',
      options: offered.map(label),
      multiSelect: true,
    })
  } catch (err) {
    $.ui.log('meetproxy: the request question was dismissed: ' + message(err), { to: 'debug' })
  }
  const picked = new Set(String(answer).split(',').map(a => a.trim()))
  const chosen = offered.filter(r => picked.has(label(r)))
  // A hold that failed is logged since the request would be offered again on the next tick
  for (const r of offered.filter(r => !chosen.includes(r))) {
    const held = await meetproxy($, ['inbox', 'hold', r.id, '--until', BATCH_HOLD, '--session', me])
    if (!held.ok) $.ui.log(`meetproxy: could not put off ${r.id}: ${firstLine(held.err) || 'hold exited ' + held.code}`)
  }
  if (!chosen.length) return
  if (!(await meetproxy($, ['inbox', 'claim', chosen[0].id, '--session', me])).out) return
  for (const r of chosen) await $.command.run({ command: $.plugin.name + ':handle', args: r.id + ' --ask' })
}

// Source problems

// Problems the user can fix on sources a delegation reads, each with the exact fix
// 1. A source whose last check failed
// 2. A source a delegation of the user needs that this session cannot reach
async function problems($, t) {
  const out = []
  const mine = (parseJSON((await meetproxy($, ['delegation'])).out) ?? []).filter(d => !d.id.startsWith('default'))
  const slackNeeded = mine.some(d => d.when === 'channel' || d.when === 'dm')
  const githubNeeded = mine.some(d => d.when === 'review-request' || d.when === 'own-pr' || d.do === 'review')
  if (failing.slack || (slackNeeded && !(await slackRoute($, t)))) {
    const fix = t?.slack?.token
      ? 'run /meetproxy:slack setup to check the token'
      : 'run `/plugin install slack@claude-plugins-official` and `/reload-plugins`, or set up a token with /meetproxy:slack setup'
    out.push({ source: 'slack', why: failing.slack ?? 'no Slack connection', fix })
  }
  if (failing.github || (githubNeeded && !reached.github)) {
    out.push({ source: 'github', why: failing.github ?? 'gh is not logged in', fix: 'run `! gh auth login` in this session' })
  }
  return out
}

// An idle session asks about a source problem at most once a day per source
// Not awaited since the question waits for the user and the tick goes on meanwhile
async function nag($, t) {
  if (turning || prompting) return
  const now = await $.clock.now()
  for (const p of await problems($, t)) {
    if (await $.store.get('muted:' + p.source)) continue
    const last = await $.store.get('nag:' + p.source)
    if (last && now - last < NAG_MS) continue
    await $.store.set('nag:' + p.source, now)
    prompting = true
    asked($, p).catch(err => $.ui.log('meetproxy: ' + message(err))).finally(() => { prompting = false })
    return
  }
}

async function asked($, p) {
  const stop = 'Stop reading ' + p.source
  let answer
  try {
    answer = await $.ui.ask(`meetproxy cannot read ${p.source}: ${p.why}. To fix it, ${p.fix}. What now?`, { header: p.source, options: [DONE, REMIND, stop] })
  } catch {
    return
  }
  if (answer === stop) {
    await $.store.set('muted:' + p.source, true)
    return
  }
  if (answer === DONE) {
    // The next check tries the source again from scratch
    delete failing[p.source]
    delete troubled[p.source]
    delete reached[p.source]
    lastTick = undefined
  }
}

// Sending

// The same check as the posting guard runs before the source sends
// A reply that went out is kept in the ledger and its thread is watched for follow-ups
async function post($, link, body, kind) {
  if (typeof link !== 'string' || typeof body !== 'string' || !body.trim()) return 'post needs a link and a text'
  const source = owner(link)
  if (!source) return 'meetproxy cannot post to ' + link
  const me = await $.session.id()
  if (!(await meetproxy($, ['can-post', link, '--session', me])).ok) {
    return `denied: ${link} is not the request's origin, its target or on the allow list`
  }
  let reply
  try {
    reply = await send($, source, link, body)
  } catch (err) {
    return 'failed to post: ' + message(err)
  }
  if (!reply) {
    $.ui.log('meetproxy: the post to ' + link + ' named no link of its own so it is not in the ledger', { to: 'debug' })
    return 'posted to ' + link
  }
  const rec = { reply, body, kind: kind === 'question' ? 'question' : 'answer' }
  const r = await meetproxy($, ['posts', 'add', '--session', me], JSON.stringify(rec))
  if (!r.ok) $.ui.log('meetproxy: could not record the post: ' + firstLine(r.err))
  return 'posted to ' + link
}

// Replaces or removes a reply only when the ledger holds it
async function retract($, reply, replacement) {
  if (typeof reply !== 'string' || !reply.trim()) return 'retract needs a reply link'
  const found = await meetproxy($, ['posts', 'find', reply.trim()])
  const p = parseJSON(found.out)
  if (!found.ok || !p?.reply) return `denied: ${reply} is not a reply meetproxy posted`
  if (p.retracted_at) return p.reply + ' was already retracted'
  const source = owner(p.reply)
  if (!source) return 'meetproxy cannot retract ' + p.reply
  let done
  try {
    done = await unsay($, source, p, typeof replacement === 'string' ? replacement.trim() : '')
  } catch (err) {
    return 'failed to retract: ' + message(err)
  }
  await meetproxy($, ['posts', 'retract', p.reply])
  return `${done} ${p.reply}`
}

function withMark(s) {
  return s.trim().endsWith(MARK) ? s : s.trim() + '\n\n' + MARK
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
  slackLink, githubLink, searchMessages, channelMessages, terms, nextPage, check, pickUp, post, retract, nag, slackTrusted, reached, failures, sorted, holding,
  busy(v) {
    turning = v
  },
  reset() {
    lastTick = undefined
    turning = false
    verified = false
    prompting = false
    for (const k of Object.keys(failing)) delete failing[k]
    for (const k of Object.keys(holding)) delete holding[k]
    for (const k of Object.keys(troubled)) delete troubled[k]
  },
}

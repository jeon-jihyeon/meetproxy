// Slack links and the answers of the Slack connector
// Only plain functions live here since the engine follows $ into functions of the entry file alone

import { byTs, parseJSON, text } from './core.js'

// Found anywhere in a text such as a message that links to another
export const SLACK_LINK = /https:\/\/[\w-]+\.slack\.com\/archives\/([A-Z0-9]+)\/p(\d{10})(\d{6})/
// A link is parsed from its start only so a link quoted inside another link is never taken for it
const SLACK_AT = new RegExp('^' + SLACK_LINK.source)
// What a Slack connector answer says when nothing matched
const NO_RESULTS = /\bno (?:results|messages|matches)\b|\bnothing found\b|^\s*Channel: [^\n]*\s*$/i

// Where a Slack message sits
// 1. channel: the channel id
// 2. ts: the message ts
// 3. thread: the ts of the thread it sits in
export function slackLink(link) {
  const m = SLACK_AT.exec(link ?? '')
  if (!m) return undefined
  const ts = m[2] + '.' + m[3]
  const thread = /[?&]thread_ts=([\d.]+)/.exec(link)?.[1] ?? ts
  return { channel: m[1], ts, thread }
}

// A connector answer with text but no message in it is a format this parser no longer reads
export function recognized(result, field, messages) {
  const md = String(parseJSON(text(result))?.[field] ?? text(result))
  if (messages.length || !md.trim() || NO_RESULTS.test(md)) return messages
  throw new Error('unrecognized Slack answer format')
}

// The cursor of the next page in a Slack tool answer
export function nextPage(result) {
  const parsed = parseJSON(text(result))
  const info = parsed?.pagination_info ?? ''
  if (typeof info !== 'string') throw new Error('unrecognized Slack pagination format')
  const cursor = /cursor:?\s+`([^`]+)`/i.exec(info)?.[1]
  if (!cursor && /next page|cursor/i.test(info)) throw new Error('unrecognized Slack pagination format')
  if (!cursor && parsed?.has_more) throw new Error('Slack has more messages without a continuation cursor')
  return cursor
}

// The search tool answers with JSON whose results field is markdown
// Each message is one block
export function searchMessages(result) {
  const md = parseJSON(text(result))?.results ?? text(result)
  return blocks(md, /^### Result [^\n]*(?:\n|$)/gm,
    /^Channel: [^\n]*\nFrom: [^\n]*\nMessage_ts: [\d.]+\nPermalink: \[link\]\(\S+\)\nText: \n/)
    .map(block => ({
      author: /^From: ([^<(]*)/m.exec(block)?.[1]?.trim() ?? '',
      channel: /^Channel: .*\(ID: (\w+)\)/m.exec(block)?.[1] ?? '',
      from: /^From: .*\(ID: (\w+)\)/m.exec(block)?.[1] ?? '',
      ts: /^Message_ts: ([\d.]+)/m.exec(block)?.[1] ?? '',
      link: /^Permalink: \[link\]\((\S+)\)/m.exec(block)?.[1] ?? '',
      text: body(block),
    }))
    .sort(byTs)
}

// The channel and detailed thread readers answer with one block per message
// Their blocks carry no permalink so one is built from the host
export function channelMessages(result, channel, host) {
  const md = parseJSON(text(result))?.messages ?? text(result)
  return blocks(md, /^=== Message from /gm, /^[^\n]* ===\s*\nMessage TS: [\d.]+(?:\n|$)/)
    .map(block => {
      const ts = /^Message TS: ([\d.]+)/m.exec(block)?.[1] ?? ''
      return {
        author: /^([^<(]*)/.exec(block)?.[1]?.trim() ?? '',
        from: /\((\w+)\) at /.exec(block)?.[1] ?? '',
        channel,
        ts,
        link: ts ? `https://${host}/archives/${channel}/p${ts.replace('.', '')}` : '',
        text: messageText(block),
      }
    })
    .sort(byTs)
}

// The detailed thread reader answers with the parent block and one block per reply
// Its blocks carry no permalink so one is built from the host
export function threadMessages(result, channel, host) {
  const md = parseJSON(text(result))?.messages ?? text(result)
  return blocks(md, /^(?:=== THREAD PARENT MESSAGE ===|--- Reply \d+ of \d+ ---)\n/gm,
    /^From: [^\n]*\n(?:Time: [^\n]*\n)?Message TS: [\d.]+(?:\n|$)/)
    .map(block => {
      const ts = /^Message TS: ([\d.]+)/m.exec(block)?.[1] ?? ''
      return {
        author: /^From: ([^<(]*)/m.exec(block)?.[1]?.trim() ?? '',
        from: /^From: .*\((\w+)\)\s*$/m.exec(block)?.[1] ?? '',
        channel,
        ts,
        link: ts ? `https://${host}/archives/${channel}/p${ts.replace('.', '')}` : '',
        text: messageText(block),
      }
    })
    .sort(byTs)
}

export function threadSnapshot(result, channel, host) {
  const messages = threadMessages(result, channel, host)
  if (!messages.length) throw new Error('thread snapshot is unreadable')
  return messages.map(m => ({ ...m, reaction_lines: (m.text.match(/^Reactions: .*$/gm) ?? [])
    .flatMap(line => line.slice('Reactions: '.length).split(/[,;]\s*/)) }))
}

// Rendered delimiters have meaning only with their immediate metadata header
function blocks(md, delimiter, header) {
  const boundaries = []
  for (const match of md.matchAll(delimiter)) {
    const start = match.index + match[0].length
    const tail = md.slice(start)
    if (header.test(tail)) boundaries.push({ at: match.index, start })
    else if (/^(?:[^\n]*\n)?(?:From:|Time:|Message TS:|Channel:|Message_ts:|Permalink:)/.test(tail)) {
      throw new Error('incomplete Slack message header')
    }
  }
  if (boundaries.length && !/^(?:Channel: [^\n]*|=== THREAD REPLIES \(\d+ total\) ===)?\s*$/.test(md.slice(0, boundaries[0].at))) {
    throw new Error('unrecognized Slack message prefix')
  }
  return boundaries.map((boundary, i) => md.slice(boundary.start, boundaries[i + 1]?.at ?? md.length))
}

function messageText(block) {
  const ts = /^Message TS: .*$/m.exec(block)
  if (!ts) return ''
  return block
    .slice(ts.index + ts[0].length)
    .trim()
}

function body(block) {
  const at = block.indexOf('Text: \n')
  return at < 0 ? '' : block.slice(at + 'Text: \n'.length).trim()
}

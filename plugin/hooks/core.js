// Plain helpers every part of the watcher shares
// The engine follows $ into functions of the entry file alone so nothing here takes it

export const MARK = '_Written by Claude on behalf of the user_'
// How far before the cursor searches read again
// Slack search and GitHub notifications can show a message minutes after its timestamp
const OVERLAP_MS = 5 * 60_000

// The time in seconds a search reads from
export function overlapped(cursor) {
  return Number(cursor) - OVERLAP_MS / 1000
}

export function withMark(s) {
  return s.trim().endsWith(MARK) ? s : s.trim() + '\n\n' + MARK
}

export function firstLine(s) {
  return (s ?? '').split('\n')[0].replace(/^meetproxy: /, '')
}

// The text parts of a tool answer
export function text(result) {
  return (result?.content ?? []).filter(c => c.type === 'text').map(c => c.text).join('\n')
}

export function message(err) {
  return err?.message ?? String(err)
}

export function parseJSON(out) {
  try {
    return JSON.parse(out)
  } catch {
    return undefined
  }
}

// Unix seconds of an RFC 3339 time with the microseconds of a Slack ts kept
// Date.parse keeps only milliseconds and a cut ts would count the message itself as a later reply
export function unix(iso) {
  const m = /^(.*?)\.(\d+)Z$/.exec(iso ?? '')
  if (!m) return String(Date.parse(iso) / 1000)
  return Date.parse(m[1] + 'Z') / 1000 + '.' + m[2].slice(0, 6).padEnd(6, '0')
}

export function byTs(a, b) {
  return Number(a.ts) - Number(b.ts)
}

// The later of two unix second strings, either of which may be missing
export function later(a, b) {
  if (a === undefined) return b
  return Number(b) > Number(a) ? b : a
}

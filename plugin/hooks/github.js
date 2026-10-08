// GitHub links
// Every read and write of GitHub runs in the binary through the gh the user is logged in to

export const GITHUB_LINK = /^https:\/\/github\.com\/([\w.-]+)\/([\w.-]+)\/(pull|issues)\/(\d+)/

// The issue or pull request and the comment or review thread when the link points at one
export function githubLink(link) {
  const m = GITHUB_LINK.exec(link ?? '')
  if (!m) return undefined
  return {
    owner: m[1],
    repo: m[2],
    number: m[4],
    pull: m[3] === 'pull',
    discussion: /#discussion_r(\d+)/.exec(link)?.[1],
    comment: /#issuecomment-(\d+)/.exec(link)?.[1],
  }
}

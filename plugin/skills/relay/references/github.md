# GitHub

- Read
  - PR: `gh pr view <link> --json title,body,author,comments,reviews`
  - Review comments: `gh api repos/<owner>/<repo>/pulls/<number>/comments`
  - A `#discussion_r<id>` link points at the comment that is the request
- Requester: the comment author
- Post
  - Reply to a review comment: `gh api repos/<owner>/<repo>/pulls/<number>/comments/<id>/replies -f body=<text>`
  - Comment on the PR: `gh pr comment <link> --repo <owner>/<repo> --body <text>`
  - Check the destination with the PR link
- Never resolve threads. That is a judgment call left to people.

# GitHub

- Read
  - Pull request: `gh pr view <link> --json title,body,author,comments,reviews`
  - Review comments: `gh api repos/<owner>/<repo>/pulls/<number>/comments`
  - Issue: `gh issue view <link> --json title,body,author,comments`
  - Comments of an issue or a pull request: `gh api repos/<owner>/<repo>/issues/<number>/comments`
  - A `#discussion_r<id>` link points at the review comment that is the request
  - An `#issuecomment-<id>` link points at the comment that is the request
- Requester: the comment author, or the author of the issue or pull request when the link points at neither comment
- Post
  - Reply to a review comment: `gh api repos/<owner>/<repo>/pulls/<number>/comments/<id>/replies -f body=<text>`
  - Comment on the pull request: `gh pr comment <link> --repo <owner>/<repo> --body <text>`
  - Comment on the issue: `gh issue comment <link> --repo <owner>/<repo> --body <text>`
- Never resolve threads. That is a judgment call left to people.

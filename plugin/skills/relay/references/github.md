# GitHub

- Read
  - Pull request: `gh pr view <link> --json title,body,author,comments,reviews`
  - Review comments: `gh api repos/<owner>/<repo>/pulls/<number>/comments`
  - Issue: `gh issue view <link> --json title,body,author,comments`
  - Comments of an issue or a pull request: `gh api repos/<owner>/<repo>/issues/<number>/comments`
  - A `#discussion_r<id>` link points at the review comment that is the request
  - An `#issuecomment-<id>` link points at the comment that is the request
- Requester: the comment author, or the author of the issue or pull request when the link points at neither comment
- Post with the meetproxy `post` tool and the request link
  - A `#discussion_r<id>` link gets a reply in that review thread, any other link a comment on the pull request or issue
- Never resolve threads. That is a judgment call left to people.

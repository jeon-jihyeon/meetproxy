## Why

<!-- The problem this solves, with the issue it closes if there is one -->

## What changed

<!-- The behavior a user or the watcher sees change, not every file -->

## Checks

- [ ] `gofmt -l .` prints nothing, and `go vet ./...` and `golangci-lint run ./...` pass
- [ ] `go test -race ./...` passes with Node 22 or later installed, so the watcher tests ran
- [ ] `claude plugin validate plugin` and `claude plugin validate .` pass
- [ ] A change to a command the watcher calls raises `protocol` and `PROTOCOL` together
- [ ] A new way to post is covered by the posting guard and its tests
- [ ] CHANGELOG.md has an entry under the next version

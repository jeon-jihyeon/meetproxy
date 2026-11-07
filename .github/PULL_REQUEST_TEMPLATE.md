## Why

<!-- The problem this solves, with the issue it closes if there is one -->

## What changed

<!-- The resulting public API or CLI behavior -->

## Checks

- [ ] `gofmt -l .` prints nothing, and `go vet ./...` and `golangci-lint run ./...` pass
- [ ] `go test -race -count=1 ./...` passes
- [ ] `claude plugin validate plugin` and `claude plugin validate .` pass
- [ ] Public contracts and support documents match the changed behavior
- [ ] Effectful paths preserve durable intent and unresolved receipt recovery
- [ ] Neutral fixtures and documentation pass the public disclosure scan
- [ ] CHANGELOG.md has an entry for the change

# Contributing

Use Go 1.26.10 or newer. Keep application inputs and results public, domain rules private under `internal`, and command handlers limited to parsing and rendering.

Run these checks from the repository:

```sh
go test -race -count=1 ./...
go vet ./...
golangci-lint run ./...
claude plugin validate plugin
claude plugin validate .
python3 -m unittest discover -s .github/scripts -p 'test_*.py'
```

The default desktop adapter needs a trusted Cua daemon for real app tests. Unit fixtures don't establish desktop success. Use an owned test window, never a login window or another person's content.

The consuming application owns approval, target selection and completion checks. Preserve unknown operation outcomes and never use a timeout as permission to repeat an effect.

Publish only neutral examples and synthetic test data. Keep private notes, credentials, run output and local paths out of the repository.

Module tags support versioned Go imports. A tag push starts the draft release workflow. The read-only build job checks the clean annotated source, runs GoReleaser without publishing and verifies the archives and binaries. It extracts only the matching tag's CHANGELOG section for release notes and stops if that section is missing, duplicated or empty.

The build job passes the four checked archives, checksum file, release notes and byte-checking Python script from the same source through an artifact to the upload job. Only that job has repository write permission. It attaches only the four archives and checksum file to a draft Release, then downloads those files with `gh` and compares their bytes and SHA256 hashes against the checked build output. Source and binary checks stay in the read-only build job.

The main push and pull request workflow runs the repository checks. The release workflow doesn't publish a draft automatically. A maintainer reviews the verification results and publishes it manually. A failed build, upload or verification isn't a successful public release. Existing releases aren't overwritten or deleted automatically.

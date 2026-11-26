# Contributing

Thanks for helping with gpu-descheduler. This guide covers how to get a change merged.

## Before you start

Follow the [Code of Conduct](https://github.com/Project-HAMi/community/blob/main/CODE-OF-CONDUCT.md). For anything beyond a small fix, open an issue first so the approach can be agreed before you write code.

If you used any AI assistance, say so in the pull request and say how much. Write your own commit messages.

## Build and test

You need the Go version from `go.mod`.

```bash
go build ./...
go vet ./...
go test ./... -race -count=1
golangci-lint run
go mod tidy && git diff --exit-code go.mod go.sum
helm lint chart
helm unittest chart   # plugin: https://github.com/helm-unittest/helm-unittest
```

To fuzz a parser, for example the HAMi metrics parser:

```bash
go test ./pkg/schedulerclient -run '^$' -fuzz FuzzParseMetrics -fuzztime 30s
```

To build the image, run `./build-image.sh`. See the [README](README.md) for deployment.

## Submitting a change

1. Fork the repository and branch from `main`.
2. Make small commits and sign each one off with `git commit -s`. The DCO check fails without a `Signed-off-by` line.
3. Add a test that fails without your change.
4. Open a pull request against `main`. Say what changed and why in a sentence, add a `/kind` line such as `/kind bug`, and link the issue with `Fixes #<number>`.

CI runs the checks from "Build and test", plus govulncheck, CodeQL, a short fuzz run, an image build with a vulnerability scan, and DCO. All must pass before merge.

If your change touches GPU allocation, test it on a real GPU. Say which GPU and driver you used.

## Review

A reviewer from [OWNERS](OWNERS) leaves `/lgtm`. An approver leaves `/approve`. The bot then merges.

`main` needs one approval, green checks and all review threads resolved. Answer review comments with code where you can.

## Reporting security problems

Do not open a public issue. See [SECURITY.md](SECURITY.md).

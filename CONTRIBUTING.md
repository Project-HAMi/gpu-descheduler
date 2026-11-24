# Contributing

Thanks for helping with gpu-descheduler. This guide covers how to get a change merged.

## Before you start

Follow the [Code of Conduct](https://github.com/Project-HAMi/community/blob/main/CODE-OF-CONDUCT.md). For anything beyond a small fix, open an issue first so the approach can be agreed before you write code.

If you used any AI assistance, say so in the pull request and say how much. Write your own commit messages.

## Build and test

You need the Go version from `go.mod`.

```bash
go build ./...
go vet ./pkg/...
go test ./pkg/... -race -count=1
golangci-lint run
```

To build the image, run `./build-image.sh`. See the [README](README.md) for deployment.

## Submitting a change

1. Fork the repository and branch from `main`.
2. Make small commits and sign each one off with `git commit -s`. The DCO check fails without a `Signed-off-by` line.
3. Add a test that fails without your change.
4. Open a pull request against `main`. Say what changed and why in a sentence, add a `/kind` line such as `/kind bug`, and link the issue with `Fixes #<number>`.

CI runs build, tests, lint and an image build on every pull request. A change that touches GPU allocation should also be tried on real hardware, and the pull request should say which device and driver were used.

## Review

A reviewer leaves `/lgtm` and an approver leaves `/approve`. Both come from the people listed in [OWNERS](OWNERS). Answer review comments with code changes where you can.

## Reporting security problems

Do not open a public issue. See [SECURITY.md](SECURITY.md).

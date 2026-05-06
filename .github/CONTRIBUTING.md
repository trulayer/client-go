# Contributing to client-go

## Activating local git hooks

This repo ships pre-push checks in `.githooks/`. Activate them once after cloning:

```bash
git config core.hooksPath .githooks
```

The pre-push hook runs `go vet`, `go test -race`, `golangci-lint`, and `go build` before every push. Fix any failures before pushing.

## Requirements

- Go 1.21+
- [golangci-lint](https://golangci-lint.run/usage/install/) installed and on `$PATH`

# CLAUDE.md — Go SDK (client-go)

## Project Purpose

The `github.com/trulayer/client-go` Go module. Provides trace capture, span instrumentation, and auto-instrumentation hooks for OpenAI, Anthropic, and other Go AI libraries. Designed for minimal latency overhead, zero external dependencies, and idiomatic Go integration.

## Tech Stack

- Go 1.22+
- Zero external dependencies — stdlib only (`net/http`, `encoding/json`, `context`, `sync`)
- `go test ./...` — testing (standard `testing` package + `testify` for assertions only)
- `golangci-lint` — lint and static analysis
- `go vet ./...` — built-in vet checks

## Merge Conflict Policy

**Merge conflicts are the engineer's responsibility.** Before opening a PR (and again before merging), rebase onto the latest `main` and resolve all conflicts:

```bash
git fetch origin && git rebase origin/main
```

Do not open a PR with a conflicting branch. If a conflict arises after the PR is open because `main` moved, the PR author owns the rebase — not the reviewer or TPM.

## Definition of Done

A task is **not done** until all of the following are true — in order:

1. **Tests pass** — `go test -race ./...` green, `go vet ./...` zero errors, `golangci-lint run` zero warnings.
2. **Docs updated** — any PR that changes public SDK exports must include the corresponding update to the public docs in the same PR. "I'll update docs in a follow-up" is not acceptable.
3. **Committed on a feature branch** — all changed files committed on a branch named `feat/...` or `fix/...`. **Never commit directly to `main`.**
4. **PR opened** — `gh pr create` targeting `main` with a summary of what changed and why.
5. **PR merged** — `gh pr merge --squash`. Work on the next task cannot begin until this PR is merged.
6. **Working tree clean** — after merge, `git status` must show nothing to commit.
7. **Branch deleted** — delete the remote feature branch immediately after the PR is squash-merged: `git push origin --delete <branch-name>`.

**Direct pushes to `main` are forbidden.** Every change must go through a pull request.

## CI is gating

Every pull request must pass CI before it can be merged. If CI fails, the engineer who opened the PR owns the fix. Don't bypass with `--admin` or `--no-verify`. If a check is flaky, fix it or remove it — don't skip it.

## Shell Conventions

**Never use `cd <path> && git <command>`** — use `git -C <path> <command>` instead.

## Key Commands

```bash
go build ./...              # Compile all packages
go test -race ./...         # Run all tests with race detector
go test -race -cover ./...  # Tests with coverage (target: >90%)
go vet ./...                # Static analysis
golangci-lint run           # Lint
go mod tidy                 # Clean up go.mod / go.sum
```

## Project Layout

```text
trulayer/
  client.go         → TruLayerClient (init, flush, shutdown)
  trace.go          → Trace and Span — start/end, context propagation
  batch.go          → async batch sender (channel + flush goroutine)
  model.go          → TraceData, SpanData, SpanType, etc.
  instruments/
    openai.go       → OpenAI Go client auto-instrumentation
    anthropic.go    → Anthropic Go SDK auto-instrumentation
  options.go        → ClientOption functional options
  errors.go         → SDK error types (never propagated to caller)
go.mod
go.sum
examples/           → runnable snippets (kept minimal; full examples in demo-go)
```

## Coding Conventions

- Idiomatic Go: context on every I/O call, errors returned (not panicked), table tests
- Zero external dependencies in the main module path — `testify` is test-only (`go.mod` `require ... // indirect` is fine for test deps)
- `context.Context` propagated from public API through all I/O paths
- All public types have godoc comments (`// TypeName does X`)
- Trace and span IDs are UUIDv7: generate via `crypto/rand` + UUIDv7 bit layout (see `model.go`)
- Functional options pattern (`WithAPIKey`, `WithBaseURL`, `WithBatchSize`, etc.) for `NewClient`
- Never expose `sync.Mutex` or internal channels in the public API

## Batch Sender Behavior

- Events are buffered in a channel and flushed every `flushInterval` (default: 2s) or when `batchSize` is reached (default: 50)
- On `Shutdown(ctx)`, drain the channel and flush with the caller's context deadline
- HTTP failures retry up to 3 times with exponential backoff
- After max retries, events are dropped with a `log.Printf` warning — never block the caller's goroutine

## ID Generation

All `trace_id` and `span_id` values are **UUIDv7**, generated in `model.go` using `crypto/rand` for the random bits and `time.Now().UnixMilli()` for the timestamp prefix. Do not import an external UUID library.

## Testing

- Unit tests mock `http.RoundTripper` via a custom `RoundTripFunc` — no real network calls
- Table tests using `t.Run` sub-tests for all branching logic
- Integration tests in `trulayer_test` package (external test package) against a local mock HTTP server
- Race detector (`-race`) must pass on every PR
- Coverage target: **90%**

## Publishing

- Module path: `github.com/trulayer/client-go`
- Published to pkg.go.dev automatically via Go module proxy when a `v*.*.*` tag is pushed to `main`
- Version follows semver — bump by tagging (no separate version file needed)
- No `production` branch — tags drive releases

## Public Repository Policy

This repository ships to TruLayer customers. Do not introduce references to internal code, internal repositories (e.g. the TruLayer API service or dashboard), internal planning documents, internal Linear issue content, or internal architectural details. Refer to the platform as "TruLayer" or "the TruLayer API" — not as specific internal components. If in doubt, leave it out.

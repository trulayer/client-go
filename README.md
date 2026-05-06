# TruLayer AI — Go SDK

[![codecov](https://codecov.io/gh/trulayer/client-go/graph/badge.svg?token=wJJd4KrvfB)](https://codecov.io/gh/trulayer/client-go)

> **Status: Alpha.** APIs are pre-`1.0.0` and may change between minor releases.
> Pin a specific version in production until `1.0.0` ships.

Go SDK for instrumenting AI applications and sending traces to TruLayer AI.

- Documentation: https://docs.trulayer.ai/sdks/go/reference
- Source: https://github.com/trulayer/client-go
- Issues: https://github.com/trulayer/client-go/issues

## Installation

```bash
go get github.com/trulayer/client-go
```

Requires Go 1.22+. Zero runtime external dependencies — stdlib only.

## Quick Start

```go
package main

import (
    "context"
    "os"

    "github.com/trulayer/client-go/trulayer"
)

func main() {
    ctx := context.Background()
    tl := trulayer.NewClient(os.Getenv("TRULAYER_API_KEY"))
    defer tl.Shutdown(ctx)

    trace, ctx := tl.NewTrace(ctx, "answer-question")
    trace.SetInput("What is the capital of France?")

    span, ctx := trace.NewSpan(ctx, "llm-call", trulayer.SpanTypeLLM,
        trulayer.WithSpanModel("gpt-4o-mini"),
    )
    // ... call your LLM here ...
    span.SetOutput("Paris.")
    span.SetTokens(12, 4)
    span.End(ctx)

    trace.SetOutput("Paris.")
    trace.End(ctx)
}
```

## Manual Instrumentation

```go
tl := trulayer.NewClient(os.Getenv("TRULAYER_API_KEY"))
defer tl.Shutdown(context.Background())

ctx := context.Background()
trace, ctx := tl.NewTrace(ctx, "rag-pipeline",
    trulayer.WithTraceExternalID("req-42"), // link to your own request ID
)

// Retrieval span
retrieveSpan, ctx := trace.NewSpan(ctx, "retrieve", trulayer.SpanTypeRetrieval)
docs := retrieve(query)
retrieveSpan.End(ctx)

// LLM span
llmSpan, ctx := trace.NewSpan(ctx, "generate", trulayer.SpanTypeLLM,
    trulayer.WithSpanMetadata(map[string]any{"model": "gpt-4o", "tokens": 512}),
)
result := llm.Complete(prompt)
llmSpan.SetOutput(result)
llmSpan.End(ctx)

trace.End(ctx)
```

## Span Types

```go
trulayer.SpanTypeLLM        // "llm"       — language model call
trulayer.SpanTypeTool       // "tool"      — tool / function call
trulayer.SpanTypeRetrieval  // "retrieval" — vector search, document fetch
trulayer.SpanTypeOther      // "other"     — any other step
```

## Feedback

```go
err := tl.SubmitFeedback(ctx, traceID, trulayer.FeedbackData{
    Label:   "good",
    Comment: "Correct answer",
})
```

## Configuration

```go
tl := trulayer.NewClient(
    apiKey,
    trulayer.WithBaseURL("https://api.trulayer.ai"), // default; override for staging
    trulayer.WithBatchSize(50),                       // events per flush (default: 50)
    trulayer.WithFlushInterval(2*time.Second),        // time between flushes (default: 2s)
    trulayer.WithHTTPClient(myHTTPClient),            // custom http.Client
)
```

## Failure behavior

The SDK is designed to never block or crash your application when the TruLayer API is unavailable.

**Default behavior — drop + warn:**

- Batches that fail to send are retried up to 3 times with exponential backoff.
- After retries exhaust, the batch is dropped and a `log.Printf` warning is emitted.
- User goroutines are never blocked; `NewTrace` and `NewSpan` never return errors.

**Dry-run mode — `TRULAYER_DRY_RUN=true`:**

Set this environment variable to no-op all HTTP calls without changing any application code. No API key is required. Used by CI and unit tests.

```bash
TRULAYER_DRY_RUN=true go run ./examples/basic_trace/
```

## Shutdown

Always call `Shutdown` before your process exits to flush any buffered events:

```go
tl := trulayer.NewClient(apiKey)
defer tl.Shutdown(context.Background())
```

Pass a context with a deadline to cap the drain time:

```go
shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
tl.Shutdown(shutdownCtx)
```

## Features

- Zero runtime external dependencies (stdlib only)
- Async, channel-based batched ingestion — never blocks the request path
- Functional options for `NewClient`, traces, and spans
- Context-propagated parent/child span relationships
- UUIDv7 trace and span IDs (RFC 9562)
- `TRULAYER_DRY_RUN=true` disables all network calls (CI-safe)

## Development

```bash
go build ./...                               # Compile all packages
go test -race -coverprofile=cover.out ./...  # Tests with race detector (target: >90%)
go vet ./...                                 # Static analysis
golangci-lint run                            # Lint
go mod tidy                                  # Clean go.mod / go.sum
```

## Links

- [Documentation](https://docs.trulayer.ai)
- [API Reference](https://docs.trulayer.ai/api-reference)
- [Go SDK reference](https://docs.trulayer.ai/sdks/go/reference)

## License

MIT — see [LICENSE](./LICENSE).

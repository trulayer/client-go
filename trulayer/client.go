// Package trulayer is the official Go SDK for the TruLayer AI platform.
//
// It captures traces and spans for AI workloads (LLM calls, tool calls,
// retrieval steps) and ships them asynchronously to the TruLayer ingestion
// API. The public surface is intentionally small:
//
//	tl := trulayer.NewClient("tl_...")
//	defer tl.Shutdown(context.Background())
//
//	t, ctx := tl.NewTrace(ctx, "my-operation")
//	t.SetInput("hello")
//	span, _ := t.NewSpan(ctx, "llm-call", trulayer.SpanTypeLLM)
//	// ... call your LLM ...
//	span.SetOutput("hi")
//	span.End(ctx)
//	t.SetOutput("hi")
//	t.End(ctx)
//
// Set TRULAYER_DRY_RUN=true to disable all network calls — useful for CI
// and offline development.
package trulayer

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// DefaultBaseURL is the production TruLayer API endpoint. Override with
// WithBaseURL for self-hosted or staging deployments.
const DefaultBaseURL = "https://api.trulayer.ai"

const (
	defaultBatchSize     = 50
	defaultFlushInterval = 2 * time.Second
)

// Client is the entry point for the SDK. Construct once per process and
// reuse — Client is safe for concurrent use from multiple goroutines.
type Client struct {
	apiKey  string
	baseURL string
	httpc   *http.Client
	batch   *batchSender
	dryRun  bool
}

// NewClient constructs a Client. The api key is required for production
// use; pass an empty string and set TRULAYER_DRY_RUN=true for offline
// development.
func NewClient(apiKey string, opts ...ClientOption) *Client {
	cfg := clientConfig{
		baseURL:       DefaultBaseURL,
		batchSize:     defaultBatchSize,
		flushInterval: defaultFlushInterval,
	}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.httpClient == nil {
		cfg.httpClient = &http.Client{Timeout: requestTimeout}
	}
	cfg.baseURL = strings.TrimRight(cfg.baseURL, "/")

	dryRun := isDryRun()

	c := &Client{
		apiKey:  apiKey,
		baseURL: cfg.baseURL,
		httpc:   cfg.httpClient,
		dryRun:  dryRun,
	}
	c.batch = newBatchSender(apiKey, cfg.baseURL, cfg.batchSize, cfg.flushInterval, cfg.httpClient, dryRun)
	if apiKey == "" && !dryRun {
		log.Printf("trulayer: API key is empty and TRULAYER_DRY_RUN is not set — no traces will be sent. Set TRULAYER_API_KEY or TRULAYER_DRY_RUN=true to silence this warning.")
	}
	return c
}

// NewTrace begins a new Trace and returns it along with a context that
// carries the trace for child-span linkage.
func (c *Client) NewTrace(ctx context.Context, name string, opts ...TraceOption) (*Trace, context.Context) {
	cfg := traceConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	t := &Trace{
		client: c,
		start:  time.Now(),
		data: TraceData{
			ID:         newID(),
			Name:       name,
			ExternalID: cfg.externalID,
			Tags:       cfg.tags,
			Metadata:   cfg.metadata,
			Spans:      []SpanData{},
		},
	}
	return t, ctx
}

// Flush blocks until all enqueued traces have been attempted. The context
// bounds how long Flush waits.
func (c *Client) Flush(ctx context.Context) error {
	if c.batch == nil {
		return nil
	}
	return c.batch.flushNow(ctx)
}

// Shutdown drains the queue, performs a final flush, and stops the
// background goroutine. Subsequent NewTrace calls still succeed but the
// resulting traces will not be sent.
func (c *Client) Shutdown(ctx context.Context) error {
	if c.batch == nil {
		return nil
	}
	return c.batch.shutdown(ctx)
}

// SubmitFeedback posts a feedback record for a previously ingested trace.
// Returns a non-nil error on transport or server failure; in dry-run mode
// it is a no-op and returns nil.
func (c *Client) SubmitFeedback(ctx context.Context, traceID string, f FeedbackData) error {
	f.TraceID = traceID
	return c.batch.sendFeedback(ctx, f)
}

func isDryRun() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("TRULAYER_DRY_RUN")))
	return v == "1" || v == "true" || v == "yes"
}

package trulayer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// roundTripFunc lets a test stand in as the http transport without
// spinning up a real server.
type roundTripFunc func(req *http.Request) *http.Response

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req), nil }

func newOKResp() *http.Response {
	return &http.Response{
		StatusCode: 201,
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"ingested":1,"ids":["x"]}`))),
		Header:     make(http.Header),
	}
}

func captureClient(t *testing.T, status int) (*Client, *atomic.Int64, *sync.Mutex, *[]batchRequest) {
	t.Helper()
	var calls atomic.Int64
	var mu sync.Mutex
	var bodies []batchRequest

	httpc := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) *http.Response {
			calls.Add(1)
			b, _ := io.ReadAll(req.Body)
			var br batchRequest
			_ = json.Unmarshal(b, &br)
			mu.Lock()
			bodies = append(bodies, br)
			mu.Unlock()
			return &http.Response{
				StatusCode: status,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"ingested":1,"ids":["x"]}`))),
				Header:     make(http.Header),
			}
		}),
	}

	c := NewClient("tl_test",
		WithBaseURL("https://example.invalid"),
		WithBatchSize(50),
		WithFlushInterval(50*time.Millisecond),
		WithHTTPClient(httpc),
	)
	return c, &calls, &mu, &bodies
}

func TestNewIDFormat(t *testing.T) {
	id := newID()
	require.Len(t, id, 36, "uuid string length")
	// Version nibble at index 14 should be '7'
	assert.Equal(t, byte('7'), id[14], "uuidv7 version nibble")
	// Variant nibble at index 19 should be 8, 9, a, or b
	assert.Contains(t, "89ab", string(id[19]))
}

func TestNewClientDefaults(t *testing.T) {
	c := NewClient("tl_x")
	defer func() { _ = c.Shutdown(context.Background()) }()
	assert.Equal(t, DefaultBaseURL, c.baseURL)
}

func TestTraceLifecycle(t *testing.T) {
	c, calls, mu, bodies := captureClient(t, 201)
	defer func() { _ = c.Shutdown(context.Background()) }()

	ctx := context.Background()
	tr, ctx := c.NewTrace(ctx, "op")
	tr.SetInput("in")
	span, ctx := tr.NewSpan(ctx, "llm", SpanTypeLLM)
	span.SetOutput("out")
	span.End(ctx)

	span2, _ := tr.NewSpan(ctx, "tool", SpanTypeTool)
	span2.End(ctx)

	tr.SetOutput("done")
	tr.End(ctx)

	require.NoError(t, c.Flush(context.Background()))
	assert.GreaterOrEqual(t, calls.Load(), int64(1))

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, *bodies)
	got := (*bodies)[0]
	require.Len(t, got.Traces, 1)
	require.Equal(t, "op", got.Traces[0].Name)
	require.Len(t, got.Traces[0].Spans, 2)
	// Second span's parent should be the first span (per ctx propagation).
	assert.Equal(t, got.Traces[0].Spans[0].ID, got.Traces[0].Spans[1].ParentSpanID)
}

func TestBatchSizeFlushTrigger(t *testing.T) {
	var calls atomic.Int64
	httpc := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) *http.Response {
			calls.Add(1)
			return newOKResp()
		}),
	}
	c := NewClient("tl_test",
		WithBaseURL("https://example.invalid"),
		WithBatchSize(3),
		WithFlushInterval(time.Hour), // don't let the timer fire
		WithHTTPClient(httpc),
	)
	defer func() { _ = c.Shutdown(context.Background()) }()

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		tr, _ := c.NewTrace(ctx, "t")
		tr.End(ctx)
	}
	// Wait briefly for the size-triggered flush to fire.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	assert.GreaterOrEqual(t, calls.Load(), int64(1))
}

func TestRetryOn5xxThenSuccess(t *testing.T) {
	var calls atomic.Int64
	httpc := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) *http.Response {
			n := calls.Add(1)
			status := 500
			if n >= 2 {
				status = 201
			}
			return &http.Response{
				StatusCode: status,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{}`))),
				Header:     make(http.Header),
			}
		}),
	}
	c := NewClient("tl_test",
		WithBaseURL("https://example.invalid"),
		WithFlushInterval(time.Hour),
		WithHTTPClient(httpc),
	)
	defer func() { _ = c.Shutdown(context.Background()) }()

	ctx := context.Background()
	tr, _ := c.NewTrace(ctx, "t")
	tr.End(ctx)
	require.NoError(t, c.Flush(ctx))
	assert.GreaterOrEqual(t, calls.Load(), int64(2))
}

func TestNoRetryOn400(t *testing.T) {
	var calls atomic.Int64
	httpc := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) *http.Response {
			calls.Add(1)
			return &http.Response{
				StatusCode: 400,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{}`))),
				Header:     make(http.Header),
			}
		}),
	}
	c := NewClient("tl_test",
		WithBaseURL("https://example.invalid"),
		WithFlushInterval(time.Hour),
		WithHTTPClient(httpc),
	)
	defer func() { _ = c.Shutdown(context.Background()) }()

	ctx := context.Background()
	tr, _ := c.NewTrace(ctx, "t")
	tr.End(ctx)
	require.NoError(t, c.Flush(ctx))
	assert.Equal(t, int64(1), calls.Load(), "no retry on 400")
}

func TestDryRunSkipsHTTP(t *testing.T) {
	t.Setenv("TRULAYER_DRY_RUN", "true")
	var calls atomic.Int64
	httpc := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) *http.Response {
			calls.Add(1)
			return newOKResp()
		}),
	}
	c := NewClient("tl_test",
		WithBaseURL("https://example.invalid"),
		WithFlushInterval(50*time.Millisecond),
		WithHTTPClient(httpc),
	)
	defer func() { _ = c.Shutdown(context.Background()) }()

	ctx := context.Background()
	tr, _ := c.NewTrace(ctx, "t")
	tr.End(ctx)
	require.NoError(t, c.Flush(ctx))
	require.NoError(t, c.SubmitFeedback(ctx, tr.ID(), FeedbackData{Label: "good"}))
	assert.Equal(t, int64(0), calls.Load(), "dry-run must not hit HTTP")
}

func TestEmptyAPIKeyImpliesDryRun(t *testing.T) {
	var calls atomic.Int64
	httpc := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) *http.Response {
			calls.Add(1)
			return newOKResp()
		}),
	}
	c := NewClient("",
		WithBaseURL("https://example.invalid"),
		WithFlushInterval(50*time.Millisecond),
		WithHTTPClient(httpc),
	)
	defer func() { _ = c.Shutdown(context.Background()) }()
	ctx := context.Background()
	tr, _ := c.NewTrace(ctx, "t")
	tr.End(ctx)
	require.NoError(t, c.Flush(ctx))
	assert.Equal(t, int64(0), calls.Load(), "empty api key must imply dry-run")
}

func TestShutdownDrainsQueue(t *testing.T) {
	c, calls, _, _ := captureClient(t, 201)
	ctx := context.Background()
	tr, _ := c.NewTrace(ctx, "t")
	tr.End(ctx)
	require.NoError(t, c.Shutdown(ctx))
	assert.GreaterOrEqual(t, calls.Load(), int64(1))
}

func TestSubmitFeedback(t *testing.T) {
	var path string
	var auth string
	var body []byte
	httpc := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) *http.Response {
			path = req.URL.Path
			auth = req.Header.Get("Authorization")
			body, _ = io.ReadAll(req.Body)
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"ok":true}`))),
				Header:     make(http.Header),
			}
		}),
	}
	c := NewClient("tl_test",
		WithBaseURL("https://example.invalid"),
		WithHTTPClient(httpc),
	)
	defer func() { _ = c.Shutdown(context.Background()) }()

	require.NoError(t, c.SubmitFeedback(context.Background(), "trace-1", FeedbackData{Label: "good", Comment: "nice"}))
	assert.Equal(t, "/v1/feedback", path)
	assert.Equal(t, "Bearer tl_test", auth)
	var got FeedbackData
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, "trace-1", got.TraceID)
	assert.Equal(t, "good", got.Label)
	assert.Equal(t, "nice", got.Comment)
}

func TestSubmitFeedbackErrorOn5xx(t *testing.T) {
	httpc := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) *http.Response {
			return &http.Response{
				StatusCode: 503,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{}`))),
				Header:     make(http.Header),
			}
		}),
	}
	c := NewClient("tl_test", WithBaseURL("https://example.invalid"), WithHTTPClient(httpc))
	defer func() { _ = c.Shutdown(context.Background()) }()

	err := c.SubmitFeedback(context.Background(), "trace-1", FeedbackData{Label: "bad"})
	require.Error(t, err)
}

func TestOptionsValidation(t *testing.T) {
	cfg := clientConfig{batchSize: 50, flushInterval: time.Second}
	WithBatchSize(0)(&cfg)
	WithBatchSize(-5)(&cfg)
	assert.Equal(t, 50, cfg.batchSize, "non-positive batch size ignored")

	WithFlushInterval(-time.Second)(&cfg)
	assert.Equal(t, time.Second, cfg.flushInterval, "non-positive flush interval ignored")

	WithBatchSize(123)(&cfg)
	assert.Equal(t, 123, cfg.batchSize)
}

func TestTraceOptions(t *testing.T) {
	c := NewClient("", WithBaseURL("https://example.invalid"))
	defer func() { _ = c.Shutdown(context.Background()) }()

	tr, _ := c.NewTrace(context.Background(), "op",
		WithTraceExternalID("ext"),
		WithTags(map[string]string{"env": "prod"}),
		WithTraceMetadata(map[string]interface{}{"k": "v"}),
	)
	assert.Equal(t, "ext", tr.data.ExternalID)
	assert.Equal(t, "prod", tr.data.Tags["env"])
	assert.Equal(t, "v", tr.data.Metadata["k"])
}

func TestSpanOptionsAndSetters(t *testing.T) {
	c := NewClient("", WithBaseURL("https://example.invalid"))
	defer func() { _ = c.Shutdown(context.Background()) }()

	tr, ctx := c.NewTrace(context.Background(), "op")
	span, ctx := tr.NewSpan(ctx, "llm", SpanTypeLLM,
		WithSpanInput("prompt"),
		WithSpanModel("gpt-test"),
		WithSpanMetadata(map[string]interface{}{"k": 1}),
	)
	span.SetOutput("done")
	span.SetTokens(10, 20)
	span.SetCost(0.0042)
	span.SetError("boom")
	span.End(ctx)

	tr.SetTag("env", "prod")
	tr.SetMetadata(map[string]interface{}{"x": "y"})
	tr.SetModel("gpt-test")
	tr.SetError("oops")
	tr.End(ctx)

	require.Len(t, tr.data.Spans, 1)
	got := tr.data.Spans[0]
	assert.Equal(t, "prompt", got.Input)
	assert.Equal(t, "done", got.Output)
	assert.Equal(t, "gpt-test", got.Model)
	assert.Equal(t, 10, got.PromptTokens)
	assert.Equal(t, 20, got.CompletionTokens)
	assert.InDelta(t, 0.0042, got.Cost, 1e-9)
	assert.Equal(t, "boom", got.Error)
	require.NotNil(t, got.EndTime)
}

func TestSpanFromContext(t *testing.T) {
	c := NewClient("")
	defer func() { _ = c.Shutdown(context.Background()) }()

	tr, ctx := c.NewTrace(context.Background(), "op")
	assert.Nil(t, SpanFromContext(ctx))
	span, ctx := tr.NewSpan(ctx, "s", SpanTypeOther)
	assert.Equal(t, span, SpanFromContext(ctx))
}

func TestSpanSettersComplete(t *testing.T) {
	c := NewClient("")
	defer func() { _ = c.Shutdown(context.Background()) }()

	tr, ctx := c.NewTrace(context.Background(), "op")
	span, _ := tr.NewSpan(ctx, "s", SpanTypeOther)
	span.SetInput("prompt")
	span.SetModel("gpt-test-direct")
	span.SetMetadata(map[string]interface{}{"k": "v"})
	assert.NotEmpty(t, span.ID())
	assert.Equal(t, "prompt", span.data.Input)
	assert.Equal(t, "gpt-test-direct", span.data.Model)
	assert.Equal(t, "v", span.data.Metadata["k"])
}

func TestNilOptionInputs(t *testing.T) {
	cfg := traceConfig{}
	WithTags(nil)(&cfg)
	WithTraceMetadata(nil)(&cfg)
	assert.Nil(t, cfg.tags)
	assert.Nil(t, cfg.metadata)

	scfg := spanConfig{}
	WithSpanMetadata(nil)(&scfg)
	assert.Nil(t, scfg.metadata)
}

func TestPeriodicTimerFlush(t *testing.T) {
	var calls atomic.Int64
	httpc := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) *http.Response {
			calls.Add(1)
			return newOKResp()
		}),
	}
	c := NewClient("tl_test",
		WithBaseURL("https://example.invalid"),
		WithBatchSize(1000),                    // never size-flush
		WithFlushInterval(20*time.Millisecond), // tick fast
		WithHTTPClient(httpc),
	)
	defer func() { _ = c.Shutdown(context.Background()) }()

	ctx := context.Background()
	tr, _ := c.NewTrace(ctx, "t")
	tr.End(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	assert.GreaterOrEqual(t, calls.Load(), int64(1), "timer should have flushed")
}

func TestFlushOnDryRunIsNoop(t *testing.T) {
	t.Setenv("TRULAYER_DRY_RUN", "true")
	c := NewClient("tl_test", WithBaseURL("https://example.invalid"))
	defer func() { _ = c.Shutdown(context.Background()) }()
	require.NoError(t, c.Flush(context.Background()))
}

func TestEnqueueAfterShutdownIsSafe(t *testing.T) {
	c, _, _, _ := captureClient(t, 201)
	require.NoError(t, c.Shutdown(context.Background()))
	// Should not panic even though the sender is closed.
	tr, ctx := c.NewTrace(context.Background(), "after")
	tr.End(ctx)
}

func TestEndIsIdempotent(t *testing.T) {
	c := NewClient("")
	defer func() { _ = c.Shutdown(context.Background()) }()

	tr, ctx := c.NewTrace(context.Background(), "op")
	span, ctx := tr.NewSpan(ctx, "s", SpanTypeOther)
	span.End(ctx)
	span.End(ctx) // second call is a no-op
	tr.End(ctx)
	tr.End(ctx)
	assert.Len(t, tr.data.Spans, 1)
}

// --- gaps added by QA audit (TRU-457) ---

func TestDropAfterMaxRetries(t *testing.T) {
	// All three attempts return 500 — the sender must give up after maxRetries
	// calls and not panic or block.
	var calls atomic.Int64
	httpc := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) *http.Response {
			calls.Add(1)
			return &http.Response{
				StatusCode: 500,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{}`))),
				Header:     make(http.Header),
			}
		}),
	}
	c := NewClient("tl_test",
		WithBaseURL("https://example.invalid"),
		WithFlushInterval(time.Hour),
		WithHTTPClient(httpc),
	)
	defer func() { _ = c.Shutdown(context.Background()) }()

	ctx := context.Background()
	tr, _ := c.NewTrace(ctx, "t")
	tr.End(ctx)
	require.NoError(t, c.Flush(ctx))
	assert.Equal(t, int64(maxRetries), calls.Load(), "must attempt exactly maxRetries times then drop")
}

func Test429IsRetried(t *testing.T) {
	// 429 is in the 4xx range but is retryable; unlike 400 it must not short-circuit.
	var calls atomic.Int64
	httpc := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) *http.Response {
			n := calls.Add(1)
			status := 429
			if n >= 2 {
				status = 201
			}
			return &http.Response{
				StatusCode: status,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{}`))),
				Header:     make(http.Header),
			}
		}),
	}
	c := NewClient("tl_test",
		WithBaseURL("https://example.invalid"),
		WithFlushInterval(time.Hour),
		WithHTTPClient(httpc),
	)
	defer func() { _ = c.Shutdown(context.Background()) }()

	ctx := context.Background()
	tr, _ := c.NewTrace(ctx, "t")
	tr.End(ctx)
	require.NoError(t, c.Flush(ctx))
	assert.GreaterOrEqual(t, calls.Load(), int64(2), "429 must be retried")
}

func TestTransportErrorIsRetried(t *testing.T) {
	// A transport-level error (not an HTTP status) must trigger the retry loop
	// and eventually drop after maxRetries. The call must not panic or block.
	var calls atomic.Int64
	httpc := &http.Client{
		Transport: &errorTransport{calls: &calls},
	}
	c := NewClient("tl_test",
		WithBaseURL("https://example.invalid"),
		WithFlushInterval(time.Hour),
		WithHTTPClient(httpc),
	)
	defer func() { _ = c.Shutdown(context.Background()) }()

	ctx := context.Background()
	tr, _ := c.NewTrace(ctx, "t")
	tr.End(ctx)
	// Must not hang; send() retries maxRetries times then drops.
	require.NoError(t, c.Flush(ctx))
	assert.Equal(t, int64(maxRetries), calls.Load(), "transport errors must be retried maxRetries times")
}

// errorTransport satisfies http.RoundTripper and always returns a non-nil error.
type errorTransport struct{ calls *atomic.Int64 }

func (e *errorTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	e.calls.Add(1)
	return nil, fmt.Errorf("simulated network failure")
}

func TestSubmitFeedback401ReturnsError(t *testing.T) {
	httpc := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) *http.Response {
			return &http.Response{
				StatusCode: 401,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"error":"unauthorized"}`))),
				Header:     make(http.Header),
			}
		}),
	}
	c := NewClient("tl_invalid", WithBaseURL("https://example.invalid"), WithHTTPClient(httpc))
	defer func() { _ = c.Shutdown(context.Background()) }()

	err := c.SubmitFeedback(context.Background(), "trace-1", FeedbackData{Label: "bad"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}

func TestIsDryRunValues(t *testing.T) {
	tests := []struct {
		envVal string
		want   bool
	}{
		{"true", true},
		{"1", true},
		{"yes", true},
		{"TRUE", true},
		{"YES", true},
		{"false", false},
		{"0", false},
		{"", false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.envVal, func(t *testing.T) {
			t.Setenv("TRULAYER_DRY_RUN", tc.envVal)
			got := isDryRun()
			assert.Equal(t, tc.want, got, "TRULAYER_DRY_RUN=%q", tc.envVal)
		})
	}
}

func TestNewIDUniqueness(t *testing.T) {
	const n = 100
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		id := newID()
		require.NotEmpty(t, id)
		require.Len(t, id, 36)
		_, dup := seen[id]
		require.False(t, dup, "duplicate ID generated: %s", id)
		seen[id] = struct{}{}
	}
}

func TestShutdownOnceDoIsIdempotent(t *testing.T) {
	// Calling Shutdown twice must be safe — once.Do ensures the second call
	// is a no-op regardless of whether the first call succeeded.
	c, _, _, _ := captureClient(t, 201)
	require.NoError(t, c.Shutdown(context.Background()))
	// Second call: goroutine is already stopped; must return nil, not block.
	require.NoError(t, c.Shutdown(context.Background()))
}

func TestFlushCancelledContextDoesNotBlock(t *testing.T) {
	// Flush with a cancelled context must return promptly (within 1 second).
	// It may return context.Canceled or nil — either is acceptable; blocking
	// is not.
	c, _, _, _ := captureClient(t, 201)
	defer func() { _ = c.Shutdown(context.Background()) }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() { done <- c.Flush(ctx) }()

	select {
	case err := <-done:
		// nil or context.Canceled are both fine
		if err != nil {
			assert.ErrorIs(t, err, context.Canceled, "only acceptable non-nil error is context.Canceled")
		}
	case <-time.After(time.Second):
		t.Fatal("Flush with cancelled context blocked for >1s")
	}
}

func TestTraceIDNonEmpty(t *testing.T) {
	c := NewClient("")
	defer func() { _ = c.Shutdown(context.Background()) }()
	tr, _ := c.NewTrace(context.Background(), "op")
	assert.NotEmpty(t, tr.ID(), "trace ID must not be empty")
	assert.Len(t, tr.ID(), 36, "trace ID must be a UUID string")
}

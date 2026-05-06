package trulayer_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trulayer/client-go/trulayer"
)

// recorded is the slice of decoded request bodies a test fixture saw.
type recorded struct {
	mu        sync.Mutex
	ingest    []map[string]interface{}
	feedbacks []map[string]interface{}
}

func (r *recorded) addIngest(b map[string]interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ingest = append(r.ingest, b)
}

func (r *recorded) addFeedback(b map[string]interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.feedbacks = append(r.feedbacks, b)
}

func newRecorder(t *testing.T) (*httptest.Server, *recorded) {
	t.Helper()
	rec := &recorded{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var b map[string]interface{}
		_ = json.Unmarshal(body, &b)
		switch r.URL.Path {
		case "/v1/ingest/batch":
			rec.addIngest(b)
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"ingested":1,"ids":["x"]}`))
		case "/v1/feedback":
			rec.addFeedback(b)
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func TestIntegration_IngestRoundTrip(t *testing.T) {
	srv, rec := newRecorder(t)

	c := trulayer.NewClient("tl_test",
		trulayer.WithBaseURL(srv.URL),
		trulayer.WithFlushInterval(50*time.Millisecond),
		trulayer.WithBatchSize(10),
	)
	t.Cleanup(func() { _ = c.Shutdown(context.Background()) })

	ctx := context.Background()
	tr, ctx := c.NewTrace(ctx, "outer", trulayer.WithTraceExternalID("req-42"))
	tr.SetInput("user prompt")

	s1, ctx := tr.NewSpan(ctx, "retrieval", trulayer.SpanTypeRetrieval)
	s1.SetOutput("doc-1, doc-2")
	s1.End(ctx)

	s2, ctx2 := tr.NewSpan(ctx, "llm", trulayer.SpanTypeLLM)
	s2.SetModel("gpt-test")
	s2.SetTokens(100, 50)
	s2.SetOutput("model response")
	s2.End(ctx2)

	tr.SetOutput("final response")
	tr.End(ctx)

	require.NoError(t, c.Flush(ctx))

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.NotEmpty(t, rec.ingest, "expected at least one ingest call")
	first := rec.ingest[0]
	traces, ok := first["traces"].([]interface{})
	require.True(t, ok)
	require.Len(t, traces, 1)
	gotTrace := traces[0].(map[string]interface{})
	assert.Equal(t, "outer", gotTrace["name"])
	assert.Equal(t, "req-42", gotTrace["external_id"])
	spans := gotTrace["spans"].([]interface{})
	require.Len(t, spans, 2)
	s := spans[1].(map[string]interface{})
	assert.Equal(t, "llm", s["type"])
	assert.Equal(t, "gpt-test", s["model"])
}

func TestIntegration_SubmitFeedback(t *testing.T) {
	srv, rec := newRecorder(t)
	c := trulayer.NewClient("tl_test", trulayer.WithBaseURL(srv.URL))
	t.Cleanup(func() { _ = c.Shutdown(context.Background()) })

	require.NoError(t, c.SubmitFeedback(context.Background(), "trace-1", trulayer.FeedbackData{
		Label:   "good",
		Comment: "lgtm",
	}))

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.Len(t, rec.feedbacks, 1)
	got := rec.feedbacks[0]
	assert.Equal(t, "trace-1", got["trace_id"])
	assert.Equal(t, "good", got["label"])
}

func TestIntegration_ShutdownWaitsForInflight(t *testing.T) {
	srv, rec := newRecorder(t)
	c := trulayer.NewClient("tl_test",
		trulayer.WithBaseURL(srv.URL),
		trulayer.WithFlushInterval(time.Hour), // never tick
	)
	ctx := context.Background()
	tr, _ := c.NewTrace(ctx, "t")
	tr.End(ctx)

	require.NoError(t, c.Shutdown(ctx))

	rec.mu.Lock()
	defer rec.mu.Unlock()
	assert.NotEmpty(t, rec.ingest, "shutdown must flush remaining traces")
}

// TestIntegration_IngestAuthorizationHeader verifies the Bearer token is set
// on every POST to /v1/ingest/batch. This was not asserted in the original
// round-trip test and is the kind of omission that lets a renamed header slip
// past the unit layer (TRU-457 audit finding).
func TestIntegration_IngestAuthorizationHeader(t *testing.T) {
	var capturedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/ingest/batch" {
			capturedAuth = r.Header.Get("Authorization")
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"ingested":1,"ids":["x"]}`))
		} else {
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)

	c := trulayer.NewClient("tl_secret",
		trulayer.WithBaseURL(srv.URL),
		trulayer.WithFlushInterval(50*time.Millisecond),
	)
	t.Cleanup(func() { _ = c.Shutdown(context.Background()) })

	ctx := context.Background()
	tr, _ := c.NewTrace(ctx, "auth-check")
	tr.End(ctx)
	require.NoError(t, c.Flush(ctx))

	assert.Equal(t, "Bearer tl_secret", capturedAuth, "ingest request must carry Bearer token")
}

// TestIntegration_401DropsSilently verifies that a 401 from the server causes
// the batch to be dropped (non-retryable 4xx) and Flush returns nil — the SDK
// must not propagate server errors to the caller's goroutine.
func TestIntegration_401DropsSilently(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	t.Cleanup(srv.Close)

	c := trulayer.NewClient("tl_bad_key",
		trulayer.WithBaseURL(srv.URL),
		trulayer.WithFlushInterval(time.Hour),
	)
	t.Cleanup(func() { _ = c.Shutdown(context.Background()) })

	ctx := context.Background()
	tr, _ := c.NewTrace(ctx, "t")
	tr.End(ctx)
	// Flush must succeed from the caller's perspective — the SDK drops and logs.
	require.NoError(t, c.Flush(ctx))
}

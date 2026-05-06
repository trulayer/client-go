package trulayer

import (
	"net/http"
	"time"
)

// ClientOption configures a Client. Pass options to NewClient.
type ClientOption func(*clientConfig)

type clientConfig struct {
	baseURL       string
	batchSize     int
	flushInterval time.Duration
	httpClient    *http.Client
}

// WithBaseURL overrides the API base URL. Defaults to https://api.trulayer.ai.
func WithBaseURL(u string) ClientOption {
	return func(c *clientConfig) { c.baseURL = u }
}

// WithBatchSize sets the maximum number of traces to buffer before forcing a
// flush. Defaults to 50. Values <= 0 are ignored.
func WithBatchSize(n int) ClientOption {
	return func(c *clientConfig) {
		if n > 0 {
			c.batchSize = n
		}
	}
}

// WithFlushInterval sets the periodic flush interval. Defaults to 2 seconds.
// Values <= 0 are ignored.
func WithFlushInterval(d time.Duration) ClientOption {
	return func(c *clientConfig) {
		if d > 0 {
			c.flushInterval = d
		}
	}
}

// WithHTTPClient overrides the http.Client used for ingest and feedback
// submissions. Useful for tests and custom transports.
func WithHTTPClient(h *http.Client) ClientOption {
	return func(c *clientConfig) { c.httpClient = h }
}

// TraceOption configures a Trace at creation time.
type TraceOption func(*traceConfig)

type traceConfig struct {
	externalID string
	tags       map[string]string
	metadata   map[string]interface{}
}

// WithTraceExternalID attaches an external identifier to the trace.
// Useful for correlating with upstream request IDs.
func WithTraceExternalID(id string) TraceOption {
	return func(c *traceConfig) { c.externalID = id }
}

// WithTags attaches a structured key-value tag map to the trace. Maximum
// 20 keys; keys and values are strings up to 64 characters each.
func WithTags(tags map[string]string) TraceOption {
	return func(c *traceConfig) {
		if tags == nil {
			return
		}
		c.tags = make(map[string]string, len(tags))
		for k, v := range tags {
			c.tags[k] = v
		}
	}
}

// WithTraceMetadata attaches arbitrary metadata to the trace.
func WithTraceMetadata(md map[string]interface{}) TraceOption {
	return func(c *traceConfig) {
		if md == nil {
			return
		}
		c.metadata = make(map[string]interface{}, len(md))
		for k, v := range md {
			c.metadata[k] = v
		}
	}
}

// SpanOption configures a Span at creation time.
type SpanOption func(*spanConfig)

type spanConfig struct {
	input    string
	model    string
	metadata map[string]interface{}
}

// WithSpanInput records the prompt or input string for the span.
func WithSpanInput(s string) SpanOption {
	return func(c *spanConfig) { c.input = s }
}

// WithSpanModel records the model identifier for the span.
func WithSpanModel(s string) SpanOption {
	return func(c *spanConfig) { c.model = s }
}

// WithSpanMetadata attaches arbitrary metadata to the span.
func WithSpanMetadata(md map[string]interface{}) SpanOption {
	return func(c *spanConfig) {
		if md == nil {
			return
		}
		c.metadata = make(map[string]interface{}, len(md))
		for k, v := range md {
			c.metadata[k] = v
		}
	}
}

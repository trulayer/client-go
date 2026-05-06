package trulayer

import (
	"context"
	"sync"
	"time"
)

type spanCtxKey struct{}

// Span is a unit of work inside a Trace (e.g. an LLM call, a tool
// invocation, a retrieval step). Create via Trace.NewSpan and call End
// to record completion.
type Span struct {
	trace *Trace
	mu    sync.Mutex
	data  SpanData
	start time.Time
	ended bool
}

// ID returns the UUIDv7 span identifier.
func (s *Span) ID() string { return s.data.ID }

// SetInput records the prompt or input string for the span.
func (s *Span) SetInput(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Input = v
}

// SetOutput records the completion or output string for the span.
func (s *Span) SetOutput(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Output = v
}

// SetModel records the model identifier for the span.
func (s *Span) SetModel(m string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Model = m
}

// SetTokens records prompt and completion token counts for the span.
func (s *Span) SetTokens(prompt, completion int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.PromptTokens = prompt
	s.data.CompletionTokens = completion
}

// SetCost records the USD cost attributed to this span (e.g. an LLM call).
func (s *Span) SetCost(usd float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Cost = usd
}

// SetError records an error message on the span.
func (s *Span) SetError(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Error = msg
}

// SetMetadata merges arbitrary metadata into the span's metadata map.
func (s *Span) SetMetadata(md map[string]interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.Metadata == nil {
		s.data.Metadata = map[string]interface{}{}
	}
	for k, v := range md {
		s.data.Metadata[k] = v
	}
}

// End finalises the span, records its end timestamp and latency, and
// attaches it to its parent trace. Subsequent calls to End are no-ops.
func (s *Span) End(ctx context.Context) {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	now := time.Now().UTC()
	s.data.EndTime = &now
	s.data.LatencyMs = time.Since(s.start).Milliseconds()
	snap := s.data
	s.mu.Unlock()

	if s.trace == nil {
		return
	}
	s.trace.mu.Lock()
	s.trace.data.Spans = append(s.trace.data.Spans, snap)
	s.trace.mu.Unlock()
}

// SpanFromContext returns the active span carried in ctx, or nil if there
// is none.
func SpanFromContext(ctx context.Context) *Span {
	if v, ok := ctx.Value(spanCtxKey{}).(*Span); ok {
		return v
	}
	return nil
}

package trulayer

import (
	"context"
	"sync"
	"time"
)

// traceCtxKey is the context key under which the active *Trace is stored.
type traceCtxKey struct{}

// TraceFromContext returns the active trace carried in ctx, or nil if there
// is none. Instruments and helper code use it to attach a span to whichever
// trace the caller already started.
func TraceFromContext(ctx context.Context) *Trace {
	if v, ok := ctx.Value(traceCtxKey{}).(*Trace); ok {
		return v
	}
	return nil
}

// Trace is a unit of work that groups one or more spans. Create via
// Client.NewTrace and call End to flush.
type Trace struct {
	client *Client
	mu     sync.Mutex
	data   TraceData
	start  time.Time
	ended  bool
}

// ID returns the UUIDv7 trace identifier. Useful for correlating with
// SubmitFeedback later in the same process.
func (t *Trace) ID() string { return t.data.ID }

// SetInput records the trace-level input string (e.g. the original user
// prompt).
func (t *Trace) SetInput(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.data.Input = s
}

// SetOutput records the trace-level output string (e.g. the final response
// returned to the user).
func (t *Trace) SetOutput(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.data.Output = s
}

// SetModel records the model identifier for the trace.
func (t *Trace) SetModel(m string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.data.Model = m
}

// SetError records an error message on the trace. Pass an empty string to
// clear a previously set error.
func (t *Trace) SetError(msg string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.data.Error = msg
}

// SetTag adds a single key-value tag to the trace. Limits: max 20 keys,
// 64 chars per key/value.
func (t *Trace) SetTag(k, v string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.data.Tags == nil {
		t.data.Tags = map[string]string{}
	}
	t.data.Tags[k] = v
}

// SetMetadata merges arbitrary metadata into the trace's metadata map.
func (t *Trace) SetMetadata(md map[string]interface{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.data.Metadata == nil {
		t.data.Metadata = map[string]interface{}{}
	}
	for k, v := range md {
		t.data.Metadata[k] = v
	}
}

// NewSpan creates a new span attached to this trace. The returned context
// carries the span so child spans can attach to it via parent linkage.
func (t *Trace) NewSpan(ctx context.Context, name string, spanType SpanType, opts ...SpanOption) (*Span, context.Context) {
	cfg := spanConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	if spanType == "" {
		spanType = SpanTypeOther
	}
	parentID := ""
	if p, ok := ctx.Value(spanCtxKey{}).(*Span); ok && p != nil {
		parentID = p.data.ID
	}
	// Capture the start instant once so the wall-clock StartTime, the
	// monotonic latency anchor (s.start), and the eventual EndTime all
	// refer to the same moment the upstream operation begins. The latency
	// waterfall positions bars by StartTime, so it must be the wall-clock
	// time right before the operation — which is exactly when callers
	// invoke NewSpan.
	start := time.Now()
	s := &Span{
		trace: t,
		start: start,
		data: SpanData{
			ID:           newID(),
			ParentSpanID: parentID,
			Name:         name,
			Type:         spanType,
			Input:        cfg.input,
			Model:        cfg.model,
			Metadata:     cfg.metadata,
			StartTime:    start.UTC(),
		},
	}
	return s, context.WithValue(ctx, spanCtxKey{}, s)
}

// Spans returns a snapshot of the spans recorded on this trace so far. The
// returned slice is a copy — mutations do not affect the trace.
//
// Spans is primarily intended for testing instruments; production code does
// not need to inspect individual spans.
func (t *Trace) Spans() []SpanData {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]SpanData, len(t.data.Spans))
	copy(out, t.data.Spans)
	return out
}

// End finalises the trace and queues it to the background batch sender.
// Subsequent calls to End are no-ops. End never blocks the caller on
// network I/O.
func (t *Trace) End(ctx context.Context) {
	t.mu.Lock()
	if t.ended {
		t.mu.Unlock()
		return
	}
	t.ended = true
	t.data.LatencyMs = time.Since(t.start).Milliseconds()
	// Emit a snapshot copy so post-End mutations (defensively guarded
	// elsewhere) cannot affect the queued payload.
	snap := t.data
	snap.Spans = append([]SpanData(nil), t.data.Spans...)
	t.mu.Unlock()

	if t.client != nil && t.client.batch != nil {
		t.client.batch.enqueue(snap)
	}
}

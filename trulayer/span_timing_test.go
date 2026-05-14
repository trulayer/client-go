package trulayer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSpanStartTimeCapturedBeforeOperation guards the invariant the dashboard
// latency waterfall depends on: StartTime is captured at the moment NewSpan
// is invoked — which instrumented callers do *before* the upstream
// operation — and EndTime is captured at the moment End is called. When a
// trace is flushed in a batch after completion, the absolute timestamps
// are the only thing that lets the waterfall position bars correctly.
func TestSpanStartTimeCapturedBeforeOperation(t *testing.T) {
	t.Setenv("TRULAYER_DRY_RUN", "true")
	c := NewClient("")
	defer func() { _ = c.Shutdown(context.Background()) }()

	tr, ctx := c.NewTrace(context.Background(), "op")
	before := time.Now().UTC()
	span, ctx := tr.NewSpan(ctx, "llm", SpanTypeLLM)
	afterNewSpan := time.Now().UTC()

	// Simulate the upstream operation.
	time.Sleep(20 * time.Millisecond)
	span.End(ctx)
	afterEnd := time.Now().UTC()

	tr.End(ctx)

	spans := tr.Spans()
	require.Len(t, spans, 1)
	got := spans[0]

	// StartTime falls inside [before, afterNewSpan].
	assert.False(t, got.StartTime.Before(before),
		"StartTime %v should not precede the moment before NewSpan (%v)",
		got.StartTime, before)
	assert.False(t, got.StartTime.After(afterNewSpan),
		"StartTime %v should not follow the moment right after NewSpan (%v)",
		got.StartTime, afterNewSpan)

	// EndTime is set, falls inside [start+sleep, afterEnd], and is strictly
	// after StartTime.
	require.NotNil(t, got.EndTime)
	end := *got.EndTime
	assert.True(t, end.After(got.StartTime),
		"EndTime %v should be after StartTime %v", end, got.StartTime)
	assert.False(t, end.After(afterEnd),
		"EndTime %v should not follow the moment after End() returned (%v)",
		end, afterEnd)
	assert.GreaterOrEqual(t, end.Sub(got.StartTime), 15*time.Millisecond,
		"EndTime - StartTime should reflect the simulated 20ms operation")
}

// TestSpanStartTimeIsUTC ensures the wire format always serialises a UTC
// timestamp regardless of the caller's local timezone.
func TestSpanStartTimeIsUTC(t *testing.T) {
	t.Setenv("TRULAYER_DRY_RUN", "true")
	c := NewClient("")
	defer func() { _ = c.Shutdown(context.Background()) }()

	tr, ctx := c.NewTrace(context.Background(), "op")
	span, ctx := tr.NewSpan(ctx, "llm", SpanTypeLLM)
	span.End(ctx)
	tr.End(ctx)

	spans := tr.Spans()
	require.Len(t, spans, 1)
	got := spans[0]
	_, offset := got.StartTime.Zone()
	assert.Equal(t, 0, offset, "StartTime must be in UTC")
	require.NotNil(t, got.EndTime)
	_, endOffset := got.EndTime.Zone()
	assert.Equal(t, 0, endOffset, "EndTime must be in UTC")
}

// TestSpanLatencyMatchesTimestamps verifies the three timing fields stay
// consistent: LatencyMs ≈ EndTime - StartTime. They are sampled from
// different clocks (StartTime uses time.Now(), LatencyMs uses
// time.Since), but the gap is bounded by a few milliseconds even on slow
// CI.
func TestSpanLatencyMatchesTimestamps(t *testing.T) {
	t.Setenv("TRULAYER_DRY_RUN", "true")
	c := NewClient("")
	defer func() { _ = c.Shutdown(context.Background()) }()

	tr, ctx := c.NewTrace(context.Background(), "op")
	span, ctx := tr.NewSpan(ctx, "llm", SpanTypeLLM)
	time.Sleep(30 * time.Millisecond)
	span.End(ctx)
	tr.End(ctx)

	spans := tr.Spans()
	require.Len(t, spans, 1)
	got := spans[0]
	require.NotNil(t, got.EndTime)

	walledMs := got.EndTime.Sub(got.StartTime).Milliseconds()
	assert.InDelta(t, walledMs, got.LatencyMs, 5.0,
		"LatencyMs (%d) should be within 5ms of EndTime - StartTime (%d)",
		got.LatencyMs, walledMs)
}

// TestSequentialSpansHaveNonDecreasingStartTime ensures the waterfall can
// rely on StartTime ordering matching span-creation order.
func TestSequentialSpansHaveNonDecreasingStartTime(t *testing.T) {
	t.Setenv("TRULAYER_DRY_RUN", "true")
	c := NewClient("")
	defer func() { _ = c.Shutdown(context.Background()) }()

	tr, ctx := c.NewTrace(context.Background(), "op")
	for _, name := range []string{"a", "b", "c"} {
		s, sctx := tr.NewSpan(ctx, name, SpanTypeLLM)
		time.Sleep(2 * time.Millisecond)
		s.End(sctx)
	}
	tr.End(ctx)

	spans := tr.Spans()
	require.Len(t, spans, 3)
	for i := 1; i < len(spans); i++ {
		assert.False(t, spans[i].StartTime.Before(spans[i-1].StartTime),
			"span[%d].StartTime (%v) should not precede span[%d].StartTime (%v)",
			i, spans[i].StartTime, i-1, spans[i-1].StartTime)
		require.NotNil(t, spans[i-1].EndTime)
		assert.False(t, spans[i].StartTime.Before(*spans[i-1].EndTime),
			"span[%d].StartTime (%v) should not precede the previous span's EndTime (%v)",
			i, spans[i].StartTime, *spans[i-1].EndTime)
	}
}

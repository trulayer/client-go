// Package anthropic provides TruLayer auto-instrumentation for the
// github.com/anthropics/anthropic-sdk-go client.
//
// The wrapper opens a TruLayer span around each Messages.New call and
// records model, input, output, token counts, latency, and errors on it.
// If the caller's context does not carry an active TruLayer trace, the
// wrapper transparently delegates to the underlying client.
//
//	tl := trulayer.NewClient(os.Getenv("TRULAYER_API_KEY"))
//	ac := anthropic.NewClient(option.WithAPIKey(os.Getenv("ANTHROPIC_API_KEY")))
//	instrumented := tlanthropic.InstrumentAnthropic(&ac, tl)
//
//	ctx, trace := tl.NewTrace(ctx, "answer-question")
//	defer trace.End(ctx)
//	msg, err := instrumented.Messages.New(ctx, anthropic.MessageNewParams{...})
package anthropic

import (
	"context"
	"log"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/trulayer/client-go/trulayer"
)

// messageNewer is the minimum surface of *anthropic.MessageService that the
// instrumented wrapper invokes. Production code passes the real service;
// tests pass a stub.
type messageNewer interface {
	New(ctx context.Context, body anthropic.MessageNewParams, opts ...option.RequestOption) (*anthropic.Message, error)
}

// InstrumentedAnthropicClient is a thin wrapper around *anthropic.Client
// that emits a TruLayer span on every Messages.New call. The zero value
// is not usable — construct via InstrumentAnthropic.
type InstrumentedAnthropicClient struct {
	// Messages is the instrumented Messages service. It mirrors the
	// upstream client.Messages field so call sites only change
	// construction, not invocation.
	Messages *InstrumentedMessageService
}

// InstrumentedMessageService wraps *anthropic.MessageService.
type InstrumentedMessageService struct {
	inner messageNewer
	tl    *trulayer.Client
}

// InstrumentAnthropic returns a wrapper around client whose Messages.New
// records a TruLayer span on every call. The span is attached to whichever
// trace is carried in the request context (via trulayer.TraceFromContext).
// If no trace is active the wrapper delegates without recording anything.
//
// The argument is *anthropic.Client because the upstream constructor
// returns the client by value but downstream code typically holds a
// pointer. Passing a pointer avoids copying the client's internal state.
func InstrumentAnthropic(client *anthropic.Client, tl *trulayer.Client) *InstrumentedAnthropicClient {
	return &InstrumentedAnthropicClient{
		Messages: &InstrumentedMessageService{inner: &client.Messages, tl: tl},
	}
}

// instrumentMessageService is a test-only constructor that accepts any
// value satisfying messageNewer. Production code should use
// InstrumentAnthropic.
func instrumentMessageService(svc messageNewer, tl *trulayer.Client) *InstrumentedAnthropicClient {
	return &InstrumentedAnthropicClient{
		Messages: &InstrumentedMessageService{inner: svc, tl: tl},
	}
}

// New mirrors the upstream signature. It opens a TruLayer span (when a
// trace is active in ctx), invokes the wrapped service, and records the
// result on the span before returning.
func (s *InstrumentedMessageService) New(
	ctx context.Context,
	body anthropic.MessageNewParams,
	opts ...option.RequestOption,
) (*anthropic.Message, error) {
	trace := trulayer.TraceFromContext(ctx)
	if trace == nil {
		return s.inner.New(ctx, body, opts...)
	}

	span, spanCtx := trace.NewSpan(ctx, "anthropic.messages", trulayer.SpanTypeLLM)
	span.SetModel(string(body.Model))
	if input := lastUserMessage(body.Messages); input != "" {
		span.SetInput(input)
	}

	defer func() {
		if r := recover(); r != nil {
			log.Printf("trulayer/instruments/anthropic: recovered from panic in Messages.New: %v", r)
			span.SetError("panic in instrumented Messages.New")
			span.End(spanCtx)
			panic(r)
		}
		span.End(spanCtx)
	}()

	resp, err := s.inner.New(spanCtx, body, opts...)
	if err != nil {
		span.SetError(err.Error())
		return resp, err
	}
	if resp != nil {
		span.SetOutput(extractText(resp.Content))
		span.SetTokens(int(resp.Usage.InputTokens), int(resp.Usage.OutputTokens))
	}
	return resp, nil
}

// lastUserMessage joins the text content of the last user-role MessageParam.
func lastUserMessage(msgs []anthropic.MessageParam) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != anthropic.MessageParamRoleUser {
			continue
		}
		var parts []string
		for _, c := range msgs[i].Content {
			if c.OfText != nil {
				parts = append(parts, c.OfText.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// extractText concatenates the text content of an assistant response.
func extractText(blocks []anthropic.ContentBlockUnion) string {
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

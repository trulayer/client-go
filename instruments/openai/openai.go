// Package openai provides TruLayer auto-instrumentation for the
// github.com/openai/openai-go client.
//
// The wrapper opens a TruLayer span around each chat-completion call and
// records model, input, output, token counts, latency, and errors on it.
// If the caller's context does not carry an active TruLayer trace, the
// wrapper transparently delegates to the underlying client.
//
//	tl := trulayer.NewClient(os.Getenv("TRULAYER_API_KEY"))
//	oai := openai.NewClient()
//	instrumented := tlopenai.InstrumentOpenAI(&oai, tl)
//
//	trace, ctx := tl.NewTrace(ctx, "answer-question")
//	defer trace.End(ctx)
//	resp, err := instrumented.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{...})
package openai

import (
	"context"
	"log"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/trulayer/client-go/trulayer"
)

// completionsNewFunc is the function signature for the underlying
// chat-completion call. Tests inject a stub; production code uses the real
// openai.ChatCompletionService.New method.
type completionsNewFunc func(ctx context.Context, body openai.ChatCompletionNewParams, opts ...option.RequestOption) (*openai.ChatCompletion, error)

// InstrumentedCompletionService mirrors the shape of openai.ChatCompletionService
// for the New method. It wraps calls in a TruLayer span.
type InstrumentedCompletionService struct {
	new completionsNewFunc
	tl  *trulayer.Client
}

// New mirrors (*openai.ChatCompletionService).New. It opens a TruLayer span
// (when a trace is active in ctx), invokes the underlying client, and records
// the result on the span before returning.
//
// The wrapper never converts panics into returned errors and never drops the
// upstream error — it forwards both verbatim.
func (s *InstrumentedCompletionService) New(
	ctx context.Context,
	body openai.ChatCompletionNewParams,
	opts ...option.RequestOption,
) (*openai.ChatCompletion, error) {
	trace := trulayer.TraceFromContext(ctx)
	if trace == nil {
		return s.new(ctx, body, opts...)
	}

	span, spanCtx := trace.NewSpan(ctx, "openai.chat.completions", trulayer.SpanTypeLLM)
	span.SetModel(string(body.Model))
	if input := lastUserMessage(body.Messages); input != "" {
		span.SetInput(input)
	}

	defer func() {
		// SDK code must never panic into user code. Recover, log, and end
		// the span so the trace still flushes cleanly.
		if r := recover(); r != nil {
			log.Printf("trulayer/instruments/openai: recovered from panic in Chat.Completions.New: %v", r)
			span.SetError("panic in instrumented Chat.Completions.New")
			span.End(spanCtx)
			panic(r)
		}
		span.End(spanCtx)
	}()

	resp, err := s.new(spanCtx, body, opts...)
	if err != nil {
		span.SetError(err.Error())
		return resp, err
	}
	if len(resp.Choices) > 0 {
		span.SetOutput(resp.Choices[0].Message.Content)
	}
	span.SetTokens(int(resp.Usage.PromptTokens), int(resp.Usage.CompletionTokens))
	return resp, nil
}

// InstrumentedChatService mirrors the shape of openai.ChatService for the
// Completions field.
type InstrumentedChatService struct {
	// Completions provides the instrumented chat-completion surface. Its New
	// method has the same signature as (*openai.ChatCompletionService).New.
	Completions *InstrumentedCompletionService
}

// InstrumentedOpenAIClient wraps an openai.Client and emits a TruLayer span
// for each Chat.Completions.New call. The zero value is not usable — construct
// via InstrumentOpenAI.
type InstrumentedOpenAIClient struct {
	// Chat provides the instrumented chat service. Its shape mirrors
	// openai.ChatService so callers can use it as a drop-in replacement for
	// the field on the original client.
	Chat InstrumentedChatService
}

// InstrumentOpenAI returns a wrapper around client that records a TruLayer
// span on every Chat.Completions.New call. The span is attached to whichever
// trace is carried in the request context (via trulayer.TraceFromContext).
// If no trace is active, the wrapper delegates without recording anything.
func InstrumentOpenAI(client *openai.Client, tl *trulayer.Client) *InstrumentedOpenAIClient {
	return newInstrumentedClient(client.Chat.Completions.New, tl)
}

// newInstrumentedClient constructs an InstrumentedOpenAIClient using the
// provided completion function. Tests inject a stub via this constructor.
func newInstrumentedClient(fn completionsNewFunc, tl *trulayer.Client) *InstrumentedOpenAIClient {
	return &InstrumentedOpenAIClient{
		Chat: InstrumentedChatService{
			Completions: &InstrumentedCompletionService{
				new: fn,
				tl:  tl,
			},
		},
	}
}

// lastUserMessage returns the string content of the last message with role
// "user" from the params slice, or the empty string if none is present.
// Only plain-string content (OfString) is extracted; multipart content parts
// are skipped.
func lastUserMessage(msgs []openai.ChatCompletionMessageParamUnion) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if u := msgs[i].OfUser; u != nil {
			if o := u.Content.OfString; o.Valid() {
				return o.Value
			}
		}
	}
	return ""
}

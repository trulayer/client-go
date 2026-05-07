package anthropic

import (
	"context"
	"errors"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trulayer/client-go/trulayer"
)

// stubMessageService is a messageNewer for unit tests — no network.
type stubMessageService struct {
	resp   *anthropic.Message
	err    error
	gotCtx context.Context
	gotReq anthropic.MessageNewParams
	calls  int
}

func (s *stubMessageService) New(ctx context.Context, body anthropic.MessageNewParams, _ ...option.RequestOption) (*anthropic.Message, error) {
	s.gotCtx = ctx
	s.gotReq = body
	s.calls++
	return s.resp, s.err
}

func newDryRunClient(t *testing.T) *trulayer.Client {
	t.Helper()
	t.Setenv("TRULAYER_DRY_RUN", "true")
	c := trulayer.NewClient("")
	t.Cleanup(func() {
		_ = c.Shutdown(context.Background())
	})
	return c
}

func readSpans(t *testing.T, trace *trulayer.Trace) []trulayer.SpanData {
	t.Helper()
	return trace.Spans()
}

func TestInstrumentAnthropic_RecordsSpan(t *testing.T) {
	tl := newDryRunClient(t)
	stub := &stubMessageService{
		resp: &anthropic.Message{
			Content: []anthropic.ContentBlockUnion{
				{Type: "text", Text: "hi there"},
			},
			Usage: anthropic.Usage{InputTokens: 12, OutputTokens: 5},
		},
	}
	wrapped := instrumentMessageService(stub, tl)

	trace, ctx := tl.NewTrace(context.Background(), "test-trace")
	resp, err := wrapped.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     "claude-sonnet-4",
		MaxTokens: 1024,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.ContentBlockParamUnion{
				OfText: &anthropic.TextBlockParam{Text: "hello"},
			}),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, 1, stub.calls)

	spans := readSpans(t, trace)
	require.Len(t, spans, 1)
	span := spans[0]
	assert.Equal(t, "anthropic.messages", span.Name)
	assert.Equal(t, trulayer.SpanTypeLLM, span.Type)
	assert.Equal(t, "claude-sonnet-4", span.Model)
	assert.Equal(t, "hello", span.Input)
	assert.Equal(t, "hi there", span.Output)
	assert.Equal(t, 12, span.PromptTokens)
	assert.Equal(t, 5, span.CompletionTokens)
	assert.Empty(t, span.Error)
	require.NotNil(t, span.EndTime)
}

func TestInstrumentAnthropic_NilTracePassthrough(t *testing.T) {
	tl := newDryRunClient(t)
	stub := &stubMessageService{
		resp: &anthropic.Message{
			Content: []anthropic.ContentBlockUnion{{Type: "text", Text: "ok"}},
		},
	}
	wrapped := instrumentMessageService(stub, tl)

	_, err := wrapped.Messages.New(context.Background(), anthropic.MessageNewParams{
		Model:     "claude-sonnet-4",
		MaxTokens: 1,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.ContentBlockParamUnion{OfText: &anthropic.TextBlockParam{Text: "hi"}}),
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, stub.calls)
}

func TestInstrumentAnthropic_RecordsError(t *testing.T) {
	tl := newDryRunClient(t)
	wantErr := errors.New("upstream boom")
	stub := &stubMessageService{err: wantErr}
	wrapped := instrumentMessageService(stub, tl)

	trace, ctx := tl.NewTrace(context.Background(), "test-trace")
	_, err := wrapped.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     "claude-sonnet-4",
		MaxTokens: 1,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.ContentBlockParamUnion{OfText: &anthropic.TextBlockParam{Text: "hi"}}),
		},
	})
	require.ErrorIs(t, err, wantErr)

	spans := readSpans(t, trace)
	require.Len(t, spans, 1)
	assert.Equal(t, "upstream boom", spans[0].Error)
	assert.Equal(t, "claude-sonnet-4", spans[0].Model)
}

func TestLastUserMessage(t *testing.T) {
	cases := []struct {
		name string
		in   []anthropic.MessageParam
		want string
	}{
		{"empty", nil, ""},
		{
			"single user text",
			[]anthropic.MessageParam{
				anthropic.NewUserMessage(anthropic.ContentBlockParamUnion{OfText: &anthropic.TextBlockParam{Text: "u"}}),
			},
			"u",
		},
		{
			"latest user wins",
			[]anthropic.MessageParam{
				anthropic.NewUserMessage(anthropic.ContentBlockParamUnion{OfText: &anthropic.TextBlockParam{Text: "first"}}),
				anthropic.NewAssistantMessage(anthropic.ContentBlockParamUnion{OfText: &anthropic.TextBlockParam{Text: "reply"}}),
				anthropic.NewUserMessage(anthropic.ContentBlockParamUnion{OfText: &anthropic.TextBlockParam{Text: "second"}}),
			},
			"second",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, lastUserMessage(tc.in))
		})
	}
}

func TestExtractText(t *testing.T) {
	got := extractText([]anthropic.ContentBlockUnion{
		{Type: "text", Text: "alpha"},
		{Type: "tool_use", Text: ""},
		{Type: "text", Text: "beta"},
	})
	assert.Equal(t, "alpha\nbeta", got)
}

// TestInstrumentAnthropic_ConstructorWithRealClient just verifies that the
// public InstrumentAnthropic constructor wires up cleanly against a real
// *anthropic.Client. We don't make any HTTP calls.
func TestInstrumentAnthropic_ConstructorWithRealClient(t *testing.T) {
	tl := newDryRunClient(t)
	c := anthropic.NewClient(option.WithAPIKey("test-key"))
	w := InstrumentAnthropic(&c, tl)
	require.NotNil(t, w)
	require.NotNil(t, w.Messages)
}

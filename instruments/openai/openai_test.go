package openai

import (
	"context"
	"errors"
	"testing"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trulayer/client-go/trulayer"
)

// stubCompletion is a completionsNewFunc provider for unit tests — no network.
type stubCompletion struct {
	resp    *openai.ChatCompletion
	err     error
	gotCtx  context.Context
	gotBody openai.ChatCompletionNewParams
	calls   int
}

func (s *stubCompletion) new(ctx context.Context, body openai.ChatCompletionNewParams, _ ...option.RequestOption) (*openai.ChatCompletion, error) {
	s.gotCtx = ctx
	s.gotBody = body
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

// readSpans returns the spans recorded on a trace via the exported Spans()
// accessor. End must have been called (or spans must have been closed) before
// calling this helper so that all spans are attached to the trace.
func readSpans(t *testing.T, trace *trulayer.Trace) []trulayer.SpanData {
	t.Helper()
	return trace.Spans()
}

func TestInstrumentOpenAI_RecordsSpan(t *testing.T) {
	tl := newDryRunClient(t)
	stub := &stubCompletion{
		resp: &openai.ChatCompletion{
			Model: "gpt-4o",
			Choices: []openai.ChatCompletionChoice{
				{Message: openai.ChatCompletionMessage{Content: "hi there"}},
			},
			Usage: openai.CompletionUsage{PromptTokens: 11, CompletionTokens: 4},
		},
	}
	wrapped := newInstrumentedClient(stub.new, tl)

	trace, ctx := tl.NewTrace(context.Background(), "test-trace")
	resp, err := wrapped.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: openai.ChatModelGPT4o,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage("you are helpful"),
			openai.UserMessage("hello"),
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "hi there", resp.Choices[0].Message.Content)
	assert.Equal(t, 1, stub.calls)

	spans := readSpans(t, trace)
	require.Len(t, spans, 1)
	s := spans[0]
	assert.Equal(t, "openai.chat.completions", s.Name)
	assert.Equal(t, trulayer.SpanTypeLLM, s.Type)
	assert.Equal(t, "gpt-4o", s.Model)
	assert.Equal(t, "hello", s.Input)
	assert.Equal(t, "hi there", s.Output)
	assert.Equal(t, 11, s.PromptTokens)
	assert.Equal(t, 4, s.CompletionTokens)
	assert.Empty(t, s.Error)
	require.NotNil(t, s.EndTime)
}

func TestInstrumentOpenAI_NilTracePassthrough(t *testing.T) {
	tl := newDryRunClient(t)
	stub := &stubCompletion{
		resp: &openai.ChatCompletion{
			Choices: []openai.ChatCompletionChoice{
				{Message: openai.ChatCompletionMessage{Content: "ok"}},
			},
		},
	}
	wrapped := newInstrumentedClient(stub.new, tl)

	// no trace in context — wrapper must pass through without recording
	_, err := wrapped.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model:    openai.ChatModelGPT4o,
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, stub.calls)
}

func TestInstrumentOpenAI_RecordsError(t *testing.T) {
	tl := newDryRunClient(t)
	wantErr := errors.New("upstream boom")
	stub := &stubCompletion{err: wantErr}
	wrapped := newInstrumentedClient(stub.new, tl)

	trace, ctx := tl.NewTrace(context.Background(), "test-trace")
	_, err := wrapped.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:    openai.ChatModelGPT4o,
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
	})
	require.ErrorIs(t, err, wantErr)

	spans := readSpans(t, trace)
	require.Len(t, spans, 1)
	assert.Equal(t, "upstream boom", spans[0].Error)
	assert.Equal(t, "gpt-4o", spans[0].Model)
}

func TestLastUserMessage(t *testing.T) {
	cases := []struct {
		name string
		in   []openai.ChatCompletionMessageParamUnion
		want string
	}{
		{"empty", nil, ""},
		{"only system", []openai.ChatCompletionMessageParamUnion{openai.SystemMessage("s")}, ""},
		{"single user", []openai.ChatCompletionMessageParamUnion{openai.UserMessage("u")}, "u"},
		{"latest wins", []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("first"),
			openai.AssistantMessage("reply"),
			openai.UserMessage("second"),
		}, "second"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, lastUserMessage(tc.in))
		})
	}
}

// TestLastUserMessage_MultipartSkipped verifies that non-string user content
// does not panic and returns the empty string.
func TestLastUserMessage_MultipartSkipped(t *testing.T) {
	var u openai.ChatCompletionUserMessageParam
	u.Content = openai.ChatCompletionUserMessageParamContentUnion{
		OfString: param.Null[string](),
	}
	msg := openai.ChatCompletionMessageParamUnion{OfUser: &u}
	assert.Equal(t, "", lastUserMessage([]openai.ChatCompletionMessageParamUnion{msg}))
}

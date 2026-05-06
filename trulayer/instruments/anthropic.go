package instruments

// AnthropicInstrument is the future home for Anthropic Go SDK
// auto-instrumentation. A user constructs it with a TruLayer client and
// uses it to wrap an Anthropic client's message calls so each call is
// recorded as a span on the active trace.
//
// TODO: implement once the Anthropic Go SDK API stabilises. Track in the
// Linear backlog.
type AnthropicInstrument struct{}

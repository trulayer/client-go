// Package instruments provides optional auto-instrumentation hooks for
// popular Go AI provider SDKs.
//
// The hooks live in subpackages so users only depend on the providers
// they use. Each instrument is a thin wrapper that opens a TruLayer span
// around the provider's completion call and records the request and
// response on the span.
package instruments

// OpenAIInstrument is the future home for OpenAI Go client
// auto-instrumentation. A user constructs it with a TruLayer client and
// uses it to wrap an OpenAI client's chat-completion calls so each call
// is recorded as a span on the active trace.
//
// TODO: implement once the OpenAI Go SDK API stabilises. Track in the
// Linear backlog.
type OpenAIInstrument struct{}

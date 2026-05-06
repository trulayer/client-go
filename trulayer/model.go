package trulayer

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// SpanType identifies the kind of work a span represents. Maps onto the
// `type` enum in the TruLayer ingestion schema.
type SpanType string

// Supported span types. Unknown values are coerced to SpanTypeOther by the
// server.
const (
	SpanTypeLLM       SpanType = "llm"
	SpanTypeTool      SpanType = "tool"
	SpanTypeRetrieval SpanType = "retrieval"
	SpanTypeOther     SpanType = "other"
)

// SpanData is the wire-shape representation of a single span in a trace.
//
// JSON keys match the TruLayer ingestion schema: `type`, `start_time`,
// `end_time`. Optional values are emitted as `omitempty` so the server can
// apply defaults.
type SpanData struct {
	ID               string                 `json:"id"`
	ParentSpanID     string                 `json:"parent_span_id,omitempty"`
	Name             string                 `json:"name"`
	Type             SpanType               `json:"type"`
	Input            string                 `json:"input,omitempty"`
	Output           string                 `json:"output,omitempty"`
	Model            string                 `json:"model,omitempty"`
	LatencyMs        int64                  `json:"latency_ms,omitempty"`
	Cost             float64                `json:"cost,omitempty"`
	Error            string                 `json:"error,omitempty"`
	PromptTokens     int                    `json:"prompt_tokens,omitempty"`
	CompletionTokens int                    `json:"completion_tokens,omitempty"`
	Metadata         map[string]interface{} `json:"metadata,omitempty"`
	StartTime        time.Time              `json:"start_time"`
	EndTime          *time.Time             `json:"end_time,omitempty"`
}

// TraceData is the wire-shape representation of a trace. The `spans` array
// carries each span in submission order.
type TraceData struct {
	ID         string                 `json:"id"`
	ExternalID string                 `json:"external_id,omitempty"`
	Name       string                 `json:"name,omitempty"`
	Input      string                 `json:"input,omitempty"`
	Output     string                 `json:"output,omitempty"`
	Model      string                 `json:"model,omitempty"`
	LatencyMs  int64                  `json:"latency_ms,omitempty"`
	Cost       float64                `json:"cost,omitempty"`
	Error      string                 `json:"error,omitempty"`
	Tags       map[string]string      `json:"tags,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
	Spans      []SpanData             `json:"spans"`
}

// FeedbackData is the wire-shape representation of a feedback submission.
type FeedbackData struct {
	TraceID  string                 `json:"trace_id"`
	Label    string                 `json:"label"`
	Score    *float64               `json:"score,omitempty"`
	Comment  string                 `json:"comment,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// batchRequest is the body of POST /v1/ingest/batch.
type batchRequest struct {
	Traces []TraceData `json:"traces"`
}

// newID returns a UUIDv7 string. UUIDv7 layout (RFC 9562):
//
//	60-bit unix-millisecond timestamp + 4-bit version + 12 bits of random + 2-bit variant + 62 bits of random.
func newID() string {
	var b [16]byte
	// 48-bit unix-millisecond timestamp in the first 6 bytes.
	ms := time.Now().UnixMilli()
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	// Fill the remaining 10 bytes with random data.
	if _, err := rand.Read(b[6:]); err != nil {
		// crypto/rand failure is unrecoverable; fall back to a zeroed
		// random suffix rather than panicking inside an SDK call path.
		for i := 6; i < 16; i++ {
			b[i] = 0
		}
	}
	// Set version (7) in the high nibble of byte 6.
	b[6] = (b[6] & 0x0f) | 0x70
	// Set the RFC 4122 variant bits in the high two bits of byte 8.
	b[8] = (b[8] & 0x3f) | 0x80

	out := make([]byte, 36)
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])
	return string(out)
}

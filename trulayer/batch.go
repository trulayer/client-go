package trulayer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

const (
	maxRetries      = 3
	retryBaseDelay  = 500 * time.Millisecond
	requestTimeout  = 10 * time.Second
	feedbackTimeout = 5 * time.Second
)

// batchSender buffers trace payloads and flushes them on a timer or when
// the buffer reaches the configured batch size. The implementation is
// channel-based so that producer goroutines (user code calling Trace.End)
// never block on the network.
type batchSender struct {
	apiKey  string
	baseURL string
	httpc   *http.Client

	batchSize     int
	flushInterval time.Duration

	dryRun bool

	in     chan TraceData
	flush  chan chan struct{}
	stop   chan chan struct{}
	once   sync.Once
	closed chan struct{}
}

func newBatchSender(apiKey, baseURL string, batchSize int, flushInterval time.Duration, httpc *http.Client, dryRun bool) *batchSender {
	bs := &batchSender{
		apiKey:        apiKey,
		baseURL:       baseURL,
		httpc:         httpc,
		batchSize:     batchSize,
		flushInterval: flushInterval,
		dryRun:        dryRun,
		in:            make(chan TraceData, 1024),
		flush:         make(chan chan struct{}),
		stop:          make(chan chan struct{}),
		closed:        make(chan struct{}),
	}
	go bs.run()
	return bs
}

func (b *batchSender) enqueue(t TraceData) {
	select {
	case <-b.closed:
		return
	default:
	}
	select {
	case b.in <- t:
	case <-b.closed:
	}
}

// flushNow blocks until the current buffer (and anything already enqueued)
// has been sent or attempted. ctx may cancel the wait.
func (b *batchSender) flushNow(ctx context.Context) error {
	done := make(chan struct{})
	select {
	case b.flush <- done:
	case <-b.closed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// shutdown drains the buffer, performs one final flush attempt, and stops
// the background goroutine.
func (b *batchSender) shutdown(ctx context.Context) error {
	var err error
	b.once.Do(func() {
		done := make(chan struct{})
		select {
		case b.stop <- done:
		case <-ctx.Done():
			err = ctx.Err()
			return
		}
		select {
		case <-done:
		case <-ctx.Done():
			err = ctx.Err()
		}
	})
	return err
}

func (b *batchSender) run() {
	ticker := time.NewTicker(b.flushInterval)
	defer ticker.Stop()
	buf := make([]TraceData, 0, b.batchSize)

	flushBuf := func() {
		if len(buf) == 0 {
			return
		}
		items := make([]TraceData, len(buf))
		copy(items, buf)
		buf = buf[:0]
		b.send(items)
	}

	for {
		select {
		case t := <-b.in:
			buf = append(buf, t)
			if len(buf) >= b.batchSize {
				flushBuf()
			}
		case <-ticker.C:
			flushBuf()
		case done := <-b.flush:
			// Drain any items still in the channel before flushing
			// so a synchronous Flush() picks up everything that was
			// enqueued before the call.
		drain:
			for {
				select {
				case t := <-b.in:
					buf = append(buf, t)
				default:
					break drain
				}
			}
			flushBuf()
			close(done)
		case done := <-b.stop:
		drainStop:
			for {
				select {
				case t := <-b.in:
					buf = append(buf, t)
				default:
					break drainStop
				}
			}
			flushBuf()
			close(b.closed)
			close(done)
			return
		}
	}
}

func (b *batchSender) send(items []TraceData) {
	if b.dryRun {
		return
	}
	body, err := json.Marshal(batchRequest{Traces: items})
	if err != nil {
		log.Printf("trulayer: marshal batch failed: %v", err)
		return
	}

	url := b.baseURL + "/v1/ingest/batch"
	for attempt := 0; attempt < maxRetries; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			cancel()
			log.Printf("trulayer: build request failed: %v", err)
			return
		}
		req.Header.Set("Authorization", "Bearer "+b.apiKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := b.httpc.Do(req)
		if err == nil {
			status := resp.StatusCode
			_ = resp.Body.Close()
			cancel()
			if status >= 200 && status < 300 {
				return
			}
			// 4xx (other than 429) is non-retryable — the request will
			// not become valid on a retry.
			if status >= 400 && status < 500 && status != http.StatusTooManyRequests {
				log.Printf("trulayer: batch rejected with status %d (dropping %d items)", status, len(items))
				return
			}
			err = fmt.Errorf("status %d", status)
		} else {
			cancel()
		}

		if attempt == maxRetries-1 {
			log.Printf("trulayer: failed to send batch of %d items after %d retries: %v", len(items), maxRetries, err)
			return
		}
		time.Sleep(retryBaseDelay * (1 << attempt))
	}
}

// sendFeedback posts a single feedback payload to /v1/feedback. Synchronous —
// returns the request error to the caller.
func (b *batchSender) sendFeedback(ctx context.Context, f FeedbackData) error {
	if b.dryRun {
		return nil
	}
	body, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("trulayer: marshal feedback: %w", err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, feedbackTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, b.baseURL+"/v1/feedback", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("trulayer: build feedback request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+b.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("trulayer: feedback request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("trulayer: feedback rejected with status %d", resp.StatusCode)
	}
	return nil
}

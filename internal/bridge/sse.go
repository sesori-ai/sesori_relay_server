package bridge

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/remote-relay/internal/protocol"
)

// SSEBridge manages an SSE subscription to the local Claude Code server
// and forwards parsed events as plaintext SSEEventMessage objects via sendFn.
// Encryption is handled at the RelayClient layer — this struct works with plaintext only.
type SSEBridge struct {
	targetURL string
	password  *string
	client    *http.Client
	cancel    context.CancelFunc
	mu        sync.Mutex
	active    bool
}

// NewSSEBridge creates a new SSEBridge targeting the given URL.
// The HTTP client has no timeout because SSE connections are long-lived.
func NewSSEBridge(targetURL string, password *string) *SSEBridge {
	return &SSEBridge{
		targetURL: targetURL,
		password:  password,
		client:    &http.Client{},
	}
}

// Subscribe cancels any existing subscription, then opens a new SSE stream to
// targetURL+path. For each complete SSE event received, sendFn is called with a
// plaintext SSEEventMessage. If the upstream connection drops while still active,
// the bridge reconnects after a 1-second delay.
//
// Returns an error only if the initial connection fails or the server does not
// respond with Content-Type: text/event-stream.
func (b *SSEBridge) Subscribe(path string, sendFn func(protocol.SSEEventMessage)) error {
	b.mu.Lock()
	if b.cancel != nil {
		b.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	b.active = true
	b.mu.Unlock()

	req, err := b.buildRequest(ctx, path)
	if err != nil {
		cancel()
		b.mu.Lock()
		b.active = false
		b.mu.Unlock()
		return fmt.Errorf("SSEBridge: failed to build request: %w", err)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		cancel()
		b.mu.Lock()
		b.active = false
		b.mu.Unlock()
		return fmt.Errorf("SSEBridge: failed to connect to SSE endpoint: %w", err)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		resp.Body.Close()
		cancel()
		b.mu.Lock()
		b.active = false
		b.mu.Unlock()
		return fmt.Errorf("SSEBridge: unexpected Content-Type %q, want text/event-stream", ct)
	}

	go b.streamLoop(ctx, path, resp, sendFn)
	return nil
}

// Unsubscribe cancels the active SSE subscription, immediately closing the upstream
// HTTP connection and stopping the reader goroutine.
// Safe to call when no subscription is active.
func (b *SSEBridge) Unsubscribe() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cancel != nil {
		b.cancel()
	}
	b.active = false
	log.Println("SSEBridge: unsubscribed")
}

// IsActive reports whether there is a live SSE subscription.
func (b *SSEBridge) IsActive() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.active
}

// buildRequest constructs a context-aware HTTP GET request with the required SSE
// headers and optional Basic auth.
func (b *SSEBridge) buildRequest(ctx context.Context, path string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.targetURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	if b.password != nil {
		creds := base64.StdEncoding.EncodeToString([]byte("opencode:" + *b.password))
		req.Header.Set("Authorization", "Basic "+creds)
	}
	return req, nil
}

// streamLoop drains the SSE response body and, when the connection drops while
// the context is still alive, waits 1 second and reconnects.
// It exits only when ctx is cancelled (i.e. Unsubscribe was called).
func (b *SSEBridge) streamLoop(ctx context.Context, path string, initialResp *http.Response, sendFn func(protocol.SSEEventMessage)) {
	resp := initialResp
	for {
		b.readStream(ctx, resp, sendFn)
		resp.Body.Close()

		select {
		case <-ctx.Done():
			return
		default:
		}

		// Connection dropped unexpectedly — reconnect after a short delay.
		log.Println("SSEBridge: connection lost, reconnecting in 1s")
		select {
		case <-ctx.Done():
			return
		case <-time.After(1 * time.Second):
		}

		req, err := b.buildRequest(ctx, path)
		if err != nil {
			log.Printf("SSEBridge: failed to build reconnect request: %v", err)
			continue
		}

		newResp, err := b.client.Do(req)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			log.Printf("SSEBridge: reconnect failed: %v", err)
			continue
		}
		resp = newResp
	}
}

// readStream reads SSE frames line-by-line from resp until EOF, a read error, or
// the context is cancelled. For each complete event (terminated by an empty line),
// it calls sendFn with an SSEEventMessage whose Data is the concatenated data fields.
//
// SSE format (per https://html.spec.whatwg.org/multipage/server-sent-events.html):
//   - Lines starting with "data:" carry payload data.
//   - Lines starting with "id:", "event:", or ":" (comments) are ignored.
//   - An empty line dispatches the accumulated event.
func (b *SSEBridge) readStream(ctx context.Context, resp *http.Response, sendFn func(protocol.SSEEventMessage)) {
	scanner := bufio.NewScanner(resp.Body)
	var dataLines []string

	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			if len(dataLines) > 0 {
				sendFn(protocol.SSEEventMessage{
					Type: "sse_event",
					Data: strings.Join(dataLines, "\n"),
				})
				dataLines = dataLines[:0]
			}
			continue
		}

		if strings.HasPrefix(line, "data:") {
			val := strings.TrimPrefix(line, "data:")
			// Strip the single leading space mandated by the SSE spec.
			val = strings.TrimPrefix(val, " ")
			dataLines = append(dataLines, val)
			continue
		}

		// id:, event:, and comment lines are intentionally ignored.
	}

	if err := scanner.Err(); err != nil {
		select {
		case <-ctx.Done():
			// Expected — context was cancelled by Unsubscribe.
		default:
			log.Printf("SSEBridge: stream read error: %v", err)
		}
	}
}

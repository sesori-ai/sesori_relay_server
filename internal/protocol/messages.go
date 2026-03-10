package protocol

import (
	"encoding/json"
	"fmt"
)

// Envelope wraps all messages with a type discriminator
type Envelope struct {
	Type string `json:"type"`
}

// KeyExchangeMessage - phone sends this plaintext to bridge
type KeyExchangeMessage struct {
	Type      string `json:"type"`      // "key_exchange"
	PublicKey string `json:"publicKey"` // base64url-encoded X25519 public key
}

// AuthMessage - client sends this plaintext as first message after WebSocket connect
type AuthMessage struct {
	Token string `json:"token"`
}

// ReadyMessage - bridge sends this encrypted to phone as proof of correct key
type ReadyMessage struct {
	Type string `json:"type"` // "ready"
}

// RequestMessage - phone sends HTTP request to bridge
type RequestMessage struct {
	ID      string            `json:"id"`     // UUID
	Type    string            `json:"type"`   // "request"
	Method  string            `json:"method"` // GET, POST, etc.
	Path    string            `json:"path"`   // /global/health
	Headers map[string]string `json:"headers"`
	Body    *string           `json:"body"` // nullable
}

// ResponseMessage - bridge sends HTTP response to phone
type ResponseMessage struct {
	ID      string            `json:"id"`
	Type    string            `json:"type"` // "response"
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    *string           `json:"body"` // nullable
}

// SSESubscribeMessage - phone asks bridge to start SSE stream
type SSESubscribeMessage struct {
	Type string `json:"type"` // "sse_subscribe"
	Path string `json:"path"` // /global/event
}

// SSEUnsubscribeMessage - phone asks bridge to stop SSE stream
type SSEUnsubscribeMessage struct {
	Type string `json:"type"` // "sse_unsubscribe"
}

// SSEEventMessage - bridge forwards SSE event to phone
type SSEEventMessage struct {
	Type string `json:"type"` // "sse_event"
	Data string `json:"data"`
}

// ParseMessage unmarshals the Envelope first, then switches on Type to unmarshal into the correct struct
func ParseMessage(data []byte) (interface{}, error) {
	var envelope Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("failed to unmarshal envelope: %w", err)
	}

	switch envelope.Type {
	case "auth":
		var msg AuthMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal auth message: %w", err)
		}
		return msg, nil

	case "key_exchange":
		var msg KeyExchangeMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal key_exchange message: %w", err)
		}
		return msg, nil

	case "ready":
		var msg ReadyMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal ready message: %w", err)
		}
		return msg, nil

	case "request":
		var msg RequestMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal request message: %w", err)
		}
		return msg, nil

	case "response":
		var msg ResponseMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal response message: %w", err)
		}
		return msg, nil

	case "sse_subscribe":
		var msg SSESubscribeMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal sse_subscribe message: %w", err)
		}
		return msg, nil

	case "sse_unsubscribe":
		var msg SSEUnsubscribeMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal sse_unsubscribe message: %w", err)
		}
		return msg, nil

	case "sse_event":
		var msg SSEEventMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal sse_event message: %w", err)
		}
		return msg, nil

	default:
		return nil, fmt.Errorf("unknown message type: %s", envelope.Type)
	}
}

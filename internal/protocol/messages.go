package protocol

import (
	"encoding/json"
	"fmt"
)

// Message type constants for the wire protocol.
const (
	TypeAuth           = "auth"
	TypeKeyExchange    = "key_exchange"
	TypeReady          = "ready"
	TypeRequest        = "request"
	TypeResponse       = "response"
	TypeSSESubscribe   = "sse_subscribe"
	TypeSSEUnsubscribe = "sse_unsubscribe"
	TypeSSEEvent       = "sse_event"

	// Control messages sent by relay
	TypePhoneConnected     = "phone_connected"
	TypePhoneDisconnected  = "phone_disconnected"
	TypeBridgeConnected    = "bridge_connected"
	TypeBridgeDisconnected = "bridge_disconnected"

	// Connection roles
	RoleBridge = "bridge"
	RolePhone  = "phone"
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

// RoleAuthMessage - client sends this plaintext as first message after WebSocket connect
type RoleAuthMessage struct {
	Type     string `json:"type"` // "auth"
	Token    string `json:"token"`
	Role     string `json:"role"` // "bridge" or "phone"
	BridgeID string `json:"bridgeId,omitempty"` // optional; bridge role only. Format: ^br_[A-Za-z0-9_-]{8,32}$. Required when the relay runs with --require-bridge-id.
}

// PhoneConnectedMessage is sent by relay to bridge when a phone joins.
type PhoneConnectedMessage struct {
	Type   string `json:"type"` // "phone_connected"
	ConnID uint16 `json:"connId"`
}

// PhoneDisconnectedMessage is sent by relay to bridge when a phone leaves.
type PhoneDisconnectedMessage struct {
	Type   string `json:"type"` // "phone_disconnected"
	ConnID uint16 `json:"connId"`
}

// BridgeConnectedMessage is sent by relay to phones when bridge is online.
type BridgeConnectedMessage struct {
	Type string `json:"type"` // "bridge_connected"
}

// BridgeDisconnectedMessage is sent by relay to phones when bridge is offline.
type BridgeDisconnectedMessage struct {
	Type string `json:"type"` // "bridge_disconnected"
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
	case TypeAuth:
		var msg RoleAuthMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal auth message: %w", err)
		}
		return msg, nil

	case TypeKeyExchange:
		var msg KeyExchangeMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal key_exchange message: %w", err)
		}
		return msg, nil

	case TypeReady:
		var msg ReadyMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal ready message: %w", err)
		}
		return msg, nil

	case TypeRequest:
		var msg RequestMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal request message: %w", err)
		}
		return msg, nil

	case TypeResponse:
		var msg ResponseMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal response message: %w", err)
		}
		return msg, nil

	case TypeSSESubscribe:
		var msg SSESubscribeMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal sse_subscribe message: %w", err)
		}
		return msg, nil

	case TypeSSEUnsubscribe:
		var msg SSEUnsubscribeMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal sse_unsubscribe message: %w", err)
		}
		return msg, nil

	case TypeSSEEvent:
		var msg SSEEventMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal sse_event message: %w", err)
		}
		return msg, nil

	default:
		return nil, fmt.Errorf("unknown message type: %s", envelope.Type)
	}
}

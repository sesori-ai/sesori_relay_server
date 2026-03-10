package relay

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Room represents a relay room with two connection slots: bridge and phone.
type Room struct {
	Code         string
	Bridge       *websocket.Conn
	Phone        *websocket.Conn
	CreatedAt    time.Time
	LastActivity time.Time
	mu           sync.Mutex
}

// Forward sends a binary message to the peer of the sender.
// If sender is Bridge, message is forwarded to Phone and vice versa.
// Returns error if peer is nil.
func (r *Room) Forward(sender *websocket.Conn, msg []byte) error {
	return r.ForwardWithType(sender, websocket.MessageBinary, msg)
}

// ForwardWithType sends a message with the given WebSocket message type to the
// peer of the sender. Used to forward both text (key exchange) and binary
// (encrypted data) messages.
func (r *Room) ForwardWithType(sender *websocket.Conn, msgType websocket.MessageType, msg []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	peer := r.peer(sender)
	if peer == nil {
		return errors.New("peer is not connected")
	}

	if err := peer.Write(context.Background(), msgType, msg); err != nil {
		return fmt.Errorf("failed to forward message: %w", err)
	}

	r.LastActivity = time.Now()
	return nil
}

func (r *Room) IsFull() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Bridge != nil && r.Phone != nil
}

// AddConnection assigns the connection to the next available slot.
// First connection becomes "bridge", second becomes "phone".
// Returns error if both slots are already occupied.
func (r *Room) AddConnection(conn *websocket.Conn) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Bridge == nil {
		r.Bridge = conn
		r.LastActivity = time.Now()
		return "bridge", nil
	}

	if r.Phone == nil {
		r.Phone = conn
		r.LastActivity = time.Now()
		return "phone", nil
	}

	return "", errors.New("room is full")
}

func (r *Room) RemoveConnection(conn *websocket.Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Bridge == conn {
		r.Bridge = nil
	} else if r.Phone == conn {
		r.Phone = nil
	}
}

func (r *Room) Peer(conn *websocket.Conn) *websocket.Conn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.peer(conn)
}

// peer is the internal unlocked version of Peer.
func (r *Room) peer(conn *websocket.Conn) *websocket.Conn {
	if conn == r.Bridge {
		return r.Phone
	}
	if conn == r.Phone {
		return r.Bridge
	}
	return nil
}

func (r *Room) IsEmpty() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Bridge == nil && r.Phone == nil
}

func (r *Room) Touch() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.LastActivity = time.Now()
}

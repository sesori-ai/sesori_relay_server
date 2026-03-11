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
	OwnerID      string
	CreatedAt    time.Time
	LastActivity time.Time
	mu           sync.Mutex
}

var (
	ErrRoomFull          = errors.New("room is full")
	ErrOwnershipMismatch = errors.New("room ownership mismatch")
)

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
// First connection becomes "bridge" and sets room ownership; second becomes "phone".
// All connections must belong to the room owner when auth is enabled.
// Pass empty userId when auth is disabled; ownership is not enforced.
func (r *Room) AddConnection(conn *websocket.Conn, userId string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.canJoin(userId) {
		return "", ErrOwnershipMismatch
	}

	if r.Bridge == nil {
		r.Bridge = conn
		r.OwnerID = userId
		r.LastActivity = time.Now()
		return "bridge", nil
	}

	if r.Phone == nil {
		r.Phone = conn
		r.LastActivity = time.Now()
		return "phone", nil
	}

	return "", ErrRoomFull
}

// canJoin reports whether userId is allowed to join this room.
// Returns true if the room has no owner yet (first connection or auth disabled),
// or the userId matches the existing owner.
func (r *Room) canJoin(userId string) bool {
	if r.OwnerID == "" {
		return true
	}
	return userId == r.OwnerID
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

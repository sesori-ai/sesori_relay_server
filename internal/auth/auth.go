package auth

import (
	"context"
	"time"

	"github.com/coder/websocket"
)

// AuthResult holds the authenticated identity and token metadata.
type AuthResult struct {
	UserID string
	Expiry time.Time
}

// Authenticator authenticates a WebSocket connection. Implementations read
// the first message, validate credentials, and return the authenticated
// identity. On failure the connection is closed with an appropriate code.
type Authenticator interface {
	Authenticate(ctx context.Context, conn *websocket.Conn) (AuthResult, error)
}

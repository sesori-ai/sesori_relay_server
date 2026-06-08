package auth

import (
	"context"
	"time"

	"github.com/coder/websocket"
)

// AuthResult holds the authenticated identity and token metadata.
type AuthResult struct {
	UserID    string
	Expiry    time.Time
	TokenType string
	Audience  string
	BridgeID  string
}

func (r AuthResult) IsAccessToken() bool {
	return r.TokenType == "access" && r.Audience == "mobile"
}

func (r AuthResult) IsBridgeTokenFor(bridgeID string) bool {
	return r.TokenType == "bridge" && r.Audience == "bridge" && r.BridgeID == bridgeID
}

// Authenticator authenticates a WebSocket connection. Implementations read
// the first message, validate credentials, and return the authenticated
// identity. On failure the connection is closed with an appropriate code.
type Authenticator interface {
	Authenticate(ctx context.Context, conn *websocket.Conn) (AuthResult, error)
}

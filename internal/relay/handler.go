package relay

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/anthropics/remote-relay/internal/protocol"
	"github.com/coder/websocket"
	"github.com/golang-jwt/jwt/v5"
)

const maxMessageSize = 32 * 1024 * 1024 // 32 MiB — session data can be large

func clientIP(remoteAddr string) string {
	ip, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return ip
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	roomCode := r.PathValue("roomCode")

	if !protocol.ValidateRoomCode(roomCode) {
		http.Error(w, "invalid room code", http.StatusBadRequest)
		return
	}

	ip := clientIP(r.RemoteAddr)
	if !s.rateLimiter.AllowConnection(ip) {
		http.Error(w, "too many connections", http.StatusTooManyRequests)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if err != nil {
		s.rateLimiter.ReleaseConnection(ip)
		slog.Debug("websocket accept failed", "room", roomCode, "err", err)
		return
	}
	conn.SetReadLimit(maxMessageSize)
	defer s.rateLimiter.ReleaseConnection(ip)

	ctx := r.Context()

	// Auth step: require AuthMessage with valid JWT within 5 seconds.
	// Skipped when auth is disabled (publicKey is nil).
	var userId string
	if s.publicKey != nil {
		userId, err = s.authenticateConnection(ctx, conn)
		if err != nil {
			// Connection already closed with appropriate code inside authenticateConnection.
			return
		}
	}

	room, exists := s.manager.GetRoom(roomCode)
	if !exists {
		if !s.rateLimiter.AllowRoom(s.manager.RoomCount()) {
			_ = conn.Close(websocket.StatusTryAgainLater, "server at capacity")
			return
		}
		room, err = s.manager.CreateRoomWithCode(roomCode)
		if err != nil {
			slog.Error("failed to create room", "room", roomCode, "err", err)
			_ = conn.Close(websocket.StatusInternalError, "internal error")
			return
		}
	} else if room.IsFull() {
		_ = conn.Close(websocket.StatusCode(protocol.CloseRoomFull), "room full")
		return
	}

	role, addErr := room.AddConnection(conn, userId)
	if addErr != nil {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), addErr.Error())
		return
	}
	slog.Debug("connection joined room", "room", roomCode, "role", role)

	defer func() {
		peer := room.Peer(conn)
		room.RemoveConnection(conn)
		if peer != nil {
			_ = peer.Close(websocket.StatusNormalClosure, "peer disconnected")
		}
		if room.IsEmpty() {
			s.manager.RemoveRoom(roomCode)
			slog.Debug("room removed", "room", roomCode)
		}
	}()

	for {
		msgType, data, err := conn.Read(ctx)
		if err != nil {
			break
		}
		if err := room.ForwardWithType(conn, msgType, data); err != nil {
			break
		}
	}
}

// authenticateConnection reads the first WebSocket message and validates it as an
// AuthMessage containing a valid RS256 JWT. The connection is closed with an
// appropriate close code on any failure. Returns the userId claim on success.
func (s *Server) authenticateConnection(ctx context.Context, conn *websocket.Conn) (string, error) {
	authCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, data, err := conn.Read(authCtx)
	if err != nil {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthRequired), "auth timeout")
		return "", fmt.Errorf("auth read failed: %w", err)
	}

	parsed, err := protocol.ParseMessage(data)
	if err != nil {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "expected auth message")
		return "", fmt.Errorf("failed to parse auth message: %w", err)
	}

	authMsg, ok := parsed.(protocol.AuthMessage)
	if !ok {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "expected auth message")
		return "", fmt.Errorf("expected AuthMessage, got %T", parsed)
	}

	token, err := jwt.Parse(authMsg.Token, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.publicKey, nil
	})
	if err != nil {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), fmt.Sprintf("invalid token: %v", err))
		return "", fmt.Errorf("JWT verification failed: %w", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "invalid token claims")
		return "", fmt.Errorf("invalid JWT claims type")
	}

	userIdClaim, ok := claims["userId"]
	if !ok {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "missing userId claim")
		return "", fmt.Errorf("missing userId claim in JWT")
	}

	userId, ok := userIdClaim.(string)
	if !ok {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "invalid userId claim")
		return "", fmt.Errorf("userId claim is not a string")
	}

	slog.Debug("connection authenticated", "userId", userId)
	return userId, nil
}

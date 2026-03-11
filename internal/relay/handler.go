package relay

import (
	"context"
	"errors"
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
	var expiry time.Time
	s.keyMu.RLock()
	hasPublicKey := s.publicKey != nil
	s.keyMu.RUnlock()
	if hasPublicKey {
		userId, expiry, err = s.authenticateConnection(ctx, conn)
		if err != nil {
			// Connection already closed with appropriate code inside authenticateConnection.
			return
		}

		remaining := time.Until(expiry)
		if remaining > 0 {
			go func() {
				select {
				case <-time.After(remaining):
					_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "token expired")
				case <-ctx.Done():
				}
			}()
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
		closeCode := protocol.CloseAuthFailure
		if errors.Is(addErr, ErrRoomFull) {
			closeCode = protocol.CloseRoomFull
		}
		_ = conn.Close(websocket.StatusCode(closeCode), addErr.Error())
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
// appropriate close code on any failure. Returns the userId claim and token expiry on success.
func (s *Server) authenticateConnection(ctx context.Context, conn *websocket.Conn) (string, time.Time, error) {
	authCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, data, err := conn.Read(authCtx)
	if err != nil {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthRequired), "auth timeout")
		return "", time.Time{}, fmt.Errorf("auth read failed: %w", err)
	}

	parsed, err := protocol.ParseMessage(data)
	if err != nil {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "expected auth message")
		return "", time.Time{}, fmt.Errorf("failed to parse auth message: %w", err)
	}

	authMsg, ok := parsed.(protocol.AuthMessage)
	if !ok {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "expected auth message")
		return "", time.Time{}, fmt.Errorf("expected AuthMessage, got %T", parsed)
	}

	parseToken := func() (*jwt.Token, error) {
		return jwt.Parse(authMsg.Token, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}

			s.keyMu.RLock()
			key := s.publicKey
			s.keyMu.RUnlock()
			return key, nil
		})
	}

	token, err := parseToken()
	if err != nil {
		if refreshErr := s.refreshPublicKey(); refreshErr != nil {
			slog.Warn("failed to refresh auth public key after JWT verification failure", "err", refreshErr)
		} else {
			token, err = parseToken()
		}
	}
	if err != nil {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), fmt.Sprintf("invalid token: %v", err))
		return "", time.Time{}, fmt.Errorf("JWT verification failed: %w", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "invalid token claims")
		return "", time.Time{}, fmt.Errorf("invalid JWT claims type")
	}

	userIdClaim, ok := claims["userId"]
	if !ok {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "missing userId claim")
		return "", time.Time{}, fmt.Errorf("missing userId claim in JWT")
	}

	userId, ok := userIdClaim.(string)
	if !ok || userId == "" {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "invalid userId claim")
		return "", time.Time{}, fmt.Errorf("userId claim is not a string")
	}

	tokenTypeClaim, ok := claims["tokenType"]
	if !ok {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "missing tokenType claim")
		return "", time.Time{}, fmt.Errorf("missing tokenType claim in JWT")
	}

	tokenType, ok := tokenTypeClaim.(string)
	if !ok || (tokenType != "access" && tokenType != "bridge") {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "invalid tokenType claim")
		return "", time.Time{}, fmt.Errorf("invalid tokenType claim: %v", tokenTypeClaim)
	}

	audClaim, ok := claims["aud"]
	if !ok {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "missing aud claim")
		return "", time.Time{}, fmt.Errorf("missing aud claim in JWT")
	}

	var audience string
	switch v := audClaim.(type) {
	case string:
		audience = v
	case []interface{}:
		if len(v) == 1 {
			audValue, ok := v[0].(string)
			if ok {
				audience = audValue
			}
		}
	}
	if audience != "mobile" && audience != "bridge" {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "invalid aud claim")
		return "", time.Time{}, fmt.Errorf("invalid aud claim: %v", audClaim)
	}

	issClaim, ok := claims["iss"]
	if !ok {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "missing iss claim")
		return "", time.Time{}, fmt.Errorf("missing iss claim in JWT")
	}

	issuer, ok := issClaim.(string)
	if !ok || issuer != "auth-backend" {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "invalid iss claim")
		return "", time.Time{}, fmt.Errorf("invalid iss claim: %v", issClaim)
	}

	expClaim, ok := claims["exp"]
	if !ok {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "missing exp claim")
		return "", time.Time{}, fmt.Errorf("missing exp claim in JWT")
	}

	expFloat, ok := expClaim.(float64)
	if !ok {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "invalid exp claim")
		return "", time.Time{}, fmt.Errorf("exp claim is not a number: %T", expClaim)
	}

	expiry := time.Unix(int64(expFloat), 0)
	slog.Debug("connection authenticated", "userId", userId, "tokenType", tokenType)
	return userId, expiry, nil
}

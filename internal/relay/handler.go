package relay

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/anthropics/remote-relay/internal/protocol"
	"github.com/coder/websocket"
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

	// Auth step: require valid credentials within 5 seconds.
	// Skipped when no authenticator is configured.
	var userId string
	if s.authenticator != nil {
		result, authErr := s.authenticator.Authenticate(ctx, conn)
		if authErr != nil {
			// Connection already closed with appropriate code inside Authenticate.
			return
		}
		userId = result.UserID

		// Schedule connection close at token expiry.
		if remaining := time.Until(result.Expiry); remaining > 0 {
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

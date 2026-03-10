package relay

import (
	"log/slog"
	"net/http"

	"github.com/anthropics/remote-relay/internal/protocol"
	"github.com/coder/websocket"
)

const maxMessageSize = 1048576

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	roomCode := r.PathValue("roomCode")

	if !protocol.ValidateRoomCode(roomCode) {
		http.Error(w, "invalid room code", http.StatusBadRequest)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if err != nil {
		slog.Debug("websocket accept failed", "room", roomCode, "err", err)
		return
	}
	conn.SetReadLimit(maxMessageSize)

	room, exists := s.manager.GetRoom(roomCode)
	if !exists {
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

	role, addErr := room.AddConnection(conn)
	if addErr != nil {
		_ = conn.Close(websocket.StatusCode(protocol.CloseRoomFull), "room full")
		return
	}
	slog.Debug("connection joined room", "room", roomCode, "role", role)

	ctx := r.Context()

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
		if msgType != websocket.MessageBinary {
			continue
		}
		if err := room.Forward(conn, data); err != nil {
			break
		}
	}
}

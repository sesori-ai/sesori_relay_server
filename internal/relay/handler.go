package relay

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/anthropics/remote-relay/internal/auth"
	"github.com/anthropics/remote-relay/internal/protocol"
	"github.com/coder/websocket"
)

const maxMessageSize = 32 * 1024 * 1024 // 32 MiB — session data can be large

func getClientIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	ip := getClientIP(r)
	if !s.rateLimiter.AllowConnection(ip) {
		http.Error(w, "too many connections", http.StatusTooManyRequests)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if err != nil {
		s.rateLimiter.ReleaseConnection(ip)
		slog.Debug("websocket accept failed", "err", err)
		return
	}
	defer s.rateLimiter.ReleaseConnection(ip)
	if s.jwtAuth == nil {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthRequired), "auth required")
		return
	}

	if !s.rateLimiter.AllowGroup(s.manager.Count()) {
		_ = conn.Close(websocket.StatusPolicyViolation, "rate limited")
		return
	}

	authMsg, userID, ok := readAndValidateAuth(r.Context(), conn, s.jwtAuth)
	if !ok {
		return
	}

	group := s.manager.GetOrCreateGroup(userID)

	slog.Debug("connection joined group", "userID", userID, "role", authMsg.Role)
	if authMsg.Role == protocol.RoleBridge {
		handleBridge(r.Context(), conn, group, s.manager, userID)
		return
	}
	handlePhone(r.Context(), conn, group, s.manager, userID)
}

func readAndValidateAuth(ctx context.Context, conn *websocket.Conn, jwtAuth *auth.JWTAuthenticator) (protocol.RoleAuthMessage, string, bool) {
	authCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	msgType, data, err := conn.Read(authCtx)
	if err != nil || msgType != websocket.MessageText {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthRequired), "auth required")
		return protocol.RoleAuthMessage{}, "", false
	}

	var authMsg protocol.RoleAuthMessage
	if err := json.Unmarshal(data, &authMsg); err != nil || authMsg.Type != protocol.TypeAuth {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "auth failed")
		return protocol.RoleAuthMessage{}, "", false
	}

	if authMsg.Role != protocol.RoleBridge && authMsg.Role != protocol.RolePhone {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "invalid role")
		return protocol.RoleAuthMessage{}, "", false
	}

	result, err := jwtAuth.Validate(authMsg.Token)
	if err != nil {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "auth failed")
		return protocol.RoleAuthMessage{}, "", false
	}

	return authMsg, result.UserID, true
}

func handleBridge(ctx context.Context, conn *websocket.Conn, group *AccountGroup, manager *GroupManager, userID string) {
	group.mu.Lock()
	oldBridge := group.Bridge
	connCtx, connCancel := context.WithCancel(ctx)
	newConn := &Connection{Conn: conn, ConnID: 0, Cancel: connCancel}
	group.Bridge = newConn
	phones := make([]*Connection, 0, len(group.Phones))
	for _, p := range group.Phones {
		phones = append(phones, p)
	}
	group.mu.Unlock()

	if oldBridge != nil {
		oldBridge.Cancel()
		_ = oldBridge.Conn.Close(websocket.StatusNormalClosure, "replaced")
	}

	bridgeConnectedMsg, _ := json.Marshal(protocol.BridgeConnectedMessage{Type: protocol.TypeBridgeConnected})
	for _, phone := range phones {
		_ = phone.Conn.Write(ctx, websocket.MessageText, bridgeConnectedMsg)
	}

	defer func() {
		connCancel()
		group.mu.Lock()
		if group.Bridge == newConn {
			group.Bridge = nil
		}
		phones := make([]*Connection, 0, len(group.Phones))
		for _, p := range group.Phones {
			phones = append(phones, p)
		}
		group.mu.Unlock()

		bridgeDisconnMsg, _ := json.Marshal(protocol.BridgeDisconnectedMessage{Type: protocol.TypeBridgeDisconnected})
		for _, phone := range phones {
			_ = phone.Conn.Write(ctx, websocket.MessageText, bridgeDisconnMsg)
		}
		manager.RemoveGroupIfEmpty(userID)
	}()

	conn.SetReadLimit(maxMessageSize)
	for {
		msgType, data, err := conn.Read(connCtx)
		if err != nil {
			return
		}
		if msgType == websocket.MessageText {
			continue
		}
		if len(data) < 2 {
			continue
		}

		connID := binary.BigEndian.Uint16(data[:2])
		payload := data[2:]

		if connID == 0 {
			group.mu.Lock()
			targets := make([]*Connection, 0, len(group.Phones))
			for _, phone := range group.Phones {
				targets = append(targets, phone)
			}
			group.mu.Unlock()
			for _, target := range targets {
				_ = target.Conn.Write(ctx, websocket.MessageBinary, payload)
			}
			continue
		}

		group.mu.Lock()
		target := group.Phones[connID]
		group.mu.Unlock()
		if target == nil {
			continue
		}
		_ = target.Conn.Write(ctx, websocket.MessageBinary, payload)
	}
}

func handlePhone(ctx context.Context, conn *websocket.Conn, group *AccountGroup, manager *GroupManager, userID string) {
	var (
		connID     uint16
		bridgeConn *Connection
		connCtx    context.Context
		connCancel context.CancelFunc
	)

	for {
		id := group.AssignConnID()
		group.mu.Lock()
		if len(group.Phones) >= 5 {
			group.mu.Unlock()
			_ = conn.Close(websocket.StatusCode(protocol.CloseAccountFull), "account full")
			return
		}
		if _, exists := group.Phones[id]; exists {
			group.mu.Unlock()
			continue
		}

		connCtx, connCancel = context.WithCancel(ctx)
		phoneConn := &Connection{Conn: conn, ConnID: id, Cancel: connCancel}
		group.Phones[id] = phoneConn
		bridgeConn = group.Bridge
		connID = id
		group.mu.Unlock()
		break
	}

	if bridgeConn != nil {
		phoneConnMsg, _ := json.Marshal(protocol.PhoneConnectedMessage{Type: protocol.TypePhoneConnected, ConnID: connID})
		_ = bridgeConn.Conn.Write(ctx, websocket.MessageText, phoneConnMsg)

		bridgeConnMsg, _ := json.Marshal(protocol.BridgeConnectedMessage{Type: protocol.TypeBridgeConnected})
		_ = conn.Write(ctx, websocket.MessageText, bridgeConnMsg)
	} else {
		bridgeDisconnMsg, _ := json.Marshal(protocol.BridgeDisconnectedMessage{Type: protocol.TypeBridgeDisconnected})
		_ = conn.Write(ctx, websocket.MessageText, bridgeDisconnMsg)
	}

	defer func() {
		connCancel()
		group.mu.Lock()
		delete(group.Phones, connID)
		bridge := group.Bridge
		group.mu.Unlock()

		if bridge != nil {
			phoneDisconnMsg, _ := json.Marshal(protocol.PhoneDisconnectedMessage{Type: protocol.TypePhoneDisconnected, ConnID: connID})
			_ = bridge.Conn.Write(ctx, websocket.MessageText, phoneDisconnMsg)
		}
		manager.RemoveGroupIfEmpty(userID)
	}()

	conn.SetReadLimit(maxMessageSize)
	for {
		msgType, data, err := conn.Read(connCtx)
		if err != nil {
			return
		}
		if msgType == websocket.MessageText {
			continue
		}

		group.mu.Lock()
		bridge := group.Bridge
		group.mu.Unlock()
		if bridge == nil {
			continue
		}

		framed := make([]byte, 2+len(data))
		binary.BigEndian.PutUint16(framed[:2], connID)
		copy(framed[2:], data)
		_ = bridge.Conn.Write(ctx, websocket.MessageBinary, framed)
	}
}

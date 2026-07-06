package relay

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"time"

	"github.com/coder/websocket"
	"github.com/sesori-ai/sesori_relay_server/internal/auth"
	"github.com/sesori-ai/sesori_relay_server/internal/notifications"
	"github.com/sesori-ai/sesori_relay_server/internal/protocol"
)

const (
	maxMessageSize      = 64 * 1024 * 1024 // 64 MiB — session data can be large
	maxPhonesPerAccount = 5
	pingInterval        = 30 * time.Second
)

var bridgeIDRegexp = regexp.MustCompile(`^br_[A-Za-z0-9_-]{8,32}$`)

var (
	bridgeConnectedJSON, _    = json.Marshal(protocol.BridgeConnectedMessage{Type: protocol.TypeBridgeConnected})
	bridgeDisconnectedJSON, _ = json.Marshal(protocol.BridgeDisconnectedMessage{Type: protocol.TypeBridgeDisconnected})
)

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

	authMsg, authResult, ok := readAndValidateAuth(r.Context(), conn, s.jwtAuth)
	if !ok {
		return
	}
	userID := authResult.UserID

	group := s.manager.GetOrCreateGroup(userID)

	slog.Debug("connection joined group", "userID", userID, "role", authMsg.Role)
	if authMsg.Role == protocol.RoleBridge {
		if authMsg.BridgeID != "" && !bridgeIDRegexp.MatchString(authMsg.BridgeID) {
			s.manager.RemoveGroupIfEmpty(userID)
			slog.Warn("bridge connection rejected: invalid bridgeId format", "userId", userID, "bridgeId", authMsg.BridgeID)
			_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "invalid bridgeId format")
			return
		}
		if s.requireBridgeID && authMsg.BridgeID == "" {
			s.manager.RemoveGroupIfEmpty(userID)
			slog.Warn("bridge connection rejected: bridgeId required", "userId", userID)
			_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "bridgeId required")
			return
		}
		if authMsg.BridgeID == "" {
			slog.Warn("legacy bridge without bridgeId; set RELAY_REQUIRE_BRIDGE_ID=true to enforce", "userId", userID)
		}
		handleBridge(r.Context(), conn, group, s.manager, userID, authMsg.BridgeID, s.notifications)
		return
	}
	handlePhone(r.Context(), conn, group, s.manager, userID)
}

func readAndValidateAuth(ctx context.Context, conn *websocket.Conn, jwtAuth *auth.JWTAuthenticator) (protocol.RoleAuthMessage, auth.AuthResult, bool) {
	authCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	msgType, data, err := conn.Read(authCtx)
	if err != nil || msgType != websocket.MessageText {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthRequired), "auth required")
		return protocol.RoleAuthMessage{}, auth.AuthResult{}, false
	}

	var authMsg protocol.RoleAuthMessage
	if err := json.Unmarshal(data, &authMsg); err != nil || authMsg.Type != protocol.TypeAuth {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "auth failed")
		return protocol.RoleAuthMessage{}, auth.AuthResult{}, false
	}

	if authMsg.Role != protocol.RoleBridge && authMsg.Role != protocol.RolePhone {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "invalid role")
		return protocol.RoleAuthMessage{}, auth.AuthResult{}, false
	}

	result, err := jwtAuth.Validate(authMsg.Token)
	if err != nil {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "auth failed")
		return protocol.RoleAuthMessage{}, auth.AuthResult{}, false
	}

	return authMsg, result, true
}

func startPingLoop(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn) {
	go func() {
		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pingCtx, cancelPing := context.WithTimeout(ctx, pingInterval/2)
				err := conn.Ping(pingCtx)
				cancelPing()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
}

func handleBridge(ctx context.Context, conn *websocket.Conn, group *AccountGroup, manager *GroupManager, userID, bridgeID string, notificationsClient *notifications.Client) {
	group.mu.Lock()
	oldBridge := group.Bridge
	connCtx, connCancel := context.WithCancel(ctx)
	newConn := &Connection{Conn: conn, ConnID: 0, Cancel: connCancel, BridgeID: bridgeID}
	group.Bridge = newConn
	group.mu.Unlock()
	startPingLoop(connCtx, connCancel, conn)

	phones := group.AllPhones()

	if oldBridge != nil {
		// Displace the old bridge with the dedicated takeover close code so the
		// displaced bridge recognises the takeover and backs off instead of
		// tight-looping. Correctness rests on a strict order — Close, then
		// Cancel — run off the hot path in one goroutine:
		//
		//   * Close wins the close CAS and writes the CloseBridgeReplaced frame
		//     synchronously (writeClose runs before the handshake wait), so the
		//     displaced bridge reliably observes 4007. Cancelling first would
		//     instead let the old read loop's ctx-cancel teardown / handler
		//     return close the socket abnormally (EOF) and race the frame away.
		//   * Cancel runs after Close returns to release the old connection
		//     context (ping loop, deferred cleanup). It is not on this new
		//     bridge's connect path (the whole block is a goroutine), so a
		//     slow/non-responsive displaced peer only delays that bridge's own
		//     disconnect bookkeeping up to the handshake timeout — a best-effort
		//     lag, never a correctness issue, and phones already learned of the
		//     new bridge via the bridge_connected writes below.
		//
		// The single-active-bridge invariant does NOT depend on this close
		// timing: the read loop's PhonesIfCurrentBridge / PhoneIfCurrentBridge
		// routing (see below) drops any frame from a bridge that is no longer
		// group.Bridge, so a displaced bridge can never relay even in the window
		// before its close/cancel lands.
		//
		// Keep the "replaced" reason as a rollout fallback the bridge can match
		// on until it keys purely on CloseBridgeReplaced; the code is
		// authoritative.
		go func() {
			_ = oldBridge.Conn.Close(websocket.StatusCode(protocol.CloseBridgeReplaced), "replaced")
			oldBridge.Cancel()
		}()
	}

	for _, phone := range phones {
		_ = phone.Conn.Write(ctx, websocket.MessageText, bridgeConnectedJSON)
	}

	if notificationsClient != nil {
		go func() {
			err := notificationsClient.NotifyBridgeStatus(context.Background(), userID, bridgeID, notifications.BridgeStatusConnected)
			if err == nil {
				return
			}
			if bridgeID != "" && errors.Is(err, notifications.ErrBridgeNotFound) {
				// The auth server explicitly reported this bridgeId as unknown,
				// revoked, or owned by another user. Close exactly this
				// connection; unblocking its read loop runs the deferred
				// cleanup below, which only touches the group if this
				// connection is still the current bridge.
				slog.Warn("bridge revoked; closing connection", "userId", userID, "bridgeId", bridgeID)
				_ = conn.Close(websocket.StatusCode(protocol.CloseBridgeRevoked), "bridge revoked")
				return
			}
			// Transport errors, timeouts, and 5xx are fail-open: log and keep
			// the connection.
			slog.Warn("failed to notify bridge connected", "error", err, "userId", userID, "bridgeId", bridgeID)
		}()
	}

	defer func() {
		connCancel()
		group.mu.Lock()
		isCurrent := group.Bridge == newConn
		currentBridgeID := ""
		if group.Bridge != nil {
			currentBridgeID = group.Bridge.BridgeID
		}
		if isCurrent {
			group.Bridge = nil
		}
		group.mu.Unlock()
		shouldNotifyDisconnected := isCurrent || (bridgeID != "" && bridgeID != currentBridgeID)

		if isCurrent {
			phones := group.AllPhones()
			for _, phone := range phones {
				_ = phone.Conn.Write(ctx, websocket.MessageText, bridgeDisconnectedJSON)
			}
		}

		if shouldNotifyDisconnected && notificationsClient != nil {
			go func() {
				if err := notificationsClient.NotifyBridgeStatus(context.Background(), userID, bridgeID, notifications.BridgeStatusDisconnected); err != nil {
					slog.Warn("failed to notify bridge disconnected", "error", err, "userId", userID, "bridgeId", bridgeID)
				}
			}()
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

		// Enforce the single-active-bridge invariant on every frame: a bridge
		// displaced by a newer connection for this account must not relay to
		// phones, even for frames it had already queued/read before its close
		// completes. The current-bridge check is folded into the same locked
		// target selection (PhonesIfCurrentBridge / PhoneIfCurrentBridge), so a
		// bridge displaced before it routes a frame gets no targets — without
		// relying on the displaced connection's close/cancel racing the read
		// loop. The lock is not held across the writes below (that would
		// serialize all relay traffic on socket I/O), so a bridge displaced in
		// the microseconds between snapshot and write can still deliver the one
		// frame it is mid-routing; that frame was read while it was the active
		// bridge, so its late arrival during handover is benign.
		if connID == 0 {
			for _, target := range group.PhonesIfCurrentBridge(newConn) {
				_ = target.Conn.Write(ctx, websocket.MessageBinary, payload)
			}
			continue
		}

		target := group.PhoneIfCurrentBridge(newConn, connID)
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
		if len(group.Phones) >= maxPhonesPerAccount {
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

	startPingLoop(connCtx, connCancel, conn)

	if bridgeConn != nil {
		phoneConnMsg, err := json.Marshal(protocol.PhoneConnectedMessage{Type: protocol.TypePhoneConnected, ConnID: connID})
		if err != nil {
			slog.Error("failed to marshal phone_connected", "err", err)
			return
		}
		_ = bridgeConn.Conn.Write(ctx, websocket.MessageText, phoneConnMsg)
		_ = conn.Write(ctx, websocket.MessageText, bridgeConnectedJSON)
	} else {
		_ = conn.Write(ctx, websocket.MessageText, bridgeDisconnectedJSON)
	}

	defer func() {
		connCancel()
		group.mu.Lock()
		delete(group.Phones, connID)
		bridge := group.Bridge
		group.mu.Unlock()

		if bridge != nil {
			phoneDisconnMsg, err := json.Marshal(protocol.PhoneDisconnectedMessage{Type: protocol.TypePhoneDisconnected, ConnID: connID})
			if err != nil {
				slog.Error("failed to marshal phone_disconnected", "err", err)
				return
			}
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

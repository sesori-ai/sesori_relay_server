package relay

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/sesori-ai/sesori_relay_server/internal/auth"
	"github.com/sesori-ai/sesori_relay_server/internal/notifications"
	"github.com/sesori-ai/sesori_relay_server/internal/protocol"
)

const (
	maxMessageSize       = 64 * 1024 * 1024 // 64 MiB — session data can be large
	maxPhonesPerAccount  = 5
	maxBridgesPerAccount = 10
	pingInterval         = 30 * time.Second
	cfConnectingIPHeader = "CF-Connecting-IP"
)

type clientIPSource string

const (
	clientIPSourceRemoteAddr     clientIPSource = "remote-addr"
	clientIPSourceCFConnectingIP clientIPSource = "cf-connecting-ip"
)

type clientIPInfo struct {
	address     string
	peerAddress string
	source      clientIPSource
}

var bridgeIDRegexp = regexp.MustCompile(`^br_[A-Za-z0-9_-]{8,32}$`)
var deviceIDRegexp = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

var (
	bridgeConnectedJSON, _    = json.Marshal(protocol.BridgeConnectedMessage{Type: protocol.TypeBridgeConnected})
	bridgeDisconnectedJSON, _ = json.Marshal(protocol.BridgeDisconnectedMessage{Type: protocol.TypeBridgeDisconnected})
)

func resolveClientIP(r *http.Request, trustCFConnectingIP bool) clientIPInfo {
	peerAddress := remoteIPAddress(r.RemoteAddr)
	if trustCFConnectingIP {
		values := r.Header.Values(cfConnectingIPHeader)
		if len(values) == 1 {
			if address, ok := canonicalIPAddress(values[0]); ok {
				return clientIPInfo{
					address:     address,
					peerAddress: peerAddress,
					source:      clientIPSourceCFConnectingIP,
				}
			}
		}
	}

	return clientIPInfo{
		address:     peerAddress,
		peerAddress: peerAddress,
		source:      clientIPSourceRemoteAddr,
	}
}

func remoteIPAddress(remoteAddr string) string {
	value := strings.TrimSpace(remoteAddr)
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	if address, ok := canonicalIPAddress(value); ok {
		return address
	}
	return value
}

func canonicalIPAddress(value string) (string, bool) {
	address, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil {
		return "", false
	}
	return address.Unmap().String(), true
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	clientIP := resolveClientIP(r, s.trustCFConnectingIP)
	ip := clientIP.address
	slog.Debug(
		"websocket client IP resolved",
		"clientIP", clientIP.address,
		"peerIP", clientIP.peerAddress,
		"source", string(clientIP.source),
	)
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

	if authMsg.Role == protocol.RoleBridge && authMsg.BridgeID == "" {
		slog.Debug("bridge connection rejected: bridgeId required", "userId", userID)
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "bridgeId required")
		return
	}
	// Bridges name themselves; a phone may name (pin) the bridge it talks to.
	if authMsg.BridgeID != "" && !bridgeIDRegexp.MatchString(authMsg.BridgeID) {
		slog.Debug("connection rejected: invalid bridgeId format", "userId", userID, "role", authMsg.Role, "bridgeId", authMsg.BridgeID)
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "invalid bridgeId format")
		return
	}

	group := s.manager.GetOrCreateGroup(userID)

	slog.Debug("connection joined group", "userID", userID, "role", authMsg.Role)
	if authMsg.Role == protocol.RoleBridge {
		handleBridge(r.Context(), conn, group, s.manager, userID, authMsg.BridgeID, authMsg.ConnectionNotificationPolicy, s.notifications)
		return
	}
	handlePhone(r.Context(), conn, group, s.manager, userID, authMsg.BridgeID, s.notifications)
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

func handleBridge(ctx context.Context, conn *websocket.Conn, group *AccountGroup, manager *GroupManager, userID, bridgeID, requestedPolicy string, notificationsClient *notifications.Client) {
	policy := normalizeNotificationPolicy(requestedPolicy)
	// Disconnect eligibility belongs to this socket episode. A later sleep
	// suppression hides DarkWake-only online churn but must not erase offline
	// eligibility already earned while the socket was normally awake.
	disconnectPolicy := policy
	connectionID := newConnectionID()
	connCtx, connCancel := context.WithCancel(ctx)
	newConn := &Connection{Conn: conn, Cancel: connCancel, BridgeID: bridgeID, ConnectionID: connectionID, ConnectedAt: time.Now()}
	displaced, changes, ok := group.AddBridge(newConn)
	if !ok {
		connCancel()
		slog.Debug("bridge connection rejected: account has too many bridges", "userId", userID, "bridgeId", bridgeID)
		_ = conn.Close(websocket.StatusCode(protocol.CloseAccountFull), "account full")
		return
	}
	startPingLoop(connCtx, connCancel, conn)
	reportControl := newControlReportSender(connCtx, notificationsClient)

	if displaced != nil {
		// Displace the previous connection with this bridgeId (the same
		// computer reconnecting) with the dedicated takeover close code so it
		// recognises the takeover and backs off instead of tight-looping. A
		// bridge with a different id never displaces anyone. Correctness rests
		// on a strict order — Close, then Cancel — run off the hot path in one
		// goroutine:
		//
		//   * Close wins the close CAS and writes the CloseBridgeReplaced frame
		//     synchronously (writeClose runs before the handshake wait), so the
		//     displaced bridge observes 4007 on the common path. Cancelling
		//     first would instead let the old read loop's ctx-cancel teardown /
		//     handler return close the socket abnormally (EOF) and race the
		//     frame away.
		//   * Cancel runs after Close returns to release the old connection
		//     context (ping loop, deferred cleanup). It is not on this new
		//     bridge's connect path (the whole block is a goroutine), so a
		//     slow/non-responsive displaced peer only delays that bridge's own
		//     disconnect bookkeeping up to the handshake timeout — a best-effort
		//     lag, never a correctness issue, and phones already learned of the
		//     new connection via the bridge_connected writes below.
		//
		// 4007 delivery is deliberately best-effort (accepted tradeoff, PR #7):
		// the close frame contends for the connection's frame-write mutex with
		// any in-flight phone→bridge write (handlePhone), so if the displaced
		// bridge stalls its reads while such a write is blocked, the close
		// write can time out (~5s) and drop the TCP connection without
		// delivering 4007 — the displaced bridge then sees an abnormal close
		// (1006) and retries on its normal backoff instead of the takeover
		// backoff. coder/websocket offers no way to preempt the in-flight
		// write: cancelling its context hard-closes the whole connection
		// (timeoutLoop -> close), which loses 4007 anyway. The miss is
		// self-limiting — the reconnected bridge is displaced again, almost
		// certainly with no write in flight, and observes 4007 then — so it
		// degrades to at most one extra reconnect, not a reconnect war.
		//
		// Routing does NOT depend on this close timing: the read loop's
		// PhonesTargeting / PhoneTargeting (see below) drops any frame from a
		// connection that is no longer current for its bridgeId, so a displaced
		// connection can never relay even in the window before its
		// close/cancel lands.
		//
		// Keep the "replaced" reason as a rollout fallback the bridge can match
		// on until it keys purely on CloseBridgeReplaced; the code is
		// authoritative.
		go func() {
			_ = displaced.Conn.Close(websocket.StatusCode(protocol.CloseBridgeReplaced), "replaced")
			displaced.Cancel()
		}()
	}

	announceTargetChanges(ctx, changes)

	if notificationsClient != nil {
		connectedAt := time.Now().UTC().Format(time.RFC3339Nano)
		go func(initialPolicy string) {
			err := notificationsClient.NotifyBridgeStatus(context.Background(), notifications.BridgeStatusPayload{
				UserID: userID, BridgeID: bridgeID, Status: notifications.BridgeStatusConnected,
				Timestamp: connectedAt, NotificationPolicy: initialPolicy, ConnectionID: connectionID,
			})
			if err == nil {
				return
			}
			if errors.Is(err, notifications.ErrBridgeNotFound) {
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
		}(policy)
	}

	defer func() {
		connCancel()
		// A connection displaced by a same-id reconnect is no longer current:
		// its replacement already owns the phones and the bridge's online
		// state, so it changes nothing and reports nothing.
		isCurrent, changes := group.RemoveBridge(newConn)
		announceTargetChanges(ctx, changes)

		// Suppress the disconnect notification if this bridgeId is already live
		// again on the user's current group: a same-id reconnect that lands
		// right after this teardown must not be marked offline in the auth
		// server after its connected notification. This checks the manager's
		// current registration, not this handler's own (possibly stale) group.
		shouldNotifyDisconnected := isCurrent && !manager.HasLiveBridgeWithID(userID, bridgeID)

		if shouldNotifyDisconnected && notificationsClient != nil {
			disconnectedAt := time.Now().UTC().Format(time.RFC3339Nano)
			go func() {
				if err := notificationsClient.NotifyBridgeStatus(context.Background(), notifications.BridgeStatusPayload{
					UserID: userID, BridgeID: bridgeID, Status: notifications.BridgeStatusDisconnected,
					Timestamp: disconnectedAt, NotificationPolicy: disconnectPolicy, ConnectionID: connectionID,
				}); err != nil {
					slog.Warn("failed to notify bridge disconnected", "error", err, "userId", userID, "bridgeId", bridgeID)
				}
			}()
		}

		manager.RemoveGroupIfEmpty(userID, group)
	}()

	conn.SetReadLimit(maxMessageSize)
	for {
		msgType, data, err := conn.Read(connCtx)
		if err != nil {
			return
		}
		if msgType == websocket.MessageText {
			var message protocol.BridgeConnectionNotificationPolicyMessage
			if err := json.Unmarshal(data, &message); err != nil || message.Type != protocol.TypeBridgeConnectionNotificationPolicy {
				continue
			}
			updatedPolicy := normalizeNotificationPolicy(message.Policy)
			if !group.IsCurrentBridge(newConn) {
				continue
			}
			policy = updatedPolicy
			if updatedPolicy != protocol.ConnectionNotificationPolicySuppress {
				disconnectPolicy = updatedPolicy
			}
			reportControl(notifications.BridgeStatusPayload{
				UserID: userID, BridgeID: bridgeID, Status: notifications.BridgeStatusConnected,
				Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Event: "notification_policy",
				NotificationPolicy: policy, ConnectionID: connectionID,
			})
			continue
		}
		if len(data) < 2 {
			continue
		}

		connID := binary.BigEndian.Uint16(data[:2])
		payload := data[2:]

		// A bridge reaches only the phones whose effective target it is, even
		// with a known connId, and a connection displaced by a same-id
		// reconnect reaches none — checked under the group lock on every frame
		// rather than relying on the displaced connection's close/cancel racing
		// the read loop (see PhonesTargeting for the residual window).
		if connID == 0 {
			for _, target := range group.PhonesTargeting(newConn) {
				_ = target.Conn.Write(ctx, websocket.MessageBinary, payload)
			}
			continue
		}

		target := group.PhoneTargeting(newConn, connID)
		if target == nil {
			continue
		}
		_ = target.Conn.Write(ctx, websocket.MessageBinary, payload)
	}
}

// handlePhone serves one phone. pinnedBridgeID is the bridge the phone named
// at auth; empty means it follows the account's default bridge.
func handlePhone(ctx context.Context, conn *websocket.Conn, group *AccountGroup, manager *GroupManager, userID, pinnedBridgeID string, notificationsClient *notifications.Client) {
	connCtx, connCancel := context.WithCancel(ctx)
	phoneConn := &Connection{Conn: conn, Cancel: connCancel, BridgeID: pinnedBridgeID}
	bridgeConn, ok := group.AddPhone(phoneConn)
	if !ok {
		connCancel()
		_ = conn.Close(websocket.StatusCode(protocol.CloseAccountFull), "account full")
		return
	}
	connID := phoneConn.ConnID

	startPingLoop(connCtx, connCancel, conn)
	reportControl := newControlReportSender(connCtx, notificationsClient)

	if bridgeConn != nil {
		notifyBridgeOfPhone(ctx, bridgeConn, protocol.PhoneConnectedMessage{Type: protocol.TypePhoneConnected, ConnID: connID})
		_ = conn.Write(ctx, websocket.MessageText, bridgeConnectedJSON)
	} else {
		_ = conn.Write(ctx, websocket.MessageText, bridgeDisconnectedJSON)
	}

	defer func() {
		connCancel()
		if bridge := group.RemovePhone(phoneConn); bridge != nil {
			notifyBridgeOfPhone(ctx, bridge, protocol.PhoneDisconnectedMessage{Type: protocol.TypePhoneDisconnected, ConnID: connID})
		}
		manager.RemoveGroupIfEmpty(userID, group)
	}()

	conn.SetReadLimit(maxMessageSize)
	for {
		msgType, data, err := conn.Read(connCtx)
		if err != nil {
			return
		}
		if msgType == websocket.MessageText {
			var message protocol.BridgeConnectionObservedMessage
			if err := json.Unmarshal(data, &message); err != nil || message.Type != protocol.TypeBridgeConnectionObserved || !deviceIDRegexp.MatchString(message.DeviceID) {
				continue
			}
			bridge := group.BridgeForPhone(phoneConn)
			if bridge == nil || bridge.ConnectionID == "" || notificationsClient == nil {
				continue
			}
			reportControl(notifications.BridgeStatusPayload{
				UserID: userID, BridgeID: bridge.BridgeID, Status: notifications.BridgeStatusConnected,
				Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Event: "connection_observed",
				ConnectionID: bridge.ConnectionID, DeviceID: message.DeviceID,
			})
			continue
		}

		bridge := group.BridgeForPhone(phoneConn)
		if bridge == nil {
			continue
		}

		framed := make([]byte, 2+len(data))
		binary.BigEndian.PutUint16(framed[:2], connID)
		copy(framed[2:], data)
		_ = bridge.Conn.Write(ctx, websocket.MessageBinary, framed)
	}
}

// announceTargetChanges tells each re-targeted phone and the bridges involved,
// in order: phone_disconnected to the previous bridge (if still connected),
// then the phone's notice, then phone_connected to the new bridge. A phone
// that moves to another bridge gets bridge_connected only — never
// bridge_disconnected first, which would flash the offline state — and re-keys
// with its new bridge on that notice.
func announceTargetChanges(ctx context.Context, changes []targetChange) {
	for _, change := range changes {
		connID := change.phone.ConnID
		if change.previous != nil {
			notifyBridgeOfPhone(ctx, change.previous, protocol.PhoneDisconnectedMessage{Type: protocol.TypePhoneDisconnected, ConnID: connID})
		}
		if change.next == nil {
			_ = change.phone.Conn.Write(ctx, websocket.MessageText, bridgeDisconnectedJSON)
			continue
		}
		_ = change.phone.Conn.Write(ctx, websocket.MessageText, bridgeConnectedJSON)
		notifyBridgeOfPhone(ctx, change.next, protocol.PhoneConnectedMessage{Type: protocol.TypePhoneConnected, ConnID: connID})
	}
}

func notifyBridgeOfPhone(ctx context.Context, bridge *Connection, message any) {
	data, err := json.Marshal(message)
	if err != nil {
		slog.Error("failed to marshal phone notice", "err", err)
		return
	}
	_ = bridge.Conn.Write(ctx, websocket.MessageText, data)
}

// One sender and one pending latest-value report per socket. The socket read
// loop is the sole producer, and never waits for HTTP or a free queue slot.
// Control reports are advisory: even a 404 must not control socket lifetime.
func newControlReportSender(ctx context.Context, client *notifications.Client) func(notifications.BridgeStatusPayload) {
	if client == nil {
		return func(notifications.BridgeStatusPayload) {}
	}
	reports := make(chan notifications.BridgeStatusPayload, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case report := <-reports:
				if ctx.Err() != nil {
					return
				}
				if err := client.NotifyBridgeStatus(ctx, report); err != nil {
					slog.Warn("failed to report connection notification metadata", "error", err,
						"userId", report.UserID, "bridgeId", report.BridgeID, "event", report.Event)
				}
			}
		}
	}()
	return func(report notifications.BridgeStatusPayload) {
		select {
		case reports <- report:
		default:
			// Replace an obsolete queued value, not the in-flight HTTP request.
			select {
			case <-reports:
			default:
			}
			reports <- report // Single producer: the queue now has room.
		}
	}
}

func normalizeNotificationPolicy(policy string) string {
	switch policy {
	case protocol.ConnectionNotificationPolicyNormal, protocol.ConnectionNotificationPolicySuppress, protocol.ConnectionNotificationPolicyConservative:
		return policy
	default:
		return protocol.ConnectionNotificationPolicyConservative
	}
}

func newConnectionID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		slog.Warn("failed to generate bridge notification connection id", "error", err)
		return ""
	}
	return hex.EncodeToString(value[:])
}

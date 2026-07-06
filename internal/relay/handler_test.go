package relay

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/golang-jwt/jwt/v5"
	"github.com/sesori-ai/sesori_relay_server/internal/auth"
	"github.com/sesori-ai/sesori_relay_server/internal/notifications"
	"github.com/sesori-ai/sesori_relay_server/internal/protocol"
)

type testEnv struct {
	relay      *Server
	httpServer *httptest.Server
	privateKey *rsa.PrivateKey
}

type testEnvOpts struct {
	requireBridgeID     bool
	notificationsClient *notifications.Client
}

func newTestEnv(t *testing.T, opts ...testEnvOpts) *testEnv {
	t.Helper()

	var o testEnvOpts
	if len(opts) > 0 {
		o = opts[0]
	}

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}

	pubDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})

	keyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(pubPEM)
	}))
	t.Cleanup(keyServer.Close)

	ks := auth.NewKeyStore(keyServer.URL)
	if err := ks.Load(); err != nil {
		t.Fatalf("KeyStore.Load: %v", err)
	}

	jwtAuth := auth.NewJWTAuthenticator(ks)
	relayServer := NewServer(":0", jwtAuth, o.notificationsClient, o.requireBridgeID)

	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		relayServer.handleWebSocket(w, r)
	}))
	t.Cleanup(httpSrv.Close)

	return &testEnv{
		relay:      relayServer,
		httpServer: httpSrv,
		privateKey: privateKey,
	}
}

func (e *testEnv) wsURL() string {
	return "ws" + strings.TrimPrefix(e.httpServer.URL, "http") + "/"
}

func (e *testEnv) makeToken(userID string) string {
	return e.makeAccessToken(userID)
}

func (e *testEnv) makeAccessToken(userID string) string {
	claims := jwt.MapClaims{
		"userId":    userID,
		"tokenType": "access",
		"aud":       "mobile",
		"iss":       "auth-backend",
		"exp":       float64(time.Now().Add(time.Hour).Unix()),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := token.SignedString(e.privateKey)
	if err != nil {
		panic(fmt.Sprintf("SignedString: %v", err))
	}
	return signed
}

// makeBridgeToken mints a legacy bridge-typed JWT. The auth server no longer
// issues these; the relay must reject them for both roles.
func (e *testEnv) makeBridgeToken(userID, bridgeID string) string {
	claims := jwt.MapClaims{
		"userId":    userID,
		"bridgeId":  bridgeID,
		"tokenType": "bridge",
		"aud":       "bridge",
		"iss":       "auth-backend",
		"exp":       float64(time.Now().Add(time.Hour).Unix()),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := token.SignedString(e.privateKey)
	if err != nil {
		panic(fmt.Sprintf("SignedString: %v", err))
	}
	return signed
}

func (e *testEnv) dial(ctx context.Context) (*websocket.Conn, error) {
	conn, _, err := websocket.Dial(ctx, e.wsURL(), nil)
	return conn, err
}

func sendAuth(ctx context.Context, conn *websocket.Conn, token, role, bridgeID string) error {
	var msg string
	if bridgeID == "" {
		msg = fmt.Sprintf(`{"type":"auth","token":"%s","role":"%s"}`, token, role)
	} else {
		msg = fmt.Sprintf(`{"type":"auth","token":"%s","role":"%s","bridgeId":%q}`, token, role, bridgeID)
	}
	return conn.Write(ctx, websocket.MessageText, []byte(msg))
}

func (e *testEnv) connectBridge(t *testing.T, userID string) *websocket.Conn {
	return e.connectBridgeWithID(t, userID, "br_defaultTest01")
}

func (e *testEnv) connectBridgeWithID(t *testing.T, userID, bridgeID string) *websocket.Conn {
	t.Helper()
	ctx := context.Background()
	conn, err := e.dial(ctx)
	if err != nil {
		t.Fatalf("dial bridge: %v", err)
	}
	if err := sendAuth(ctx, conn, e.makeAccessToken(userID), "bridge", bridgeID); err != nil {
		conn.CloseNow()
		t.Fatalf("sendAuth bridge: %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	return conn
}

func (e *testEnv) connectBridgeNoID(t *testing.T, userID string) *websocket.Conn {
	return e.connectBridgeWithID(t, userID, "")
}

func (e *testEnv) connectPhone(t *testing.T, userID string) (*websocket.Conn, []byte) {
	t.Helper()
	ctx := context.Background()
	conn, err := e.dial(ctx)
	if err != nil {
		t.Fatalf("dial phone: %v", err)
	}
	if err := sendAuth(ctx, conn, e.makeToken(userID), "phone", ""); err != nil {
		conn.CloseNow()
		t.Fatalf("sendAuth phone: %v", err)
	}
	firstMsg := readTextMsg(t, conn, 2*time.Second)
	return conn, firstMsg
}

func readTextMsg(t *testing.T, conn *websocket.Conn, timeout time.Duration) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	msgType, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("readTextMsg: %v", err)
	}
	if msgType != websocket.MessageText {
		t.Fatalf("expected text message, got type %d", msgType)
	}
	return data
}

func readBinaryMsg(t *testing.T, conn *websocket.Conn, timeout time.Duration) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	msgType, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("readBinaryMsg: %v", err)
	}
	if msgType != websocket.MessageBinary {
		t.Fatalf("expected binary message, got type %d", msgType)
	}
	return data
}

func expectClose(t *testing.T, conn *websocket.Conn, expected websocket.StatusCode) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err := conn.Read(ctx)
	if err == nil {
		t.Fatal("expected connection to be closed, but read succeeded")
	}
	code := websocket.CloseStatus(err)
	if code != expected {
		t.Errorf("expected close code %d, got %d (err: %v)", expected, code, err)
	}
}

func expectAnyClose(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err := conn.Read(ctx)
	if err == nil {
		t.Fatal("expected connection to be closed, but read succeeded")
	}
}

func expectNoMsg(t *testing.T, conn *websocket.Conn, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, _, err := conn.Read(ctx)
	if err == nil {
		t.Error("expected no message but received one")
	}
}

// expectOpen reads with a timeout and asserts the connection was NOT closed
// by the server: a read timeout means the connection is still open, while a
// close frame surfaces its status code.
func expectOpen(t *testing.T, conn *websocket.Conn, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, _, err := conn.Read(ctx)
	if err == nil {
		t.Fatal("expected no message on open connection, but received one")
	}
	if status := websocket.CloseStatus(err); status != -1 {
		t.Fatalf("expected connection to stay open, got close status %d (err: %v)", status, err)
	}
}

func parseJSON(t *testing.T, data []byte) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parseJSON %q: %v", data, err)
	}
	return m
}

func TestHandler_AuthRequired_BinaryMessage(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	conn, err := env.dial(ctx)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	if err := conn.Write(ctx, websocket.MessageBinary, []byte("binary")); err != nil {
		t.Fatalf("write: %v", err)
	}
	expectClose(t, conn, websocket.StatusCode(protocol.CloseAuthRequired))
}

func TestHandler_AuthFailure_InvalidJSON(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	conn, err := env.dial(ctx)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	if err := conn.Write(ctx, websocket.MessageText, []byte("not json")); err != nil {
		t.Fatalf("write: %v", err)
	}
	expectClose(t, conn, websocket.StatusCode(protocol.CloseAuthFailure))
}

func TestHandler_AuthFailure_InvalidToken(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	conn, err := env.dial(ctx)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"auth","token":"bad-token","role":"bridge"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	expectClose(t, conn, websocket.StatusCode(protocol.CloseAuthFailure))
}

func TestHandler_AuthFailure_InvalidRole(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	conn, err := env.dial(ctx)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	token := env.makeToken("user123")
	msg := fmt.Sprintf(`{"type":"auth","token":"%s","role":"admin"}`, token)
	if err := conn.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
		t.Fatalf("write: %v", err)
	}
	expectClose(t, conn, websocket.StatusCode(protocol.CloseAuthFailure))
}

func TestHandler_BridgeConnection(t *testing.T) {
	env := newTestEnv(t)

	bridge := env.connectBridge(t, "user1")
	defer bridge.CloseNow()

	if env.relay.manager.Count() != 1 {
		t.Errorf("expected 1 group, got %d", env.relay.manager.Count())
	}
}

func TestHandler_BridgeReplacement(t *testing.T) {
	env := newTestEnv(t)

	bridge1 := env.connectBridge(t, "user1")
	defer bridge1.CloseNow()

	bridge2 := env.connectBridge(t, "user1")
	defer bridge2.CloseNow()

	// A displaced bridge is closed with the dedicated CloseBridgeReplaced code
	// so it can recognise the takeover and back off instead of tight-looping.
	expectClose(t, bridge1, websocket.StatusCode(protocol.CloseBridgeReplaced))

	if env.relay.manager.Count() != 1 {
		t.Errorf("expected 1 group after replacement, got %d", env.relay.manager.Count())
	}
}

func TestHandler_PhoneNoBridge(t *testing.T) {
	env := newTestEnv(t)

	phone, firstMsg := env.connectPhone(t, "user1")
	defer phone.CloseNow()

	m := parseJSON(t, firstMsg)
	if m["type"] != "bridge_disconnected" {
		t.Errorf("expected bridge_disconnected, got %q", m["type"])
	}
}

func TestHandler_PhoneWithBridge(t *testing.T) {
	env := newTestEnv(t)

	bridge := env.connectBridge(t, "user1")
	defer bridge.CloseNow()

	phone, firstMsg := env.connectPhone(t, "user1")
	defer phone.CloseNow()

	m := parseJSON(t, firstMsg)
	if m["type"] != "bridge_connected" {
		t.Errorf("phone expected bridge_connected, got %q", m["type"])
	}

	bridgeMsg := readTextMsg(t, bridge, 2*time.Second)
	bm := parseJSON(t, bridgeMsg)
	if bm["type"] != "phone_connected" {
		t.Errorf("bridge expected phone_connected, got %q", bm["type"])
	}
	if _, ok := bm["connId"]; !ok {
		t.Error("phone_connected missing connId field")
	}
}

func TestHandler_PhoneCap(t *testing.T) {
	env := newTestEnv(t)

	phones := make([]*websocket.Conn, 5)
	for i := 0; i < 5; i++ {
		phones[i], _ = env.connectPhone(t, "user1")
		defer phones[i].CloseNow()
	}

	conn6, err := env.dial(context.Background())
	if err != nil {
		t.Fatalf("dial 6th phone: %v", err)
	}
	defer conn6.CloseNow()

	if err := sendAuth(context.Background(), conn6, env.makeToken("user1"), "phone", ""); err != nil {
		t.Fatalf("sendAuth 6th phone: %v", err)
	}
	expectClose(t, conn6, websocket.StatusCode(protocol.CloseAccountFull))
}

func TestHandler_BinaryBroadcast(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	bridge := env.connectBridge(t, "user1")
	defer bridge.CloseNow()

	phone1, _ := env.connectPhone(t, "user1")
	defer phone1.CloseNow()
	readTextMsg(t, bridge, 2*time.Second)

	phone2, _ := env.connectPhone(t, "user1")
	defer phone2.CloseNow()
	readTextMsg(t, bridge, 2*time.Second)

	payload := []byte("broadcast-payload")
	frame := append([]byte{0x00, 0x00}, payload...)
	if err := bridge.Write(ctx, websocket.MessageBinary, frame); err != nil {
		t.Fatalf("bridge write: %v", err)
	}

	msg1 := readBinaryMsg(t, phone1, 2*time.Second)
	msg2 := readBinaryMsg(t, phone2, 2*time.Second)

	if string(msg1) != string(payload) {
		t.Errorf("phone1: expected %q, got %q", payload, msg1)
	}
	if string(msg2) != string(payload) {
		t.Errorf("phone2: expected %q, got %q", payload, msg2)
	}
}

func TestHandler_BinaryUnicast(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	bridge := env.connectBridge(t, "user1")
	defer bridge.CloseNow()

	phone1, _ := env.connectPhone(t, "user1")
	defer phone1.CloseNow()
	readTextMsg(t, bridge, 2*time.Second)

	phone2, _ := env.connectPhone(t, "user1")
	defer phone2.CloseNow()
	phone2ConnMsg := readTextMsg(t, bridge, 2*time.Second)
	pm2 := parseJSON(t, phone2ConnMsg)
	connID2 := uint16(pm2["connId"].(float64))

	payload := []byte("unicast-payload")
	frame := make([]byte, 2+len(payload))
	binary.BigEndian.PutUint16(frame[:2], connID2)
	copy(frame[2:], payload)
	if err := bridge.Write(ctx, websocket.MessageBinary, frame); err != nil {
		t.Fatalf("bridge write: %v", err)
	}

	msg2 := readBinaryMsg(t, phone2, 2*time.Second)
	if string(msg2) != string(payload) {
		t.Errorf("phone2: expected %q, got %q", payload, msg2)
	}

	expectNoMsg(t, phone1, 200*time.Millisecond)
}

func TestHandler_PhoneToBridge(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	bridge := env.connectBridge(t, "user1")
	defer bridge.CloseNow()

	phone, _ := env.connectPhone(t, "user1")
	defer phone.CloseNow()

	bridgeMsg := readTextMsg(t, bridge, 2*time.Second)
	bm := parseJSON(t, bridgeMsg)
	connID := uint16(bm["connId"].(float64))

	payload := []byte("from-phone")
	if err := phone.Write(ctx, websocket.MessageBinary, payload); err != nil {
		t.Fatalf("phone write: %v", err)
	}

	data := readBinaryMsg(t, bridge, 2*time.Second)
	if len(data) < 2 {
		t.Fatalf("expected ≥2 bytes, got %d", len(data))
	}
	gotConnID := binary.BigEndian.Uint16(data[:2])
	gotPayload := data[2:]

	if gotConnID != connID {
		t.Errorf("connId: expected %d, got %d", connID, gotConnID)
	}
	if string(gotPayload) != string(payload) {
		t.Errorf("payload: expected %q, got %q", payload, gotPayload)
	}
}

func TestHandler_BridgeDisconnect_NotifiesPhones(t *testing.T) {
	env := newTestEnv(t)

	bridge := env.connectBridge(t, "user1")

	phone, firstMsg := env.connectPhone(t, "user1")
	defer phone.CloseNow()

	m := parseJSON(t, firstMsg)
	if m["type"] != "bridge_connected" {
		t.Fatalf("phone expected bridge_connected, got %q", m["type"])
	}
	readTextMsg(t, bridge, 2*time.Second)

	bridge.CloseNow()

	phoneMsg := readTextMsg(t, phone, 3*time.Second)
	pm := parseJSON(t, phoneMsg)
	if pm["type"] != "bridge_disconnected" {
		t.Errorf("expected bridge_disconnected, got %q", pm["type"])
	}
}

func TestHandler_PhoneDisconnect_NotifiesBridge(t *testing.T) {
	env := newTestEnv(t)

	bridge := env.connectBridge(t, "user1")
	defer bridge.CloseNow()

	phone, _ := env.connectPhone(t, "user1")

	bridgeMsg := readTextMsg(t, bridge, 2*time.Second)
	bm := parseJSON(t, bridgeMsg)
	connID := uint16(bm["connId"].(float64))

	phone.CloseNow()

	bridgeMsg2 := readTextMsg(t, bridge, 3*time.Second)
	dm := parseJSON(t, bridgeMsg2)
	if dm["type"] != "phone_disconnected" {
		t.Errorf("expected phone_disconnected, got %q", dm["type"])
	}
	if uint16(dm["connId"].(float64)) != connID {
		t.Errorf("connId: expected %d, got %v", connID, dm["connId"])
	}
}

func TestBinaryFraming_ConnIDParsing(t *testing.T) {
	cases := []struct {
		frame    []byte
		wantID   uint16
		wantData []byte
	}{
		{[]byte{0x00, 0x00, 'a', 'b'}, 0, []byte{'a', 'b'}},
		{[]byte{0x00, 0x03, 'x'}, 3, []byte{'x'}},
		{[]byte{0x01, 0x00, 'y'}, 256, []byte{'y'}},
		{[]byte{0xFF, 0xFF, 'z'}, 65535, []byte{'z'}},
	}

	for _, c := range cases {
		gotID := binary.BigEndian.Uint16(c.frame[:2])
		gotData := c.frame[2:]
		if gotID != c.wantID {
			t.Errorf("frame %v: connId expected %d, got %d", c.frame[:2], c.wantID, gotID)
		}
		if string(gotData) != string(c.wantData) {
			t.Errorf("frame %v: data expected %q, got %q", c.frame[:2], c.wantData, gotData)
		}
	}
}

func TestBinaryFraming_PhonePrepend(t *testing.T) {
	connID := uint16(42)
	payload := []byte("hello")

	framed := make([]byte, 2+len(payload))
	binary.BigEndian.PutUint16(framed[:2], connID)
	copy(framed[2:], payload)

	if binary.BigEndian.Uint16(framed[:2]) != connID {
		t.Errorf("expected connId %d in prefix", connID)
	}
	if string(framed[2:]) != string(payload) {
		t.Errorf("expected payload %q after prefix", payload)
	}
}

func TestHandler_BridgeID_StoredOnConnection(t *testing.T) {
	env := newTestEnv(t)

	bridge := env.connectBridgeWithID(t, "user1", "br_abc12345")
	defer bridge.CloseNow()

	g := env.relay.manager.GetOrCreateGroup("user1")
	g.mu.Lock()
	bridgeID := ""
	if g.Bridge != nil {
		bridgeID = g.Bridge.BridgeID
	}
	g.mu.Unlock()
	if bridgeID == "" {
		t.Fatal("expected bridge to be set on group")
	}
	if bridgeID != "br_abc12345" {
		t.Errorf("expected bridgeId br_abc12345 on connection, got %q", bridgeID)
	}
}

func TestHandler_BridgeAuth_RequiresBridgeID_PostTransition(t *testing.T) {
	env := newTestEnv(t, testEnvOpts{requireBridgeID: true})

	bridge := env.connectBridgeNoID(t, "user1")
	expectClose(t, bridge, websocket.StatusCode(protocol.CloseAuthFailure))

	if env.relay.manager.Count() != 0 {
		t.Errorf("expected 0 groups (rejected), got %d", env.relay.manager.Count())
	}
}

func TestHandler_BridgeAuth_AcceptsBridgeID_PostTransition(t *testing.T) {
	env := newTestEnv(t, testEnvOpts{requireBridgeID: true})

	bridge := env.connectBridgeWithID(t, "user1", "br_abc12345")
	defer bridge.CloseNow()

	if env.relay.manager.Count() != 1 {
		t.Errorf("expected 1 group, got %d", env.relay.manager.Count())
	}
}

func TestHandler_BridgeAuth_AcceptsAccessTokenWithBridgeID(t *testing.T) {
	env := newTestEnv(t, testEnvOpts{requireBridgeID: false})
	ctx := context.Background()

	conn, err := env.dial(ctx)
	if err != nil {
		t.Fatalf("dial bridge: %v", err)
	}
	defer conn.CloseNow()

	if err := sendAuth(ctx, conn, env.makeAccessToken("user1"), "bridge", "br_abc12345"); err != nil {
		t.Fatalf("sendAuth bridge: %v", err)
	}
	time.Sleep(60 * time.Millisecond)

	if env.relay.manager.Count() != 1 {
		t.Errorf("expected 1 group (access token + bridgeId accepted), got %d", env.relay.manager.Count())
	}
}

func TestHandler_BridgeAuth_RejectsBridgeTypedToken(t *testing.T) {
	env := newTestEnv(t, testEnvOpts{requireBridgeID: true})
	ctx := context.Background()

	conn, err := env.dial(ctx)
	if err != nil {
		t.Fatalf("dial bridge: %v", err)
	}
	defer conn.CloseNow()

	if err := sendAuth(ctx, conn, env.makeBridgeToken("user1", "br_tokenBridge01"), "bridge", "br_tokenBridge01"); err != nil {
		t.Fatalf("sendAuth bridge: %v", err)
	}
	expectClose(t, conn, websocket.StatusCode(protocol.CloseAuthFailure))
}

func TestHandler_BridgeAuth_RejectsBridgeTokenWithoutBridgeID(t *testing.T) {
	env := newTestEnv(t, testEnvOpts{requireBridgeID: false})
	ctx := context.Background()

	conn, err := env.dial(ctx)
	if err != nil {
		t.Fatalf("dial bridge: %v", err)
	}
	defer conn.CloseNow()

	if err := sendAuth(ctx, conn, env.makeBridgeToken("user1", "br_tokenBridge01"), "bridge", ""); err != nil {
		t.Fatalf("sendAuth bridge: %v", err)
	}
	expectClose(t, conn, websocket.StatusCode(protocol.CloseAuthFailure))
}

func TestHandler_BridgeAuth_LegacyAllowedInTransition(t *testing.T) {
	env := newTestEnv(t, testEnvOpts{requireBridgeID: false})

	bridge := env.connectBridgeNoID(t, "user1")
	defer bridge.CloseNow()

	if env.relay.manager.Count() != 1 {
		t.Errorf("expected 1 group (legacy accepted in transition), got %d", env.relay.manager.Count())
	}
}

func TestHandler_BridgeAuth_InvalidBridgeID_Format(t *testing.T) {
	env := newTestEnv(t, testEnvOpts{requireBridgeID: false})

	bridge := env.connectBridgeWithID(t, "user1", "not-a-valid-bridge-id")
	expectClose(t, bridge, websocket.StatusCode(protocol.CloseAuthFailure))
}

func TestHandler_BridgeAuth_InvalidBridgeID_FormatPostTransition(t *testing.T) {
	env := newTestEnv(t, testEnvOpts{requireBridgeID: true})

	bridge := env.connectBridgeWithID(t, "user1", "br_short")
	expectClose(t, bridge, websocket.StatusCode(protocol.CloseAuthFailure))
}

func TestHandler_PhoneAuth_IgnoresStrayBridgeID(t *testing.T) {
	env := newTestEnv(t, testEnvOpts{requireBridgeID: true})

	ctx := context.Background()
	conn, err := env.dial(ctx)
	if err != nil {
		t.Fatalf("dial phone: %v", err)
	}
	defer conn.CloseNow()

	token := env.makeToken("user1")
	msg := fmt.Sprintf(`{"type":"auth","token":"%s","role":"phone","bridgeId":"br_strayValue"}`, token)
	if err := conn.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
		t.Fatalf("sendAuth: %v", err)
	}

	firstMsg := readTextMsg(t, conn, 2*time.Second)
	m := parseJSON(t, firstMsg)
	if m["type"] != "bridge_disconnected" {
		t.Errorf("phone should be accepted; expected bridge_disconnected, got %q", m["type"])
	}
}

func TestHandler_PhoneAuth_RejectsBridgeToken(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	conn, err := env.dial(ctx)
	if err != nil {
		t.Fatalf("dial phone: %v", err)
	}
	defer conn.CloseNow()

	if err := sendAuth(ctx, conn, env.makeBridgeToken("user1", "br_phoneBridge1"), "phone", ""); err != nil {
		t.Fatalf("sendAuth phone: %v", err)
	}
	expectClose(t, conn, websocket.StatusCode(protocol.CloseAuthFailure))
}

// --- end-to-end: bridgeId is forwarded in the /internal/bridge-status payload ---

func TestHandler_NotificationsClient_ForwardsBridgeID(t *testing.T) {
	const secret = "test-relay-secret"
	const bridgeID = "br_endToEnd1234"

	type captured struct {
		userID   string
		bridgeID string
		status   string
	}

	capturedCh := make(chan captured, 4)
	notifServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Relay-Secret"); got != secret {
			t.Errorf("expected X-Relay-Secret %q, got %q", secret, got)
		}
		var payload struct {
			UserID   string `json:"userId"`
			BridgeID string `json:"bridgeId"`
			Status   string `json:"status"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		select {
		case capturedCh <- captured{userID: payload.UserID, bridgeID: payload.BridgeID, status: payload.Status}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(notifServer.Close)

	notif := notifications.NewClient(notifServer.URL, secret)
	env := newTestEnv(t, testEnvOpts{requireBridgeID: true, notificationsClient: notif})

	conn := env.connectBridgeWithID(t, "user1", bridgeID)

	select {
	case c := <-capturedCh:
		if c.userID != "user1" {
			t.Errorf("expected userId user1, got %q", c.userID)
		}
		if c.bridgeID != bridgeID {
			t.Errorf("expected bridgeId %q, got %q", bridgeID, c.bridgeID)
		}
		if c.status != notifications.BridgeStatusConnected {
			t.Errorf("expected status connected, got %q", c.status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for connected notification")
	}

	conn.CloseNow()

	select {
	case c := <-capturedCh:
		if c.userID != "user1" {
			t.Errorf("expected userId user1, got %q", c.userID)
		}
		if c.bridgeID != bridgeID {
			t.Errorf("expected bridgeId %q, got %q", bridgeID, c.bridgeID)
		}
		if c.status != notifications.BridgeStatusDisconnected {
			t.Errorf("expected status disconnected, got %q", c.status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for disconnected notification")
	}
}

func TestHandler_BridgeReplacement_MarksDistinctOldBridgeDisconnected(t *testing.T) {
	const secret = "test-relay-secret"

	type captured struct {
		bridgeID string
		status   string
	}

	capturedCh := make(chan captured, 8)
	readCaptured := func(label string) captured {
		t.Helper()
		select {
		case event := <-capturedCh:
			return event
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %s notification", label)
			return captured{}
		}
	}
	notifServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Relay-Secret"); got != secret {
			t.Errorf("expected X-Relay-Secret %q, got %q", secret, got)
		}
		var payload struct {
			BridgeID string `json:"bridgeId"`
			Status   string `json:"status"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		capturedCh <- captured{bridgeID: payload.BridgeID, status: payload.Status}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(notifServer.Close)

	notif := notifications.NewClient(notifServer.URL, secret)
	env := newTestEnv(t, testEnvOpts{requireBridgeID: true, notificationsClient: notif})
	oldBridge := env.connectBridgeWithID(t, "user1", "br_oldBridge01")

	first := readCaptured("old bridge connected")
	if first.bridgeID != "br_oldBridge01" || first.status != notifications.BridgeStatusConnected {
		t.Fatalf("expected old bridge connected event, got %+v", first)
	}

	newBridge := env.connectBridgeWithID(t, "user1", "br_newBridge01")
	defer newBridge.CloseNow()
	defer oldBridge.CloseNow()

	// A real displaced bridge keeps reading, so it observes and replies to the
	// server's CloseBridgeReplaced frame, completing the close handshake
	// promptly (which then unblocks the old connection's teardown). Drain the
	// old bridge here so the server's close does not sit out its full
	// handshake-wait timeout waiting on an unresponsive peer.
	go func() {
		for {
			if _, _, err := oldBridge.Read(context.Background()); err != nil {
				return
			}
		}
	}()

	replacementEvents := []captured{
		readCaptured("new bridge connected or old bridge disconnected"),
		readCaptured("new bridge connected or old bridge disconnected"),
	}
	seenNewConnected := false
	seenOldDisconnected := false
	for _, event := range replacementEvents {
		if event.bridgeID == "br_newBridge01" && event.status == notifications.BridgeStatusConnected {
			seenNewConnected = true
		}
		if event.bridgeID == "br_oldBridge01" && event.status == notifications.BridgeStatusDisconnected {
			seenOldDisconnected = true
		}
	}
	if !seenNewConnected || !seenOldDisconnected {
		t.Fatalf("expected new connected and old disconnected events, got %+v", replacementEvents)
	}

	newBridge.CloseNow()
	fourth := readCaptured("new bridge disconnected")
	if fourth.bridgeID != "br_newBridge01" || fourth.status != notifications.BridgeStatusDisconnected {
		t.Fatalf("expected new bridge disconnected event, got %+v", fourth)
	}
}

// --- revocation enforcement: 404 on the connect-time status report closes
// the bridge with CloseBridgeRevoked; everything else is fail-open ---

func TestHandler_BridgeRevoked_ConnectReport404_ClosesWith4006(t *testing.T) {
	const secret = "test-relay-secret"

	notifServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Relay-Secret"); got != secret {
			t.Errorf("expected X-Relay-Secret %q, got %q", secret, got)
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(notifServer.Close)

	notif := notifications.NewClient(notifServer.URL, secret)
	env := newTestEnv(t, testEnvOpts{requireBridgeID: true, notificationsClient: notif})

	conn := env.connectBridgeWithID(t, "user1", "br_revoked001")
	defer conn.CloseNow()

	expectClose(t, conn, websocket.StatusCode(protocol.CloseBridgeRevoked))

	deadline := time.Now().Add(2 * time.Second)
	for env.relay.manager.Count() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("expected group cleanup after revoked close, got %d groups", env.relay.manager.Count())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHandler_BridgeRevoked_ConnectReport5xx_FailOpen(t *testing.T) {
	notifServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(notifServer.Close)

	notif := notifications.NewClient(notifServer.URL, "test-relay-secret")
	env := newTestEnv(t, testEnvOpts{requireBridgeID: true, notificationsClient: notif})

	conn := env.connectBridgeWithID(t, "user1", "br_abc12345")
	defer conn.CloseNow()

	time.Sleep(150 * time.Millisecond) // let the async status report complete

	if env.relay.manager.Count() != 1 {
		t.Fatalf("expected bridge to stay connected on 5xx, got %d groups", env.relay.manager.Count())
	}
	expectOpen(t, conn, 300*time.Millisecond)
}

func TestHandler_BridgeRevoked_ConnectReportTransportError_FailOpen(t *testing.T) {
	notifServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	unreachableURL := notifServer.URL
	notifServer.Close()

	notif := notifications.NewClient(unreachableURL, "test-relay-secret")
	env := newTestEnv(t, testEnvOpts{requireBridgeID: true, notificationsClient: notif})

	conn := env.connectBridgeWithID(t, "user1", "br_abc12345")
	defer conn.CloseNow()

	time.Sleep(150 * time.Millisecond) // let the async status report fail

	if env.relay.manager.Count() != 1 {
		t.Fatalf("expected bridge to stay connected on transport error, got %d groups", env.relay.manager.Count())
	}
	expectOpen(t, conn, 300*time.Millisecond)
}

func TestHandler_BridgeRevoked_LegacyNoBridgeID_404_FailOpen(t *testing.T) {
	notifServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(notifServer.Close)

	notif := notifications.NewClient(notifServer.URL, "test-relay-secret")
	env := newTestEnv(t, testEnvOpts{requireBridgeID: false, notificationsClient: notif})

	conn := env.connectBridgeNoID(t, "user1")
	defer conn.CloseNow()

	time.Sleep(150 * time.Millisecond) // let the async status report complete

	if env.relay.manager.Count() != 1 {
		t.Fatalf("expected legacy bridge to stay connected, got %d groups", env.relay.manager.Count())
	}
	expectOpen(t, conn, 300*time.Millisecond)
}

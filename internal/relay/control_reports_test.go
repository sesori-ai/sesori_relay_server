package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/sesori-ai/sesori_relay_server/internal/notifications"
	"github.com/sesori-ai/sesori_relay_server/internal/protocol"
)

func TestControlReportsCoalesceWithoutBlockingSocketProducer(t *testing.T) {
	received := make(chan notifications.BridgeStatusPayload, 3)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload notifications.BridgeStatusPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode report: %v", err)
		}
		received <- payload
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	send := newControlReportSender(ctx, notifications.NewClient(server.URL, "test-secret"))
	readReport := func() notifications.BridgeStatusPayload {
		t.Helper()
		select {
		case report := <-received:
			return report
		case <-time.After(2 * time.Second):
			t.Fatal("report did not arrive")
			return notifications.BridgeStatusPayload{}
		}
	}
	send(notifications.BridgeStatusPayload{NotificationPolicy: protocol.ConnectionNotificationPolicyConservative})
	if first := readReport(); first.NotificationPolicy != protocol.ConnectionNotificationPolicyConservative {
		t.Fatalf("wrong initial report: %+v", first)
	}
	producerDone := make(chan struct{})
	go func() {
		for i := 0; i < 10_000; i++ {
			send(notifications.BridgeStatusPayload{NotificationPolicy: protocol.ConnectionNotificationPolicySuppress})
		}
		send(notifications.BridgeStatusPayload{NotificationPolicy: protocol.ConnectionNotificationPolicyNormal})
		close(producerDone)
	}()
	select {
	case <-producerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("socket producer blocked behind notification HTTP")
	}
	select {
	case unexpected := <-received:
		t.Fatalf("more than one concurrent HTTP request: %+v", unexpected)
	default:
	}
	close(release)
	if last := readReport(); last.NotificationPolicy != protocol.ConnectionNotificationPolicyNormal {
		t.Fatalf("queued report was not the latest policy: %+v", last)
	}
}

func TestControlReportRequestEndsWithConnectionContext(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload notifications.BridgeStatusPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode report: %v", err)
		}
		close(started)
		<-r.Context().Done()
		close(cancelled)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	send := newControlReportSender(ctx, notifications.NewClient(server.URL, "test-secret"))
	send(notifications.BridgeStatusPayload{Event: "connection_observed"})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("report did not start")
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("report survived connection cancellation")
	}
}

func TestDeviceObservationUUIDCase(t *testing.T) {
	for _, value := range []string{
		"123e4567-e89b-42d3-a456-426614174000",
		"123E4567-E89B-42D3-A456-426614174000",
		"123e4567-E89b-42D3-a456-426614174000",
	} {
		if !deviceIDRegexp.MatchString(value) {
			t.Errorf("rejected valid device UUID: %s", value)
		}
	}
	if deviceIDRegexp.MatchString("not-a-device-id") {
		t.Fatal("accepted invalid device ID")
	}
}

func TestControlReportsUseEffectiveTargetBridge(t *testing.T) {
	reports := make(chan notifications.BridgeStatusPayload, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload notifications.BridgeStatusPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode report: %v", err)
		}
		reports <- payload
	}))
	defer server.Close()
	env := newTestEnv(t, testEnvOpts{notificationsClient: notifications.NewClient(server.URL, "test-secret")})
	readReport := func(label string, matches func(notifications.BridgeStatusPayload) bool) notifications.BridgeStatusPayload {
		t.Helper()
		deadline := time.After(2 * time.Second)
		for {
			select {
			case report := <-reports:
				if matches(report) {
					return report
				}
			case <-deadline:
				t.Fatalf("timed out waiting for %s", label)
				return notifications.BridgeStatusPayload{}
			}
		}
	}
	connected := func(bridgeID string) func(notifications.BridgeStatusPayload) bool {
		return func(report notifications.BridgeStatusPayload) bool {
			return report.BridgeID == bridgeID && report.Status == notifications.BridgeStatusConnected && report.Event == ""
		}
	}

	first := env.connectBridgeWithID(t, "user1", "br_firstBridge1")
	defer first.CloseNow()
	firstConnectionID := readReport("first connected", connected("br_firstBridge1")).ConnectionID
	second := env.connectBridgeWithID(t, "user1", "br_secondBridge")
	defer second.CloseNow()
	secondConnectionID := readReport("second connected", connected("br_secondBridge")).ConnectionID

	pinned, _ := env.connectPhoneWithBridgeID(t, "user1", "br_firstBridge1")
	defer pinned.CloseNow()
	unpinned, _ := env.connectPhone(t, "user1")
	defer unpinned.CloseNow()

	// Each phone's observation names the bridge connection it talks to.
	observe := func(phone *websocket.Conn, deviceID string) {
		t.Helper()
		message := `{"type":"bridge_connection_observed","deviceId":"` + deviceID + `"}`
		if err := phone.Write(context.Background(), websocket.MessageText, []byte(message)); err != nil {
			t.Fatalf("send observation: %v", err)
		}
	}
	const pinnedDevice = "123e4567-e89b-42d3-a456-426614174001"
	const unpinnedDevice = "123e4567-e89b-42d3-a456-426614174002"
	observe(pinned, pinnedDevice)
	report := readReport("pinned observation", func(r notifications.BridgeStatusPayload) bool { return r.DeviceID == pinnedDevice })
	if report.BridgeID != "br_firstBridge1" || report.ConnectionID != firstConnectionID {
		t.Errorf("pinned observation reported %s/%s, want the first bridge", report.BridgeID, report.ConnectionID)
	}
	observe(unpinned, unpinnedDevice)
	report = readReport("unpinned observation", func(r notifications.BridgeStatusPayload) bool { return r.DeviceID == unpinnedDevice })
	if report.BridgeID != "br_secondBridge" || report.ConnectionID != secondConnectionID {
		t.Errorf("unpinned observation reported %s/%s, want the default bridge", report.BridgeID, report.ConnectionID)
	}

	// A bridge that is not the default still reports its own policy.
	if err := first.Write(context.Background(), websocket.MessageText, []byte(
		`{"type":"bridge_connection_notification_policy","policy":"suppress"}`,
	)); err != nil {
		t.Fatalf("send policy: %v", err)
	}
	report = readReport("first policy", func(r notifications.BridgeStatusPayload) bool { return r.Event == "notification_policy" })
	if report.BridgeID != "br_firstBridge1" || report.ConnectionID != firstConnectionID || report.NotificationPolicy != protocol.ConnectionNotificationPolicySuppress {
		t.Errorf("policy report = %+v, want the first bridge's suppress policy", report)
	}
}

package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClient_NotifyBridgeStatus_Success(t *testing.T) {
	const secret = "test-secret"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/internal/bridge-status" {
			t.Fatalf("expected /internal/bridge-status, got %s", r.URL.Path)
		}
		if got := r.Header.Get("X-Relay-Secret"); got != secret {
			t.Fatalf("expected X-Relay-Secret %q, got %q", secret, got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("expected Content-Type application/json, got %q", got)
		}

		var payload BridgeStatusPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}

		if payload.UserID != "user-123" {
			t.Fatalf("expected userId user-123, got %q", payload.UserID)
		}
		if payload.BridgeID != "br_abc12345" {
			t.Fatalf("expected bridgeId br_abc12345, got %q", payload.BridgeID)
		}
		if payload.Status != BridgeStatusConnected {
			t.Fatalf("expected status %s, got %q", BridgeStatusConnected, payload.Status)
		}
		if _, err := time.Parse(time.RFC3339, payload.Timestamp); err != nil {
			t.Fatalf("timestamp not RFC3339: %v", err)
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := NewClient(ts.URL, secret)
	err := client.NotifyBridgeStatus(context.Background(), "user-123", "br_abc12345", BridgeStatusConnected)
	if err != nil {
		t.Fatalf("NotifyBridgeStatus returned error: %v", err)
	}
}

func TestClient_NotifyBridgeStatus_LegacyNoBridgeID(t *testing.T) {
	const secret = "test-secret"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read payload: %v", err)
		}
		if strings.Contains(string(body), "bridgeId") {
			t.Fatalf("expected payload to omit bridgeId key, got %s", string(body))
		}

		var payload BridgeStatusPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload.UserID != "user-123" {
			t.Fatalf("expected userId user-123, got %q", payload.UserID)
		}
		if payload.BridgeID != "" {
			t.Fatalf("expected empty bridgeId in legacy path, got %q", payload.BridgeID)
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := NewClient(ts.URL, secret)
	if err := client.NotifyBridgeStatus(context.Background(), "user-123", "", BridgeStatusConnected); err != nil {
		t.Fatalf("NotifyBridgeStatus returned error: %v", err)
	}
}

func TestClient_NotifyBridgeStatus_Unauthorized(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer ts.Close()

	client := NewClient(ts.URL, "secret")
	err := client.NotifyBridgeStatus(context.Background(), "user-123", "br_abc12345", BridgeStatusConnected)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected status: 401") {
		t.Fatalf("expected unexpected status: 401 error, got %v", err)
	}
}

func TestClient_NotifyBridgeStatus_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	client := NewClient(ts.URL, "secret")
	err := client.NotifyBridgeStatus(context.Background(), "user-123", "br_abc12345", BridgeStatusDisconnected)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected status") {
		t.Fatalf("expected unexpected status error, got %v", err)
	}
	if errors.Is(err, ErrBridgeNotFound) {
		t.Fatalf("5xx must not map to ErrBridgeNotFound, got %v", err)
	}
}

func TestClient_NotifyBridgeStatus_NotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	client := NewClient(ts.URL, "secret")
	err := client.NotifyBridgeStatus(context.Background(), "user-123", "br_abc12345", BridgeStatusConnected)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrBridgeNotFound) {
		t.Fatalf("expected ErrBridgeNotFound, got %v", err)
	}
}

func TestClient_NotifyBridgeStatus_ServerUnreachable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	url := ts.URL
	ts.Close()

	client := NewClient(url, "secret")
	err := client.NotifyBridgeStatus(context.Background(), "user-123", "br_abc12345", BridgeStatusConnected)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if errors.Is(err, ErrBridgeNotFound) {
		t.Fatalf("transport errors must not map to ErrBridgeNotFound, got %v", err)
	}
}

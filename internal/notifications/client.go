package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

const (
	BridgeStatusConnected    = "connected"
	BridgeStatusDisconnected = "disconnected"
)

// ErrBridgeNotFound is returned when the auth server responds with an explicit
// HTTP 404 to a bridge-status report, meaning the reported bridgeId is unknown,
// revoked, or not owned by the user. Transport errors and other status codes
// return generic errors so callers can stay fail-open on them.
var ErrBridgeNotFound = errors.New("bridge not found")

type BridgeStatusPayload struct {
	UserID             string `json:"userId"`
	BridgeID           string `json:"bridgeId"`
	Status             string `json:"status"`
	Timestamp          string `json:"timestamp"`
	Event              string `json:"event,omitempty"`
	NotificationPolicy string `json:"notificationPolicy,omitempty"`
	ConnectionID       string `json:"connectionId,omitempty"`
	DeviceID           string `json:"deviceId,omitempty"`
}

type Client struct {
	baseURL    string
	secret     string
	httpClient *http.Client
}

func NewClient(baseURL string, secret string) *Client {
	return &Client{
		baseURL: baseURL,
		secret:  secret,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

func (c *Client) NotifyBridgeStatus(ctx context.Context, payload BridgeStatusPayload) error {

	if payload.Timestamp == "" {
		payload.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/internal/bridge-status", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Relay-Secret", c.secret)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return ErrBridgeNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	return nil
}

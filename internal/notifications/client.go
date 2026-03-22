package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type BridgeStatusPayload struct {
	UserID    string `json:"userId"`
	Status    string `json:"status"`
	Timestamp string `json:"timestamp"`
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

func (c *Client) NotifyBridgeStatus(ctx context.Context, userID string, status string) error {
	payload := BridgeStatusPayload{
		UserID:    userID,
		Status:    status,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
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

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	return nil
}

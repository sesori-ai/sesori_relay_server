package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sync"

	"github.com/anthropics/remote-relay/internal/protocol"
	"github.com/coder/websocket"
)

type RelayClient struct {
	conn        *websocket.Conn
	roomCode    string
	keyExchange *KeyExchange
	relayURL    string
	mu          sync.Mutex
}

func NewRelayClient(relayURL string) *RelayClient {
	return &RelayClient{relayURL: relayURL}
}

func (c *RelayClient) Connect(ctx context.Context) error {
	roomCode, err := protocol.GenerateRoomCode()
	if err != nil {
		return fmt.Errorf("generate room code: %w", err)
	}

	kx, err := NewKeyExchange()
	if err != nil {
		return fmt.Errorf("initialize key exchange: %w", err)
	}

	wsURL, err := url.JoinPath(c.relayURL, "ws", roomCode)
	if err != nil {
		return fmt.Errorf("build websocket URL: %w", err)
	}

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("connect to relay websocket: %w", err)
	}

	c.mu.Lock()
	c.conn = conn
	c.roomCode = roomCode
	c.keyExchange = kx
	c.mu.Unlock()

	return nil
}

func (c *RelayClient) RoomCode() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.roomCode
}

func (c *RelayClient) KeyExchange() *KeyExchange {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.keyExchange
}

func (c *RelayClient) WaitForPeer(ctx context.Context) error {
	c.mu.Lock()
	conn := c.conn
	kx := c.keyExchange
	c.mu.Unlock()

	if conn == nil {
		return fmt.Errorf("websocket connection is not established")
	}
	if kx == nil {
		return fmt.Errorf("key exchange is not initialized")
	}

	_, payload, err := conn.Read(ctx)
	if err != nil {
		return fmt.Errorf("read key exchange message: %w", err)
	}

	var msg protocol.KeyExchangeMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return fmt.Errorf("unmarshal key exchange message: %w", err)
	}
	if msg.Type != "key_exchange" {
		return fmt.Errorf("unexpected message type: %s", msg.Type)
	}

	if err := kx.HandleKeyExchangeMessage(msg); err != nil {
		return fmt.Errorf("complete key exchange: %w", err)
	}

	readyPayload, err := json.Marshal(protocol.ReadyMessage{Type: "ready"})
	if err != nil {
		return fmt.Errorf("marshal ready message: %w", err)
	}

	framed, err := protocol.Frame(readyPayload, kx.Encryptor())
	if err != nil {
		return fmt.Errorf("frame ready message: %w", err)
	}

	if err := conn.Write(ctx, websocket.MessageBinary, framed); err != nil {
		return fmt.Errorf("send ready message: %w", err)
	}

	return nil
}

func (c *RelayClient) Send(plaintext []byte) error {
	c.mu.Lock()
	conn := c.conn
	kx := c.keyExchange
	c.mu.Unlock()

	if conn == nil {
		return fmt.Errorf("websocket connection is not established")
	}
	if kx == nil || kx.Encryptor() == nil {
		return fmt.Errorf("key exchange is not complete")
	}

	framed, err := protocol.Frame(plaintext, kx.Encryptor())
	if err != nil {
		return fmt.Errorf("frame message: %w", err)
	}

	if err := conn.Write(context.Background(), websocket.MessageBinary, framed); err != nil {
		return fmt.Errorf("send message: %w", err)
	}

	return nil
}

func (c *RelayClient) Receive(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	conn := c.conn
	kx := c.keyExchange
	c.mu.Unlock()

	if conn == nil {
		return nil, fmt.Errorf("websocket connection is not established")
	}
	if kx == nil || kx.Encryptor() == nil {
		return nil, fmt.Errorf("key exchange is not complete")
	}

	msgType, payload, err := conn.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("read message: %w", err)
	}
	if msgType != websocket.MessageBinary {
		return nil, fmt.Errorf("unexpected websocket message type: %v", msgType)
	}

	plaintext, err := protocol.Unframe(payload, kx.Encryptor())
	if err != nil {
		return nil, fmt.Errorf("unframe message: %w", err)
	}

	return plaintext, nil
}

func (c *RelayClient) Close() error {
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.mu.Unlock()

	if conn == nil {
		return nil
	}

	return conn.Close(websocket.StatusNormalClosure, "")
}

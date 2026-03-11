package relay

import (
	"context"
	"sync"

	"github.com/coder/websocket"
)

type Connection struct {
	Conn   *websocket.Conn
	ConnID uint16
	Cancel context.CancelFunc
}

type AccountGroup struct {
	UserID     string
	Bridge     *Connection
	Phones     map[uint16]*Connection
	NextConnID uint16
	mu         sync.Mutex
}

func NewAccountGroup(userID string) *AccountGroup {
	return &AccountGroup{
		UserID:     userID,
		Phones:     make(map[uint16]*Connection),
		NextConnID: 1,
	}
}

func (g *AccountGroup) AssignConnID() uint16 {
	g.mu.Lock()
	defer g.mu.Unlock()

	id := g.NextConnID
	g.NextConnID++
	if g.NextConnID == 0 {
		g.NextConnID = 1
	}

	return id
}

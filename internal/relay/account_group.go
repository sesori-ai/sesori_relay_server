package relay

import (
	"context"
	"sync"

	"github.com/coder/websocket"
)

type Connection struct {
	Conn     *websocket.Conn
	ConnID   uint16
	Cancel   context.CancelFunc
	BridgeID string // empty for phones; populated for bridge connections (per-instance identifier from sesori_auth_server)
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

// AllPhones returns a snapshot slice of all phone connections.
func (g *AccountGroup) AllPhones() []*Connection {
	g.mu.Lock()
	defer g.mu.Unlock()
	phones := make([]*Connection, 0, len(g.Phones))
	for _, p := range g.Phones {
		phones = append(phones, p)
	}
	return phones
}

// IsEmpty returns true if the group has no bridge and no phone connections.
func (g *AccountGroup) IsEmpty() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.Bridge == nil && len(g.Phones) == 0
}

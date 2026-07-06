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

// PhonesIfCurrentBridge atomically snapshots all phone connections, but only if
// bridge is still this group's current bridge. A bridge displaced by a newer
// connection gets nil, so its read loop can never fan a frame out to phones —
// the current-bridge check and the target selection happen under one lock hold,
// leaving no window where a stale bridge could pass the check and then relay.
func (g *AccountGroup) PhonesIfCurrentBridge(bridge *Connection) []*Connection {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.Bridge != bridge {
		return nil
	}
	phones := make([]*Connection, 0, len(g.Phones))
	for _, p := range g.Phones {
		phones = append(phones, p)
	}
	return phones
}

// PhoneIfCurrentBridge atomically returns the phone with connID, but only if
// bridge is still this group's current bridge (nil otherwise). Same TOCTOU-free
// guarantee as PhonesIfCurrentBridge for the unicast routing path.
func (g *AccountGroup) PhoneIfCurrentBridge(bridge *Connection, connID uint16) *Connection {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.Bridge != bridge {
		return nil
	}
	return g.Phones[connID]
}

// IsEmpty returns true if the group has no bridge and no phone connections.
func (g *AccountGroup) IsEmpty() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.Bridge == nil && len(g.Phones) == 0
}

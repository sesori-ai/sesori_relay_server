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

// PhonesIfCurrentBridge snapshots all phone connections, but only if bridge is
// still this group's current bridge (nil otherwise) — the current-bridge check
// and the target selection happen under one lock hold. This drops all frames
// from a bridge that was already displaced before it routes them.
//
// It does NOT hold the lock across the caller's subsequent websocket writes
// (that would serialize all relay traffic on socket I/O), so a bridge that is
// current at snapshot time but displaced microseconds later can still deliver
// the single frame it is mid-routing. That residual frame is one the bridge
// read while it was the active bridge, so delivering it during handover is
// benign — indistinguishable from a frame sent just before the swap. Closing
// that last window would require writing under the group lock, which is not
// worth the contention.
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

// PhoneIfCurrentBridge returns the phone with connID, but only if bridge is
// still this group's current bridge (nil otherwise). Same narrowed-window
// characterization as PhonesIfCurrentBridge for the unicast routing path: the
// check + lookup are atomic, but the caller's write is not under the lock.
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

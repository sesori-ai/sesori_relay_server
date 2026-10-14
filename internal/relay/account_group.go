package relay

import (
	"context"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type Connection struct {
	Conn   *websocket.Conn
	ConnID uint16
	Cancel context.CancelFunc
	// BridgeID is the bridge's own id (per-instance identifier from
	// sesori_auth_server) for bridge connections, and the pinned bridge id for
	// phones; empty for a phone that named no bridge (it follows the default).
	BridgeID     string
	ConnectionID string    // ephemeral correlation for connection-notification policy only
	ConnectedAt  time.Time // bridges only; the latest one is the default bridge
}

// AccountGroup holds every live connection of one account: up to
// maxBridgesPerAccount bridges keyed by bridgeId and up to maxPhonesPerAccount
// phones keyed by connId. Each phone talks to at most one bridge, its effective
// target: its pinned bridge, else the default bridge (the most recently
// connected one). Bridges, Phones, and NextConnID are guarded by mu.
type AccountGroup struct {
	UserID     string
	Bridges    map[string]*Connection
	Phones     map[uint16]*Connection
	NextConnID uint16
	mu         sync.Mutex
}

// targetChange is a phone whose effective target bridge changed. previous is
// the old target only while it is still connected (it must learn that the
// phone left); next is nil when the phone has no bridge any more.
type targetChange struct {
	phone    *Connection
	previous *Connection
	next     *Connection
}

func NewAccountGroup(userID string) *AccountGroup {
	return &AccountGroup{
		UserID:     userID,
		Bridges:    make(map[string]*Connection),
		Phones:     make(map[uint16]*Connection),
		NextConnID: 1,
	}
}

func (g *AccountGroup) assignConnIDLocked() uint16 {
	id := g.NextConnID
	g.NextConnID++
	if g.NextConnID == 0 {
		g.NextConnID = 1
	}

	return id
}

// defaultBridgeLocked returns the most recently connected bridge, or nil.
// Equal timestamps fall back to the bridge id so the choice never flips
// between calls.
func (g *AccountGroup) defaultBridgeLocked() *Connection {
	var latest *Connection
	for _, bridge := range g.Bridges {
		if latest == nil || bridge.ConnectedAt.After(latest.ConnectedAt) ||
			(bridge.ConnectedAt.Equal(latest.ConnectedAt) && bridge.BridgeID > latest.BridgeID) {
			latest = bridge
		}
	}
	return latest
}

func (g *AccountGroup) targetLocked(phone *Connection) *Connection {
	if phone.BridgeID != "" {
		return g.Bridges[phone.BridgeID]
	}
	return g.defaultBridgeLocked()
}

// changeBridgesLocked applies mutate to the bridge map and reports every phone
// whose effective target changed as a result.
func (g *AccountGroup) changeBridgesLocked(mutate func()) []targetChange {
	before := make(map[*Connection]*Connection, len(g.Phones))
	for _, phone := range g.Phones {
		before[phone] = g.targetLocked(phone)
	}
	mutate()
	var changes []targetChange
	for phone, previous := range before {
		next := g.targetLocked(phone)
		if next == previous {
			continue
		}
		if previous != nil && g.Bridges[previous.BridgeID] != previous {
			previous = nil
		}
		changes = append(changes, targetChange{phone: phone, previous: previous, next: next})
	}
	return changes
}

// AddBridge registers bridge as the current connection for its id. It
// displaces a previous connection with the same id (returned so the caller can
// close it); a different id never displaces anyone. ok is false when the
// account already holds maxBridgesPerAccount bridges with other ids.
func (g *AccountGroup) AddBridge(bridge *Connection) (displaced *Connection, changes []targetChange, ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	displaced = g.Bridges[bridge.BridgeID]
	if displaced == nil && len(g.Bridges) >= maxBridgesPerAccount {
		return nil, nil, false
	}
	changes = g.changeBridgesLocked(func() { g.Bridges[bridge.BridgeID] = bridge })
	return displaced, changes, true
}

// RemoveBridge unregisters bridge if it is still the current connection for
// its id. A connection displaced by a same-id reconnect is not current and
// changes nothing.
func (g *AccountGroup) RemoveBridge(bridge *Connection) (wasCurrent bool, changes []targetChange) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.Bridges[bridge.BridgeID] != bridge {
		return false, nil
	}
	return true, g.changeBridgesLocked(func() { delete(g.Bridges, bridge.BridgeID) })
}

// IsCurrentBridge reports whether bridge is still the current connection for
// its id (not displaced by a same-id reconnect and not yet removed).
func (g *AccountGroup) IsCurrentBridge(bridge *Connection) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.Bridges[bridge.BridgeID] == bridge
}

// PhonesTargeting snapshots the phones whose effective target is bridge (nil
// when bridge is no longer current) — the current-bridge check and the target
// selection happen under one lock hold, so a bridge never reaches a phone that
// targets another bridge, and a displaced bridge reaches no phone at all.
//
// It does NOT hold the lock across the caller's subsequent websocket writes
// (that would serialize all relay traffic on socket I/O), so a frame read
// while bridge was a phone's target can still arrive just after the target
// changed. That frame is indistinguishable from one sent just before the
// change, and the phone re-keys on the bridge_connected it receives for its
// new target, so it is benign. Closing that window would require writing under
// the group lock, which is not worth the contention.
func (g *AccountGroup) PhonesTargeting(bridge *Connection) []*Connection {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.Bridges[bridge.BridgeID] != bridge {
		return nil
	}
	phones := make([]*Connection, 0, len(g.Phones))
	for _, phone := range g.Phones {
		if g.targetLocked(phone) == bridge {
			phones = append(phones, phone)
		}
	}
	return phones
}

// PhoneTargeting returns the phone with connID only if its effective target is
// bridge (nil otherwise). Same narrowed window as PhonesTargeting for unicast.
func (g *AccountGroup) PhoneTargeting(bridge *Connection, connID uint16) *Connection {
	g.mu.Lock()
	defer g.mu.Unlock()
	phone := g.Phones[connID]
	if phone == nil || g.Bridges[bridge.BridgeID] != bridge || g.targetLocked(phone) != bridge {
		return nil
	}
	return phone
}

// AddPhone assigns phone a free connId and registers it, unless the account is
// full, and returns its effective target bridge (nil when none).
func (g *AccountGroup) AddPhone(phone *Connection) (target *Connection, ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.Phones) >= maxPhonesPerAccount {
		return nil, false
	}
	for {
		phone.ConnID = g.assignConnIDLocked()
		if _, exists := g.Phones[phone.ConnID]; !exists {
			break
		}
	}
	g.Phones[phone.ConnID] = phone
	return g.targetLocked(phone), true
}

// RemovePhone unregisters phone and returns the bridge it was targeting.
func (g *AccountGroup) RemovePhone(phone *Connection) *Connection {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.Phones, phone.ConnID)
	return g.targetLocked(phone)
}

// BridgeForPhone returns phone's effective target bridge, or nil when phone is
// no longer registered or has no bridge.
func (g *AccountGroup) BridgeForPhone(phone *Connection) *Connection {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.Phones[phone.ConnID] != phone {
		return nil
	}
	return g.targetLocked(phone)
}

// IsEmpty returns true if the group has no bridge and no phone connections.
func (g *AccountGroup) IsEmpty() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.Bridges) == 0 && len(g.Phones) == 0
}

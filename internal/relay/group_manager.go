package relay

import "sync"

type GroupManager struct {
	groups map[string]*AccountGroup
	mu     sync.RWMutex
}

func NewGroupManager() *GroupManager {
	return &GroupManager{
		groups: make(map[string]*AccountGroup),
	}
}

func (m *GroupManager) GetOrCreateGroup(userID string) *AccountGroup {
	m.mu.Lock()
	defer m.mu.Unlock()

	if g, ok := m.groups[userID]; ok {
		return g
	}

	g := NewAccountGroup(userID)
	m.groups[userID] = g
	return g
}

// RemoveGroupIfEmpty deletes group from the manager only if it is still the
// current group instance registered for userID AND it is empty. Passing the
// caller's own group instance (rather than deleting by userID key alone) makes
// cleanup safe against group recreation: a stale handler whose teardown runs
// late must not delete a fresh group that a newer connection created for the
// same user in the meantime.
func (m *GroupManager) RemoveGroupIfEmpty(userID string, group *AccountGroup) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if g, ok := m.groups[userID]; ok && g == group {
		if g.IsEmpty() {
			delete(m.groups, userID)
		}
	}
}

func (m *GroupManager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.groups)
}

// HasLiveBridgeWithID reports whether the user's currently-registered group has
// a live bridge with bridgeID. A displaced handler uses this before emitting a
// stale BridgeStatusDisconnected: if a fresh connection already re-registered
// the same bridgeId (possibly in a recreated group), the old handler's late
// teardown must not mark that live bridge offline in the auth server. bridgeID
// must be non-empty (legacy bridges without an id are never matched).
func (m *GroupManager) HasLiveBridgeWithID(userID, bridgeID string) bool {
	if bridgeID == "" {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	g, ok := m.groups[userID]
	if !ok {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.Bridge != nil && g.Bridge.BridgeID == bridgeID
}

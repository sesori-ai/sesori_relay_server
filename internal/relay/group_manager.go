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

func (m *GroupManager) RemoveGroupIfEmpty(userID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if g, ok := m.groups[userID]; ok {
		g.mu.Lock()
		empty := g.Bridge == nil && len(g.Phones) == 0
		g.mu.Unlock()

		if empty {
			delete(m.groups, userID)
		}
	}
}

func (m *GroupManager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.groups)
}

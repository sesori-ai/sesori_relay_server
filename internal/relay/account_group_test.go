package relay

import (
	"sync"
	"testing"
	"time"
)

func TestNewAccountGroup(t *testing.T) {
	g := NewAccountGroup("user1")
	if g.UserID != "user1" {
		t.Errorf("expected UserID user1, got %s", g.UserID)
	}
	if g.NextConnID != 1 {
		t.Errorf("expected NextConnID 1, got %d", g.NextConnID)
	}
	if g.Phones == nil {
		t.Error("expected non-nil Phones map")
	}
	if g.Bridges == nil || len(g.Bridges) != 0 {
		t.Error("expected empty Bridges map")
	}
}

func TestAccountGroup_AssignConnID_StartsAtOne(t *testing.T) {
	g := NewAccountGroup("user1")
	id := g.assignConnIDLocked()
	if id != 1 {
		t.Errorf("expected first ID 1, got %d", id)
	}
}

func TestAccountGroup_AssignConnID_Monotonic(t *testing.T) {
	g := NewAccountGroup("user1")
	for i := uint16(1); i <= 10; i++ {
		id := g.assignConnIDLocked()
		if id != i {
			t.Errorf("expected ID %d, got %d", i, id)
		}
	}
}

func TestAccountGroup_AssignConnID_WrapSkipsZero(t *testing.T) {
	g := NewAccountGroup("user1")
	g.NextConnID = 65535

	id1 := g.assignConnIDLocked()
	id2 := g.assignConnIDLocked()

	if id1 != 65535 {
		t.Errorf("expected 65535, got %d", id1)
	}
	if id2 != 1 {
		t.Errorf("expected 1 (skip zero on wrap), got %d", id2)
	}
}

func TestAccountGroup_AssignConnID_NeverReturnsZero(t *testing.T) {
	g := NewAccountGroup("user1")
	g.NextConnID = 65534

	for i := 0; i < 5; i++ {
		id := g.assignConnIDLocked()
		if id == 0 {
			t.Errorf("AssignConnID returned 0 on iteration %d", i)
		}
	}
}

func TestAccountGroup_AddPhone_ConcurrentDistinctIDs(t *testing.T) {
	g := NewAccountGroup("user1")

	phones := make([]*Connection, maxPhonesPerAccount)
	var wg sync.WaitGroup
	for i := range phones {
		phones[i] = &Connection{}
		wg.Add(1)
		go func(phone *Connection) {
			defer wg.Done()
			if _, ok := g.AddPhone(phone); !ok {
				t.Error("AddPhone rejected a phone below the cap")
			}
		}(phones[i])
	}
	wg.Wait()

	seen := make(map[uint16]bool)
	for _, phone := range phones {
		if phone.ConnID == 0 || seen[phone.ConnID] {
			t.Errorf("connId %d is zero or duplicate", phone.ConnID)
		}
		seen[phone.ConnID] = true
	}
	if _, ok := g.AddPhone(&Connection{}); ok {
		t.Error("AddPhone accepted a phone above the cap")
	}
}

func TestGroupManager_GetOrCreateGroup_SameUser(t *testing.T) {
	m := NewGroupManager()

	g1 := m.GetOrCreateGroup("user1")
	g2 := m.GetOrCreateGroup("user1")

	if g1 != g2 {
		t.Error("expected same group pointer for same userID")
	}
	if m.Count() != 1 {
		t.Errorf("expected count 1, got %d", m.Count())
	}
}

func TestGroupManager_GetOrCreateGroup_DifferentUsers(t *testing.T) {
	m := NewGroupManager()

	g1 := m.GetOrCreateGroup("user1")
	g2 := m.GetOrCreateGroup("user2")

	if g1 == g2 {
		t.Error("expected different groups for different userIDs")
	}
	if m.Count() != 2 {
		t.Errorf("expected count 2, got %d", m.Count())
	}
}

func TestGroupManager_RemoveGroupIfEmpty_WithBridge(t *testing.T) {
	m := NewGroupManager()
	g := m.GetOrCreateGroup("user1")

	g.Bridges["br_x"] = &Connection{BridgeID: "br_x"}

	m.RemoveGroupIfEmpty("user1", g)

	if m.Count() != 1 {
		t.Errorf("group with bridge should not be removed, count=%d", m.Count())
	}
}

func TestGroupManager_RemoveGroupIfEmpty_WithPhone(t *testing.T) {
	m := NewGroupManager()
	g := m.GetOrCreateGroup("user1")

	g.Phones[1] = &Connection{}

	m.RemoveGroupIfEmpty("user1", g)

	if m.Count() != 1 {
		t.Errorf("group with phone should not be removed, count=%d", m.Count())
	}
}

func TestGroupManager_RemoveGroupIfEmpty_Empty(t *testing.T) {
	m := NewGroupManager()
	g1 := m.GetOrCreateGroup("user1")
	m.GetOrCreateGroup("user2")

	if m.Count() != 2 {
		t.Fatalf("expected count 2, got %d", m.Count())
	}

	m.RemoveGroupIfEmpty("user1", g1)

	if m.Count() != 1 {
		t.Errorf("empty group should be removed, count=%d", m.Count())
	}
}

func TestGroupManager_RemoveGroupIfEmpty_NonExistent(t *testing.T) {
	m := NewGroupManager()

	m.RemoveGroupIfEmpty("nonexistent", NewAccountGroup("nonexistent"))

	if m.Count() != 0 {
		t.Errorf("expected count 0, got %d", m.Count())
	}
}

func TestGroupManager_HasLiveBridgeWithID(t *testing.T) {
	m := NewGroupManager()

	// No group / no bridge yet.
	if m.HasLiveBridgeWithID("user1", "br_x") {
		t.Error("expected false with no group")
	}

	g := m.GetOrCreateGroup("user1")
	if m.HasLiveBridgeWithID("user1", "br_x") {
		t.Error("expected false with no bridge")
	}

	g.Bridges["br_x"] = &Connection{BridgeID: "br_x"}
	if !m.HasLiveBridgeWithID("user1", "br_x") {
		t.Error("expected true for the live bridge id")
	}
	if m.HasLiveBridgeWithID("user1", "br_y") {
		t.Error("expected false for a different bridge id")
	}
}

// A stale handler whose cleanup runs late must not delete a fresh group that a
// newer connection created for the same user after the stale handler's group
// was already removed.
func TestGroupManager_RemoveGroupIfEmpty_DoesNotRemoveRecreatedGroup(t *testing.T) {
	m := NewGroupManager()
	stale := m.GetOrCreateGroup("user1")

	// The stale group is removed (empty), then a fresh group is created for the
	// same user and gains a live bridge.
	m.RemoveGroupIfEmpty("user1", stale)
	fresh := m.GetOrCreateGroup("user1")
	if fresh == stale {
		t.Fatal("expected a distinct fresh group instance")
	}

	// The stale handler's late cleanup for the SAME userID must be a no-op: it
	// does not own the fresh group instance.
	m.RemoveGroupIfEmpty("user1", stale)

	if m.Count() != 1 {
		t.Errorf("fresh group must survive a stale handler's cleanup, count=%d", m.Count())
	}
	if got := m.GetOrCreateGroup("user1"); got != fresh {
		t.Error("fresh group instance must remain registered for the user")
	}
}

func TestGroupManager_Count_ReflectsRemovals(t *testing.T) {
	m := NewGroupManager()

	ga := m.GetOrCreateGroup("a")
	gb := m.GetOrCreateGroup("b")
	m.GetOrCreateGroup("c")

	if m.Count() != 3 {
		t.Fatalf("expected 3, got %d", m.Count())
	}

	m.RemoveGroupIfEmpty("a", ga)
	m.RemoveGroupIfEmpty("b", gb)

	if m.Count() != 1 {
		t.Errorf("expected 1 after two removals, got %d", m.Count())
	}
}

// --- multi-bridge routing: default selection and pins (target changes are
// covered end to end in handler_test.go) ---

func testBridge(id string, connectedAt time.Time) *Connection {
	return &Connection{BridgeID: id, ConnectedAt: connectedAt}
}

func addTestBridge(t *testing.T, g *AccountGroup, bridge *Connection) {
	t.Helper()
	if _, _, ok := g.AddBridge(bridge); !ok {
		t.Fatalf("AddBridge(%s) rejected", bridge.BridgeID)
	}
}

func addTestPhone(t *testing.T, g *AccountGroup, pin string) *Connection {
	t.Helper()
	phone := &Connection{BridgeID: pin}
	if _, ok := g.AddPhone(phone); !ok {
		t.Fatal("AddPhone rejected")
	}
	return phone
}

func TestAccountGroup_DefaultIsLatestConnectedBridge(t *testing.T) {
	g := NewAccountGroup("user1")
	start := time.Now()
	older := testBridge("br_olderBridge1", start)
	newer := testBridge("br_newerBridge1", start.Add(time.Second))
	addTestBridge(t, g, newer)
	addTestBridge(t, g, older)

	unpinned := addTestPhone(t, g, "")
	pinned := addTestPhone(t, g, older.BridgeID)
	if got := g.BridgeForPhone(unpinned); got != newer {
		t.Errorf("unpinned phone target = %v, want the latest bridge", got)
	}
	if got := g.BridgeForPhone(pinned); got != older {
		t.Errorf("pinned phone target = %v, want its pinned bridge", got)
	}

	// Equal timestamps resolve by bridge id, so the choice never flips.
	tied := testBridge("br_zzzzzzzzzz", newer.ConnectedAt)
	addTestBridge(t, g, tied)
	for i := 0; i < 20; i++ {
		if got := g.BridgeForPhone(unpinned); got != tied {
			t.Fatalf("tie resolved to %v, want %s", got, tied.BridgeID)
		}
	}
}

package relay

import (
	"sync"
	"testing"
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
	if g.Bridge != nil {
		t.Error("expected nil Bridge")
	}
}

func TestAccountGroup_AssignConnID_StartsAtOne(t *testing.T) {
	g := NewAccountGroup("user1")
	id := g.AssignConnID()
	if id != 1 {
		t.Errorf("expected first ID 1, got %d", id)
	}
}

func TestAccountGroup_AssignConnID_Monotonic(t *testing.T) {
	g := NewAccountGroup("user1")
	for i := uint16(1); i <= 10; i++ {
		id := g.AssignConnID()
		if id != i {
			t.Errorf("expected ID %d, got %d", i, id)
		}
	}
}

func TestAccountGroup_AssignConnID_WrapSkipsZero(t *testing.T) {
	g := NewAccountGroup("user1")
	g.NextConnID = 65535

	id1 := g.AssignConnID()
	id2 := g.AssignConnID()

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
		id := g.AssignConnID()
		if id == 0 {
			t.Errorf("AssignConnID returned 0 on iteration %d", i)
		}
	}
}

func TestAccountGroup_AssignConnID_Concurrent(t *testing.T) {
	g := NewAccountGroup("user1")

	const n = 100
	ids := make([]uint16, n)
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		idx := i
		go func() {
			defer wg.Done()
			ids[idx] = g.AssignConnID()
		}()
	}
	wg.Wait()

	seen := make(map[uint16]bool)
	for i, id := range ids {
		if id == 0 {
			t.Errorf("goroutine %d got ID 0", i)
		}
		if seen[id] {
			t.Errorf("duplicate ID %d", id)
		}
		seen[id] = true
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

	g.Bridge = &Connection{}

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

	g.Bridge = &Connection{BridgeID: "br_x"}
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

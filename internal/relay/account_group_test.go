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

	m.RemoveGroupIfEmpty("user1")

	if m.Count() != 1 {
		t.Errorf("group with bridge should not be removed, count=%d", m.Count())
	}
}

func TestGroupManager_RemoveGroupIfEmpty_WithPhone(t *testing.T) {
	m := NewGroupManager()
	g := m.GetOrCreateGroup("user1")

	g.Phones[1] = &Connection{}

	m.RemoveGroupIfEmpty("user1")

	if m.Count() != 1 {
		t.Errorf("group with phone should not be removed, count=%d", m.Count())
	}
}

func TestGroupManager_RemoveGroupIfEmpty_Empty(t *testing.T) {
	m := NewGroupManager()
	m.GetOrCreateGroup("user1")
	m.GetOrCreateGroup("user2")

	if m.Count() != 2 {
		t.Fatalf("expected count 2, got %d", m.Count())
	}

	m.RemoveGroupIfEmpty("user1")

	if m.Count() != 1 {
		t.Errorf("empty group should be removed, count=%d", m.Count())
	}
}

func TestGroupManager_RemoveGroupIfEmpty_NonExistent(t *testing.T) {
	m := NewGroupManager()

	m.RemoveGroupIfEmpty("nonexistent")

	if m.Count() != 0 {
		t.Errorf("expected count 0, got %d", m.Count())
	}
}

func TestGroupManager_Count_ReflectsRemovals(t *testing.T) {
	m := NewGroupManager()

	m.GetOrCreateGroup("a")
	m.GetOrCreateGroup("b")
	m.GetOrCreateGroup("c")

	if m.Count() != 3 {
		t.Fatalf("expected 3, got %d", m.Count())
	}

	m.RemoveGroupIfEmpty("a")
	m.RemoveGroupIfEmpty("b")

	if m.Count() != 1 {
		t.Errorf("expected 1 after two removals, got %d", m.Count())
	}
}

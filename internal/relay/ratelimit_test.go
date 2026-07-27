package relay

import (
	"testing"
)

func TestRateLimiter_AllowConnection_WithinLimit(t *testing.T) {
	rl := NewRateLimiter(3, 100)

	for i := 0; i < 3; i++ {
		if !rl.AllowConnection("127.0.0.1") {
			t.Fatalf("connection %d should be allowed (limit=3)", i+1)
		}
	}
}

func TestRateLimiter_AllowConnection_ExceedLimit(t *testing.T) {
	rl := NewRateLimiter(2, 100)

	if !rl.AllowConnection("10.0.0.1") {
		t.Fatal("first connection should be allowed")
	}
	if !rl.AllowConnection("10.0.0.1") {
		t.Fatal("second connection should be allowed")
	}
	if rl.AllowConnection("10.0.0.1") {
		t.Fatal("third connection should be denied (limit=2)")
	}
}

func TestRateLimiter_ReleaseConnection_AllowsReconnect(t *testing.T) {
	rl := NewRateLimiter(1, 100)

	if !rl.AllowConnection("192.168.1.1") {
		t.Fatal("first connection should be allowed")
	}
	if rl.AllowConnection("192.168.1.1") {
		t.Fatal("second connection should be denied (limit=1)")
	}

	rl.ReleaseConnection("192.168.1.1")

	if !rl.AllowConnection("192.168.1.1") {
		t.Fatal("connection after release should be allowed")
	}
}

func TestRateLimiter_AllowGroup_UnderLimit(t *testing.T) {
	rl := NewRateLimiter(10, 5)

	if !rl.AllowGroup(0) {
		t.Error("0 groups should be allowed (max=5)")
	}
	if !rl.AllowGroup(4) {
		t.Error("4 groups should be allowed (max=5)")
	}
}

func TestRateLimiter_AllowGroup_AtOrOverLimit(t *testing.T) {
	rl := NewRateLimiter(10, 5)

	if rl.AllowGroup(5) {
		t.Error("5 groups should be denied (max=5)")
	}
	if rl.AllowGroup(100) {
		t.Error("100 groups should be denied (max=5)")
	}
}

func TestRateLimiter_DifferentIPs_Independent(t *testing.T) {
	rl := NewRateLimiter(1, 100)

	if !rl.AllowConnection("1.1.1.1") {
		t.Fatal("first IP should be allowed")
	}
	if !rl.AllowConnection("2.2.2.2") {
		t.Fatal("second IP should be allowed (different IP, independent limit)")
	}
}

func TestDefaultPerIPConnectionLimit(t *testing.T) {
	rl := NewRateLimiter(defaultMaxPerIP, defaultMaxRooms)
	for i := 0; i < 20; i++ {
		if !rl.AllowConnection("192.0.2.1") {
			t.Fatalf("connection %d should be allowed", i+1)
		}
	}
	if rl.AllowConnection("192.0.2.1") {
		t.Fatal("connection 21 should be denied")
	}
}

func TestRateLimiter_ConnectionStats(t *testing.T) {
	rl := NewRateLimiter(2, 100)
	for i := 0; i < 2; i++ {
		if !rl.AllowConnection("192.0.2.1") {
			t.Fatalf("connection %d for first IP should be allowed", i+1)
		}
	}
	if !rl.AllowConnection("198.51.100.1") {
		t.Fatal("connection for second IP should be allowed")
	}
	if rl.AllowConnection("192.0.2.1") {
		t.Fatal("connection over the limit should be denied")
	}

	want := ConnectionStats{ActiveConnections: 3, ActiveClientIPs: 2, MaxConnectionsPerIP: 2}
	if got := rl.ConnectionStats(); got != want {
		t.Fatalf("ConnectionStats() = %+v, want %+v", got, want)
	}

	rl.ReleaseConnection("192.0.2.1")
	want = ConnectionStats{ActiveConnections: 2, ActiveClientIPs: 2, MaxConnectionsPerIP: 1}
	if got := rl.ConnectionStats(); got != want {
		t.Fatalf("ConnectionStats() after first release = %+v, want %+v", got, want)
	}

	rl.ReleaseConnection("198.51.100.1")
	want = ConnectionStats{ActiveConnections: 1, ActiveClientIPs: 1, MaxConnectionsPerIP: 1}
	if got := rl.ConnectionStats(); got != want {
		t.Fatalf("ConnectionStats() after second release = %+v, want %+v", got, want)
	}
}

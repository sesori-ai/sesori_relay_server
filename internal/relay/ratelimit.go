package relay

import (
	"sync"
	"sync/atomic"
)

const (
	defaultMaxPerIP = 20
	defaultMaxRooms = 10000
)

type ConnectionStats struct {
	ActiveConnections   int64
	ActiveClientIPs     int
	MaxConnectionsPerIP int64
}

type RateLimiter struct {
	maxPerIP int
	maxRooms int
	counts   sync.Map
}

func NewRateLimiter(maxPerIP, maxRooms int) *RateLimiter {
	return &RateLimiter{
		maxPerIP: maxPerIP,
		maxRooms: maxRooms,
	}
}

func (rl *RateLimiter) AllowConnection(ip string) bool {
	actual, _ := rl.counts.LoadOrStore(ip, new(int64))
	counter := actual.(*int64)

	newVal := atomic.AddInt64(counter, 1)
	if int(newVal) > rl.maxPerIP {
		atomic.AddInt64(counter, -1)
		return false
	}
	return true
}

func (rl *RateLimiter) ReleaseConnection(ip string) {
	if actual, ok := rl.counts.Load(ip); ok {
		atomic.AddInt64(actual.(*int64), -1)
	}
}

func (rl *RateLimiter) ConnectionStats() ConnectionStats {
	var stats ConnectionStats
	rl.counts.Range(func(_, value any) bool {
		count := atomic.LoadInt64(value.(*int64))
		if count <= 0 {
			return true
		}

		stats.ActiveConnections += count
		stats.ActiveClientIPs++
		if count > stats.MaxConnectionsPerIP {
			stats.MaxConnectionsPerIP = count
		}
		return true
	})
	return stats
}

func (rl *RateLimiter) AllowGroup(currentGroupCount int) bool {
	return currentGroupCount < rl.maxRooms
}

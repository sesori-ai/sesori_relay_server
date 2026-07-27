package relay

import "sync"

const (
	defaultMaxPerIP = 20
	defaultMaxRooms = 10000
)

type ConnectionStats struct {
	ActiveConnections   int64
	ActiveClientIPs     int
	MaxConnectionsPerIP int64
	RejectedConnections int64
}

type RateLimiter struct {
	maxPerIP            int
	maxRooms            int
	mu                  sync.Mutex
	counts              map[string]int
	rejectedConnections int64
}

func NewRateLimiter(maxPerIP, maxRooms int) *RateLimiter {
	return &RateLimiter{
		maxPerIP: maxPerIP,
		maxRooms: maxRooms,
		counts:   make(map[string]int),
	}
}

func (rl *RateLimiter) AllowConnection(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	count := rl.counts[ip]
	if count >= rl.maxPerIP {
		rl.rejectedConnections++
		return false
	}
	rl.counts[ip] = count + 1
	return true
}

func (rl *RateLimiter) ReleaseConnection(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	count, ok := rl.counts[ip]
	if !ok {
		return
	}
	if count <= 1 {
		delete(rl.counts, ip)
		return
	}
	rl.counts[ip] = count - 1
}

func (rl *RateLimiter) ConnectionStats() ConnectionStats {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	stats := ConnectionStats{RejectedConnections: rl.rejectedConnections}
	for _, count := range rl.counts {
		stats.ActiveConnections += int64(count)
		stats.ActiveClientIPs++
		if int64(count) > stats.MaxConnectionsPerIP {
			stats.MaxConnectionsPerIP = int64(count)
		}
	}
	return stats
}

func (rl *RateLimiter) AllowGroup(currentGroupCount int) bool {
	return currentGroupCount < rl.maxRooms
}

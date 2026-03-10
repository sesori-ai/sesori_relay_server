package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/anthropics/remote-relay/internal/protocol"
)

const (
	maxCodeRetries      = 10
	cleanupInterval     = 30 * time.Second
	waitingRoomTimeout  = 5 * time.Minute
	inactiveRoomTimeout = 30 * time.Minute
)

type RoomManager struct {
	rooms map[string]*Room
	mu    sync.RWMutex
}

func NewRoomManager() *RoomManager {
	return &RoomManager{
		rooms: make(map[string]*Room),
	}
}

func (m *RoomManager) CreateRoom() (string, *Room, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i := 0; i < maxCodeRetries; i++ {
		code, err := protocol.GenerateRoomCode()
		if err != nil {
			return "", nil, fmt.Errorf("failed to generate room code: %w", err)
		}

		if _, exists := m.rooms[code]; !exists {
			now := time.Now()
			room := &Room{
				Code:         code,
				CreatedAt:    now,
				LastActivity: now,
			}
			m.rooms[code] = room
			return code, room, nil
		}
	}

	return "", nil, errors.New("failed to generate unique room code after max retries")
}

func (m *RoomManager) GetRoom(code string) (*Room, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	room, ok := m.rooms[code]
	return room, ok
}

func (m *RoomManager) RemoveRoom(code string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rooms, code)
}

func (m *RoomManager) RoomCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.rooms)
}

func (m *RoomManager) ConnectionCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	count := 0
	for _, room := range m.rooms {
		room.mu.Lock()
		if room.Bridge != nil {
			count++
		}
		if room.Phone != nil {
			count++
		}
		room.mu.Unlock()
	}
	return count
}

func (m *RoomManager) StartCleanup(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.cleanup()
			}
		}
	}()
}

func (m *RoomManager) cleanup() {
	now := time.Now()

	m.mu.Lock()
	defer m.mu.Unlock()

	for code, room := range m.rooms {
		room.mu.Lock()
		hasBridge := room.Bridge != nil
		hasPhone := room.Phone != nil
		lastActivity := room.LastActivity
		createdAt := room.CreatedAt
		room.mu.Unlock()

		onlyOneConn := hasBridge != hasPhone
		if onlyOneConn && now.Sub(createdAt) > waitingRoomTimeout {
			slog.Info("cleanup: removing waiting room", "code", code)
			delete(m.rooms, code)
			continue
		}

		if now.Sub(lastActivity) > inactiveRoomTimeout {
			slog.Info("cleanup: removing inactive room", "code", code)
			delete(m.rooms, code)
		}
	}
}

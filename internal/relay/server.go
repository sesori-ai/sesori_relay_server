package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type Server struct {
	addr        string
	manager     *RoomManager
	rateLimiter *RateLimiter
	httpServer  *http.Server
}

func NewServer(addr string) *Server {
	return &Server{
		addr:        addr,
		manager:     NewRoomManager(),
		rateLimiter: NewRateLimiter(defaultMaxPerIP, defaultMaxRooms),
	}
}

func (s *Server) Manager() *RoomManager {
	return s.manager
}

func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws/{roomCode}", s.handleWebSocket)
	mux.HandleFunc("GET /status", handleStatus)
	mux.HandleFunc("GET /health", s.handleHealth)

	s.httpServer = &http.Server{
		Addr:    s.addr,
		Handler: mux,
	}

	s.manager.StartCleanup(ctx)

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = s.httpServer.Shutdown(shutdownCtx)
	}()

	return s.httpServer.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}
	return s.httpServer.Shutdown(ctx)
}

func handleStatus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":      "ok",
		"rooms":       s.manager.RoomCount(),
		"connections": s.manager.ConnectionCount(),
	})
}

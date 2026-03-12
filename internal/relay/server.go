package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/sesori-ai/sesori_relay_server/internal/auth"
)

// Server is the relay WebSocket server. It manages rooms, rate limiting, and
// delegates authentication to the provided Authenticator (nil = auth disabled).
type Server struct {
	addr        string
	manager     *GroupManager
	rateLimiter *RateLimiter
	httpServer  *http.Server
	jwtAuth     *auth.JWTAuthenticator
}

// NewServer creates a relay server. Pass a nil authenticator to disable auth.
func NewServer(addr string, jwtAuth *auth.JWTAuthenticator) *Server {
	return &Server{
		addr:        addr,
		manager:     NewGroupManager(),
		rateLimiter: NewRateLimiter(defaultMaxPerIP, defaultMaxRooms),
		jwtAuth:     jwtAuth,
	}
}

func (s *Server) Manager() *GroupManager {
	return s.manager
}

func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", s.handleWebSocket)
	mux.HandleFunc("GET /status", handleStatus)
	mux.HandleFunc("GET /health", s.handleHealth)

	s.httpServer = &http.Server{
		Addr:    s.addr,
		Handler: mux,
	}

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
		"status": "ok",
		"groups": s.manager.Count(),
	})
}

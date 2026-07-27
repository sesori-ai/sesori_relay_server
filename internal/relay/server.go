package relay

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/sesori-ai/sesori_relay_server/internal/auth"
	"github.com/sesori-ai/sesori_relay_server/internal/notifications"
)

const connectionStatsLogInterval = 10 * time.Minute

// Server is the relay WebSocket server. It manages rooms, rate limiting, and
// delegates authentication to the provided Authenticator (nil = auth disabled).
type Server struct {
	addr                string
	manager             *GroupManager
	rateLimiter         *RateLimiter
	httpServer          *http.Server
	jwtAuth             *auth.JWTAuthenticator
	notifications       *notifications.Client
	requireBridgeID     bool
	trustCFConnectingIP bool
}

// NewServer creates a relay server. Pass a nil authenticator to disable auth.
// requireBridgeID enforces that every bridge connection sends a bridgeId in
// its auth message; when false, bridges without bridgeId are accepted (legacy
// path) and no bridgeId is forwarded to the auth server.
// trustCFConnectingIP uses Cloudflare's visitor IP header for rate limiting and
// must only be enabled when direct access to the origin is blocked.
func NewServer(
	addr string,
	jwtAuth *auth.JWTAuthenticator,
	notifs *notifications.Client,
	requireBridgeID, trustCFConnectingIP bool,
) *Server {
	return &Server{
		addr:                addr,
		manager:             NewGroupManager(),
		rateLimiter:         NewRateLimiter(defaultMaxPerIP, defaultMaxRooms),
		jwtAuth:             jwtAuth,
		notifications:       notifs,
		requireBridgeID:     requireBridgeID,
		trustCFConnectingIP: trustCFConnectingIP,
	}
}

func (s *Server) Manager() *GroupManager {
	return s.manager
}

func (s *Server) Start(ctx context.Context) error {
	statsCtx, stopStats := context.WithCancel(ctx)
	defer stopStats()
	go s.logConnectionStats(statsCtx)

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

func (s *Server) logConnectionStats(ctx context.Context) {
	ticker := time.NewTicker(connectionStatsLogInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			stats := s.rateLimiter.ConnectionStats()
			slog.Info(
				"relay connection stats",
				"activeConnections", stats.ActiveConnections,
				"activeClientIPs", stats.ActiveClientIPs,
				"maxConnectionsPerIP", stats.MaxConnectionsPerIP,
				"activeGroups", s.manager.Count(),
			)
		case <-ctx.Done():
			return
		}
	}
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

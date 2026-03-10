package relay

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

type Server struct {
	addr           string
	manager        *RoomManager
	rateLimiter    *RateLimiter
	httpServer     *http.Server
	publicKey      any
	authBackendURL string
}

func NewServer(addr string, authBackendURL string) *Server {
	return &Server{
		addr:           addr,
		manager:        NewRoomManager(),
		rateLimiter:    NewRateLimiter(defaultMaxPerIP, defaultMaxRooms),
		authBackendURL: authBackendURL,
	}
}

// FetchPublicKey fetches the RSA public key from the auth backend and stores it.
// If authBackendURL is empty, this is a no-op (auth disabled).
func (s *Server) FetchPublicKey() error {
	if s.authBackendURL == "" {
		return nil
	}

	url := s.authBackendURL + "/auth/public-key"
	resp, err := http.Get(url) //nolint:noctx
	if err != nil {
		return fmt.Errorf("failed to fetch public key from %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read public key response: %w", err)
	}

	block, _ := pem.Decode(body)
	if block == nil {
		return fmt.Errorf("failed to decode PEM block from public key response")
	}

	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse public key: %w", err)
	}

	rsaKey, ok := pub.(*rsa.PublicKey)
	if !ok {
		return fmt.Errorf("public key is not RSA (got %T)", pub)
	}

	s.publicKey = rsaKey
	slog.Info("auth public key loaded successfully")
	return nil
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

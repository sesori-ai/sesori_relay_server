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
	"sync"
	"time"
)

type Server struct {
	addr           string
	manager        *RoomManager
	rateLimiter    *RateLimiter
	httpServer     *http.Server
	publicKey      *rsa.PublicKey
	authBackendURL string
	keyMu          sync.RWMutex
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

	rsaKey, err := s.fetchAndParseKey()
	if err != nil {
		return err
	}

	s.keyMu.Lock()
	s.publicKey = rsaKey
	s.keyMu.Unlock()
	slog.Info("auth public key loaded successfully")
	return nil
}

func (s *Server) refreshPublicKey() error {
	if s.authBackendURL == "" {
		return nil
	}

	rsaKey, err := s.fetchAndParseKey()
	if err != nil {
		return err
	}

	s.keyMu.Lock()
	s.publicKey = rsaKey
	s.keyMu.Unlock()
	return nil
}

func (s *Server) fetchAndParseKey() (*rsa.PublicKey, error) {
	if s.authBackendURL == "" {
		return nil, nil
	}

	url := s.authBackendURL + "/auth/public-key"
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch public key from %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read public key response: %w", err)
	}

	block, _ := pem.Decode(body)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block from public key response")
	}

	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse public key: %w", err)
	}

	rsaKey, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is not RSA (got %T)", pub)
	}

	return rsaKey, nil
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
	if s.authBackendURL != "" {
		go func() {
			ticker := time.NewTicker(5 * time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					if err := s.refreshPublicKey(); err != nil {
						slog.Warn("failed to refresh auth public key", "err", err)
					}
				case <-ctx.Done():
					return
				}
			}
		}()
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
		"status":      "ok",
		"rooms":       s.manager.RoomCount(),
		"connections": s.manager.ConnectionCount(),
	})
}

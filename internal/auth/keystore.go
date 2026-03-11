package auth

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// KeyStore manages an RSA public key fetched from a remote auth backend.
// It supports periodic background refresh and thread-safe access.
type KeyStore struct {
	url string
	key *rsa.PublicKey
	mu  sync.RWMutex
}

// NewKeyStore creates a KeyStore that fetches the public key from the given URL.
func NewKeyStore(publicKeyURL string) *KeyStore {
	return &KeyStore{url: publicKeyURL}
}

// Load fetches the public key for the first time. Call this at startup.
func (ks *KeyStore) Load() error {
	key, err := ks.fetchAndParse()
	if err != nil {
		return err
	}

	ks.mu.Lock()
	ks.key = key
	ks.mu.Unlock()
	slog.Info("auth public key loaded successfully")
	return nil
}

// PublicKey returns the current public key under a read lock.
func (ks *KeyStore) PublicKey() *rsa.PublicKey {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	return ks.key
}

// Refresh fetches and replaces the current public key.
func (ks *KeyStore) Refresh() error {
	key, err := ks.fetchAndParse()
	if err != nil {
		return err
	}

	ks.mu.Lock()
	ks.key = key
	ks.mu.Unlock()
	return nil
}

// StartPeriodicRefresh launches a background goroutine that refreshes the key
// at the given interval. It stops when ctx is cancelled.
func (ks *KeyStore) StartPeriodicRefresh(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := ks.Refresh(); err != nil {
					slog.Warn("failed to refresh auth public key", "err", err)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (ks *KeyStore) fetchAndParse() (*rsa.PublicKey, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(ks.url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch public key from %s: %w", ks.url, err)
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

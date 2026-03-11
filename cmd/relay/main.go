package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anthropics/remote-relay/internal/auth"
	"github.com/anthropics/remote-relay/internal/relay"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	logLevel := flag.String("log-level", "info", "log level (debug, info, warn, error)")
	authBackendURL := flag.String("auth-backend-url", "", "auth backend base URL (e.g. https://auth.example.com); auth is disabled when empty")
	flag.Parse()

	// Also honour the AUTH_BACKEND_URL environment variable (flag takes precedence).
	if *authBackendURL == "" {
		*authBackendURL = os.Getenv("AUTH_BACKEND_URL")
	}

	level := parseLogLevel(*logLevel)
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if *authBackendURL != "" {
		slog.Info("auth enabled", "backend", *authBackendURL)
	} else {
		slog.Info("auth disabled (no --auth-backend-url provided)")
	}

	slog.Info("starting relay server", "addr", *addr, "log-level", *logLevel)

	var authenticator auth.Authenticator
	if *authBackendURL != "" {
		keyStore := auth.NewKeyStore(*authBackendURL + "/auth/public-key")
		if err := keyStore.Load(); err != nil {
			slog.Error("failed to fetch auth public key", "err", err)
			os.Exit(1)
		}
		keyStore.StartPeriodicRefresh(ctx, 5*time.Minute)
		authenticator = auth.NewJWTAuthenticator(keyStore)
	}

	server := relay.NewServer(*addr, authenticator)

	if err := server.Start(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}

	slog.Info("relay server stopped")
}

func parseLogLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

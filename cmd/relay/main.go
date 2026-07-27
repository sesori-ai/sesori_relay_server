package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/sesori-ai/sesori_relay_server/internal/auth"
	"github.com/sesori-ai/sesori_relay_server/internal/notifications"
	"github.com/sesori-ai/sesori_relay_server/internal/relay"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	logLevel := flag.String("log-level", "info", "log level (debug, info, warn, error)")
	authBackendURL := flag.String("auth-backend-url", "", "auth backend base URL (e.g. https://auth.example.com); auth is disabled when empty")
	relayWebhookSecret := flag.String("relay-webhook-secret", "", "shared secret for auth server webhook")
	requireBridgeID := flag.Bool("require-bridge-id", false, "require bridgeId field on bridge auth messages (transition gate; set true once the bridge fleet has rolled over)")
	trustCFConnectingIP := flag.Bool(
		"trust-cf-connecting-ip",
		false,
		"trust Cloudflare's CF-Connecting-IP header (enable only when all traffic passes through Cloudflare)",
	)
	flag.Parse()

	// Also honour the AUTH_BACKEND_URL environment variable (flag takes precedence).
	if *authBackendURL == "" {
		*authBackendURL = os.Getenv("AUTH_BACKEND_URL")
	}
	if *relayWebhookSecret == "" {
		*relayWebhookSecret = os.Getenv("RELAY_WEBHOOK_SECRET")
	}
	if !flagWasSet("log-level") {
		if value := os.Getenv("LOG_LEVEL"); value != "" {
			*logLevel = value
		}
	}
	if !flagWasSet("require-bridge-id") {
		v, ok, err := envBool("RELAY_REQUIRE_BRIDGE_ID")
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "invalid RELAY_REQUIRE_BRIDGE_ID: %v\n", err)
			os.Exit(1)
		}
		if ok {
			*requireBridgeID = v
		}
	}
	if !flagWasSet("trust-cf-connecting-ip") {
		v, ok, err := envBool("RELAY_TRUST_CF_CONNECTING_IP")
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "invalid RELAY_TRUST_CF_CONNECTING_IP: %v\n", err)
			os.Exit(1)
		}
		if ok {
			*trustCFConnectingIP = v
		}
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

	slog.Info(
		"starting relay server",
		"addr", *addr,
		"log-level", *logLevel,
		"require-bridge-id", *requireBridgeID,
		"trust-cf-connecting-ip", *trustCFConnectingIP,
	)

	var authenticator *auth.JWTAuthenticator
	if *authBackendURL != "" {
		keyStore := auth.NewKeyStore(*authBackendURL + "/auth/public-key")
		if err := keyStore.Load(); err != nil {
			slog.Error("failed to fetch auth public key", "err", err)
			os.Exit(1)
		}
		keyStore.StartPeriodicRefresh(ctx, 5*time.Minute)
		authenticator = auth.NewJWTAuthenticator(keyStore)
	}

	var notifs *notifications.Client
	if *relayWebhookSecret != "" && *authBackendURL != "" {
		notifs = notifications.NewClient(*authBackendURL, *relayWebhookSecret)
		slog.Info("push notifications enabled")
	} else {
		slog.Info("push notifications disabled (no webhook secret)")
	}

	server := relay.NewServer(*addr, authenticator, notifs, *requireBridgeID, *trustCFConnectingIP)

	if err := server.Start(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}

	slog.Info("relay server stopped")
}

func flagWasSet(name string) bool {
	seen := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			seen = true
		}
	})
	return seen
}

func envBool(name string) (bool, bool, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return false, false, nil
	}

	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, true, err
	}
	return value, true, nil
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

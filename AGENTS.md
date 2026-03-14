# Sesori Relay Server

Go WebSocket relay. Routes E2E-encrypted binary traffic between bridge and phones by userId from JWT. Zero persistence — all state is in-memory connections.

## STRUCTURE

```
cmd/relay/main.go              Entry point — flag parsing, JWT auth init, server start
internal/
├── auth/                      JWT verification + JWKS key fetching from auth server
│   ├── authenticator.go       Token parsing, claim validation (RS256, aud, iss, exp)
│   └── keystore.go            Public key fetching + caching from auth backend /auth/public-key
├── protocol/                  WebSocket wire format
│   ├── close_codes.go         Custom close codes (4001–4008: auth failure, rate limit, etc.)
│   ├── constants.go           Role strings, message types, limits
│   ├── framing.go             Binary frame read/write with connId prefix
│   └── messages.go            Auth/control message JSON types
└── relay/                     Core relay logic
    ├── handler.go             WebSocket upgrade + auth handshake + message routing
    ├── server.go              HTTP server, /health endpoint, connection lifecycle
    ├── account_group.go       Per-userId group: 1 bridge + up to 5 phones, connId assignment
    └── rate_limiter.go        Per-IP connection limiting (max 10), global group cap (10,000)
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Change auth/JWT | `internal/auth/` | RS256 verification, claims in authenticator.go |
| Modify protocol messages | `internal/protocol/messages.go` | JSON control messages (bridge/phone connected/disconnected) |
| Change binary framing | `internal/protocol/framing.go` | 2-byte connId prefix on binary frames |
| Adjust rate limits | `internal/relay/rate_limiter.go` | Per-IP (10) and global group (10,000) caps |
| Add endpoints | `internal/relay/server.go` | HTTP mux, currently /health and /ws |
| Modify group behavior | `internal/relay/account_group.go` | Max 5 phones per account, connId=0 broadcast |

## CONVENTIONS

- **Standard Go layout**: `cmd/` for binaries, `internal/` for private packages
- **No external deps** beyond `coder/websocket` — stdlib for everything else
- **slog** for structured logging (Go 1.21+)
- **Context propagation**: All functions accept `context.Context`
- **Flag + env**: CLI flags (`--addr`, `--log-level`) with env var fallbacks (`RELAY_ADDR`, `LOG_LEVEL`)

## ANTI-PATTERNS

- **No database** — zero persistence by design. Group state lives in memory only
- **No payload inspection** — relay CANNOT read encrypted traffic. Never add decryption logic here
- **No goroutine leaks** — all goroutines tied to connection context, cancelled on disconnect

## TESTING

```bash
go test ./...                  # All tests
go vet ./...                   # Static analysis
```

## DEPLOYMENT

- **Docker**: Multi-stage build (golang:1.23-alpine → alpine:3.19), non-root user `relay:relay`
- **Health**: `GET /health` → `{"status":"ok","groups":N,"connections":N}`
- See `DEPLOY.md` for Fly.io and VPS (Docker + Caddy) guides

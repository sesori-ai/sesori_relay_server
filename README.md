# Sesori Relay Server

WebSocket relay server for proxying encrypted traffic between the [Sesori Bridge CLI](https://github.com/sesori-ai/sesori_bridge) running on a laptop and the [Sesori mobile app](https://github.com/sesori-ai/sesori_mobile) running on a phone.

The relay routes traffic by account — connections are grouped by the `userId` extracted from a JWT token, so a bridge and up to 5 phones belonging to the same account can exchange data without the relay reading any of it. All payload data is end-to-end encrypted (XChaCha20-Poly1305) and the relay server **cannot** read any of it.

## How it works

1. Bridge CLI connects to `/ws` and sends an auth message with a signed JWT, role `"bridge"`, and its `bridgeId`
2. Phone connects to `/ws` and sends an auth message with a signed JWT and role `"phone"`
3. Relay extracts `userId` from each JWT and groups connections by account — 1 bridge + up to 5 phones per account
4. Binary frames from phone → relay → bridge carry a 2-byte `connId` prefix so the bridge can address replies back to the correct phone
5. Control messages notify each side when the other connects or disconnects (`bridge_connected`, `bridge_disconnected`, `phone_connected`, `phone_disconnected`)
6. Group is torn down when all connections close — no state is persisted

## Architecture

```
Phone ←──(encrypted)──→ Relay Server ←──(encrypted)──→ Bridge CLI → opencode serve
```

- **Account groups**: Connections are grouped by `userId` from the JWT. Each group holds 1 bridge + up to 5 phones.
- **Rate limiting**: Max 20 connections per resolved client IP, max 10,000 active groups globally.
- **No storage**: The relay holds no state beyond active WebSocket connections. Nothing is persisted.

## Running locally

```bash
# Build and run
make run

# Or with Docker
docker compose up
```

The server listens on `:8080` by default.

## Configuration

| Flag | Env var | Default | Description |
|------|---------|---------|-------------|
| `--addr` | `RELAY_ADDR` | `:8080` | Listen address |
| `--log-level` | `LOG_LEVEL` | `info` | Log level (`debug`, `info`, `warn`, `error`) |
| `--trust-cf-connecting-ip` | `RELAY_TRUST_CF_CONNECTING_IP` | `false` | Use Cloudflare's validated `CF-Connecting-IP` value for per-IP limits. Enable only when every request reaches the relay through Cloudflare and direct origin access is blocked. |

## Endpoints

| Endpoint | Description |
|----------|-------------|
| `GET /health` | Health check — returns `{"status":"ok","groups":N,"connections":N}` |
| `GET /ws` | WebSocket endpoint — authenticated via JWT, routed by userId |

## Deployment

### Docker

```bash
docker build -t sesori-relay .
docker run -d -p 8080:8080 sesori-relay
```

The image is a multi-stage build (~15MB) running as a non-root user.

### Production (with TLS)

See [`DEPLOY.md`](DEPLOY.md) for Fly.io and VPS (Docker + Caddy) deployment guides.

### CI

The GitHub Actions workflows run on every push and PR:
- `.github/workflows/docker.yml` — builds the Docker image, starts a container, and verifies the health endpoint
- `.github/workflows/test.yml` — runs `go vet` and `go test ./...`

## Security

- The relay **cannot decrypt** any traffic. All data between phone and bridge is encrypted with XChaCha20-Poly1305 using keys derived from an X25519 key exchange that happens directly between the two peers.
- JWT authentication is required before any data is forwarded. Tokens must be RS256-signed with valid `userId`, `tokenType`, `aud`, `iss`, and `exp` claims.
- Rate limiting prevents abuse: max 20 connections per resolved client IP, max groups globally, and at most 5 phones per account group.
- The server runs as a non-root user in Docker.

## Protocol

### Auth handshake

Every client sends this as the first WebSocket message (plaintext JSON):

```json
{ "type": "auth", "token": "<user access JWT>", "role": "bridge", "bridgeId": "br_..." }
```

Both roles authenticate with the user's **access token** (`tokenType:
"access"`, `aud: "mobile"`); no other token type is accepted.

The `bridgeId` field is required for `role: "bridge"` and ignored for
`role: "phone"`. Its format is `^br_[A-Za-z0-9_-]{8,32}$`. The relay
does not validate the value
against any database at handshake time; it forwards it to the auth
server in the bridge-status report. If the auth server answers that
report with an explicit HTTP 404 (bridgeId unknown, revoked, or owned
by another user), the relay closes that bridge connection with close
code `4006` (bridge revoked). Transport errors, timeouts, and 5xx
responses are fail-open: the connection stays up.

Roles: `"bridge"` or `"phone"`. The relay closes the connection if auth fails.

### Control messages (JSON text frames)

| Message | Direction | Description |
|---------|-----------|-------------|
| `phone_connected` | relay → bridge | A phone joined; includes `connId` (uint16) |
| `phone_disconnected` | relay → bridge | A phone left; includes `connId` |
| `bridge_connected` | relay → phone | The bridge is online |
| `bridge_disconnected` | relay → phone | The bridge disconnected |
| `bridge_connection_notification_policy` | bridge → relay | Advisory `normal`, `suppress`, or `conservative` connection-push policy; never changes routing |
| `bridge_connection_observed` | phone → relay | Bounded device UUID proving that exact surface restored E2E connectivity; used only to suppress its pending online push |

These two client controls are plaintext connection metadata. Relay validates and forwards them to auth with an
in-memory socket correlation ID, but never decrypts or inspects binary session traffic. A socket that earned offline
notification eligibility while normally awake retains it across later sleep suppression; a suppress-only socket stays
quiet unless a normal/full-wake update promotes it. Missing or invalid metadata falls back to normal conservative
notification behavior and cannot close or delay transport.

### Data frames (binary)

Binary frames from phone → relay → bridge are prefixed with a 2-byte big-endian `connId`. The bridge uses this to address responses back to the correct phone. `connId == 0` is a broadcast from bridge to all connected phones.

### Encryption protocol

- **Key exchange**: X25519 (Diffie-Hellman)
- **Key derivation**: HKDF-SHA256 with info `"sesori-relay-v1"`
- **Symmetric encryption**: XChaCha20-Poly1305 (24-byte nonce)
- **Message framing**: `[0x01 version byte][24-byte nonce][ciphertext + 16-byte auth tag]`

## Tech stack

- Go 1.23
- [`coder/websocket`](https://github.com/coder/websocket) for WebSocket handling
- No external databases or message brokers

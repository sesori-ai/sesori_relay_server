# Remote Relay

WebSocket relay server for proxying encrypted traffic between the [OpenCode Bridge CLI](https://github.com/anthropics/opencode-bridge) running on a laptop and the OpenCode mobile app running on a phone.

The relay is a dumb pipe — it forwards opaque binary blobs between two peers in a room. All payload data is end-to-end encrypted (XChaCha20-Poly1305) and the relay server **cannot** read any of it.

## How it works

1. Bridge CLI connects to the relay and creates a room (8-char hex code, e.g. `a1b2-c3d4`)
2. Phone scans a QR code containing the room code and the bridge's public key
3. Phone joins the same room via WebSocket
4. Relay forwards all messages between the two peers — encrypted, opaque, no inspection
5. Room is destroyed when either peer disconnects

## Architecture

```
Phone ←──(encrypted)──→ Relay Server ←──(encrypted)──→ Bridge CLI → opencode serve
```

- **Rooms**: Each room holds exactly 2 peers (bridge + phone). Single-use, 5-minute expiry if second peer never joins.
- **Rate limiting**: Max 10 connections per IP, max 10,000 rooms globally.
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

See [`.env.example`](.env.example) for a template.

## Endpoints

| Endpoint | Description |
|----------|-------------|
| `GET /health` | Health check — returns `{"status":"ok","rooms":N,"connections":N}` |
| `GET /ws/{roomCode}` | WebSocket endpoint — join or create a room |

## Deployment

### Docker

```bash
docker build -t remote-relay .
docker run -d -p 8080:8080 remote-relay
```

The image is a multi-stage build (~15MB) running as a non-root user.

### Production (with TLS)

See [`DEPLOY.md`](DEPLOY.md) for Fly.io and VPS (Docker + Caddy) deployment guides.

### CI

The GitHub Actions workflow (`.github/workflows/docker.yml`) runs on every push and PR:
- Builds the Docker image
- Starts a container and verifies the health endpoint
- Runs `go vet` and builds the binary

## Security

- The relay **cannot decrypt** any traffic. All data between phone and bridge is encrypted with XChaCha20-Poly1305 using keys derived from an X25519 key exchange that happens directly between the two peers.
- Rooms are single-use and expire after 5 minutes if the second peer never connects.
- The server runs as a non-root user in Docker.
- Rate limiting prevents abuse (10 connections per IP).

## Protocol

The relay doesn't define the message format — it forwards raw WebSocket frames. The [encryption protocol](https://github.com/anthropics/opencode-bridge) is defined by the bridge and mobile app:

- **Key exchange**: X25519 (Diffie-Hellman)
- **Key derivation**: HKDF-SHA256 with info `"opencode-relay-v1"`
- **Symmetric encryption**: XChaCha20-Poly1305 (24-byte nonce)
- **Message framing**: `[0x01 version byte][24-byte nonce][ciphertext + 16-byte auth tag]`

## Tech stack

- Go 1.23
- [`coder/websocket`](https://github.com/coder/websocket) for WebSocket handling
- No external databases or message brokers

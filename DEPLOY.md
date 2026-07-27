# Deployment Guide for Remote Relay

This guide covers deploying the remote relay server to production environments.

## Quick Start (Local Development)

```bash
docker-compose up
```

The relay server will be available at `http://localhost:8080`.

## Fly.io Deployment (Recommended)

Fly.io provides a simple, scalable deployment platform with automatic TLS.

### Prerequisites

- [Fly CLI](https://fly.io/docs/getting-started/installing-flyctl/) installed
- Fly.io account

### Deployment Steps

1. **Initialize Fly app:**
   ```bash
   fly launch
   ```
   - Choose an app name (e.g., `sesori-relay`)
   - Select a region close to your users
   - Skip database setup

2. **Configure fly.toml (if needed):**
   ```toml
   [env]
     LOG_LEVEL = "info"
   
   [[services]]
     internal_port = 8080
     protocol = "tcp"
     
     [[services.ports]]
       port = 80
       handlers = ["http"]
     
     [[services.ports]]
       port = 443
       handlers = ["tls", "http"]
   ```

3. **Deploy:**
   ```bash
   fly deploy
   ```

4. **View logs:**
   ```bash
   fly logs
   ```

5. **Scale (if needed):**
   ```bash
   fly scale count 2  # Run 2 instances
   ```

## VPS Deployment (Docker + Caddy)

For self-hosted deployments on a VPS (AWS EC2, DigitalOcean, Linode, etc.).

### Prerequisites

- VPS with Docker and Docker Compose installed
- Domain name pointing to VPS IP
- SSH access to VPS

### Deployment Steps

1. **Clone repository:**
   ```bash
   git clone https://github.com/sesori-ai/sesori_relay_server.git
   cd sesori_relay_server
   ```

2. **Create Caddyfile for TLS:**
   ```bash
   cat > Caddyfile << 'EOF'
   relay.example.com {
     reverse_proxy relay:8080
   }
   EOF
   ```
   Replace `relay.example.com` with your domain.

3. **Enable Caddy in docker-compose.yml:**
   Uncomment the `caddy` service and `volumes` section in `docker-compose.yml`.

4. **Start services:**
   ```bash
   docker-compose up -d
   ```

5. **Verify:**
   ```bash
   curl https://relay.example.com/health
   ```

6. **View logs:**
   ```bash
   docker-compose logs -f relay
   ```

## Configuration

### Environment Variables

- `LOG_LEVEL` — Logging level: `debug`, `info`, `warn`, `error` (default: `info`)
- `RELAY_TRUST_CF_CONNECTING_IP` — Set to `true` only when every request passes through Cloudflare and direct access to the origin is blocked. Uses Cloudflare's single-IP `CF-Connecting-IP` header for per-client connection limits instead of the ingress proxy address.

### Command-Line Flags

The relay server accepts the following flags:

- `--addr` — Listen address (default: `:8080`)
- `--log-level` — Logging level (overrides `LOG_LEVEL` env var)
- `--trust-cf-connecting-ip` — Trust `CF-Connecting-IP` for per-client connection limits. This is the flag equivalent of `RELAY_TRUST_CF_CONNECTING_IP=true`.

### Example: Custom Port

```bash
docker run -p 9000:9000 sesori-relay --addr :9000
```

## Monitoring

### Health Check

The relay server provides a health check endpoint:

```bash
curl http://localhost:8080/health
```

### Logs

At `info` level, the relay logs aggregate connection statistics every 10
minutes: active WebSockets, distinct resolved client IPs, the busiest IP
bucket, and active account groups.

To verify client-IP extraction after a Cloudflare deployment:

1. Set `RELAY_TRUST_CF_CONNECTING_IP=true` and temporarily set
   `LOG_LEVEL=debug`.
2. Connect from two different public networks, such as Wi-Fi and phone
   cellular data.
3. Inspect `websocket client IP resolved` records. `source` must be
   `cf-connecting-ip`; `clientIP` should match each network's public address
   and vary between the two networks, while `peerIP` may repeat because it is
   the ingress proxy.
4. Confirm the startup record contains `trust-cf-connecting-ip=true` and the
   10-minute `relay connection stats` records show a plausible
   `activeClientIPs` count rather than one shared proxy bucket.
5. Restore `LOG_LEVEL=info` after verification because debug records contain
   client IP addresses.

**Docker Compose:**
```bash
docker-compose logs -f relay
```

**Fly.io:**
```bash
fly logs
```

**VPS (systemd):**
```bash
journalctl -u relay -f
```

## Troubleshooting

### Port Already in Use

If port 8080 is already in use:

```bash
# Find process using port 8080
lsof -i :8080

# Kill process
kill -9 <PID>

# Or use a different port
docker run -p 9000:8080 sesori-relay
```

### Container Won't Start

Check logs:
```bash
docker-compose logs relay
```

Common issues:
- Missing dependencies (check `go.mod`)
- Permission denied (ensure non-root user has correct permissions)
- Port binding failed (check firewall rules)

### TLS Certificate Issues (Caddy)

Caddy automatically obtains Let's Encrypt certificates. If issues occur:

```bash
# Restart Caddy
docker-compose restart caddy

# Check Caddy logs
docker-compose logs caddy
```

## Security Considerations

- The relay server runs as a non-root user (`relay:relay`) for security
- TLS is enforced in production (via Caddy or Fly.io)
- No secrets are baked into the Docker image
- Use environment variables for sensitive configuration

## Scaling

### Horizontal Scaling (Multiple Instances)

**Fly.io:**
```bash
fly scale count 3
```

**Docker Swarm / Kubernetes:**
Use orchestration tools to manage multiple relay instances behind a load balancer.

### Vertical Scaling (More Resources)

**Fly.io:**
```bash
fly scale vm shared-cpu-2x
```

**VPS:**
Upgrade your VPS plan or allocate more resources.

## Rollback

### Fly.io

```bash
fly releases
fly rollback <VERSION>
```

### Docker

```bash
docker-compose down
git checkout <PREVIOUS_COMMIT>
docker-compose up -d
```

## Support

For issues or questions:
- Check logs: `docker-compose logs relay`
- Review [Go relay server documentation](./README.md)
- Open an issue on GitHub

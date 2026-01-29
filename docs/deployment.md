# Deployment

This guide covers various deployment options for RPCGofer.

## Docker

### Using Pre-built Image

```bash
# Pull image from Docker Hub
docker pull jellyb0y/rpcgofer:latest

# Run with config file
docker run -d \
  --name rpcgofer \
  -p 8545:8545 \
  -p 8546:8546 \
  -v $(pwd)/config.json:/app/config.json:ro \
  jellyb0y/rpcgofer:latest
```

### Building Docker Image

```bash
# Build image
docker build -t rpcgofer:latest .

# Run container
docker run -d \
  --name rpcgofer \
  -p 8545:8545 \
  -p 8546:8546 \
  -v $(pwd)/config.json:/app/config.json:ro \
  rpcgofer:latest
```

### Dockerfile Details

The provided Dockerfile uses multi-stage build:

```dockerfile
# Stage 1: Build
FROM golang:1.22-alpine AS builder
WORKDIR /app
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /rpcgofer ./cmd/rpcgofer

# Stage 2: Runtime
FROM alpine:3.19
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app
COPY --from=builder /rpcgofer /app/rpcgofer
EXPOSE 8545 8546
ENTRYPOINT ["/app/rpcgofer"]
CMD ["-config", "/app/config.json"]
```

**Features:**
- Small final image (~15MB)
- No Go runtime required
- CA certificates for HTTPS upstreams
- Timezone data included

## Docker Compose

### Basic Setup

```yaml
version: "3.8"

services:
  rpcgofer:
    build:
      context: .
      dockerfile: Dockerfile
    container_name: rpcgofer
    restart: unless-stopped
    ports:
      - "8545:8545"
      - "8546:8546"
    volumes:
      - ./config.json:/app/config.json:ro
    healthcheck:
      test: ["CMD", "wget", "-q", "--spider", "http://localhost:8545"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 10s
    logging:
      driver: "json-file"
      options:
        max-size: "10m"
        max-file: "3"
```

### Start Services

```bash
# Start in background
docker-compose up -d

# View logs
docker-compose logs -f rpcgofer

# Stop services
docker-compose down
```

### Production Docker Compose

```yaml
version: "3.8"

services:
  rpcgofer:
    image: jellyb0y/rpcgofer:latest
    container_name: rpcgofer
    restart: always
    ports:
      - "127.0.0.1:8545:8545"
      - "127.0.0.1:8546:8546"
    volumes:
      - ./config.json:/app/config.json:ro
    environment:
      - TZ=UTC
    healthcheck:
      test: ["CMD", "wget", "-q", "--spider", "http://localhost:8545"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 10s
    deploy:
      resources:
        limits:
          cpus: '2'
          memory: 1G
        reservations:
          cpus: '0.5'
          memory: 256M
    logging:
      driver: "json-file"
      options:
        max-size: "50m"
        max-file: "5"
    networks:
      - rpc-network

networks:
  rpc-network:
    driver: bridge
```

## Building from Source

### Prerequisites

- Go 1.22 or later
- Git

### Build Steps

```bash
# Clone repository
git clone https://github.com/jellyb0y/RPCGofer.git
cd RPCGofer

# Download dependencies
go mod download

# Build binary
go build -o rpcgofer ./cmd/rpcgofer

# Or build with optimizations
CGO_ENABLED=0 go build -ldflags="-w -s" -o rpcgofer ./cmd/rpcgofer
```

### Run Binary

```bash
# With config file
./rpcgofer -config config.json

# Or specify path
./rpcgofer -config /etc/rpcgofer/config.json
```

## Systemd Service

### Create Service File

```bash
sudo nano /etc/systemd/system/rpcgofer.service
```

```ini
[Unit]
Description=RPCGofer JSON-RPC Proxy
After=network.target

[Service]
Type=simple
User=rpcgofer
Group=rpcgofer
WorkingDirectory=/opt/rpcgofer
ExecStart=/opt/rpcgofer/rpcgofer -config /opt/rpcgofer/config.json
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal

# Security hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadOnlyPaths=/opt/rpcgofer

[Install]
WantedBy=multi-user.target
```

### Enable and Start

```bash
# Create user
sudo useradd -r -s /bin/false rpcgofer

# Set permissions
sudo chown -R rpcgofer:rpcgofer /opt/rpcgofer

# Enable service
sudo systemctl enable rpcgofer

# Start service
sudo systemctl start rpcgofer

# Check status
sudo systemctl status rpcgofer

# View logs
sudo journalctl -u rpcgofer -f
```

## Reverse Proxy Setup

### Nginx Configuration

```nginx
upstream rpcgofer_http {
    server 127.0.0.1:8545;
    keepalive 32;
}

upstream rpcgofer_ws {
    server 127.0.0.1:8546;
    keepalive 32;
}

server {
    listen 443 ssl http2;
    server_name rpc.example.com;

    ssl_certificate /etc/ssl/certs/rpc.example.com.crt;
    ssl_certificate_key /etc/ssl/private/rpc.example.com.key;

    # HTTP RPC
    location / {
        proxy_pass http://rpcgofer_http;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        
        proxy_connect_timeout 60s;
        proxy_send_timeout 60s;
        proxy_read_timeout 60s;
    }

    # WebSocket
    location /ws/ {
        proxy_pass http://rpcgofer_ws/;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        
        proxy_connect_timeout 60s;
        proxy_send_timeout 3600s;
        proxy_read_timeout 3600s;
    }
}
```

### Traefik Configuration

```yaml
# docker-compose.yml with Traefik
version: "3.8"

services:
  rpcgofer:
    image: jellyb0y/rpcgofer:latest
    labels:
      - "traefik.enable=true"
      # HTTP RPC
      - "traefik.http.routers.rpc.rule=Host(`rpc.example.com`)"
      - "traefik.http.routers.rpc.entrypoints=websecure"
      - "traefik.http.routers.rpc.tls.certresolver=letsencrypt"
      - "traefik.http.services.rpc.loadbalancer.server.port=8545"
      # WebSocket
      - "traefik.http.routers.ws.rule=Host(`ws.example.com`)"
      - "traefik.http.routers.ws.entrypoints=websecure"
      - "traefik.http.routers.ws.tls.certresolver=letsencrypt"
      - "traefik.http.services.ws.loadbalancer.server.port=8546"
    networks:
      - traefik

networks:
  traefik:
    external: true
```

## Health Checks

### HTTP Health Check

```bash
# Simple check
curl -s http://localhost:8545/ethereum \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_chainId","params":[],"id":1}'
```

### Docker Health Check

The provided docker-compose includes health check:

```yaml
healthcheck:
  test: ["CMD", "wget", "-q", "--spider", "http://localhost:8545"]
  interval: 30s
  timeout: 10s
  retries: 3
```

### Kubernetes Probes

```yaml
livenessProbe:
  httpGet:
    path: /ethereum
    port: 8545
  initialDelaySeconds: 10
  periodSeconds: 30

readinessProbe:
  httpGet:
    path: /ethereum
    port: 8545
  initialDelaySeconds: 5
  periodSeconds: 10
```

## Resource Requirements

### Minimum Requirements

| Resource | Value |
|----------|-------|
| CPU | 0.5 cores |
| Memory | 256 MB |
| Disk | 50 MB |

### Recommended Production

| Resource | Value |
|----------|-------|
| CPU | 2 cores |
| Memory | 1 GB |
| Disk | 100 MB |

### Memory Scaling

Memory usage scales with:
- Cache size (configurable)
- Number of WebSocket connections
- Deduplication cache size
- Number of upstream connections

## Monitoring

### Log Output

Configure appropriate log level:

```json
{
  "logLevel": "info"
}
```

| Level | Use Case |
|-------|----------|
| `debug` | Development, troubleshooting |
| `info` | Production (recommended) |
| `warn` | Production (quiet) |
| `error` | Minimal logging |

### Metrics Integration

Parse JSON logs for metrics:

```bash
# Count requests per minute
docker logs rpcgofer 2>&1 | grep "request succeeded" | wc -l

# Watch for errors
docker logs -f rpcgofer 2>&1 | grep -i error
```

## Security Considerations

1. **Bind to localhost**: Use reverse proxy for external access
2. **Use TLS**: Terminate SSL at reverse proxy
3. **Rate limiting**: Implement at reverse proxy level
4. **API key protection**: Don't expose upstream API keys
5. **Network isolation**: Use Docker networks or firewalls
6. **Read-only config**: Mount config as read-only volume

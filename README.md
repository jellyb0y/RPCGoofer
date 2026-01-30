# RPCGofer

High-performance JSON-RPC proxy for Ethereum-compatible blockchain nodes with built-in load balancing, caching, automatic failover, and WebSocket subscription support.

## Features

- **Load Balancing**: Weighted round-robin distribution across multiple upstream nodes
- **Automatic Failover**: Seamless switching to fallback nodes when primary nodes fail
- **Smart Caching**: In-memory LRU cache with TTL for immutable blockchain data
- **WebSocket Subscriptions**: Full support for `eth_subscribe` with event deduplication
- **Shared Subscriptions**: Connection multiplexing - only M upstream connections regardless of client count
- **Health Monitoring**: Real-time block-based health checks with configurable lag threshold
- **Batch Requests**: Native support for JSON-RPC batch processing
- **Retry Logic**: Configurable automatic retries with intelligent error classification
- **Multi-Chain Support**: Configure multiple blockchain networks in a single instance

### Shared Subscriptions

RPCGofer optimizes WebSocket connections through shared subscriptions. Instead of creating N x M connections (N clients x M upstreams), it maintains only M connections per subscription type:

```
100 clients subscribed to newHeads + 3 upstreams = 3 connections (not 300)
```

Events are deduplicated at the shared subscription level and fanned out to all subscribers, including both clients and internal components (health monitoring).

## Quick Start

### Using Docker Hub Image

```bash
# Pull the image
docker pull jellyb0y/rpcgofer:latest

# Create config.json file (see Configuration Example below)

# Run container
docker run -d \
  --name rpcgofer \
  -p 8545:8545 \
  -p 8546:8546 \
  -v $(pwd)/config.json:/app/config.json:ro \
  jellyb0y/rpcgofer:latest
```

### Using Docker Compose

```bash
# Clone the repository
git clone https://github.com/jellyb0y/RPCGofer.git
cd RPCGofer

# Copy and edit configuration
cp config.example.json config.json

# Start with Docker Compose
docker-compose up -d
```

### Building from Source

```bash
# Build
go build -o rpcgofer ./cmd/rpcgofer

# Run
./rpcgofer -config config.json
```

## Configuration Example

```json
{
  "host": "0.0.0.0",
  "rpcPort": 8545,
  "wsPort": 8546,
  "logLevel": "info",
  "retryEnabled": true,
  "retryMaxAttempts": 3,
  "cache": {
    "enabled": true,
    "ttl": 300,
    "size": 10000
  },
  "groups": [
    {
      "name": "ethereum",
      "upstreams": [
        {
          "name": "infura",
          "rpcUrl": "https://mainnet.infura.io/v3/YOUR_API_KEY",
          "wsUrl": "wss://mainnet.infura.io/ws/v3/YOUR_API_KEY",
          "weight": 10,
          "role": "main"
        },
        {
          "name": "alchemy",
          "rpcUrl": "https://eth-mainnet.g.alchemy.com/v2/YOUR_API_KEY",
          "weight": 10,
          "role": "main"
        },
        {
          "name": "public-fallback",
          "rpcUrl": "https://eth.llamarpc.com",
          "weight": 5,
          "role": "fallback"
        }
      ]
    }
  ]
}
```

## Usage

After starting RPCGofer, endpoints become available at:

- **HTTP RPC**: `http://localhost:8545/{group_name}`
- **WebSocket**: `ws://localhost:8546/{group_name}`

Example requests:

```bash
# Single HTTP RPC request
curl -X POST http://localhost:8545/ethereum \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'

# Batch HTTP RPC request
curl -X POST http://localhost:8545/ethereum \
  -H "Content-Type: application/json" \
  -d '[
    {"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1},
    {"jsonrpc":"2.0","method":"eth_chainId","params":[],"id":2}
  ]'

# WebSocket subscription (using wscat)
wscat -c ws://localhost:8546/ethereum
> {"jsonrpc":"2.0","method":"eth_subscribe","params":["newHeads"],"id":1}
```

## Documentation

See the [docs](./docs) folder for detailed documentation:

- [Getting Started](./docs/getting-started.md)
- [Architecture Overview](./docs/architecture.md)
- [Configuration Reference](./docs/configuration.md)
- [Load Balancing](./docs/load-balancing.md)
- [Health Monitoring](./docs/health-monitoring.md)
- [Caching System](./docs/caching.md)
- [WebSocket Subscriptions](./docs/subscriptions.md)
- [Deployment](./docs/deployment.md)

## License

MIT

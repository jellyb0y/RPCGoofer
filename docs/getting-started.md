# Getting Started

## Prerequisites

- Go 1.22 or later (for building from source)
- Docker and Docker Compose (for containerized deployment)

## Installation

### Using Docker Hub Image

```bash
docker pull jellyb0y/rpcgofer:latest
```

### Building from Source

```bash
git clone https://github.com/jellyb0y/RPCGofer.git
cd RPCGofer
go build -o rpcgofer ./cmd/rpcgofer
```

### Building Docker Image Locally

```bash
docker build -t rpcgofer .
```

## Quick Start

1. Create a configuration file `config.json`:

```json
{
  "host": "0.0.0.0",
  "rpcPort": 8545,
  "wsPort": 8546,
  "logLevel": "info",
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

2. Run RPCGofer:

```bash
./rpcgofer -config config.json
```

3. Send requests to RPCGofer:

```bash
# HTTP JSON-RPC
curl -X POST http://localhost:8545/ethereum \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'

# WebSocket
wscat -c ws://localhost:8546/ethereum
```

## Docker Deployment

### Using Docker Compose

1. Create your `config.json` file

2. Run with Docker Compose:

```bash
docker-compose up -d
```

### Using Docker Hub Image

```bash
docker run -d \
  --name rpcgofer \
  -p 8545:8545 \
  -p 8546:8546 \
  -v $(pwd)/config.json:/app/config.json:ro \
  jellyb0y/rpcgofer:latest
```

### Using Locally Built Image

```bash
docker run -d \
  --name rpcgofer \
  -p 8545:8545 \
  -p 8546:8546 \
  -v $(pwd)/config.json:/app/config.json:ro \
  rpcgofer
```

## Endpoints

After starting RPCGofer, endpoints are available based on your configured groups:

| Protocol | URL Pattern | Example |
|----------|-------------|---------|
| HTTP RPC | `http://{host}:{rpcPort}/{group}` | `http://localhost:8545/ethereum` |
| WebSocket | `ws://{host}:{wsPort}/{group}` | `ws://localhost:8546/ethereum` |

## Next Steps

- [Configuration](configuration.md) - Learn about all configuration options
- [Features](features.md) - Explore RPCGofer's capabilities
- [Architecture](architecture.md) - Understand how RPCGofer works

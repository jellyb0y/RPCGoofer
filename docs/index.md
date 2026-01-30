# RPCGofer Documentation

RPCGofer is a high-performance JSON-RPC proxy designed for Ethereum-compatible blockchain nodes. It provides load balancing, caching, automatic failover, and WebSocket subscription support.

## Table of Contents

1. [Getting Started](./getting-started.md)
   - Prerequisites
   - Installation
   - Quick Start
   - Endpoints

2. [Architecture Overview](./architecture.md)
   - System Components
   - Request Flow
   - Batch Requests
   - Module Structure

3. [Configuration Reference](./configuration.md)
   - Server Settings
   - Cache Configuration
   - Upstream Groups
   - Default Values

4. [Load Balancing](./load-balancing.md)
   - Weighted Round-Robin Algorithm
   - Main vs Fallback Upstreams
   - Retry Logic

5. [Health Monitoring](./health-monitoring.md)
   - Block-Based Health Checks
   - WebSocket vs Polling Monitoring
   - Lag Threshold Configuration
   - Request Statistics Logging

6. [Caching System](./caching.md)
   - Cacheable Methods
   - Cache Key Generation
   - TTL and Eviction

7. [WebSocket Subscriptions](./subscriptions.md)
   - Supported Subscription Types
   - Multi-Upstream Aggregation
   - Event Deduplication

8. [Plugins](./plugins.md)
   - Writing Custom RPC Methods
   - Available APIs (upstream, utils, console)
   - Configuration and Directory Setup
   - Examples and Best Practices

9. [Request Batching](./batching.md)
   - Automatic Request Coalescing
   - Configuration and Batch Keys
   - Performance Tuning

10. [Deployment](./deployment.md)
   - Docker and Docker Compose
   - Building from Source
   - Systemd Service
   - Reverse Proxy Setup

## Quick Links

- [Configuration Example](../config.example.json)
- [Dockerfile](../Dockerfile)
- [Docker Compose](../docker-compose.yml)

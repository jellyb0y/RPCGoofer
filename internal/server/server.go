package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/rs/zerolog"

	"rpcgofer/internal/balancer"
	"rpcgofer/internal/batcher"
	"rpcgofer/internal/cache"
	"rpcgofer/internal/config"
	"rpcgofer/internal/plugin"
	"rpcgofer/internal/proxy"
	"rpcgofer/internal/subscription"
	"rpcgofer/internal/upstream"
	"rpcgofer/internal/ws"
)

// Server represents the main server
type Server struct {
	cfg              *config.Config
	router           *proxy.Router
	cache            cache.Cache
	pluginManager    *plugin.PluginManager
	batchAggregator  *batcher.Aggregator
	subManager *subscription.Manager
	registries map[string]*subscription.Registry // pool name -> subscription registry
	rpcServer  *http.Server
	wsServer         *http.Server
	logger           zerolog.Logger
}

// New creates a new Server
func New(cfg *config.Config, logger zerolog.Logger) (*Server, error) {
	router := proxy.NewRouter()

	// Create cache based on config
	var rpcCache cache.Cache
	if cfg.IsCacheEnabled() {
		var err error
		rpcCache, err = cache.NewMemoryCache(cfg.Cache.Size, cfg.Cache.GetTTLDuration())
		if err != nil {
			return nil, fmt.Errorf("failed to create cache: %w", err)
		}

		// Set disabled methods if configured
		if len(cfg.Cache.DisabledMethods) > 0 {
			cache.SetDisabledMethods(cfg.Cache.DisabledMethods)
			logger.Info().
				Strs("disabledMethods", cfg.Cache.DisabledMethods).
				Msg("cache disabled for specific methods")
		}

		logger.Info().
			Int("size", cfg.Cache.Size).
			Int("ttl", cfg.Cache.TTL).
			Msg("cache enabled")
	} else {
		rpcCache = cache.NewNoopCache()
		logger.Info().Msg("cache disabled")
	}

	// Create plugin manager based on config
	var pluginMgr *plugin.PluginManager
	if cfg.IsPluginsEnabled() {
		pluginMgr = plugin.NewPluginManager(logger)
		pluginMgr.SetTimeout(cfg.GetPluginTimeoutDuration())

		if err := pluginMgr.LoadFromDirectory(cfg.GetPluginDirectory()); err != nil {
			return nil, fmt.Errorf("failed to load plugins: %w", err)
		}

		methods := pluginMgr.GetMethods()
		if len(methods) > 0 {
			logger.Info().
				Strs("methods", methods).
				Str("directory", cfg.GetPluginDirectory()).
				Msg("plugins enabled")
		} else {
			logger.Info().
				Str("directory", cfg.GetPluginDirectory()).
				Msg("plugins enabled but no plugins loaded")
		}
	} else {
		logger.Info().Msg("plugins disabled")
	}

	// Create batch aggregator based on config
	var batchAgg *batcher.Aggregator
	if cfg.IsBatchingEnabled() {
		batchAgg = batcher.NewAggregator(cfg.Batching, logger)
		methods := batchAgg.GetMethods()

		// Disable caching for batched methods
		cache.AddDisabledMethods(methods)

		logger.Info().
			Strs("methods", methods).
			Msg("batching enabled")
	} else {
		logger.Info().Msg("batching disabled")
	}

	registries := make(map[string]*subscription.Registry)
	subManager := subscription.NewManager(cfg, registries, logger)

	return &Server{
		cfg:             cfg,
		router:          router,
		cache:           rpcCache,
		pluginManager:   pluginMgr,
		batchAggregator: batchAgg,
		subManager:      subManager,
		registries:      registries,
		logger:          logger,
	}, nil
}

// AddGroup adds an upstream group to the server
func (s *Server) AddGroup(groupCfg config.GroupConfig) {
	pool := upstream.NewPool(groupCfg, s.cfg, s.logger)
	pool.SetSelector(balancer.NewWeightedRoundRobin(pool))

	subRegistry := subscription.NewRegistry(s.cfg.DedupCacheSize, s.logger)
	s.registries[groupCfg.Name] = subRegistry

	newHeadsProvider := subscription.NewRegistryNewHeadsProviderAdapter(subRegistry)
	pool.SetNewHeadsProvider(newHeadsProvider)
	pool.SetOnUpstreamWSConnected(func(u *upstream.Upstream) {
		u.SetSubscriptionRegistry(subRegistry)
		if u.WsClient() != nil {
			u.WsClient().SetOnDisconnect(func() { subRegistry.Unregister(u.Name()) })
			u.WsClient().SetOnReconnected(func() { subRegistry.Register(u.Name(), u) })
		}
		subRegistry.Register(u.Name(), u)
	})

	if s.batchAggregator != nil {
		pool.SetBatchStats(s.batchAggregator)
	}

	s.router.AddPool(pool)
	s.logger.Info().
		Str("group", groupCfg.Name).
		Int("upstreams", len(groupCfg.Upstreams)).
		Msg("added group with shared subscription manager")
}

// Start starts the server
func (s *Server) Start() error {
	// Start all pools (health monitors)
	s.router.StartAll()

	// Create HTTP RPC handler
	rpcHandler := proxy.NewHandler(s.router, s.cache, s.pluginManager, s.batchAggregator, s.cfg, s.logger)

	// Set batch executor if batching is enabled
	if s.batchAggregator != nil {
		s.batchAggregator.SetExecutor(proxy.NewHandlerExecutor(rpcHandler))
	}

	// Create WebSocket handler
	wsHandler := ws.NewHandler(s.router, s.cache, s.subManager, s.cfg, s.pluginManager, s.batchAggregator, s.logger)

	rpcAddr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.RPCPort)
	wsAddr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.WSPort)

	// Start RPC server
	s.rpcServer = &http.Server{
		Addr:         rpcAddr,
		Handler:      rpcHandler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		s.logger.Info().
			Str("addr", rpcAddr).
			Msg("starting RPC server")
		if err := s.rpcServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Error().Err(err).Msg("RPC server error")
		}
	}()

	// Start WebSocket server
	s.wsServer = &http.Server{
		Addr:         wsAddr,
		Handler:      wsHandler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		s.logger.Info().
			Str("addr", wsAddr).
			Msg("starting WebSocket server")
		if err := s.wsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Error().Err(err).Msg("WebSocket server error")
		}
	}()

	// Log available endpoints
	for _, name := range s.router.GetGroupNames() {
		s.logger.Info().
			Str("group", name).
			Str("rpc", fmt.Sprintf("http://%s/%s", rpcAddr, name)).
			Str("ws", fmt.Sprintf("ws://%s/%s", wsAddr, name)).
			Msg("endpoint available")
	}

	return nil
}

// Stop gracefully stops the server
func (s *Server) Stop(ctx context.Context) error {
	s.logger.Info().Msg("shutting down server...")

	// Close subscription manager (client sessions)
	s.subManager.CloseAll()

	// Shutdown HTTP servers
	var rpcErr, wsErr error

	if s.rpcServer != nil {
		rpcErr = s.rpcServer.Shutdown(ctx)
	}
	if s.wsServer != nil {
		wsErr = s.wsServer.Shutdown(ctx)
	}

	// Stop all pools (this will unsubscribe health monitors from shared subscriptions)
	s.router.StopAll()

	for name, reg := range s.registries {
		reg.Close()
		s.logger.Debug().Str("group", name).Msg("closed subscription registry")
	}

	// Close batch aggregator
	if s.batchAggregator != nil {
		s.batchAggregator.Close(ctx)
	}

	// Close plugin manager
	if s.pluginManager != nil {
		s.pluginManager.Close()
	}

	// Close cache
	if s.cache != nil {
		s.cache.Close()
	}

	if rpcErr != nil {
		return fmt.Errorf("RPC server shutdown error: %w", rpcErr)
	}
	if wsErr != nil {
		return fmt.Errorf("WebSocket server shutdown error: %w", wsErr)
	}

	s.logger.Info().Msg("server stopped")
	return nil
}

// GetRouter returns the router
func (s *Server) GetRouter() *proxy.Router {
	return s.router
}

// GetSubscriptionManager returns the subscription manager
func (s *Server) GetSubscriptionManager() *subscription.Manager {
	return s.subManager
}

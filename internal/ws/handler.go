package ws

import (
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"

	"rpcgofer/internal/cache"
	"rpcgofer/internal/config"
	"rpcgofer/internal/proxy"
	"rpcgofer/internal/subscription"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins
	},
}

// Handler handles WebSocket connections
type Handler struct {
	router     *proxy.Router
	cache      cache.Cache
	subManager *subscription.Manager
	cfg        *config.Config
	logger     zerolog.Logger
}

// NewHandler creates a new WebSocket handler
func NewHandler(router *proxy.Router, rpcCache cache.Cache, subManager *subscription.Manager, cfg *config.Config, logger zerolog.Logger) *Handler {
	return &Handler{
		router:     router,
		cache:      rpcCache,
		subManager: subManager,
		cfg:        cfg,
		logger:     logger.With().Str("component", "ws").Logger(),
	}
}

// ServeHTTP handles WebSocket upgrade requests
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Get pool from path
	pool, err := h.router.GetPoolFromPath(r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// Extract group name from path
	groupName := extractGroupName(r.URL.Path)

	// Upgrade to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Error().Err(err).Msg("failed to upgrade connection")
		return
	}

	h.logger.Info().
		Str("group", pool.Name()).
		Str("remoteAddr", r.RemoteAddr).
		Msg("new WebSocket connection")

	// Create and run client
	client := NewClient(conn, pool, groupName, h.cache, h.subManager, h.cfg, h.logger.With().Str("remoteAddr", r.RemoteAddr).Logger())
	client.Run(r.Context())
}

// extractGroupName extracts the group name from a URL path
func extractGroupName(path string) string {
	path = strings.TrimPrefix(path, "/")
	if idx := strings.Index(path, "/"); idx != -1 {
		path = path[:idx]
	}
	return path
}

package subscription

import (
	"context"
	"encoding/json"
	"sync"
	"unsafe"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"

	"rpcgofer/internal/config"
	"rpcgofer/internal/upstream"
)

// Manager manages all subscription sessions
type Manager struct {
	sessions   map[uintptr]*ClientSession
	registries map[string]*Registry // pool name -> subscription registry
	mu         sync.RWMutex
	maxSubs    int
	dedupSize  int
	logger     zerolog.Logger
}

// NewManager creates a new subscription Manager. Registries must be set per group before sessions are created.
func NewManager(cfg *config.Config, registries map[string]*Registry, logger zerolog.Logger) *Manager {
	return &Manager{
		sessions:   make(map[uintptr]*ClientSession),
		registries: registries,
		maxSubs:    cfg.MaxSubscriptionsPerClient,
		dedupSize:  cfg.DedupCacheSize,
		logger:     logger.With().Str("component", "subscription").Logger(),
	}
}

// connKey returns a unique key for the connection
func connKey(conn *websocket.Conn) uintptr {
	return uintptr(unsafe.Pointer(conn))
}

// GetOrCreateSession gets or creates a session for the given connection
func (m *Manager) GetOrCreateSession(conn *websocket.Conn, sendFunc SendFunc, pool *upstream.Pool, groupName string) (*ClientSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := connKey(conn)
	if session, ok := m.sessions[key]; ok {
		return session, nil
	}

	var backend SubscriptionBackend
	if m.registries != nil {
		backend = m.registries[groupName]
	}
	session, err := NewClientSession(sendFunc, pool, backend, m.maxSubs, m.dedupSize, m.logger)
	if err != nil {
		return nil, err
	}

	m.sessions[key] = session
	m.logger.Debug().Str("group", groupName).Msg("created new client session")
	return session, nil
}

// GetSession returns the session for the given connection
func (m *Manager) GetSession(conn *websocket.Conn) *ClientSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[connKey(conn)]
}

// RemoveSession removes and closes a session
func (m *Manager) RemoveSession(conn *websocket.Conn) {
	m.mu.Lock()
	key := connKey(conn)
	session, ok := m.sessions[key]
	if ok {
		delete(m.sessions, key)
	}
	m.mu.Unlock()

	if session != nil {
		session.Close()
		m.logger.Debug().Msg("removed client session")
	}
}

// Subscribe creates a subscription for a client
func (m *Manager) Subscribe(ctx context.Context, conn *websocket.Conn, sendFunc SendFunc, pool *upstream.Pool, groupName string, subType string, params json.RawMessage) (string, error) {
	session, err := m.GetOrCreateSession(conn, sendFunc, pool, groupName)
	if err != nil {
		return "", err
	}

	return session.Subscribe(ctx, SubscriptionType(subType), params)
}

// Unsubscribe removes a subscription
func (m *Manager) Unsubscribe(conn *websocket.Conn, subID string) error {
	session := m.GetSession(conn)
	if session == nil {
		return nil
	}

	return session.Unsubscribe(subID)
}

// CloseAll closes all sessions
func (m *Manager) CloseAll() {
	m.mu.Lock()
	sessions := make([]*ClientSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.sessions = make(map[uintptr]*ClientSession)
	m.mu.Unlock()

	for _, session := range sessions {
		session.Close()
	}
	m.logger.Info().Int("sessions", len(sessions)).Msg("closed all sessions")
}

// GetSessionCount returns the number of active sessions
func (m *Manager) GetSessionCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions)
}

// GetTotalSubscriptionCount returns the total number of subscriptions across all sessions
func (m *Manager) GetTotalSubscriptionCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	total := 0
	for _, session := range m.sessions {
		total += session.GetSubscriptionCount()
	}
	return total
}

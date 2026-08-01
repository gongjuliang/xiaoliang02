package control

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"nattuser/internal/db"
	"nattuser/internal/model"
)

const (
	defaultConnectionStateFlushInterval = 30 * time.Second
	defaultConnectionStateRetention     = 2 * time.Minute
)

type cachedConnectionState struct {
	status      model.ServerConnectionStatus
	lastError   string
	remotePort  int
	dirty       bool
	persistedAt time.Time
	version     uint64
}

type connectionStateCache struct {
	database      *sql.DB
	flushInterval time.Duration
	retention     time.Duration

	mu      sync.RWMutex
	entries map[int64]cachedConnectionState
}

func newConnectionStateCache(database *sql.DB, flushInterval time.Duration, retention time.Duration) *connectionStateCache {
	if flushInterval <= 0 {
		flushInterval = defaultConnectionStateFlushInterval
	}
	if retention <= 0 {
		retention = defaultConnectionStateRetention
	}
	return &connectionStateCache{
		database:      database,
		flushInterval: flushInterval,
		retention:     retention,
		entries:       make(map[int64]cachedConnectionState),
	}
}

func (c *connectionStateCache) set(connectionID int64, status model.ServerConnectionStatus, lastError string, remotePort int, now time.Time) {
	if connectionID <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[connectionID]
	entry.status = status
	entry.lastError = lastError
	if remotePort > 0 {
		entry.remotePort = remotePort
	}
	entry.dirty = true
	entry.persistedAt = time.Time{}
	entry.version++
	c.entries[connectionID] = entry
}

func (c *connectionStateCache) markPersisted(connectionID int64, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[connectionID]
	if !ok {
		return
	}
	entry.dirty = false
	entry.persistedAt = now
	c.entries[connectionID] = entry
}

func (c *connectionStateCache) removeExpired(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for connectionID, entry := range c.entries {
		if entry.status != model.ServerConnectionStatusConnected && !entry.dirty && !entry.persistedAt.IsZero() && now.Sub(entry.persistedAt) > c.retention {
			delete(c.entries, connectionID)
		}
	}
}

func (c *connectionStateCache) overlay(connection model.ServerConnection) model.ServerConnection {
	c.mu.RLock()
	entry, ok := c.entries[connection.ID]
	c.mu.RUnlock()
	if !ok {
		return connection
	}
	connection.Status = entry.status
	connection.LastError = entry.lastError
	if entry.remotePort > 0 {
		connection.RemotePort = entry.remotePort
	}
	return connection
}

func (c *connectionStateCache) overlayAll(connections []model.ServerConnection) []model.ServerConnection {
	for index := range connections {
		connections[index] = c.overlay(connections[index])
	}
	return connections
}

func (c *connectionStateCache) remove(connectionID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, connectionID)
}

func (c *connectionStateCache) flush(ctx context.Context) error {
	if c.database == nil {
		return nil
	}
	type pendingState struct {
		id      int64
		entry   cachedConnectionState
		version uint64
	}
	c.mu.RLock()
	pending := make([]pendingState, 0, len(c.entries))
	for connectionID, entry := range c.entries {
		if entry.dirty {
			pending = append(pending, pendingState{id: connectionID, entry: entry, version: entry.version})
		}
	}
	c.mu.RUnlock()

	for _, state := range pending {
		err := db.RetrySQLiteBusy(ctx, func() error {
			return db.SetServerConnectionRuntimeState(ctx, c.database, state.id, state.entry.status, state.entry.lastError, state.entry.remotePort)
		})
		if err != nil {
			return err
		}
		c.mu.Lock()
		entry, ok := c.entries[state.id]
		if ok && entry.version == state.version {
			entry.dirty = false
			entry.persistedAt = time.Now()
			c.entries[state.id] = entry
		}
		c.mu.Unlock()
	}
	c.removeExpired(time.Now())
	return nil
}

func (c *connectionStateCache) flushOne(ctx context.Context, connectionID int64) error {
	return c.flush(ctx)
}

func (c *connectionStateCache) run(ctx context.Context, logError func(string, ...any)) {
	ticker := time.NewTicker(c.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if err := c.flush(context.Background()); err != nil && logError != nil {
				logError("flush connection state cache failed: %v", err)
			}
			return
		case <-ticker.C:
			if err := c.flush(ctx); err != nil && logError != nil {
				logError("flush connection state cache failed: %v", err)
			}
		}
	}
}

package control

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"nattserver/internal/db"
	"nattserver/internal/model"
)

const (
	defaultTunnelStateFlushInterval = 30 * time.Second
	defaultTunnelStateRetention     = 2 * time.Minute
)

type cachedTunnelState struct {
	online      bool
	lastIP      string
	lastSeen    time.Time
	offline     bool
	lastError   string
	dirty       bool
	persistedAt time.Time
	version     uint64
}

type tunnelStateCache struct {
	database      *sql.DB
	flushInterval time.Duration
	retention     time.Duration

	mu      sync.RWMutex
	flushMu sync.Mutex
	entries map[int64]cachedTunnelState
}

func newTunnelStateCache(database *sql.DB, flushInterval time.Duration, retention time.Duration) *tunnelStateCache {
	if flushInterval <= 0 {
		flushInterval = defaultTunnelStateFlushInterval
	}
	if retention <= 0 {
		retention = defaultTunnelStateRetention
	}
	return &tunnelStateCache{
		database:      database,
		flushInterval: flushInterval,
		retention:     retention,
		entries:       make(map[int64]cachedTunnelState),
	}
}

func (c *tunnelStateCache) markOnline(tunnelID int64, ip string, now time.Time) {
	if tunnelID <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[tunnelID]
	entry.online = true
	if ip != "" {
		entry.lastIP = ip
	}
	entry.lastSeen = now
	entry.offline = false
	entry.lastError = ""
	entry.dirty = true
	entry.persistedAt = time.Time{}
	entry.version++
	c.entries[tunnelID] = entry
}

func (c *tunnelStateCache) markOffline(tunnelID int64, reason string, now time.Time) {
	if tunnelID <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[tunnelID]
	entry.online = false
	entry.lastSeen = now
	entry.offline = true
	entry.lastError = reason
	entry.dirty = true
	entry.persistedAt = time.Time{}
	entry.version++
	c.entries[tunnelID] = entry
}

func (c *tunnelStateCache) markPersisted(tunnelID int64, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[tunnelID]
	if !ok {
		return
	}
	entry.dirty = false
	entry.persistedAt = now
	c.entries[tunnelID] = entry
}

func (c *tunnelStateCache) removeExpired(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for tunnelID, entry := range c.entries {
		if entry.offline && !entry.dirty && !entry.persistedAt.IsZero() && now.Sub(entry.persistedAt) > c.retention {
			delete(c.entries, tunnelID)
		}
	}
}

func (c *tunnelStateCache) overlay(tunnel model.Tunnel) model.Tunnel {
	c.mu.RLock()
	entry, ok := c.entries[tunnel.ID]
	c.mu.RUnlock()
	if ok && entry.offline {
		tunnel.Status = model.TunnelStatusError
		tunnel.LastError = entry.lastError
	}
	return tunnel
}

func (c *tunnelStateCache) overlayAll(tunnels []model.Tunnel) []model.Tunnel {
	for index := range tunnels {
		tunnels[index] = c.overlay(tunnels[index])
	}
	return tunnels
}

func (c *tunnelStateCache) remove(tunnelID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, tunnelID)
}

func (c *tunnelStateCache) flush(ctx context.Context) error {
	if c.database == nil {
		return nil
	}
	c.flushMu.Lock()
	defer c.flushMu.Unlock()
	type pendingState struct {
		id      int64
		entry   cachedTunnelState
		version uint64
	}
	c.mu.RLock()
	pending := make([]pendingState, 0, len(c.entries))
	for tunnelID, entry := range c.entries {
		if entry.dirty {
			pending = append(pending, pendingState{id: tunnelID, entry: entry, version: entry.version})
		}
	}
	c.mu.RUnlock()

	for _, state := range pending {
		var err error
		if state.entry.offline {
			err = db.MarkTunnelControlOffline(ctx, c.database, state.id, state.entry.lastError)
		} else {
			err = db.RetrySQLiteBusy(ctx, func() error {
				return db.MarkTunnelKeyOnline(ctx, c.database, state.id, state.entry.lastIP)
			})
		}
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

func (c *tunnelStateCache) run(ctx context.Context, logError func(string, ...any)) {
	ticker := time.NewTicker(c.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if err := c.flush(context.Background()); err != nil && logError != nil {
				logError("flush tunnel state cache failed: %v", err)
			}
			return
		case <-ticker.C:
			if err := c.flush(ctx); err != nil && logError != nil {
				logError("flush tunnel state cache failed: %v", err)
			}
		}
	}
}

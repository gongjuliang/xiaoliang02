package control

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"nattserver/internal/db"
	"nattserver/internal/logger"
)

const defaultTrafficFlushInterval = time.Second

type trafficRecorder struct {
	database      *sql.DB
	log           *logger.Logger
	flushInterval time.Duration

	mu      sync.Mutex
	pending map[int64]trafficDelta
}

type trafficDelta struct {
	connectionCountDelta   int64
	activeConnectionsDelta int64
	bytesInDelta           int64
	bytesOutDelta          int64
}

func newTrafficRecorder(database *sql.DB, log *logger.Logger, flushInterval time.Duration) *trafficRecorder {
	if flushInterval <= 0 {
		flushInterval = defaultTrafficFlushInterval
	}
	return &trafficRecorder{
		database:      database,
		log:           log,
		flushInterval: flushInterval,
		pending:       make(map[int64]trafficDelta),
	}
}

func (r *trafficRecorder) run(ctx context.Context) {
	ticker := time.NewTicker(r.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// Flush with a fresh background context so shutdown still persists
			// counters after the caller's context has already been canceled.
			r.flush(context.Background())
			return
		case <-ticker.C:
			r.flush(ctx)
		}
	}
}

func (r *trafficRecorder) recordConnectionOpen(tunnelID int64) {
	r.add(tunnelID, trafficDelta{connectionCountDelta: 1, activeConnectionsDelta: 1})
}

func (r *trafficRecorder) recordConnectionClose(tunnelID int64) {
	r.add(tunnelID, trafficDelta{activeConnectionsDelta: -1})
}

func (r *trafficRecorder) recordTrafficDelta(tunnelID int64, bytesIn int64, bytesOut int64) {
	if bytesIn == 0 && bytesOut == 0 {
		return
	}
	r.add(tunnelID, trafficDelta{bytesInDelta: bytesIn, bytesOutDelta: bytesOut})
}

func (r *trafficRecorder) add(tunnelID int64, delta trafficDelta) {
	if tunnelID <= 0 {
		return
	}
	// Traffic is updated from proxy goroutines, so deltas are batched in memory
	// and written periodically instead of issuing a SQLite write per packet.
	r.mu.Lock()
	current := r.pending[tunnelID]
	current.connectionCountDelta += delta.connectionCountDelta
	current.activeConnectionsDelta += delta.activeConnectionsDelta
	current.bytesInDelta += delta.bytesInDelta
	current.bytesOutDelta += delta.bytesOutDelta
	r.pending[tunnelID] = current
	r.mu.Unlock()
}

func (r *trafficRecorder) flush(ctx context.Context) {
	pending := r.takePending()
	if len(pending) == 0 || r.database == nil {
		return
	}
	for tunnelID, delta := range pending {
		if err := db.ApplyTunnelTrafficDelta(ctx, r.database, tunnelID, delta.connectionCountDelta, delta.activeConnectionsDelta, delta.bytesInDelta, delta.bytesOutDelta); err != nil {
			r.logError("flush traffic stats failed tunnel_id=%d: %v", tunnelID, err)
		}
	}
}

func (r *trafficRecorder) takePending() map[int64]trafficDelta {
	r.mu.Lock()
	defer r.mu.Unlock()
	pending := r.pending
	r.pending = make(map[int64]trafficDelta)
	return pending
}

func (r *trafficRecorder) logError(format string, args ...any) {
	if r.log != nil {
		r.log.Errorf(format, args...)
	}
}

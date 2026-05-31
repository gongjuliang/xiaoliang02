package api

import (
	"sync"
	"time"
)

type RateLimiter struct {
	limit    int
	window   time.Duration
	mu       sync.Mutex
	attempts map[string]rateState
}

type rateState struct {
	count     int
	resetTime time.Time
}

func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	if limit <= 0 {
		limit = 10
	}
	return &RateLimiter{
		limit:    limit,
		window:   window,
		attempts: make(map[string]rateState),
	}
}

func (r *RateLimiter) Allow(key string) bool {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.attempts[key]
	if state.resetTime.IsZero() || now.After(state.resetTime) {
		r.attempts[key] = rateState{count: 1, resetTime: now.Add(r.window)}
		return true
	}
	if state.count >= r.limit {
		return false
	}
	state.count++
	r.attempts[key] = state
	return true
}

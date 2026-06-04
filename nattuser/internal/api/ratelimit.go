package api

import (
	"fmt"
	"sync"
	"time"
)

type RateLimiter struct {
	mu       sync.Mutex
	failures map[string]rateState
}

type rateState struct {
	failures int
	level    int
	bannedTo time.Time
}

func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		failures: make(map[string]rateState),
	}
}

func (r *RateLimiter) Allow(key string) (time.Duration, bool) {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.failures[key]
	if now.Before(state.bannedTo) {
		return time.Until(state.bannedTo), false
	}
	return 0, true
}

func (r *RateLimiter) RecordFailure(key string) {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.failures[key]
	if now.Before(state.bannedTo) {
		r.failures[key] = state
		return
	}
	state.failures++
	if state.failures >= 10 {
		state.failures = 0
		state.level++
		state.bannedTo = now.Add(banDuration(state.level))
	}
	r.failures[key] = state
}

func (r *RateLimiter) RecordSuccess(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.failures[key]
	state.failures = 0
	state.bannedTo = time.Time{}
	r.failures[key] = state
}

func banDuration(level int) time.Duration {
	switch {
	case level <= 1:
		return 5 * time.Minute
	case level == 2:
		return 10 * time.Minute
	case level == 3:
		return 30 * time.Minute
	case level == 4:
		return time.Hour
	default:
		return 6 * time.Hour
	}
}

func formatBanDuration(duration time.Duration) string {
	if duration <= 0 {
		return "稍后"
	}
	minutes := int(duration.Round(time.Minute) / time.Minute)
	if minutes < 1 {
		minutes = 1
	}
	if minutes < 60 {
		return fmt.Sprintf("%d 分钟", minutes)
	}
	hours := minutes / 60
	if minutes%60 != 0 {
		hours++
	}
	return fmt.Sprintf("%d 小时", hours)
}

// Package ratelimit provides the two throttles mandated by the plan: a
// per-app sending limit (token bucket) and a per-IP authentication failure
// lockout shared by the SMTP and management surfaces.
package ratelimit

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limiter bounds each app to its configured messages-per-hour. Buckets are
// created lazily and re-created when the app's configuration changes.
type Limiter struct {
	mu      sync.Mutex
	buckets map[int64]*rate.Limiter
}

// NewLimiter builds an empty Limiter.
func NewLimiter() *Limiter {
	return &Limiter{buckets: make(map[int64]*rate.Limiter)}
}

// Allow reports whether app may send one more message this hour. A perHour
// value of zero or less (invalid configuration) is treated as unlimited.
func (l *Limiter) Allow(appID int64, perHour int) bool {
	if perHour <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	bucket, ok := l.buckets[appID]
	if !ok {
		bucket = rate.NewLimiter(rate.Limit(float64(perHour)/3600.0), perHour)
		l.buckets[appID] = bucket
	}
	return bucket.Allow()
}

// Update replaces the bucket for app, e.g. after a configuration change.
func (l *Limiter) Update(appID int64, perHour int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if perHour <= 0 {
		delete(l.buckets, appID)
		return
	}
	l.buckets[appID] = rate.NewLimiter(rate.Limit(float64(perHour)/3600.0), perHour)
}

// Remove drops the bucket for app, e.g. after the app is deleted.
func (l *Limiter) Remove(appID int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, appID)
}

// Lockout parameters from the plan: 10 failures within 10 minutes ban the IP
// for 1 hour.
const (
	DefaultFailureLimit  = 10
	DefaultFailureWindow = 10 * time.Minute
	DefaultBanDuration   = time.Hour
	// maxTrackedIPs bounds the failure map; beyond it stale entries are
	// pruned on the next event.
	maxTrackedIPs = 4096
)

type ipState struct {
	failures    int
	windowStart time.Time
	bannedUntil time.Time
}

// Lockout bans IPs that authenticate unsuccessfully too often. Both the SMTP
// and the management surface share one instance.
type Lockout struct {
	limit int
	window,
	ban time.Duration

	mu    sync.Mutex
	state map[string]*ipState
}

// NewLockout builds a Lockout with the plan's defaults; non-positive
// parameters fall back to them.
func NewLockout(failureLimit int, window, ban time.Duration) *Lockout {
	if failureLimit <= 0 {
		failureLimit = DefaultFailureLimit
	}
	if window <= 0 {
		window = DefaultFailureWindow
	}
	if ban <= 0 {
		ban = DefaultBanDuration
	}
	return &Lockout{limit: failureLimit, window: window, ban: ban, state: make(map[string]*ipState)}
}

// Banned reports whether ip is currently banned.
func (l *Lockout) Banned(ip string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	st, ok := l.state[ip]
	if !ok {
		return false
	}
	return now.Before(st.bannedUntil)
}

// RecordFailure counts one failed authentication for ip and bans it when the
// limit is reached inside the window.
func (l *Lockout) RecordFailure(ip string) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	l.pruneLocked(now)
	st, ok := l.state[ip]
	if !ok {
		st = &ipState{windowStart: now}
		l.state[ip] = st
	}
	if now.Sub(st.windowStart) > l.window {
		st.windowStart = now
		st.failures = 0
	}
	st.failures++
	if st.failures >= l.limit {
		st.bannedUntil = now.Add(l.ban)
		st.failures = 0
		st.windowStart = now
	}
}

// Reset clears the failure count for ip, e.g. after a successful login.
func (l *Lockout) Reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.state, ip)
}

// pruneLocked drops entries that are neither banned nor inside their failure
// window; called when the map grows beyond maxTrackedIPs.
func (l *Lockout) pruneLocked(now time.Time) {
	if len(l.state) < maxTrackedIPs {
		return
	}
	for ip, st := range l.state {
		if now.After(st.bannedUntil) && now.Sub(st.windowStart) > l.window {
			delete(l.state, ip)
		}
	}
}

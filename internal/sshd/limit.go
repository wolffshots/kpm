package sshd

import (
	"sync"
	"time"
)

// The refusal rule of SSH.md section 6: after FailLimit failed logins from one
// IP address within FailWindow, refuse that address for BlockTime.
const (
	FailLimit  = 5
	FailWindow = time.Minute
	BlockTime  = 5 * time.Minute
)

// limiter counts failed logins per IP address. A failed login is one
// connection that ended without a login after at least one refused attempt, so
// a client that offers several agent keys before the right one is not counted.
type limiter struct {
	mu      sync.Mutex
	now     func() time.Time
	fails   map[string][]time.Time
	blocked map[string]time.Time // ip -> end of the refusal
}

func newLimiter(now func() time.Time) *limiter {
	if now == nil {
		now = time.Now
	}
	return &limiter{now: now, fails: map[string][]time.Time{}, blocked: map[string]time.Time{}}
}

// isBlocked reports whether ip is refused now.
func (l *limiter) isBlocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	until, ok := l.blocked[ip]
	if !ok {
		return false
	}
	if l.now().Before(until) {
		return true
	}
	delete(l.blocked, ip)
	return false
}

// fail records a failed login from ip. It returns true when this failure starts
// a refusal.
func (l *limiter) fail(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)
	recent := l.fails[ip][:0]
	for _, t := range l.fails[ip] {
		if now.Sub(t) < FailWindow {
			recent = append(recent, t)
		}
	}
	recent = append(recent, now)
	if len(recent) >= FailLimit {
		delete(l.fails, ip)
		l.blocked[ip] = now.Add(BlockTime)
		return true
	}
	l.fails[ip] = recent
	return false
}

// sweep drops old entries of other addresses, so the maps stay small when many
// addresses fail once.
func (l *limiter) sweep(now time.Time) {
	if len(l.fails)+len(l.blocked) < 256 {
		return
	}
	for ip, ts := range l.fails {
		if len(ts) == 0 || now.Sub(ts[len(ts)-1]) >= FailWindow {
			delete(l.fails, ip)
		}
	}
	for ip, until := range l.blocked {
		if !now.Before(until) {
			delete(l.blocked, ip)
		}
	}
}

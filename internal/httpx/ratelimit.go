package httpx

import (
	"sync"
	"time"
)

// CustomerTokenHeader carries the anonymous customer identity. It is not a
// credential for anything except that customer's own entry.
const CustomerTokenHeader = "X-Customer-Token"

// Limiter is a plain in-memory token bucket, one bucket per key. That is
// deliberately enough for a single-instance deployment; it is abuse
// protection, not fraud prevention.
type Limiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	rate     float64
	burst    float64
	lastSwep time.Time
}

type bucket struct {
	tokens float64
	seen   time.Time
}

// NewLimiter allows burst requests immediately, refilling at ratePerMinute.
func NewLimiter(ratePerMinute, burst int) *Limiter {
	return &Limiter{
		buckets:  make(map[string]*bucket),
		rate:     float64(ratePerMinute) / 60,
		burst:    float64(burst),
		lastSwep: time.Now(),
	}
}

func (l *Limiter) Allow(key string) bool {
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	l.sweep(now)

	b, ok := l.buckets[key]
	if !ok {
		l.buckets[key] = &bucket{tokens: l.burst - 1, seen: now}
		return true
	}

	b.tokens += now.Sub(b.seen).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.seen = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Lockout is the second half of protecting a guessable endpoint. A token
// bucket paces an attacker; it never stops one, because it refills. After
// enough wrong answers from the same source, this shuts the door for a while.
//
// It counts failures only. A caller that succeeds clears its own record, so a
// person mistyping a code four times and getting it right on the fifth walks
// away with nothing held against them.
type Lockout struct {
	mu        sync.Mutex
	records   map[string]*failures
	threshold int
	window    time.Duration
	penalty   time.Duration
	lastSwept time.Time
}

type failures struct {
	count      int
	first      time.Time
	lockedThru time.Time
}

// NewLockout blocks a key for penalty once it accumulates threshold failures
// inside window.
func NewLockout(threshold int, window, penalty time.Duration) *Lockout {
	return &Lockout{
		records:   make(map[string]*failures),
		threshold: threshold,
		window:    window,
		penalty:   penalty,
		lastSwept: time.Now(),
	}
}

// Locked reports whether this key is currently shut out.
func (l *Lockout) Locked(key string) bool {
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	l.sweep(now)

	record, ok := l.records[key]
	return ok && now.Before(record.lockedThru)
}

// Fail records one wrong answer, locking the key once the count reaches the
// threshold within the window.
func (l *Lockout) Fail(key string) {
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	record, ok := l.records[key]
	if !ok || now.Sub(record.first) > l.window {
		l.records[key] = &failures{count: 1, first: now}
		return
	}

	record.count++
	if record.count >= l.threshold {
		record.lockedThru = now.Add(l.penalty)
		// The window restarts with the lockout, so a key that comes back and
		// fails again is locked out again rather than immediately.
		record.count = 0
		record.first = now.Add(l.penalty)
	}
}

// Reset clears a key's record after a success.
func (l *Lockout) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.records, key)
}

// sweep drops records that are neither locked nor recently active. Caller holds
// the lock.
func (l *Lockout) sweep(now time.Time) {
	if now.Sub(l.lastSwept) < time.Minute {
		return
	}
	l.lastSwept = now

	for key, record := range l.records {
		if now.After(record.lockedThru) && now.Sub(record.first) > l.window {
			delete(l.records, key)
		}
	}
}

// sweep drops buckets that have been idle long enough to have fully refilled,
// which keeps the map from growing without bound. Caller holds the lock.
func (l *Limiter) sweep(now time.Time) {
	if now.Sub(l.lastSwep) < time.Minute {
		return
	}
	l.lastSwep = now

	idleLimit := 10 * time.Minute
	for key, b := range l.buckets {
		if now.Sub(b.seen) > idleLimit {
			delete(l.buckets, key)
		}
	}
}

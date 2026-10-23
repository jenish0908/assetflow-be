package httpapi

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

const (
	limiterIdleTTL    = 3 * time.Minute
	limiterSweepEvery = time.Minute
)

// rateLimiter keeps one token bucket per client key. Idle buckets are swept
// lazily during allow, so the map cannot grow without bound and no
// background goroutine is needed.
type rateLimiter struct {
	rps   rate.Limit
	burst int
	now   func() time.Time

	mu        sync.Mutex
	clients   map[string]*clientBucket
	lastSweep time.Time
}

type clientBucket struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func newRateLimiter(rps float64, burst int, now func() time.Time) *rateLimiter {
	return &rateLimiter{
		rps:       rate.Limit(rps),
		burst:     burst,
		now:       now,
		clients:   make(map[string]*clientBucket),
		lastSweep: now(),
	}
}

func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if now.Sub(l.lastSweep) > limiterSweepEvery {
		for k, b := range l.clients {
			if now.Sub(b.lastSeen) > limiterIdleTTL {
				delete(l.clients, k)
			}
		}
		l.lastSweep = now
	}

	b, ok := l.clients[key]
	if !ok {
		b = &clientBucket{limiter: rate.NewLimiter(l.rps, l.burst)}
		l.clients[key] = b
	}
	b.lastSeen = now
	return b.limiter.AllowN(now, 1)
}

// rateLimit rejects clients (keyed by IP) that exceed their bucket with 429.
func rateLimit(l *rateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !l.allow(c.ClientIP()) {
			c.Header("Retry-After", "1")
			abortWithError(c, http.StatusTooManyRequests, "rate_limited", "too many requests")
			return
		}
		c.Next()
	}
}

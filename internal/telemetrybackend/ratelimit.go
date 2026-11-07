package telemetrybackend

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

const (
	// bucketIdleTTL is how long an unused bucket is kept. A deleted bucket
	// comes back full, so keep this above burst / perMinute minutes, or an idle
	// period becomes a free refill.
	bucketIdleTTL = 10 * time.Minute
	// sweepInterval bounds how often Allow scans the whole registry for idle
	// buckets, so the scan isn't paid on every request.
	sweepInterval = time.Minute
)

type rateBucket struct {
	tokens   float64
	lastSeen time.Time
}

type ipRateLimiter struct {
	mu         sync.Mutex
	buckets    map[string]rateBucket
	perSecond  float64
	burst      float64
	maxBuckets int
	lastSweep  time.Time
	now        func() time.Time
}

func newIPRateLimiter(perMinute, burst int) *ipRateLimiter {
	return &ipRateLimiter{
		buckets:    make(map[string]rateBucket),
		perSecond:  float64(perMinute) / 60,
		burst:      float64(burst),
		maxBuckets: 10_000,
		now:        time.Now,
	}
}

func (l *ipRateLimiter) Allow(key string) bool {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepIdle(now)

	bucket, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= l.maxBuckets {
			l.evictOne()
		}
		bucket = rateBucket{tokens: l.burst, lastSeen: now}
	} else {
		elapsed := now.Sub(bucket.lastSeen).Seconds()
		bucket.tokens = min(l.burst, bucket.tokens+elapsed*l.perSecond)
		bucket.lastSeen = now
	}
	if bucket.tokens < 1 {
		l.buckets[key] = bucket
		return false
	}
	bucket.tokens--
	l.buckets[key] = bucket
	return true
}

func (l *ipRateLimiter) sweepIdle(now time.Time) {
	if now.Sub(l.lastSweep) < sweepInterval {
		return
	}
	l.lastSweep = now
	for key, bucket := range l.buckets {
		if now.Sub(bucket.lastSeen) > bucketIdleTTL {
			delete(l.buckets, key)
		}
	}
}

// evictOne makes room for a new key when the registry is full. Denying new
// keys instead would let anyone who fills the registry lock out every new
// client; an evicted client only starts again with a full bucket.
func (l *ipRateLimiter) evictOne() {
	for key := range l.buckets {
		delete(l.buckets, key)
		return
	}
}

func clientIP(request *http.Request, trusted []netip.Prefix) string {
	remote := parseRemoteIP(request.RemoteAddr)
	if isTrustedProxy(remote, trusted) {
		if addr, ok := forwardedClientIP(request.Header.Values("X-Forwarded-For"), trusted); ok {
			return addr.String()
		}
	}
	if remote.IsValid() {
		return remote.Unmap().String()
	}
	return "unknown"
}

// forwardedClientIP walks X-Forwarded-For from the right, where trusted
// proxies append the address they saw, and returns the first address that
// isn't a trusted proxy. Entries further left came from the client and can be
// forged, so an unparseable entry stops the walk.
func forwardedClientIP(values []string, trusted []netip.Prefix) (netip.Addr, bool) {
	var items []string
	for _, value := range values {
		items = append(items, strings.Split(value, ",")...)
	}
	for i := len(items) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(items[i]))
		if err != nil {
			return netip.Addr{}, false
		}
		addr = addr.Unmap()
		if !isTrustedProxy(addr, trusted) {
			return addr, true
		}
	}
	return netip.Addr{}, false
}

// rateLimitKey groups IPv6 clients by /64, the block a single host usually
// controls, so rotating addresses inside it neither dodges the limit nor fills
// the registry. IPv4-mapped addresses are keyed as IPv4; other values are
// used as they are.
func rateLimitKey(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	addr = addr.Unmap()
	if !addr.Is6() {
		return addr.String()
	}
	prefix, err := addr.Prefix(64)
	if err != nil {
		return ip
	}
	return prefix.String()
}

func parseRemoteIP(remoteAddr string) netip.Addr {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	addr, _ := netip.ParseAddr(strings.Trim(host, "[]"))
	return addr
}

func isTrustedProxy(addr netip.Addr, trusted []netip.Prefix) bool {
	if !addr.IsValid() {
		return false
	}
	for _, prefix := range trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

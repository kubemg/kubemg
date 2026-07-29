/*
 * Package cache is a small in-memory TTL cache for reads that are expensive to
 * repeat and harmless to be a couple of seconds old.
 *
 * Every live read in KubeMG is a round trip down an agent tunnel to a cluster's
 * API server, and the console asks for the same list several times over: a
 * sidebar click back and forth, a drawer opening over the list it came from, a
 * chart re-rendering on a resize. None of those are new questions, but each one
 * currently costs a tunnel call, an impersonated API request and an audit
 * record.
 *
 * Two rules make caching an answer safe here. **An entry belongs to the
 * identity that asked for it**: the key is built from the caller as well as the
 * question, because two people with different grants asking the same question
 * are entitled to different answers, and a shared entry would hand one of them
 * the other's. And **an entry is a few seconds old at most** — the TTL is short
 * enough that the console still reads as live and short enough that a grant
 * revoked a moment ago cannot be exercised through a stale entry for longer
 * than one refresh takes.
 *
 * A scope is the third rule, and it is what makes writes safe: everything read
 * from one cluster is filed under that cluster, so a write to it drops the lot
 * rather than leaving a scaled deployment reporting its old replica count.
 */
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultTTL is how long an entry stays servable. Five seconds is chosen
	// against how fast the underlying facts move: a pod list changes on
	// deployment, metrics-server resamples every 15s, and nobody navigating a
	// console can tell a five-second-old list from a live one.
	DefaultTTL = 5 * time.Second

	// DefaultMaxEntries bounds what the cache can hold. Keys carry the caller,
	// the cluster and the query, so a large fleet with many operators has a lot
	// of distinct questions; this is the ceiling that keeps a cache from
	// becoming a leak.
	DefaultMaxEntries = 4096
)

type entry[V any] struct {
	// scope groups entries that are invalidated together — in practice one
	// cluster's reads.
	scope   string
	value   V
	expires time.Time
}

// Cache is a thread-safe TTL cache. It is safe for concurrent use and holds no
// goroutine of its own: expired entries are dropped when they are read, and
// swept when the cache fills.
type Cache[V any] struct {
	mu      sync.Mutex
	ttl     time.Duration
	max     int
	entries map[string]entry[V]
}

// New builds a cache with the given entry lifetime. A TTL of zero or less takes
// DefaultTTL, so a miswired caller gets the documented behaviour rather than a
// cache that never hits.
func New[V any](ttl time.Duration) *Cache[V] {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Cache[V]{ttl: ttl, max: DefaultMaxEntries, entries: map[string]entry[V]{}}
}

// TTL is the configured entry lifetime.
func (c *Cache[V]) TTL() time.Duration { return c.ttl }

// Get returns a live entry. An expired one is dropped on the way past rather
// than left to the next sweep, so a key that is read and never written again
// does not hold its value.
func (c *Cache[V]) Get(key string) (V, bool) {
	var zero V

	c.mu.Lock()
	defer c.mu.Unlock()

	found, ok := c.entries[key]
	if !ok {
		return zero, false
	}
	if !time.Now().Before(found.expires) {
		delete(c.entries, key)
		return zero, false
	}
	return found.value, true
}

// Put files a value under a key and a scope. The scope is what a write
// invalidates; an empty one is legal and simply belongs to no group.
func (c *Cache[V]) Put(scope, key string, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.entries) >= c.max {
		c.evictLocked()
	}
	c.entries[key] = entry[V]{scope: scope, value: value, expires: time.Now().Add(c.ttl)}
}

// InvalidateScope drops every entry in a scope. This is the call a write makes:
// a scale, a restart or a manifest update changes what every read of that
// cluster would answer, and serving the previous answer for another five
// seconds would make the console look like the write did not land.
func (c *Cache[V]) InvalidateScope(scope string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for key, found := range c.entries {
		if found.scope == scope {
			delete(c.entries, key)
		}
	}
}

// Len is how many entries are held, expired ones included. Tests use it; so
// does anyone wondering whether the bound is being hit.
func (c *Cache[V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// evictLocked makes room. Expired entries go first, since they are worth
// nothing; if the cache is genuinely full of live entries, the soonest to
// expire are dropped, because they have the least life left to serve.
func (c *Cache[V]) evictLocked() {
	now := time.Now()
	for key, found := range c.entries {
		if !now.Before(found.expires) {
			delete(c.entries, key)
		}
	}
	if len(c.entries) < c.max {
		return
	}

	type aged struct {
		key     string
		expires time.Time
	}
	order := make([]aged, 0, len(c.entries))
	for key, found := range c.entries {
		order = append(order, aged{key: key, expires: found.expires})
	}
	sort.Slice(order, func(i, j int) bool { return order[i].expires.Before(order[j].expires) })

	// A quarter, so this runs occasionally rather than on every write once the
	// ceiling is reached.
	drop := len(order) / 4
	if drop == 0 {
		drop = 1
	}
	for _, victim := range order[:drop] {
		delete(c.entries, victim.key)
	}
}

/*
 * Keys.
 *
 * A key is a hash rather than a joined string for one reason that matters: the
 * parts include a request path and a query, both of which can contain the
 * separator, and two different questions that render to the same key would
 * serve one caller the other's answer. Hashing removes the question of
 * escaping, and the parts are length-prefixed so no rearrangement of the
 * boundaries can collide either.
 */

// Key builds a cache key from its parts. Order is significant; pass the same
// parts in the same order every time.
func Key(parts ...string) string {
	digest := sha256.New()
	for _, part := range parts {
		// The length prefix is what keeps ("a", "bc") apart from ("ab", "c").
		digest.Write([]byte(strconv.Itoa(len(part))))
		digest.Write([]byte{0})
		digest.Write([]byte(part))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// SortedQuery renders query parameters into one stable string, so the same
// question asked with its parameters in a different order is the same key.
func SortedQuery(values map[string][]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	var out strings.Builder
	for _, key := range keys {
		entries := slices.Clone(values[key])
		slices.Sort(entries)
		for _, value := range entries {
			out.WriteString(key)
			out.WriteByte('=')
			out.WriteString(value)
			out.WriteByte('\n')
		}
	}
	return out.String()
}

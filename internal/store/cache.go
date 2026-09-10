package store

import (
	"context"
	"errors"
	"sync"
	"time"
)

// keyLookuper is what Cache wraps — normally *Store, swapped for a fake in
// tests so the cache can be tested without Postgres.
type keyLookuper interface {
	Lookup(ctx context.Context, rawKey string) (*APIKey, error)
}

type cacheEntry struct {
	key       *APIKey // nil means a negative entry: this hash was not found
	expiresAt time.Time
}

// Cache is an in-memory, TTL'd cache in front of a keyLookuper (normally
// Postgres). Querying Postgres on every proxied request would defeat the
// point of a gateway. Both hits and misses are cached — negative caching
// means a client hammering the gateway with junk keys can't turn every
// request into a Postgres query.
//
// Trade-off, not a bug: a key revoked in Postgres keeps authenticating
// through the gateway for up to ttl after it was last looked up, because
// the cache has no way to learn about the change out of band. Avoiding
// that would mean never caching positive results at all, which defeats
// the cache's purpose. See cache_test.go's revocation test.
type Cache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
	ttl     time.Duration
	source  keyLookuper
	now     func() time.Time // overridable in tests
}

func NewCache(source keyLookuper, ttl time.Duration) *Cache {
	return &Cache{
		entries: make(map[string]cacheEntry),
		ttl:     ttl,
		source:  source,
		now:     time.Now,
	}
}

func (c *Cache) Lookup(ctx context.Context, rawKey string) (*APIKey, error) {
	hash := hashKey(rawKey)
	now := c.now()

	c.mu.RLock()
	entry, ok := c.entries[hash]
	c.mu.RUnlock()

	if ok && now.Before(entry.expiresAt) {
		if entry.key == nil {
			return nil, ErrKeyNotFound
		}
		return entry.key, nil
	}

	key, err := c.source.Lookup(ctx, rawKey)
	if err != nil {
		if errors.Is(err, ErrKeyNotFound) {
			c.mu.Lock()
			c.entries[hash] = cacheEntry{key: nil, expiresAt: now.Add(c.ttl)}
			c.mu.Unlock()
			return nil, ErrKeyNotFound
		}
		// Don't cache anything else (a Postgres blip, a timeout): caching
		// a transient failure for ttl would turn a brief outage into a
		// much longer one from the caller's point of view.
		return nil, err
	}

	c.mu.Lock()
	c.entries[hash] = cacheEntry{key: key, expiresAt: now.Add(c.ttl)}
	c.mu.Unlock()

	return key, nil
}

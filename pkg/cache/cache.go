// Package cache provides two types of efficient concurrency-safe key-value
// stores for generic (but comparable) keys and values, with optional expiration.
//
// Think of it as a basic in-memory and in-process version of Redis, or a modern
// idiomatic and performant replacement for https://github.com/patrickmn/go-cache.
//
// [FastCache] is a [sync.Map] wrapper that prioritizes speed over memory efficiency, and
// [LeanCache] is a [sync.RWMutex]-protected map that prioritizes memory usage over speed.
// Both of them implement the same [Cache] interface, so they can be used interchangeably.
package cache

import (
	"sync"
	"time"
)

const (
	// DefaultExpirationInterval is the default expiration interval (a.k.a. Time-To-Live, or TTL) for items in
	// a [Cache]. A zero (or negative) duration means "no expiration". You can override it using [WithExpiration]
	// when creating a new [Cache], or only for specific items - in [Cache.Set], [Cache.Add], and [Cache.Replace].
	DefaultExpirationInterval time.Duration = 0

	// DefaultCleanupInterval is a prime number of minutes and seconds, slightly over an hour.
	// This interval minimizes synchronization with other periodic tasks, and balances memory
	// and CPU overhead. You can override it using [WithCleanup] when creating a new [Cache].
	// A zero (or negative) duration disables the periodic cleanup worker's goroutine,
	// and relies solely on lazy expiration (if expiration is enabled at all).
	DefaultCleanupInterval time.Duration = 3671 * time.Second
)

type base struct {
	stop chan struct{}

	// Once is a separate allocation, not a plain [sync.Once] value, so that passing it
	// (not &base.once) to [runtime.AddCleanup] never gives the cleanup argument a pointer
	// into the cache's own memory. See [cleanupWorker] below for additional details.
	once *sync.Once

	expiration time.Duration
	cleanup    time.Duration
}

// Cache is a generic concurrency-safe key-value store with optional expiration. It can be initialized
// with [NewFastCache] to prioritize speed, or with [NewLeanCache] to prioritize memory efficiency.
type Cache[K comparable, V comparable] interface {
	// Set adds a value to the cache with an optional Time-To-Live interval
	// (regardless of whether or not the cache has a default expiration policy).
	// Note that any non-positive expiration value is treated as "never expires".
	Set(key K, value V, expiration ...ItemOption)

	// Add adds a value to the cache, like [Cache.Set], but only if the key does
	// not already exist or is expired. It returns true if the item was added,
	// false otherwise. Either way, it also returns the currently-stored value.
	// Note that any non-positive expiration value is treated as "never expires".
	Add(key K, value V, expiration ...ItemOption) (V, bool)

	// Replace updates a value in the cache, like [Cache.Set], but only if the key already
	// exists and is unexpired. It returns true if the item was replaced, false otherwise.
	// Note that any non-positive expiration value is treated as "never expires".
	Replace(key K, value V, expiration ...ItemOption) bool

	// Get retrieves a value from the cache, and also returns a
	// boolean indicating whether it was found and is unexpired.
	Get(key K) (V, bool)

	// Delete removes a specified item from the cache. If the item does not exist, this is a no-op.
	Delete(key K)

	// Clear removes all items from the cache.
	Clear()

	// Item retrieves a copy of an [Item] from the cache, and also returns
	// a boolean indicating whether it was found and is unexpired.
	Item(key K) (Item[V], bool)

	// Keys returns a slice of all the unexpired keys in the cache. The order is not stable.
	Keys() []K

	// Items returns a copy of all the unexpired [Item]s in the cache. The order is not stable.
	Items() map[K]Item[V]

	// ItemCount returns the number of unexpired items in the cache. This is different from [Cache.Len],
	// which returns the total number of items, including expired-but-not-yet-deleted ones.
	ItemCount() int

	// Len returns the total number of items in the cache. Unlike [Cache.ItemCount],
	// this includes expired-but-not-yet-deleted ones too.
	Len() int

	// StopCleanupWorker stops the cache's periodic cleanup goroutine, if the cache was
	// initialized with a non-zero cleanup interval. It is safe to call this multiple times.
	StopCleanupWorker()
}

// Item is a container for a single cache item, with its value and expiration time.
// An Expiration of zero means the item never expires.
type Item[V comparable] struct {
	Value      V
	Expiration time.Time
}

// Expired checks if the item is already expired.
func (i Item[V]) Expired() bool {
	if i.Expiration.IsZero() {
		return false
	}
	return time.Now().After(i.Expiration)
}

type (
	// Option configures optional [time.Duration]-based settings in a [Cache] instance, for all items.
	Option func(*base)

	// ItemOption configures an optional [time.Duration]-based setting for specific items in a [Cache] instance.
	ItemOption func(*base)
)

// WithCleanup sets the cleanup interval for a new [Cache], to periodically remove expired items.
// See also [DefaultCleanupInterval]. You can stop this goroutine by calling [Cache.StopCleanupWorker].
func WithCleanup(interval time.Duration) Option {
	return func(c *base) {
		c.cleanup = interval
	}
}

// WithExpiration sets a default expiration interval (a.k.a. Time-To-Live, or TTL) for
// all items in a new [Cache]. You can override this default (whether you set it explicitly
// or implicitly) in specific items, using [WithCustomExpiration] - in [Cache.Set],
// [Cache.Add], and [Cache.Replace]. See also [DefaultExpirationInterval].
func WithExpiration(ttl time.Duration) Option {
	return func(c *base) {
		c.expiration = ttl
	}
}

// WithCustomExpiration overrides the default expiration interval (a.k.a. Time-To-Live, or TTL) with a custom one,
// in specific items - in [Cache.Set], [Cache.Add], and [Cache.Replace]. See also [DefaultExpirationInterval].
func WithCustomExpiration(ttl time.Duration) ItemOption {
	return func(c *base) {
		c.expiration = ttl
	}
}

// CleanupWorker is a subset of [base], but it's intentionally separate from it. We need to pass
// to [runtime.AddCleanup] an argument that is not - and does not contain - a pointer to the cache
// itself: "If `ptr` (the pointer to the cache) is reachable from `cleanup` ([stopGoroutine]) or
// `arg` ([cleanupWorker]), `ptr` will never be garbage-collected and the cleanup will never run".
type cleanupWorker struct {
	stop chan struct{}
	once *sync.Once
}

func stopGoroutine(cw cleanupWorker) {
	cw.once.Do(func() {
		close(cw.stop)
	})
}

func (b *base) durationOptionToTime(expiration ...ItemOption) time.Time {
	if len(expiration) == 0 {
		if b.expiration > 0 {
			return time.Now().Add(b.expiration)
		}
		return time.Time{}
	}

	cache := &base{expiration: b.expiration}
	for _, opt := range expiration {
		opt(cache)
	}
	if cache.expiration > 0 {
		return time.Now().Add(cache.expiration)
	}
	return time.Time{}
}

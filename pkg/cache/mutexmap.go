package cache

import (
	"runtime"
	"sync"
	"time"
	"weak"
)

// LeanCache is a [sync.RWMutex]-protected map that provides an efficient concurrency-safe
// key-value store for generic (but comparable) keys and values, with optional expiration.
//
// LeanCache is optimal for memory-sensitive environments, single-threaded/low-concurrency tasks,
// and workloads requiring fast, low-allocation, large-scale iterations over the entire cache.
// In other words, it prioritizes memory efficiency over speed, but implements the
// same [Cache] interface as [FastCache], so they can be used interchangeably.
type LeanCache[K comparable, V comparable] struct {
	base

	mu   sync.RWMutex
	data map[K]Item[V]
}

// NewLeanCache creates a [sync.RWMutex]-protected map that provides an efficient concurrency-safe
// key-value store for generic (but comparable) keys and values, with optional expiration.
func NewLeanCache[K comparable, V comparable](opts ...Option) Cache[K, V] {
	c := &LeanCache[K, V]{
		stop:    make(chan struct{}),
		once:    new(sync.Once),
		cleanup: DefaultCleanupInterval,
		data:    make(map[K]Item[V]),
	}
	for _, opt := range opts {
		opt(&c.base)
	}
	if c.cleanup > 0 {
		runtime.AddCleanup(c, stopGoroutine, cleanupWorker{stop: c.stop, once: c.once})
		go leanCleanup(weak.Make(c), c.stop, c.cleanup)
	}
	return c
}

const (
	cleanupBatchSize = 256
)

// leanCleanup is an optional goroutine that periodically removes expired items.
// You may stop it by calling [Cache.StopCleanupWorker], but you don't have to:
//  1. Lazy expiration of stale items is also implemented in cache methods.
//  2. Go will run [stopGoroutine] (sometime) after the cache is garbage-collected.
//  3. Even if (2) doesn't happen, this goroutine uses a weak pointer to the cache,
//     so it will exit on its own once the cache becomes unreachable,
//     to prevent a leak that would keep both of them alive.
func leanCleanup[K comparable, V comparable](ref weak.Pointer[LeanCache[K, V]], stop <-chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case now := <-ticker.C:
			strongRef := ref.Value()

			// The cache has been garbage collected.
			if strongRef == nil {
				return
			}

			// Scan for expired keys (under a read lock to minimize write lock contention).
			var expired []K
			strongRef.mu.RLock()
			for key, item := range strongRef.data {
				if !item.Expiration.IsZero() && now.After(item.Expiration) {
					expired = append(expired, key)
				}
			}
			strongRef.mu.RUnlock()

			if len(expired) == 0 {
				continue
			}

			// Delete expired items in batches under a write lock, while still respecting the stop signal.
			for i := 0; i < len(expired); i += cleanupBatchSize {
				select {
				case <-stop:
					return
				default:
					// Continue to delete expired items.
				}

				end := min(i+cleanupBatchSize, len(expired))
				batch := expired[i:end]

				strongRef.mu.Lock()
				for _, key := range batch {
					if item, found := strongRef.data[key]; found && !item.Expiration.IsZero() && now.After(item.Expiration) {
						delete(strongRef.data, key)
					}
				}
				strongRef.mu.Unlock()
			}

		// [Cache.StopCleanupWorker] or [stopWorkerAfterGC] have been called.
		case <-stop:
			return
		}
	}
}

// Set adds a value to the cache with an optional Time-To-Live interval
// (regardless of whether or not the cache has a default expiration policy).
// Note that any non-positive expiration value is treated as "never expires".
func (c *LeanCache[K, V]) Set(key K, value V, expiration ...ItemOption) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.data[key] = Item[V]{Value: value, Expiration: c.durationOptionToTime(expiration...)}
}

// Add adds a value to the cache, like [Cache.Set], but only if the key does
// not already exist or is expired. It returns true if the item was added,
// false otherwise. Either way, it also returns the currently-stored value.
// Note that any non-positive expiration value is treated as "never expires".
func (c *LeanCache[K, V]) Add(key K, value V, expiration ...ItemOption) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if item, found := c.data[key]; found && !item.Expired() {
		return item.Value, false
	}

	c.data[key] = Item[V]{Value: value, Expiration: c.durationOptionToTime(expiration...)}
	return value, true
}

// Replace updates a value in the cache, like [Cache.Set], but only if the key already
// exists and is unexpired. It returns true if the item was replaced, false otherwise.
// Note that any non-positive expiration value is treated as "never expires".
func (c *LeanCache[K, V]) Replace(key K, value V, expiration ...ItemOption) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	item, found := c.data[key]
	if !found {
		return false
	}
	if item.Expired() {
		delete(c.data, key)
		return false
	}

	item.Value = value
	if len(expiration) > 0 {
		item.Expiration = c.durationOptionToTime(expiration...)
	}
	c.data[key] = item
	return true
}

// Get retrieves a value from the cache, and also returns a
// boolean indicating whether it was found and is unexpired.
func (c *LeanCache[K, V]) Get(key K) (V, bool) {
	item, ok := c.Item(key)
	return item.Value, ok
}

// Delete removes a specified item from the cache. If the item does not exist, this is a no-op.
func (c *LeanCache[K, V]) Delete(key K) {
	c.mu.Lock()
	delete(c.data, key)
	c.mu.Unlock()
}

// Clear removes all items from the cache.
func (c *LeanCache[K, V]) Clear() {
	c.mu.Lock()
	clear(c.data)
	c.mu.Unlock()
}

// Item retrieves a copy of an [Item] from the cache, and also returns
// a boolean indicating whether it was found and is unexpired.
func (c *LeanCache[K, V]) Item(key K) (Item[V], bool) {
	c.mu.RLock()
	item, found := c.data[key]
	c.mu.RUnlock()

	if !found {
		return Item[V]{}, false
	}
	if !item.Expired() {
		return item, true
	}

	// "Upgrade" to write lock to delete the expired item...
	c.mu.Lock()
	defer c.mu.Unlock()

	// ...But only if it's still expired (another goroutine might have updated it in the meantime).
	item, found = c.data[key]
	switch {
	case !found:
		return Item[V]{}, false
	case !item.Expired():
		return item, true
	default:
		delete(c.data, key)
		return Item[V]{}, false
	}
}

// Keys returns a slice of all the unexpired keys in the cache. The order is not stable.
func (c *LeanCache[K, V]) Keys() []K {
	c.mu.RLock()
	defer c.mu.RUnlock()

	now := time.Now()
	keys := make([]K, 0, len(c.data))
	for k, item := range c.data {
		if item.Expiration.IsZero() || now.Before(item.Expiration) {
			keys = append(keys, k)
		}
	}

	if len(keys) == 0 {
		keys = nil
	}
	return keys
}

// Items returns a copy of all the unexpired [Item]s in the cache. The order is not stable.
func (c *LeanCache[K, V]) Items() map[K]Item[V] {
	c.mu.RLock()
	defer c.mu.RUnlock()

	now := time.Now()
	m := make(map[K]Item[V], len(c.data))
	for k, item := range c.data {
		if item.Expiration.IsZero() || now.Before(item.Expiration) {
			m[k] = item
		}
	}
	return m
}

// ItemCount returns the number of unexpired items in the cache. This is different from [Cache.Len],
// which returns the total number of items, including expired-but-not-yet-deleted ones.
func (c *LeanCache[K, V]) ItemCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	count := 0
	now := time.Now()
	for _, item := range c.data {
		if item.Expiration.IsZero() || now.Before(item.Expiration) {
			count++
		}
	}
	return count
}

// Len returns the total number of items in the cache. Unlike [Cache.ItemCount],
// this includes expired-but-not-yet-deleted ones too.
func (c *LeanCache[K, V]) Len() int {
	c.mu.RLock()
	l := len(c.data)
	c.mu.RUnlock()
	return l
}

// StopCleanupWorker stops the cache's periodic cleanup goroutine, if the cache was
// initialized with a non-zero cleanup interval. It is safe to call this multiple times.
func (c *LeanCache[K, V]) StopCleanupWorker() {
	stopGoroutine(cleanupWorker{stop: c.stop, once: c.once})
}

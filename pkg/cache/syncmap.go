package cache

import (
	"runtime"
	"sync"
	"time"
	"weak"
)

// FastCache is a [sync.Map] wrapper that provides an efficient concurrency-safe key-value
// store for generic (but comparable) keys and values, with optional expiration.
//
// FastCache is optimal for high-concurrency multi-core production services that use either
// read-heavy or write-heavy workloads. It scales almost linearly across CPU cores.
// In other words, it prioritizes speed over memory efficiency, but implements the
// same [Cache] interface as [LeanCache], so they can be used interchangeably.
//
// Contrary to popular belief, [sync.Map] is actually much faster than a [sync.RWMutex]-protected
// map in almost all benchmarks, even the historically-slow scenarios of heavy "dirty" writing.
// This has been true since Go 1.24: see https://antonz.org/go-1-24/#concurrent-hash-trie-map
// and https://victoriametrics.com/blog/go-sync-map-hash-trie/.
type FastCache[K comparable, V comparable] struct {
	base

	m sync.Map
}

// NewFastCache creates a [sync.Map] wrapper that provides an efficient concurrency-safe
// key-value store for generic (but comparable) keys and values, with optional expiration.
//
// Note that while the underlying [sync.Map] guarantees memory safety, it cannot prevent race conditions
// between concurrent operations on the same key, because it doesn't use a global lock like [LeanCache].
func NewFastCache[K comparable, V comparable](opts ...Option) Cache[K, V] {
	c := &FastCache[K, V]{
		stop:    make(chan struct{}),
		once:    new(sync.Once),
		cleanup: DefaultCleanupInterval,
	}
	for _, opt := range opts {
		opt(&c.base)
	}
	if c.cleanup > 0 {
		runtime.AddCleanup(c, stopGoroutine, cleanupWorker{stop: c.stop, once: c.once})
		go fastCleanup(weak.Make(c), c.stop, c.cleanup)
	}
	return c
}

// fastCleanup is an optional goroutine that periodically removes expired items.
// You may stop it by calling [Cache.StopCleanupWorker], but you don't have to:
//  1. Lazy expiration of stale items is also implemented in cache methods.
//  2. Go will run [stopGoroutine] (sometime) after the cache is garbage-collected.
//  3. Even if (2) doesn't happen, this goroutine uses a weak pointer to the cache,
//     so it will exit on its own once the cache becomes unreachable,
//     to prevent a leak that would keep both of them alive.
func fastCleanup[K comparable, V comparable](ref weak.Pointer[FastCache[K, V]], stop <-chan struct{}, interval time.Duration) {
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

			// Iterate over all the items in the cache and delete the expired ones.
			strongRef.m.Range(func(key, value any) bool {
				if item, ok := value.(Item[V]); ok && !item.Expiration.IsZero() && now.After(item.Expiration) {
					strongRef.m.CompareAndDelete(key, value)
				}
				return true
			})

		// [Cache.StopCleanupWorker] or [stopWorkerAfterGC] have been called.
		case <-stop:
			return
		}
	}
}

// Set adds a value to the cache with an optional Time-To-Live interval
// (regardless of whether or not the cache has a default expiration policy).
// Note that any non-positive expiration value is treated as "never expires".
func (c *FastCache[K, V]) Set(key K, value V, expiration ...ItemOption) {
	c.m.Store(key, Item[V]{Value: value, Expiration: c.durationOptionToTime(expiration...)})
}

// Add adds a value to the cache, like [Cache.Set], but only if the key does
// not already exist or is expired. It returns true if the item was added,
// false otherwise. Either way, it also returns the currently-stored value.
// Note that any non-positive expiration value is treated as "never expires".
func (c *FastCache[K, V]) Add(key K, value V, expiration ...ItemOption) (V, bool) {
	newItem := Item[V]{Value: value, Expiration: c.durationOptionToTime(expiration...)}
	for {
		actual, loaded := c.m.LoadOrStore(key, newItem)
		if !loaded {
			return value, true
		}

		if item, ok := actual.(Item[V]); ok && !item.Expired() {
			return item.Value, false
		}

		if c.m.CompareAndSwap(key, actual, newItem) {
			return value, true
		}

		// If we reached this point, another goroutine has modified the value between our
		// [sync.Map.LoadOrStore] and [sync.Map.CompareAndSwap] calls. However, the for loop
		// is polite and logically guaranteed to be finite despite not having a stopping condition.
		runtime.Gosched()
	}
}

// Replace updates a value in the cache, like [Cache.Set], but only if the key already
// exists and is unexpired. It returns true if the item was replaced, false otherwise.
// Note that any non-positive expiration value is treated as "never expires".
func (c *FastCache[K, V]) Replace(key K, value V, expiration ...ItemOption) bool {
	newItem := Item[V]{Value: value}
	for {
		actual, found := c.m.Load(key)
		if !found {
			return false
		}

		item, ok := actual.(Item[V])
		if !ok || item.Expired() {
			c.m.CompareAndDelete(key, actual)
			return false
		}

		newItem.Expiration = item.Expiration
		if len(expiration) > 0 {
			newItem.Expiration = c.durationOptionToTime(expiration...)
		}
		if c.m.CompareAndSwap(key, actual, newItem) {
			return true
		}

		// If we reached this point, another goroutine has modified the value between our
		// [sync.Map.Load] and [sync.Map.CompareAndSwap] calls. However, the for loop is
		// polite and logically guaranteed to be finite despite not having a stopping condition.
		runtime.Gosched()
	}
}

// Get retrieves a value from the cache, and also returns a
// boolean indicating whether it was found and is unexpired.
func (c *FastCache[K, V]) Get(key K) (V, bool) {
	if item, ok := c.Item(key); ok {
		return item.Value, true
	}
	var zero V
	return zero, false
}

// Delete removes a specified item from the cache. If the item does not exist, this is a no-op.
func (c *FastCache[K, V]) Delete(key K) {
	c.m.Delete(key)
}

// Clear removes all items from the cache.
func (c *FastCache[K, V]) Clear() {
	c.m.Clear()
}

// Item retrieves a copy of an [Item] from the cache, and also returns
// a boolean indicating whether it was found and is unexpired.
func (c *FastCache[K, V]) Item(key K) (Item[V], bool) {
	if v, found := c.m.Load(key); found {
		if item, ok := v.(Item[V]); ok && !item.Expired() {
			return item, true
		}
		c.m.CompareAndDelete(key, v)
		return Item[V]{}, false
	}
	return Item[V]{}, false
}

// Keys returns a slice of all the unexpired keys in the cache. The order is not stable.
func (c *FastCache[K, V]) Keys() []K {
	var keys []K
	now := time.Now()
	c.m.Range(func(key, value any) bool {
		if k, ok := key.(K); ok {
			if item, ok := value.(Item[V]); ok && (item.Expiration.IsZero() || now.Before(item.Expiration)) {
				keys = append(keys, k)
			}
		}
		return true
	})
	return keys
}

// Items returns a copy of all the unexpired [Item]s in the cache. The order is not stable.
func (c *FastCache[K, V]) Items() map[K]Item[V] {
	now := time.Now()
	m := make(map[K]Item[V])
	c.m.Range(func(key, value any) bool {
		if k, ok := key.(K); ok {
			if item, ok := value.(Item[V]); ok && (item.Expiration.IsZero() || now.Before(item.Expiration)) {
				m[k] = item
			}
		}
		return true
	})
	return m
}

// ItemCount returns the number of unexpired items in the cache. This is different from [Cache.Len],
// which returns the total number of items, including expired-but-not-yet-deleted ones.
func (c *FastCache[K, V]) ItemCount() int {
	count := 0
	now := time.Now()
	c.m.Range(func(_, value any) bool {
		if item, ok := value.(Item[V]); ok && (item.Expiration.IsZero() || now.Before(item.Expiration)) {
			count++
		}
		return true
	})
	return count
}

// Len returns the total number of items in the cache. Unlike [Cache.ItemCount],
// this includes expired-but-not-yet-deleted ones too.
func (c *FastCache[K, V]) Len() int {
	l := 0
	c.m.Range(func(_, _ any) bool {
		l++
		return true
	})
	return l
}

// StopCleanupWorker stops the cache's periodic cleanup goroutine, if the cache was
// initialized with a non-zero cleanup interval. It is safe to call this multiple times.
func (c *FastCache[K, V]) StopCleanupWorker() {
	stopGoroutine(cleanupWorker{stop: c.stop, once: c.once})
}

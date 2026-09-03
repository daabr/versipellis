package cache_test

import (
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/daabr/versipellis/pkg/cache"
)

func TestCacheSingleItemMethods(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		cache  func(...cache.Option) cache.Cache[int, int]
		exist  bool
		expire time.Duration // Only used in Replace() for branch coverage.

		wantGetVal     int
		wantGetOK      bool
		wantAddVal     int
		wantAddOK      bool
		wantReplaceVal int
		wantReplaceOK  bool
	}{
		{
			name:   "syncmap_new_items_no_custom_expiration",
			cache:  cache.NewFastCache[int, int],
			exist:  false,
			expire: 0,

			wantGetVal:     0,
			wantGetOK:      false,
			wantAddVal:     22,
			wantAddOK:      true,
			wantReplaceVal: 0,
			wantReplaceOK:  false,
		},
		{
			name:   "mutexmap_new_items_no_custom_expiration",
			cache:  cache.NewLeanCache[int, int],
			exist:  false,
			expire: 0,

			wantGetVal:     0,
			wantGetOK:      false,
			wantAddVal:     22,
			wantAddOK:      true,
			wantReplaceVal: 0,
			wantReplaceOK:  false,
		},
		{
			name:   "syncmap_existing_items_no_custom_expiration",
			cache:  cache.NewFastCache[int, int],
			exist:  true,
			expire: 0,

			wantGetVal:     1,
			wantGetOK:      true,
			wantAddVal:     2,
			wantAddOK:      false,
			wantReplaceVal: 33,
			wantReplaceOK:  true,
		},
		{
			name:   "mutexmap_existing_items_no_custom_expiration",
			cache:  cache.NewLeanCache[int, int],
			exist:  true,
			expire: 0,

			wantGetVal:     1,
			wantGetOK:      true,
			wantAddVal:     2,
			wantAddOK:      false,
			wantReplaceVal: 33,
			wantReplaceOK:  true,
		},
		{
			name:   "syncmap_existing_items_with_custom_expiration",
			cache:  cache.NewFastCache[int, int],
			exist:  true,
			expire: time.Hour,

			wantGetVal:     1,
			wantGetOK:      true,
			wantAddVal:     2,
			wantAddOK:      false,
			wantReplaceVal: 33,
			wantReplaceOK:  true,
		},
		{
			name:   "mutexmap_existing_items_with_custom_expiration",
			cache:  cache.NewLeanCache[int, int],
			exist:  true,
			expire: time.Hour,

			wantGetVal:     1,
			wantGetOK:      true,
			wantAddVal:     2,
			wantAddOK:      false,
			wantReplaceVal: 33,
			wantReplaceOK:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := tt.cache(cache.WithCleanup(0))
			if tt.exist {
				c.Set(1, 1)
				c.Set(2, 2)
				c.Set(3, 3)
				c.Set(4, 4)
				c.Set(5, 5)
				c.Set(6, 6)
			}

			// Get.
			if got, ok := c.Get(1); ok != tt.wantGetOK || got != tt.wantGetVal {
				t.Errorf("Cache.Get(1) = (%v, %v), want (%v, %v)", got, ok, tt.wantGetVal, tt.wantGetOK)
			}
			// Add.
			if got, ok := c.Add(2, 22); ok != tt.wantAddOK || got != tt.wantAddVal {
				t.Errorf("Cache.Add() = (%v, %v), want (%v, %v)", got, ok, tt.wantAddVal, tt.wantAddOK)
			}
			if got, ok := c.Get(2); !ok || got != tt.wantAddVal {
				t.Errorf("Cache.Get(2) = (%v, %v), want (%v, %v)", got, ok, tt.wantAddVal, tt.wantAddOK)
			}
			// Replace.
			if ok := c.Replace(3, 33, cache.WithCustomExpiration(tt.expire)); ok != tt.wantReplaceOK {
				t.Errorf("Cache.Replace() = %v, want %v", ok, tt.wantReplaceOK)
			}
			if got, ok := c.Get(3); ok != tt.wantReplaceOK || got != tt.wantReplaceVal {
				t.Errorf("Cache.Get(3) = (%v, %v), want (%v, %v)", got, ok, tt.wantReplaceVal, tt.wantReplaceOK)
			}

			if !tt.exist {
				return // Skip the rest of the tests for initially-non-existent item scenarios.
			}

			// Delete.
			c.Delete(4)
			if got, ok := c.Get(4); ok || got != 0 {
				t.Errorf("Cache.Get(4) = (%v, %v), want (0, false)", got, ok)
			}

			c.Set(4, 4, cache.WithCustomExpiration(time.Nanosecond)) // "Auto-expired" no matter how fast the test runs.

			// Clear.
			if count := c.Len(); count != 6 {
				t.Errorf("Cache.Len(1) = %v, want 6", count)
			}
			if count := c.ItemCount(); count != 5 {
				t.Errorf("Cache.ItemCount(1) = %v, want 5", count)
			}

			c.Clear()

			if got, ok := c.Get(5); ok || got != 0 {
				t.Errorf("Cache.Get(5) = (%v, %v), want (0, false)", got, ok)
			}
			if got, ok := c.Get(6); ok || got != 0 {
				t.Errorf("Cache.Get(6) = (%v, %v), want (0, false)", got, ok)
			}
			if count := c.Len(); count != 0 {
				t.Errorf("Cache.Len(2) = %v, want 0", count)
			}
			if count := c.ItemCount(); count != 0 {
				t.Errorf("Cache.ItemCount(2) = %v, want 0", count)
			}
		})
	}
}

func TestCacheItemExpiration(t *testing.T) {
	tests := []struct {
		name    string
		cache   func(...cache.Option) cache.Cache[string, string]
		cleanup time.Duration
	}{
		{
			name:    "syncmap_lazy_expiration",
			cache:   cache.NewFastCache[string, string],
			cleanup: 0,
		},
		{
			name:    "mutexmap_lazy_expiration",
			cache:   cache.NewLeanCache[string, string],
			cleanup: 0,
		},
		{
			name:    "syncmap_with_cleanup",
			cache:   cache.NewFastCache[string, string],
			cleanup: time.Minute,
		},
		{
			name:    "mutexmap_with_cleanup",
			cache:   cache.NewLeanCache[string, string],
			cleanup: time.Minute,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := tt.cache(cache.WithExpiration(time.Second), cache.WithCleanup(tt.cleanup))
				t.Cleanup(c.StopCleanupWorker)
				c.Set("key", "val")

				synctest.Sleep(time.Minute + time.Nanosecond)
				want := cache.Item[string]{Value: ""}

				if got, ok := c.Item("key"); ok || got != want {
					t.Errorf("Cache.Item(key) = (%v, %+v), want (%+v, %v)", got, ok, want, false)
				}
			})
		})
	}
}

func TestCacheAddAndReplaceOverExpiredItem(t *testing.T) {
	tests := []struct {
		name  string
		cache func(...cache.Option) cache.Cache[int, int]
	}{
		{
			name:  "syncmap",
			cache: cache.NewFastCache[int, int],
		},
		{
			name:  "mutexmap",
			cache: cache.NewLeanCache[int, int],
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := tt.cache(cache.WithExpiration(time.Second), cache.WithCleanup(0))

				// Test case 1: Add an item, wait for it to expire, then add it
				// again with a new value and check if the new value is returned.
				c.Set(1, 2)
				synctest.Sleep(time.Second + time.Nanosecond)
				got, ok := c.Add(1, 3, cache.WithCustomExpiration(time.Minute))
				if !ok || got != 3 {
					t.Errorf("Cache.Add(1) = (%v, %v), want (3, true)", got, ok)
				}
				if got, ok := c.Get(1); !ok || got != 3 {
					t.Errorf("Cache.Get(1) = (%v, %v), want (3, true)", got, ok)
				}

				// Test case 2: Add an item, wait for it to expire, then replace it
				// with a new value and check if the new value is returned.
				c.Set(2, 3)
				synctest.Sleep(time.Second + time.Nanosecond)
				if c.Replace(2, 4, cache.WithCustomExpiration(time.Minute)) {
					t.Errorf("Cache.Replace(2) = true, want false")
				}
				if got, ok := c.Get(2); ok || got != 0 {
					t.Errorf("Cache.Get(2) = (%v, %v), want (0, false)", got, ok)
				}
			})
		})
	}
}

func TestCacheIteration(t *testing.T) {
	tests := []struct {
		name  string
		cache func(...cache.Option) cache.Cache[int, int]
		size  int
		keys  bool
	}{
		{
			name:  "syncmap_empty_keys",
			cache: cache.NewFastCache[int, int],
			size:  0,
			keys:  true,
		},
		{
			name:  "mutexmap_empty_keys",
			cache: cache.NewLeanCache[int, int],
			size:  0,
			keys:  true,
		},
		{
			name:  "syncmap_non_empty_keys",
			cache: cache.NewFastCache[int, int],
			size:  4,
			keys:  true,
		},
		{
			name:  "mutexmap_non_empty_keys",
			cache: cache.NewLeanCache[int, int],
			size:  4,
			keys:  true,
		},
		{
			name:  "syncmap_non_empty_items",
			cache: cache.NewFastCache[int, int],
			size:  4,
			keys:  false,
		},
		{
			name:  "mutexmap_non_empty_items",
			cache: cache.NewLeanCache[int, int],
			size:  4,
			keys:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := tt.cache(cache.WithExpiration(time.Second), cache.WithCleanup(0))
				for i := range tt.size {
					c.Set(i, i, cache.WithCustomExpiration(time.Duration(i)*time.Second))
				}

				// Wait until half of the items should be expired, if there are any.
				synctest.Sleep(time.Duration(tt.size/2)*time.Second + time.Nanosecond)

				got, want := 0, tt.size/2
				fname := ""
				if tt.keys {
					got = len(c.Keys())
					fname = "Cache.Keys()"
				} else {
					got = len(c.Items())
					fname = "Cache.Items()"
				}

				if got != want {
					t.Errorf("%s len = %v, want %v", fname, got, want)
				}
			})
		})
	}
}

func TestCacheItemCopyNotReferences(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		cache func(...cache.Option) cache.Cache[string, string]
	}{
		{
			name:  "syncmap",
			cache: cache.NewFastCache[string, string],
		},
		{
			name:  "mutexmap",
			cache: cache.NewLeanCache[string, string],
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := tt.cache(cache.WithCleanup(0))

			k, v := "key1", "val1"
			if _, ok := c.Add(k, v); !ok {
				t.Fatalf("Cache.Add() failed for key %q with value %q", k, v)
			}

			item1, found := c.Item(k)
			if !found {
				t.Fatalf("Cache.Item() did not find key %q", k)
			}
			items1 := c.Items()
			if len(items1) != 1 {
				t.Fatalf("Cache.Items() did not find key %q", k)
			}

			// Intentional modifications to test that the returned [cache.Item]
			// and map are copies, not references to the internal item.
			item1.Value = "val2"
			item1.Expiration = time.Now()
			items1[k] = item1

			item2, found := c.Item(k)
			if !found {
				t.Fatalf("Cache.Item() did not find key %q", k)
			}
			if item2.Value != v {
				t.Errorf("Item was modified through copy: got %q, want %q", item2.Value, v)
			}
			if !item2.Expiration.IsZero() {
				t.Errorf("Item expiration was modified through copy: got %v, want zero value", item2.Expiration)
			}

			wantItems := map[string]cache.Item[string]{k: item2}
			if items2 := c.Items(); !reflect.DeepEqual(items2, wantItems) {
				t.Errorf("Cache.Items() did not match expected items: got %#v, want %#v", items2, wantItems)
			}
		})
	}
}

func TestCacheReplaceKeepsExactTTL(t *testing.T) {
	tests := []struct {
		name  string
		cache func(...cache.Option) cache.Cache[string, string]
	}{
		{
			name:  "syncmap",
			cache: cache.NewFastCache[string, string],
		},
		{
			name:  "mutexmap",
			cache: cache.NewLeanCache[string, string],
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := tt.cache(cache.WithExpiration(time.Hour), cache.WithCleanup(0))

				k, v1, v2 := "key1", "val1", "val2"
				twoHours := 2 * time.Hour

				if _, ok := c.Add(k, v1, cache.WithCustomExpiration(twoHours)); !ok {
					t.Fatalf("Cache.Add() failed for key %q with value %q", k, v1)
				}

				item1, found := c.Item(k)
				if !found {
					t.Fatalf("Cache.Item() did not find key %q", k)
				}
				if item1.Value != v1 {
					t.Fatalf("Cache.Item().Value = %q, want %q", item1.Value, v1)
				}
				exp, until := item1.Expiration, time.Until(item1.Expiration)
				if exp.IsZero() || until != twoHours {
					t.Fatalf("Cache.Item().Expiration = %v, want 2 hours", exp)
				}

				synctest.Sleep(time.Hour)

				if replaced := c.Replace(k, v2); !replaced {
					t.Fatal("Cache.Replace() failed to update the item")
				}

				item2, found := c.Item(k)
				if !found {
					t.Fatalf("Cache.Item() did not find key %q", k)
				}
				if item2.Value != v2 {
					t.Errorf("Cache.Replace() did not update the value: got %q, want %q", item2.Value, v2)
				}
				if item2.Expiration != item1.Expiration {
					t.Errorf("Cache.Replace() caused a TTL drift: got expirat. %v, want %v", item2.Expiration, item1.Expiration)
				}
			})
		})
	}
}

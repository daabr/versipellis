package cache_test

import (
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daabr/versipellis/pkg/cache"
)

func TestConcurrentCRUD(t *testing.T) {
	t.Parallel()

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
			t.Parallel()

			c := tt.cache(cache.WithExpiration(100*time.Millisecond), cache.WithCleanup(20*time.Millisecond))
			t.Cleanup(c.StopCleanupWorker)

			const (
				numWorkers   = 16
				opsPerWorker = 1000
				keySpace     = 50
			)

			var wg sync.WaitGroup
			wg.Add(numWorkers)

			for workerID := range numWorkers {
				go func(wid int) {
					defer wg.Done()
					for op := range opsPerWorker {
						key := (wid*opsPerWorker + op) % keySpace
						val := wid*10000 + op

						switch rand.IntN(9) { //gosec:disable G404 // Test needs to be fast, not cryptographically secure.
						case 0:
							c.Set(key, val)
						case 1:
							c.Get(key)
						case 2:
							c.Add(key, val)
						case 3:
							c.Replace(key, val)
						case 4:
							c.Delete(key)
						case 5:
							c.Item(key)
						case 6:
							_ = c.Keys()
						case 7:
							_ = c.Items()
						case 8:
							_ = c.ItemCount()
							_ = c.Len()
						}
					}
				}(workerID)
			}

			wg.Wait()
		})
	}
}

func TestConcurrentAddMutualExclusion(t *testing.T) {
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

			const numRounds = 50
			const numGoroutines = 20

			for round := range numRounds {
				c := tt.cache(cache.WithCleanup(0))
				key := fmt.Sprintf("contended_key_%d", round)

				var (
					startBarrier sync.WaitGroup
					finishGroup  sync.WaitGroup
					addedCount   atomic.Int32
					winningVal   string
					mu           sync.Mutex
				)

				startBarrier.Add(1)
				finishGroup.Add(numGoroutines)

				for i := range numGoroutines {
					val := fmt.Sprintf("val_goroutine_%d", i)
					go func(v string) {
						defer finishGroup.Done()
						startBarrier.Wait()

						stored, added := c.Add(key, v)
						if added {
							addedCount.Add(1)
							mu.Lock()
							winningVal = stored
							mu.Unlock()
						}
					}(val)
				}

				startBarrier.Done()
				finishGroup.Wait()

				if got := addedCount.Load(); got != 1 {
					t.Fatalf("round %d: Add() succeeded %d times, want exactly 1", round, got)
				}

				gotVal, ok := c.Get(key)
				if !ok {
					t.Fatalf("round %d: key was not found after concurrent Add()", round)
				}
				mu.Lock()
				wantVal := winningVal
				mu.Unlock()
				if gotVal != wantVal {
					t.Errorf("round %d: got cached value %q, want %q", round, gotVal, wantVal)
				}
			}
		})
	}
}

func TestConcurrentReplace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		cache func(...cache.Option) cache.Cache[string, int]
	}{
		{
			name:  "syncmap",
			cache: cache.NewFastCache[string, int],
		},
		{
			name:  "mutexmap",
			cache: cache.NewLeanCache[string, int],
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := tt.cache(cache.WithCleanup(0))
			const key = "replace_key"
			c.Set(key, 0)

			const numWorkers = 16
			const opsPerWorker = 500

			var wg sync.WaitGroup
			wg.Add(numWorkers)

			for w := range numWorkers {
				go func(workerID int) {
					defer wg.Done()
					for op := range opsPerWorker {
						c.Replace(key, workerID*1000+op)
					}
				}(w)
			}

			wg.Wait()

			if _, ok := c.Get(key); !ok {
				t.Fatalf("key %q should still exist after concurrent replaces", key)
			}
		})
	}
}

func TestConcurrentClear(t *testing.T) {
	t.Parallel()

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
			t.Parallel()

			c := tt.cache(cache.WithCleanup(0))
			done := make(chan struct{})

			// Writers.
			var wg sync.WaitGroup
			for w := range 8 {
				wg.Add(1)
				go func(wid int) {
					defer wg.Done()
					for i := 0; ; i++ {
						select {
						case <-done:
							return
						default:
							c.Set(i%100, wid)
							c.Get(i % 100)
						}
					}
				}(w)
			}

			// Clearer.
			for range 20 {
				time.Sleep(2 * time.Millisecond)
				c.Clear()
			}

			close(done)
			wg.Wait()
		})
	}
}

func TestConcurrentExpirationAndCleanup(t *testing.T) {
	t.Parallel()

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
			t.Parallel()

			// Fast cleanup and short expiration.
			c := tt.cache(cache.WithExpiration(5*time.Millisecond), cache.WithCleanup(5*time.Millisecond))
			t.Cleanup(c.StopCleanupWorker)

			var wg sync.WaitGroup
			const workers = 8
			const ops = 500

			for w := range workers {
				wg.Add(1)
				go func(wid int) {
					defer wg.Done()
					for i := range ops {
						key := (wid*ops + i) % 20
						c.Set(key, wid)
						c.Get(key)
						time.Sleep(100 * time.Microsecond)
					}
				}(w)
			}

			wg.Wait()
		})
	}
}

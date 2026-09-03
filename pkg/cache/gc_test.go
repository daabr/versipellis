package cache

import (
	"runtime"
	"testing"
	"time"
	"weak"
)

func TestRuntimeCleanupOnGC(t *testing.T) {
	tests := []struct {
		name  string
		cache func(...Option) Cache[string, string]
	}{
		{
			name:  "syncmap",
			cache: NewFastCache[string, string],
		},
		{
			name:  "mutexmap",
			cache: NewLeanCache[string, string],
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stop <-chan struct{}
			var gc func() bool

			createAndAbandonCache := func() {
				c := tt.cache(WithCleanup(time.Hour))

				if f, ok := c.(*FastCache[string, string]); ok {
					stop = f.stop
					ref := weak.Make(f)
					gc = func() bool { return ref.Value() == nil }
				}
				if l, ok := c.(*LeanCache[string, string]); ok {
					stop = l.stop
					ref := weak.Make(l)
					gc = func() bool { return ref.Value() == nil }
				}
			}

			createAndAbandonCache()

			// Force garbage collection to trigger [runtime.AddCleanup].
			go func() {
				for range 200 {
					runtime.GC()
					time.Sleep(5 * time.Millisecond)
				}
			}()

			select {
			case <-stop:
				// Success.
			case <-time.After(2 * time.Second):
				t.Fatal("garbage collection didn't close the cache's stop channel")
			}
			if !gc() {
				t.Fatal("cache memory was not reclaimed by the garbage collector")
			}
		})
	}
}

func newFastCacheWorker(done chan struct{}) {
	c := &FastCache[string, string]{stop: make(chan struct{})}
	ref := weak.Make(c)
	stop := c.stop

	go func() {
		defer close(done)
		fastCleanup(ref, stop, 10*time.Millisecond)
	}()
}

func newLeanCacheWorker(done chan struct{}) {
	c := &LeanCache[string, string]{stop: make(chan struct{}), data: make(map[string]Item[string])}
	ref := weak.Make(c)
	stop := c.stop

	go func() {
		defer close(done)
		leanCleanup(ref, stop, 10*time.Millisecond)
	}()
}

func TestWeakPointerFallbackOnGC(t *testing.T) {
	tests := []struct {
		name  string
		start func(done chan struct{})
	}{
		{
			name:  "syncmap",
			start: newFastCacheWorker,
		},
		{
			name:  "mutexmap",
			start: newLeanCacheWorker,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			done := make(chan struct{})
			tt.start(done)

			// Force garbage collection to make the cache unreachable.
			go func() {
				for range 30 {
					runtime.GC()
					time.Sleep(10 * time.Millisecond)
				}
			}()

			select {
			case <-done:
				// Success: cleanup goroutine detected weak.Pointer == nil and terminated.
			case <-time.After(2 * time.Second):
				t.Fatal("cleanup goroutine failed to exit after cache became unreachable")
			}
		})
	}
}

package flow

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// recorder is a concurrency-safe [Batcher] dispatch function which records all the dispatched batches.
type recorder struct {
	mu      sync.Mutex
	batches [][]string
}

func (r *recorder) dispatch(_ context.Context, batch [][]byte) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.batches = append(r.batches, strs(batch))
}

func (r *recorder) get() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.batches
}

func blobs(items ...string) [][]byte {
	if items == nil {
		return nil
	}
	b := make([][]byte, 0, len(items))
	for _, s := range items {
		b = append(b, []byte(s))
	}
	return b
}

func strs(items [][]byte) []string {
	if items == nil {
		return nil
	}
	s := make([]string, 0, len(items))
	for _, b := range items {
		s = append(s, string(b))
	}
	return s
}

func TestNewBatcher(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		limits   Limits
		sizeOf   func([]byte) int
		dispatch func(context.Context, [][]byte)
		want     Limits
		wantErr  bool
	}{
		{
			name:     "disabled",
			dispatch: new(recorder).dispatch,
		},
		{
			name:     "all_limits",
			limits:   Limits{MaxItems: 10, MaxBytes: 100, Window: time.Second},
			sizeOf:   func(s []byte) int { return len(s) },
			dispatch: new(recorder).dispatch,
			want:     Limits{MaxItems: 10, MaxBytes: 100, Window: time.Second},
		},
		{
			name:    "missing_dispatch_func",
			limits:  Limits{MaxItems: 10},
			wantErr: true,
		},
		{
			name:     "missing_size_func",
			limits:   Limits{MaxBytes: 100},
			dispatch: new(recorder).dispatch,
			wantErr:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := NewBatcher(tt.limits, tt.dispatch, &Options[[]byte]{SizeOf: tt.sizeOf})
			if (err != nil) != tt.wantErr {
				t.Fatalf("NewBatcher() error = %v, wantErr = %v", err, tt.wantErr)
			}
			if (got == nil) != tt.wantErr {
				t.Fatalf("NewBatcher() = %v, want nil = %v", got, tt.wantErr)
			}
			if got != nil && got.limits != tt.want {
				t.Errorf("NewBatcher().limits = %+v, want %+v", got.limits, tt.want)
			}
		})
	}
}

func TestBatcherAdd(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		limits Limits
		adds   [][]string // Each element is a single Add call, e.g., a single item or a chunk of items.
		want   [][]string // Dispatched batches, including a final flush.
	}{
		{
			name: "nothing",
			adds: [][]string{nil, {}},
		},
		{
			name: "disabled_passthrough",
			adds: [][]string{{"a"}, {"b", "c"}},
			want: [][]string{{"a"}, {"b", "c"}},
		},
		{
			name:   "window_without_size_limits_is_disabled",
			limits: Limits{Window: time.Second},
			adds:   [][]string{{"a"}, {"b", "c"}},
			want:   [][]string{{"a"}, {"b", "c"}},
		},
		{
			name:   "single_item_batches",
			limits: Limits{MaxItems: 1},
			adds:   [][]string{{"a", "b"}, {"c"}},
			want:   [][]string{{"a"}, {"b"}, {"c"}},
		},
		{
			name:   "items_counted_across_singles_and_chunks",
			limits: Limits{MaxItems: 3},
			adds:   [][]string{{"a"}, {"b", "c", "d", "e"}, {"f"}, {"g", "h"}},
			want:   [][]string{{"a", "b", "c"}, {"d", "e", "f"}, {"g", "h"}},
		},
		{
			name:   "max_bytes",
			limits: Limits{MaxBytes: 5},
			adds:   [][]string{{"ab", "cd"}, {"e", "fgh"}},
			want:   [][]string{{"ab", "cd", "e"}, {"fgh"}},
		},
		{
			name:   "oversized_item_dispatched_alone",
			limits: Limits{MaxBytes: 5},
			adds:   [][]string{{"ab", "oversized", "c"}},
			want:   [][]string{{"ab"}, {"oversized"}, {"c"}},
		},
		{
			name:   "items_or_bytes_whichever_first",
			limits: Limits{MaxItems: 3, MaxBytes: 5},
			adds:   [][]string{{"a", "b", "c", "dddd", "e", "f"}},
			want:   [][]string{{"a", "b", "c"}, {"dddd", "e"}, {"f"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := new(recorder)
			b, err := NewBatcher(tt.limits, r.dispatch, &Options[[]byte]{SizeOf: func(s []byte) int { return len(s) }})
			if err != nil {
				t.Fatalf("NewBatcher() error = %v", err)
			}

			for _, items := range tt.adds {
				b.Add(t.Context(), blobs(items...)...)
			}
			b.Flush(t.Context())

			if got := r.get(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("dispatched batches = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBatcherWindow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		maxItems int
		items    int           // Added one by one, with a delay between them.
		delay    time.Duration // Between items.
		want     []int         // Sizes of dispatched batches, without a final flush.
	}{
		{
			name:     "single_timer_from_first_item",
			maxItems: 100,
			items:    50,
			delay:    10 * time.Millisecond, // 50 items within 500ms < [Limits.Window].
			want:     []int{50},
		},
		{
			name:     "multiple_delayed_batches",
			maxItems: 100,
			items:    6,
			delay:    400 * time.Millisecond, // Items at 0, 0.4, 0.8 | 1.2, 1.6, 2.0.
			want:     []int{3, 3},
		},
		{
			name:     "stale_timer_after_full_batch",
			maxItems: 10,
			items:    15, // 10 dispatched due to [Limits.MaxItems], then 5 after [Limits.Window]: no double dispatch.
			delay:    time.Millisecond,
			want:     []int{10, 5},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				r := new(recorder)
				b, err := NewBatcher(Limits{MaxItems: tt.maxItems, Window: time.Second}, r.dispatch, nil)
				if err != nil {
					t.Fatalf("NewBatcher() error = %v", err)
				}

				for range tt.items {
					b.Add(t.Context(), []byte("x"))
					time.Sleep(tt.delay)
				}
				synctest.Sleep(2 * time.Second)

				var got []int
				for _, batch := range r.get() {
					got = append(got, len(batch))
				}
				if !reflect.DeepEqual(got, tt.want) {
					t.Errorf("dispatched batch sizes = %v, want %v", got, tt.want)
				}
			})
		})
	}
}

// TestBatcherDetachedContext ensures that all the dispatch paths detach the context from the caller's cancellation,
// e.g., when a SQL query's context is canceled after a timeout, rows that were already scanned must still be sent.
func TestBatcherDetachedContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		limits Limits
		flush  bool // Call Flush after Add.
		wait   bool // Wait for the window timer after Add.
	}{
		{
			name: "batching_disabled",
		},
		{
			name:   "full_batch",
			limits: Limits{MaxItems: 1},
		},
		{
			name:   "flush",
			limits: Limits{MaxItems: 10},
			flush:  true,
		},
		{
			name:   "window",
			limits: Limits{MaxItems: 10, Window: time.Second},
			wait:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				dispatched := false
				var gotErr error
				dispatch := func(ctx context.Context, _ [][]byte) {
					dispatched = true
					gotErr = ctx.Err()
				}
				b, err := NewBatcher(tt.limits, dispatch, nil)
				if err != nil {
					t.Fatalf("NewBatcher() error = %v", err)
				}

				ctx, cancel := context.WithCancel(t.Context())
				cancel() // E.g., a canceled request or query context.

				b.Add(ctx, []byte("x"))
				if tt.flush {
					b.Flush(ctx)
				}
				if tt.wait {
					synctest.Sleep(2 * time.Second)
				}

				if !dispatched {
					t.Fatal("nothing was dispatched")
				}
				if gotErr != nil {
					t.Errorf("dispatch context error = %v, want nil", gotErr)
				}
			})
		})
	}
}

func TestBatcherFlush(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		r := new(recorder)
		b, err := NewBatcher(Limits{MaxItems: 10, Window: time.Second}, r.dispatch, nil)
		if err != nil {
			t.Fatalf("NewBatcher() error = %v", err)
		}

		b.Flush(t.Context()) // Nothing is pending, so nothing is dispatched.
		if got := r.get(); got != nil {
			t.Errorf("dispatched batches after empty Batcher.Flush() = %q, want nil", got)
		}

		b.Add(t.Context(), blobs("a", "b")...)
		b.Flush(t.Context())
		want := [][]string{{"a", "b"}}
		if got := r.get(); !reflect.DeepEqual(got, want) {
			t.Errorf("dispatched batches after Batcher.Flush() = %q, want %q", got, want)
		}

		synctest.Sleep(2 * time.Second) // The timer of the flushed batch must not dispatch anything.

		if got := r.get(); !reflect.DeepEqual(got, want) {
			t.Errorf("dispatched batches after Batcher.Flush() and window = %q, want %q", got, want)
		}
	})
}

// TestBatcherReentrantDispatch ensures that a dispatch function may call the batcher again without
// deadlocking, e.g., a sender that flushes the pending batch while it's handling a full or flushed one.
func TestBatcherReentrantDispatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		limit int
		flush bool // Call Flush after Add, instead of relying on [Limits.MaxItems].
	}{
		{
			name:  "full_batch",
			limit: 1,
		},
		{
			name:  "flush",
			limit: 10,
			flush: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var b *Batcher[[]byte]
			r := new(recorder)
			dispatch := func(ctx context.Context, batch [][]byte) {
				r.dispatch(ctx, batch)
				b.Flush(ctx)
			}

			var err error
			if b, err = NewBatcher(Limits{MaxItems: tt.limit}, dispatch, nil); err != nil {
				t.Fatalf("NewBatcher() error = %v", err)
			}

			done := make(chan struct{})
			go func() {
				defer close(done)
				b.Add(t.Context(), blobs("a", "b")...)
				if tt.flush {
					b.Flush(t.Context())
				}
			}()

			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("Batcher deadlocked with a reentrant dispatch function")
			}

			total := 0
			for _, batch := range r.get() {
				total += len(batch)
			}
			if total != 2 {
				t.Errorf("total dispatched items = %d, want 2", total)
			}
		})
	}
}

func TestBatcherConcurrency(t *testing.T) {
	t.Parallel()

	const producers, itemsPerProducer, maxItems = 20, 100, 7

	r := new(recorder)
	b, err := NewBatcher(Limits{MaxItems: maxItems, Window: time.Millisecond}, r.dispatch, nil)
	if err != nil {
		t.Fatalf("NewBatcher() error = %v", err)
	}

	var wg sync.WaitGroup
	for range producers {
		wg.Go(func() {
			for i := range itemsPerProducer {
				if i%2 == 0 {
					b.Add(t.Context(), []byte("x"))
				} else {
					b.Add(t.Context(), blobs("y", "z")...)
				}
				b.Flush(t.Context())
			}
		})
	}
	wg.Wait()
	b.Flush(t.Context())

	total := 0
	for _, batch := range r.get() {
		if len(batch) > maxItems {
			t.Errorf("dispatched batch size = %d, want <= %d", len(batch), maxItems)
		}
		total += len(batch)
	}
	if want := producers * itemsPerProducer * 3 / 2; total != want {
		t.Errorf("total dispatched items = %d, want %d", total, want)
	}
}

func TestBatcherAllocsPerBatch(t *testing.T) { //nolint:paralleltest // [testing.AllocsPerRun] can't run in parallel.
	b, err := NewBatcher(Limits{MaxItems: 1000}, func(context.Context, [][]byte) {}, nil)
	if err != nil {
		t.Fatalf("NewBatcher() error = %v", err)
	}

	// Without a delay limit, adding a single item never allocates, except when the buffer grows.
	// The item is created only once, so that its own allocation doesn't count as the batcher's.
	item := []byte("x")
	if allocs := testing.AllocsPerRun(10_000, func() { b.Add(t.Context(), item) }); allocs > 0.1 {
		t.Errorf("Batcher.Add() allocations per item = %.2f, want ~0", allocs)
	}
}

func TestBatcherGuard(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		allow       bool
		wantBatch   []string // Dispatched after [Limits.Window].
		wantFlushed []string // Still pending afterwards.
	}{
		{
			name:      "guard_allows_delayed_dispatch",
			allow:     true,
			wantBatch: []string{"a", "b"},
		},
		{
			name:        "guard_blocks_delayed_dispatch",
			allow:       false,
			wantFlushed: []string{"a", "b"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				r := new(recorder)
				guard := func(flush func()) {
					if tt.allow {
						flush()
					}
				}
				l := Limits{MaxItems: 10, Window: time.Second}
				b, err := NewBatcher(l, r.dispatch, &Options[[]byte]{Guard: guard})
				if err != nil {
					t.Fatalf("NewBatcher() error = %v", err)
				}

				b.Add(t.Context(), blobs("a", "b")...)
				synctest.Sleep(2 * time.Second)

				var gotBatch []string
				if batches := r.get(); len(batches) > 0 {
					gotBatch = batches[0]
				}
				if !reflect.DeepEqual(gotBatch, tt.wantBatch) {
					t.Errorf("delayed batch = %q, want %q", gotBatch, tt.wantBatch)
				}

				n := len(r.get())
				b.Flush(t.Context())
				var gotFlushed []string
				if batches := r.get(); len(batches) > n {
					gotFlushed = batches[n]
				}
				if !reflect.DeepEqual(gotFlushed, tt.wantFlushed) {
					t.Errorf("flushed batch = %q, want %q", gotFlushed, tt.wantFlushed)
				}
			})
		})
	}
}

// TestBatcherGuardShutdown simulates a sender's shutdown pattern: delayed dispatches are guarded by a read lock
// and a lame-duck check, and Close sets the lame-duck flag under the write lock and then flushes the pending batch.
// No item may be lost or dispatched by the timer after Close starts, however the delayed dispatch interleaves.
func TestBatcherGuardShutdown(t *testing.T) {
	t.Parallel()

	for range 200 {
		var closeMu sync.RWMutex
		var lameDuck, flushing, dispatchedAfterClose bool
		var dispatched [][]byte
		var dispatchedMu sync.Mutex

		post := func(_ context.Context, batch [][]byte) {
			dispatchedMu.Lock()
			defer dispatchedMu.Unlock()
			if lameDuck && !flushing {
				dispatchedAfterClose = true
			}
			dispatched = append(dispatched, batch...)
		}
		guard := func(flush func()) {
			closeMu.RLock()
			defer closeMu.RUnlock()
			if !lameDuck {
				flush()
			}
		}
		l := Limits{MaxItems: 10, Window: time.Microsecond}
		b, err := NewBatcher(l, post, &Options[[]byte]{Guard: guard})
		if err != nil {
			t.Fatalf("NewBatcher() error = %v", err)
		}

		closeMu.RLock()
		b.Add(t.Context(), blobs("a", "b", "c")...)
		closeMu.RUnlock()

		closeMu.Lock()
		dispatchedMu.Lock()
		lameDuck = true
		dispatchedMu.Unlock()
		closeMu.Unlock()

		dispatchedMu.Lock()
		flushing = true
		dispatchedMu.Unlock()
		b.Flush(t.Context())
		dispatchedMu.Lock()
		flushing = false
		dispatchedMu.Unlock()

		time.Sleep(10 * time.Microsecond) // Let a racing delayed dispatch run, if any.

		dispatchedMu.Lock()
		gotAfterClose := dispatchedAfterClose
		got := strs(dispatched)
		dispatchedMu.Unlock()

		if gotAfterClose {
			t.Fatal("batch dispatched after Close")
		}
		if !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
			t.Fatalf("dispatched items = %q, want exactly %q", got, []string{"a", "b", "c"})
		}
	}
}

package flow

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Limits determine when a [Batcher] dispatches a batch. Zero and negative values disable the corresponding
// limit. Batching is disabled entirely unless [Limits.MaxItems] or [Limits.MaxBytes] are positive, because
// time-based batching alone would accumulate an unbounded amount of data.
type Limits struct {
	MaxItems int           // Maximum number of items in each batch.
	MaxBytes int           // Maximum sum of the item sizes in each batch, as reported by [Options.SizeOf].
	Window   time.Duration // Maximum time between adding the first item to a new batch and dispatching it.
}

// Options are optional settings for a [Batcher].
type Options[T Kind] struct {
	// SizeOf returns the size of an item. It is required if [Limits.MaxBytes] is positive.
	SizeOf func(T) int

	// Guard wraps the dispatching of batches when their [Limits.Window] expires, which happens in a separate
	// goroutine. It must either call flush synchronously, or not at all (in which case the batch remains pending).
	// This lets owners synchronize delayed dispatches with their own shutdown: e.g., a sender's guard acquires a read
	// lock and checks if it's shutting down, and its Close method sets that state under the write lock, and then calls
	// [Batcher.Flush]. To prevent deadlocks, owners must acquire their own lock before calling the batcher's methods,
	// never after, and the dispatch function must not acquire it again (recursive read locking is prohibited).
	Guard func(flush func())
}

// Batcher accumulates items of any [Kind], and dispatches them in batches when a batch reaches [Limits.MaxItems]
// or [Limits.MaxBytes], when [Limits.Window] expires, or when it's flushed. If batching is disabled, it acts as
// a trivial passthrough. It is safe for concurrent use, but the order of batches from concurrent producers is
// non-deterministic. This mechanism is usable by collectors, receivers, and senders alike.
type Batcher[T Kind] struct {
	limits   Limits
	dispatch func(context.Context, []T)
	sizeOf   func(T) int
	guard    func(flush func())

	// Relevant only when batching is enabled.
	mu    sync.Mutex
	buf   []T
	bytes int
	timer *time.Timer
	gen   uint64 // Generation counter: invalidates timers of batches that were already dispatched.
}

// NewBatcher creates a new [Batcher], which passes each batch to the dispatch function, which takes
// ownership of it. User-configured [Limits] are parsed and normalized by BatchLimits() in the "config"
// package. This function returns an error and a nil instance if and only if the configuration is invalid.
func NewBatcher[T Kind](limits Limits, dispatch func(context.Context, []T), opts *Options[T]) (*Batcher[T], error) {
	if opts == nil {
		opts = new(Options[T])
	}

	switch {
	case dispatch == nil:
		return nil, errors.New("dispatch function is required")
	case limits.MaxBytes > 0 && opts.SizeOf == nil:
		return nil, errors.New("item size function is required when byte size limit is enabled")
	}

	return &Batcher[T]{limits: limits, dispatch: dispatch, sizeOf: opts.SizeOf, guard: opts.Guard}, nil
}

// AddItem appends a single data item to a new or pending batch, and dispatches the batch if it's ready.
// If batching is disabled, it acts as a trivial passthrough. If an item's size exceeds [Limits.MaxBytes]
// it is dispatched immediately on its own, never dropped. The batcher takes ownership of the item, so the
// caller must not modify it after this call.
//
// Concurrency & lifecycle: batches are dispatched in the caller's goroutine, but outside the batcher's
// critical-path lock, so a slow or reentrant dispatch function doesn't block or deadlock other callers,
// and the dispatch function operates with a context that is detached from the caller's cancellation.
func (b *Batcher[T]) AddItem(ctx context.Context, item T) {
	if b.passthrough() {
		b.dispatch(detach(ctx), []T{item})
		return
	}

	b.mu.Lock()
	ready := b.appendToBatch(item, nil)
	b.startTimer(ctx)
	b.mu.Unlock()

	b.dispatchBatches(ctx, ready)
}

// AddChunk is like [Batcher.AddItem] for multiple items, but it also takes ownership of the slice itself, so
// it doesn't copy it when batching is disabled. Callers must not modify or reuse the slice after this call.
func (b *Batcher[T]) AddChunk(ctx context.Context, items []T) {
	if len(items) == 0 {
		return
	}

	if b.passthrough() {
		b.dispatch(detach(ctx), items)
		return
	}

	var ready [][]T
	b.mu.Lock()
	for _, item := range items {
		ready = b.appendToBatch(item, ready)
	}
	b.startTimer(ctx) // Once per call, not each new batch (full batches within the chunk don't need a timer).
	b.mu.Unlock()

	b.dispatchBatches(ctx, ready)
}

func (b *Batcher[T]) passthrough() bool {
	return b.limits.MaxItems <= 0 && b.limits.MaxBytes <= 0
}

func detach(ctx context.Context) context.Context {
	if ctx.Done() == nil {
		return ctx
	}
	return context.WithoutCancel(ctx)
}

// appendToBatch must be called while the caller is holding [Batcher.mu].
func (b *Batcher[T]) appendToBatch(item T, ready [][]T) [][]T {
	size := 0
	if b.limits.MaxBytes > 0 {
		size = b.sizeOf(item)
		if len(b.buf) > 0 && b.bytes+size > b.limits.MaxBytes {
			// Batch by byte size.
			ready = append(ready, b.nextBatch())
		}
	}

	b.buf = append(b.buf, item)
	b.bytes += size

	if b.limits.MaxItems > 0 && len(b.buf) >= b.limits.MaxItems || b.limits.MaxBytes > 0 && b.bytes >= b.limits.MaxBytes {
		// Batch by either item count or byte size, whichever limit is reached first. We repeat
		// the byte size check from above to dispatch a single oversized item immediately.
		ready = append(ready, b.nextBatch())
	}

	return ready
}

// startTimer starts a timer for a new pending batch, measured from its first item - if there is such a batch which
// doesn't have a timer yet. It must be called after [Batcher.appendToBatch], while the caller is holding [Batcher.mu].
func (b *Batcher[T]) startTimer(ctx context.Context) {
	if b.limits.Window <= 0 || len(b.buf) == 0 || b.timer != nil {
		return
	}

	currentGen := b.gen
	b.timer = time.AfterFunc(b.limits.Window, func() {
		b.timedFlush(detach(ctx), currentGen)
	})
}

// dispatchBatches must be called after [Batcher.mu] has been released by the caller.
func (b *Batcher[T]) dispatchBatches(ctx context.Context, ready [][]T) {
	if len(ready) == 0 {
		return
	}

	detached := detach(ctx)
	for _, batch := range ready {
		b.dispatch(detached, batch)
	}
}

// Flush dispatches all pending data immediately, if there is any. Collectors should call it after each retrieval
// operation (e.g., SQL query) because there's no point in waiting for more items at that point. Senders should call
// it within their Close method (i.e. while already in lame-duck mode, but before waiting for work that is in progress)
// to avoid data loss. Like the Add* methods, the dispatch function operates with a context that is detached from the
// caller's lifecycle, so owners must abort dispatched work by their own means (e.g., after a shutdown timeout).
func (b *Batcher[T]) Flush(ctx context.Context) {
	b.mu.Lock()
	batch := b.nextBatch()
	b.mu.Unlock()

	if len(batch) > 0 {
		b.dispatch(detach(ctx), batch)
	}
}

func (b *Batcher[T]) timedFlush(ctx context.Context, gen uint64) {
	if b.guard == nil {
		b.guardedTimedFlush(ctx, gen)
		return
	}
	b.guard(func() { b.guardedTimedFlush(ctx, gen) })
}

// guardedTimedFlush dispatches the pending batch only if it's still the batch whose timer expired.
// With a [Batcher.guard], it must be called within it: checking and taking the batch before acquiring
// the owner's lock could take a batch that the owner then refuses to dispatch, so it would be lost.
func (b *Batcher[T]) guardedTimedFlush(ctx context.Context, gen uint64) {
	var batch []T

	b.mu.Lock()
	if gen == b.gen {
		batch = b.nextBatch()
	}
	b.mu.Unlock()

	if len(batch) > 0 {
		b.dispatch(ctx, batch)
	}
}

// nextBatch must be called while the caller is holding [Batcher.mu].
func (b *Batcher[T]) nextBatch() []T {
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	b.gen++

	batch := b.buf
	b.buf, b.bytes = nil, 0
	return batch
}

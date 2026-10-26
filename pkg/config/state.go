package config

import (
	"context"
	"sync"
)

// State manages the lifecycle and concurrency in collectors, receivers, and senders,
// with an emphasis on safe and robust control of shutdown procedures.
type State struct {
	cancelSched context.CancelFunc
	cancelExec  context.CancelFunc

	InProgress sync.WaitGroup
	CloseOnce  sync.Once
	CloseMu    sync.RWMutex

	Closing chan struct{}
	Closed  chan struct{}
}

// StartWith initializes the state of the calling collector within its own "Start" function,
// and returns its scheduling and execution contexts, which are both derived from ctx.
func (s *State) StartWith(ctx context.Context) (sched, exec context.Context) {
	s.Start()
	sched, s.cancelSched = context.WithCancel(ctx)
	exec, s.cancelExec = context.WithCancel(context.WithoutCancel(ctx))
	return sched, exec
}

// Start initializes the state of the calling receiver or sender within their own "Start" function.
// It is idempotent for extra safety, even though no component can call it more than once.
func (s *State) Start() {
	if s.IsStarted() {
		return
	}
	s.Closing = make(chan struct{})
	s.Closed = make(chan struct{})
}

// IsStarted returns true if the component's "Start" function has been called. Components check it at the
// beginning of their "Close" function, so closing before startup doesn't consume [State.CloseOnce].
func (s *State) IsStarted() bool {
	return s.Closed != nil
}

// BeginClose signals the beginning of a shutdown procedure for a collector, receiver, or sender. This function
// is called at the very beginning of the component's "Close" function (if appropriate). It closes [State.Closing]
// (senders use it to stop waiting between retries), and for collectors, it also cancels their scheduling context.
func (s *State) BeginClose() {
	if s.Closing == nil {
		return
	}

	s.CloseMu.Lock()
	defer s.CloseMu.Unlock()

	close(s.Closing)
	if s.cancelSched != nil {
		s.cancelSched()
		s.cancelSched = nil
	}
}

// StopStragglers cancels the calling collector's execution context when its graceful shutdown period has expired.
func (s *State) StopStragglers() {
	if s.cancelExec != nil {
		s.cancelExec()
		s.cancelExec = nil
	}
}

// FinishClose marks the calling component as closed. Collectors call it at the very end of their "Close" function to
// signal to the main goroutine that it's safe to start shutting down senders without risking data loss. Senders call
// it right after waiting for their work in progress: if that wait timed out, it's also a signal for their straggler
// workers to abort (they can't use [State.StopStragglers] because their contexts are detached by design). It also
// cancels the collector's execution context, if [State.StopStragglers] didn't already, to release its resources:
// by now, all the collector's workers have either finished or been aborted.
func (s *State) FinishClose() {
	if s.Closed == nil {
		return
	}
	s.StopStragglers()
	close(s.Closed)
}

// IsClosing returns true if the calling component is in the process of shutting down. This is a "lame-duck" state
// indicator to prevent data loss by blocking new operations from starting just before the application terminates.
func (s *State) IsClosing() bool {
	select {
	case <-s.Closing:
		return true
	default: // [State.Closing] is either nil or not yet closed.
		return false
	}
}

// IfClosingElseLock returns true if the calling component is in the process of shutting down (a.k.a. "lame-duck"
// mode). This function is concurrency-safe, however when it returns false it's the caller's responsibility to call
// [State.CloseMu].RUnlock() - which allows it to perform operations safely under the same lock!
func (s *State) IfClosingElseLock() bool {
	if s.IsClosing() {
		return true
	}

	s.CloseMu.RLock()
	closing := s.IsClosing()
	if closing {
		s.CloseMu.RUnlock()
	}

	return closing
}

// IfNotClosing executes the provided function only if the calling component is not in the process
// of shutting down. This function is concurrency-safe and manages the necessary locking on its own.
func (s *State) IfNotClosing(fn func()) {
	s.CloseMu.RLock()
	defer s.CloseMu.RUnlock()

	if !s.IsClosing() {
		fn()
	}
}

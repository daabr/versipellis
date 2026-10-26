package config_test

import (
	"context"
	"runtime"
	"testing"

	"github.com/daabr/versipellis/pkg/config"
)

func TestStateUnstarted(t *testing.T) {
	t.Parallel()

	s := config.State{}
	if s.IsStarted() {
		t.Error("State.IsStarted() = true, want false")
	}
	if s.IsClosing() {
		t.Error("State.IsClosing() = true, want false")
	}

	// Closing an unstarted component must be safe.
	s.BeginClose()
	s.StopStragglers()
	s.FinishClose()
	if s.IsStarted() {
		t.Error("State.IsStarted() after closing = true, want false")
	}
	if s.IsClosing() {
		t.Error("State.IsClosing() after closing = true, want false")
	}
}

func TestStateStart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		withCtx      bool // Collectors use [config.State.StartWith], receivers and senders use [config.State.Start].
		wantContexts bool
	}{
		{
			name:         "collector",
			withCtx:      true,
			wantContexts: true,
		},
		{
			name:         "receiver_or_sender",
			withCtx:      false,
			wantContexts: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := config.State{}
			var sched, exec context.Context
			if tt.withCtx {
				sched, exec = s.StartWith(t.Context())
			} else {
				s.Start()
			}

			if gotContexts := sched != nil && exec != nil; gotContexts != tt.wantContexts {
				t.Errorf("State.Start*() returned contexts = %v, want %v", gotContexts, tt.wantContexts)
			}
			if !s.IsStarted() {
				t.Error("State.IsStarted() = false, want true")
			}
			if s.IsClosing() {
				t.Error("State.IsClosing() = true, want false")
			}
			if isClosed(s.Closed) {
				t.Error("State.Closed is closed, want open")
			}

			closing, closed := s.Closing, s.Closed
			s.Start() // Idempotent.
			if s.Closing != closing || s.Closed != closed {
				t.Error("second State.Start() replaced the state's channels")
			}
		})
	}
}

func TestStateClose(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string

		noCtx          bool // Start with [config.State.Start], like receivers and senders.
		cancelParent   bool // Cancel the context passed to [config.State.StartWith], like a shutdown signal.
		beginClose     bool
		stopStragglers bool
		finishClose    bool

		wantClosing       bool
		wantClosed        bool
		wantSchedCanceled bool
		wantExecCanceled  bool
	}{
		{
			name: "started",
		},
		{
			name:              "parent_canceled",
			cancelParent:      true,
			wantSchedCanceled: true, // Doesn't close [config.State.Closing], only [config.State.BeginClose] does.
		},
		{
			name:              "begin_close",
			beginClose:        true,
			wantClosing:       true,
			wantSchedCanceled: true,
		},
		{
			name:             "stop_stragglers",
			stopStragglers:   true,
			wantExecCanceled: true,
		},
		{
			name:             "finish_close",
			finishClose:      true,
			wantClosed:       true,
			wantExecCanceled: true, // Released even without [config.State.StopStragglers].
		},
		{
			name:              "close_without_timeout", // The usual collector shutdown.
			beginClose:        true,
			finishClose:       true,
			wantClosing:       true,
			wantClosed:        true,
			wantSchedCanceled: true,
			wantExecCanceled:  true,
		},
		{
			name:              "full_close",
			beginClose:        true,
			stopStragglers:    true,
			finishClose:       true,
			wantClosing:       true,
			wantClosed:        true,
			wantSchedCanceled: true,
			wantExecCanceled:  true,
		},
		{
			name:           "no_ctx_full_close",
			noCtx:          true,
			beginClose:     true,
			stopStragglers: true,
			finishClose:    true,
			wantClosing:    true,
			wantClosed:     true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			parent, cancel := context.WithCancel(t.Context())
			defer cancel()

			s := config.State{}
			var sched, exec context.Context
			if tt.noCtx {
				s.Start()
			} else {
				sched, exec = s.StartWith(parent)
			}

			if tt.cancelParent {
				cancel()
			}
			if tt.beginClose {
				s.BeginClose()
			}
			if tt.stopStragglers {
				s.StopStragglers()
			}
			if tt.finishClose {
				s.FinishClose()
			}

			if got := s.IsClosing(); got != tt.wantClosing {
				t.Errorf("State.IsClosing() = %v, want %v", got, tt.wantClosing)
			}
			if got := isClosed(s.Closed); got != tt.wantClosed {
				t.Errorf("State.Closed is closed = %v, want %v", got, tt.wantClosed)
			}
			if tt.noCtx {
				return // No contexts to check.
			}
			if got := sched.Err() != nil; got != tt.wantSchedCanceled {
				t.Errorf("scheduling context canceled = %v, want %v", got, tt.wantSchedCanceled)
			}
			if got := exec.Err() != nil; got != tt.wantExecCanceled {
				t.Errorf("execution context canceled = %v, want %v", got, tt.wantExecCanceled)
			}
		})
	}
}

func TestStateCloseOnce(t *testing.T) {
	t.Parallel()

	s := config.State{}
	s.Start()

	// A registered worker that stops when the shutdown begins.
	s.InProgress.Go(func() { <-s.Closing })

	closeFunc := func() {
		s.CloseOnce.Do(func() {
			s.BeginClose()
			s.InProgress.Wait()
			s.FinishClose()
		})
	}
	closeFunc()
	closeFunc()

	if !isClosed(s.Closed) {
		t.Error("State.Closed isn't closed after closing")
	}
}

// isClosed reports whether the given channel is closed, without blocking (false if it's nil).
func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestStateIfClosingElseLock(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		started bool
		closing bool
		want    bool
	}{
		{
			name: "unstarted",
		},
		{
			name:    "started",
			started: true,
		},
		{
			name:    "closing",
			started: true,
			closing: true,
			want:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := config.State{}
			if tt.started {
				s.Start()
			}
			if tt.closing {
				s.BeginClose()
			}

			got := s.IfClosingElseLock()
			if got != tt.want {
				t.Errorf("State.IfClosingElseLock() = %v, want %v", got, tt.want)
			}

			// When it returns false, the caller holds the read lock and must release it.
			if !got {
				if s.CloseMu.TryLock() {
					t.Fatal("State.IfClosingElseLock() = false without holding the read lock")
				}
				s.CloseMu.RUnlock()
			}
			if !s.CloseMu.TryLock() {
				t.Fatal("State.CloseMu is still locked")
			}
			s.CloseMu.Unlock()
		})
	}
}

func TestStateIfNotClosing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		started bool
		closing bool
		want    bool
	}{
		{
			name: "unstarted",
			want: true,
		},
		{
			name:    "started",
			started: true,
			want:    true,
		},
		{
			name:    "closing",
			started: true,
			closing: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := config.State{}
			if tt.started {
				s.Start()
			}
			if tt.closing {
				s.BeginClose()
			}

			called := false
			s.IfNotClosing(func() {
				called = true
				if s.CloseMu.TryLock() {
					t.Error("State.IfNotClosing() called fn without holding the read lock")
				}
			})
			if called != tt.want {
				t.Errorf("State.IfNotClosing() called fn = %v, want %v", called, tt.want)
			}

			if !s.CloseMu.TryLock() {
				t.Fatal("State.CloseMu is still locked")
			}
			s.CloseMu.Unlock()
		})
	}
}

// TestStateBeginCloseWaitsForReaders checks the admission guarantee: work that was admitted
// under the read lock is registered before [config.State.BeginClose] can mark the state as closing.
func TestStateBeginCloseWaitsForReaders(t *testing.T) {
	t.Parallel()

	// Not using [synctest.Wait], because goroutines blocked on a [sync.RWMutex] aren't durably blocked.
	s := config.State{}
	s.Start()

	entered, release := make(chan struct{}), make(chan struct{})
	go s.IfNotClosing(func() {
		close(entered)
		<-release
	})
	<-entered

	closed := make(chan struct{})
	go func() {
		defer close(closed)
		s.BeginClose()
	}()

	// A pending [sync.RWMutex.Lock] call blocks new readers, so this detects that BeginClose is waiting.
	for s.CloseMu.TryRLock() {
		s.CloseMu.RUnlock()
		runtime.Gosched()
	}
	if s.IsClosing() {
		t.Error("State.BeginClose() closed State.Closing while the read lock was held")
	}

	close(release)
	<-closed
	if !s.IsClosing() {
		t.Error("State.IsClosing() after State.BeginClose() = false, want true")
	}

	s.IfNotClosing(func() { t.Error("State.IfNotClosing() called fn after State.BeginClose()") })
}

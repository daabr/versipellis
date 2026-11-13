package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
)

// blockingTokenSource counts its token requests, and blocks each one until its release channel is closed.
type blockingTokenSource struct {
	release chan struct{}
	token   *oauth2.Token
	err     error
	calls   atomic.Int32
}

func (b *blockingTokenSource) Token() (*oauth2.Token, error) {
	b.calls.Add(1)
	<-b.release
	return b.token, b.err
}

func newBlockingTokenSource(token *oauth2.Token, err error, released bool) *blockingTokenSource {
	b := &blockingTokenSource{release: make(chan struct{}), token: token, err: err}
	if released {
		close(b.release)
	}
	return b
}

func TestSharedTokenSourceReuse(t *testing.T) {
	t.Parallel()

	valid := &oauth2.Token{AccessToken: "valid", Expiry: time.Now().Add(time.Hour)}
	expired := &oauth2.Token{AccessToken: "expired", Expiry: time.Now().Add(-time.Hour)}
	fresh := &oauth2.Token{AccessToken: "fresh", Expiry: time.Now().Add(time.Hour)}

	tests := []struct {
		name      string
		initial   *oauth2.Token
		wantToken string
		wantCalls int32
	}{
		{"no_initial_token", nil, "fresh", 1},
		{"valid_initial_token", valid, "valid", 0},
		{"expired_initial_token", expired, "fresh", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			src := newBlockingTokenSource(fresh, nil, true)
			s := newSharedTokenSource(src, tt.initial)
			for range 3 { // A valid token must be reused, instead of requesting a new one each time.
				got, err := s.Token(t.Context())
				if err != nil {
					t.Fatalf("sharedTokenSource.Token() error = %v", err)
				}
				if got.AccessToken != tt.wantToken {
					t.Errorf("sharedTokenSource.Token() = %q, want %q", got.AccessToken, tt.wantToken)
				}
			}
			if got := src.calls.Load(); got != tt.wantCalls {
				t.Errorf("token requests = %d, want %d", got, tt.wantCalls)
			}
		})
	}
}

// Concurrent callers must share a single token request, including its error, instead of
// sending their own requests in turn, one at a time (like [oauth2.ReuseTokenSource] does).
// Note that callers waiting for a [sync.Mutex] aren't durably blocked, so if they serialize
// again, this test fails by hanging in [synctest.Wait] until the "go test" timeout.
func TestSharedTokenSourceConcurrentFailure(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		src := newBlockingTokenSource(nil, errors.New("token endpoint unavailable"), false)
		s := newSharedTokenSource(src, nil)

		const callers = 10
		var wg sync.WaitGroup
		var failures atomic.Int32
		for range callers {
			wg.Go(func() {
				if _, err := s.Token(t.Context()); err != nil {
					failures.Add(1)
				}
			})
		}

		synctest.Wait() // All the callers are waiting for the same request.
		close(src.release)
		wg.Wait()

		if got := src.calls.Load(); got != 1 {
			t.Errorf("token requests = %d, want 1", got)
		}
		if got := failures.Load(); got != callers {
			t.Errorf("sharedTokenSource.Token() failures = %d, want %d", got, callers)
		}
	})
}

// Callers must be able to stop waiting for a token request, which then continues in
// the background, so its token is still reusable afterwards, without another request.
func TestSharedTokenSourceCanceledWait(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		src := newBlockingTokenSource(&oauth2.Token{AccessToken: "t0k3n", Expiry: time.Now().Add(time.Hour)}, nil, false)
		s := newSharedTokenSource(src, nil)

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error)
		go func() {
			_, err := s.Token(ctx)
			done <- err
		}()

		synctest.Wait() // The caller is waiting for the token request.
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("sharedTokenSource.Token() error = %v, want %v", err, context.Canceled)
		}

		close(src.release)
		synctest.Wait() // The abandoned token request completes in the background.

		got, err := s.Token(t.Context())
		if err != nil {
			t.Fatalf("sharedTokenSource.Token() error = %v", err)
		}
		if got.AccessToken != "t0k3n" {
			t.Errorf("sharedTokenSource.Token() = %q, want %q", got.AccessToken, "t0k3n")
		}
		if got := src.calls.Load(); got != 1 {
			t.Errorf("token requests = %d, want 1", got)
		}
	})
}

func TestJWTTokenSourceSigningError(t *testing.T) {
	t.Parallel()

	// An ECDSA signing method with an Ed25519 key (any other type would be rejected too).
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey() error = %v", err)
	}
	src := &jwtTokenSource{method: jwt.SigningMethodES256, claims: map[string]any{"iss": "issuer"}, exp: time.Minute, key: key}

	if _, err := src.Token(); err == nil {
		t.Error("jwtTokenSource.Token() error = nil, want error")
	}
}

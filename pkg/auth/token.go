package auth

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
	"golang.org/x/sync/singleflight"
)

type jwtTokenSource struct {
	ctx context.Context //nolint:containedctx // Copied from the Go Team's own [golang.org/x/oauth2/jwt].

	tokenURL string
	method   jwt.SigningMethod
	claims   map[string]any
	exp      time.Duration
	key      any
}

func (j *jwtTokenSource) Token() (*oauth2.Token, error) {
	now := time.Now()
	j.claims["iat"] = now.Unix()
	j.claims["exp"] = now.Add(j.exp).Unix()

	_, err := jwt.NewWithClaims(j.method, jwt.MapClaims(j.claims)).SignedString(j.key)
	if err != nil {
		return nil, fmt.Errorf("failed to sign JWT with private key: %w", err)
	}

	// The exchange will be implemented in a separate PR, to simplify the review.
	return nil, errors.New("OAuth JWT bearer flow not yet implemented")
}

// sharedTokenSource wraps an [oauth2.TokenSource], to share each token request among all its concurrent callers,
// including its error. Without it, an [oauth2.TokenSource] instance would make each caller that waited for a failed
// request send its own request in turn, one at a time, because it holds its lock during each request, and it doesn't
// cache errors. It also lets callers stop waiting when their own context is canceled (e.g., when a client aborts its
// in-flight requests), while the request itself continues in the background, bounded by the context and timeout of
// its own HTTP client, so it doesn't block or leak, and its token is still reusable by other callers.
type sharedTokenSource struct {
	src   oauth2.TokenSource
	token atomic.Pointer[oauth2.Token]
	group singleflight.Group
}

func newSharedTokenSource(src oauth2.TokenSource, t *oauth2.Token) *sharedTokenSource {
	s := &sharedTokenSource{src: src}
	s.token.Store(t)
	return s
}

// Token returns the current token if it's still valid, without any locking. Otherwise, it requests a new one,
// or joins a request that's already in progress, and waits for its result until the given context is canceled.
func (s *sharedTokenSource) Token(ctx context.Context) (*oauth2.Token, error) {
	if t := s.token.Load(); t.Valid() { // Nil-safe, and has the same expiry margin as [oauth2.ReuseTokenSource].
		return t, nil
	}

	ch := s.group.DoChan("", func() (any, error) {
		t, err := s.src.Token()
		if err != nil {
			return nil, err //nolint:wrapcheck // The caller wraps it.
		}
		s.token.Store(t)
		return t, nil
	})

	select {
	case <-ctx.Done():
		return nil, ctx.Err() //nolint:wrapcheck // The caller wraps it.
	case result := <-ch:
		if result.Err != nil {
			return nil, result.Err
		}
		return result.Val.(*oauth2.Token), nil //nolint:errcheck,forcetypeassert // OK by definition.
	}
}

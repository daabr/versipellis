package http

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"github.com/daabr/versipellis/pkg/cache"
)

var (
	transportH2 = cache.NewFastCache[string, *http.Transport]()
	transportH3 = cache.NewFastCache[string, *http3.Transport]()
)

const (
	// MaxSuccessfulStatusCode defines the highest status code considered successful.
	// Any status code above this value is considered an error, including 3xx
	// redirections that could not be handled automatically by the client.
	MaxSuccessfulStatusCode = 299
)

type (
	// Used when sending received requests. See [serializeData] and [Sender.sendWithRetries].
	getBodyFunc func() (io.ReadCloser, error)

	// Used when sending collected responses. See [serializeData] and [Collector.processResponse].
	bodyProvider interface {
		GetBody() (io.ReadCloser, error)
	}

	// Used when sending collected responses. See [Collector.processResponse] and [serializeData].
	// This is a memory optimization, to avoid duplicate allocations for response bodies during retries,
	// working around the fact that [http.Response] doesn't have a GetBody() method, unlike [http.Request].
	reusableBody struct {
		*bytes.Reader

		raw []byte
	}
)

func (b *reusableBody) Close() error {
	return nil
}

func (b *reusableBody) GetBody() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(b.raw)), nil
}

// clientH2 constructs a client that supports both HTTP/1.1 and HTTP/2. It reuses [http.Transport]
// instances with the same TLS configuration, to optimize connection pooling. HTTP/2 requires TLS, so
// if the provided TLS configuration is nil, this function returns a client that only supports HTTP/1.1.
func clientH2(cfg *tls.Config, maxHeaderSize int64, timeout time.Duration, transportID string) *http.Client {
	d := &net.Dialer{
		Timeout:   timeout / 2,
		KeepAlive: 30 * time.Second,
	}
	t := &http.Transport{
		Proxy:           http.ProxyFromEnvironment,
		DialContext:     d.DialContext,
		TLSClientConfig: cfg,

		MaxIdleConns:           100,
		MaxIdleConnsPerHost:    100,
		TLSHandshakeTimeout:    timeout / 2,
		IdleConnTimeout:        90 * time.Second,
		ExpectContinueTimeout:  time.Second,
		MaxResponseHeaderBytes: maxHeaderSize,
	}
	t.Protocols = &http.Protocols{}
	t.Protocols.SetHTTP1(true)
	t.Protocols.SetHTTP2(cfg != nil)
	t.ForceAttemptHTTP2 = t.Protocols.HTTP2()

	reusable, ok := transportH2.Add(transportID, t)
	if ok {
		slog.Debug("initializing a new HTTP/1.1 and HTTP/2 client")
	}
	return &http.Client{Transport: reusable, Timeout: timeout}
}

// clientH3 constructs an HTTP/3 client. It reuses [http3.Transport] instances with
// the same TLS configuration, to optimize connection pooling. HTTP/3 requires TLS, so
// unlike [clientH2], this function returns nil if the provided TLS configuration is nil.
func clientH3(cfg *tls.Config, maxHeaderSize int64, timeout time.Duration, transportID string) *http.Client {
	if cfg == nil {
		slog.Warn("TLS configuration is required for HTTP/3 clients")
		return nil
	}

	t := &http3.Transport{
		TLSClientConfig:        cfg,
		MaxResponseHeaderBytes: int(min(maxHeaderSize, math.MaxInt)),
		QUICConfig: &quic.Config{
			HandshakeIdleTimeout: timeout / 2,
			MaxIdleTimeout:       30 * time.Second,
			KeepAlivePeriod:      15 * time.Second,
		},
		Logger: slog.Default(),
	}

	reusable, ok := transportH3.Add(transportID, t)
	if ok {
		slog.Debug("initializing a new HTTP/3 client")
	}
	return &http.Client{Transport: reusable, Timeout: timeout}
}

// requestWithRetries sends an HTTP request, retrying on transient errors based on [Collector.retries].
// In-flight execution is governed by execCtx, giving attempt 0 a [Collector.timeout] grace period to
// complete during shutdown. Subsequent retries (i > 0) also check schedCtx: if collector shutdown has
// already been initiated, retrying against a failing service is aborted to ensure prompt termination.
func (c *Collector) requestWithRetries(schedCtx, execCtx context.Context) *http.Response {
	resp := newErrorResponse(http.StatusServiceUnavailable)
	start := time.Now()

	for i := range c.retries.MaxAttempts {
		// Attempt 0 was already scheduled and runs under execCtx (up to [Collector.timeout]).
		// Retries (i > 0) abort if either execCtx or schedCtx (shutdown requested) is done.
		if execCtx.Err() != nil || (i > 0 && schedCtx.Err() != nil) {
			return resp
		}

		var retry bool
		resp, retry = c.requestOnce(execCtx)
		if resp != nil && resp.StatusCode <= MaxSuccessfulStatusCode {
			slog.Debug("HTTP request completed successfully",
				slog.String("name", c.Name), slog.Int("attempt", i+1), slog.String("status", resp.Status),
				slog.Time("start_time", start), slog.Duration("duration", time.Since(start)),
			)
			return resp
		}

		if !retry {
			break
		}

		c.retries.waitBeforeRetry(schedCtx, execCtx, nil, i)
	}
	return resp
}

// sendWithRetries sends an HTTP request, retrying on transient errors based on [Sender.retries].
// It runs asynchronously in a separate goroutine and does not return any error to the caller. The
// request parameters were either cloned or constructed by the caller in order to prevent data races.
func (s *Sender) sendWithRetries(ctx context.Context, u *url.URL, h http.Header, getBody getBodyFunc, size int64) {
	resp := newErrorResponse(http.StatusServiceUnavailable) //nolint:bodyclose // False positive despite bodyclose:handled.
	start := time.Now()

	for i := range s.retries.MaxAttempts {
		if ctx.Err() != nil || (i > 0 && s.lameDuck.Load()) {
			break
		}

		var retry bool
		resp, retry = s.sendOnce(ctx, u, h, getBody, size) //nolint:bodyclose // False positive despite bodyclose:handled.
		if resp != nil && resp.StatusCode <= MaxSuccessfulStatusCode {
			slog.Debug("HTTP request completed successfully",
				slog.String("name", s.Name), slog.Int("attempt", i+1), slog.String("status", resp.Status),
				slog.Time("start_time", start), slog.Duration("duration", time.Since(start)),
			)
			break
		}
		if !retry {
			break
		}

		// Interrupted by [Sender.closing] when [Sender.Close] is called.
		s.retries.waitBeforeRetry(ctx, ctx, s.closing, i)
	}

	if resp.StatusCode > MaxSuccessfulStatusCode {
		slog.Warn("failed to send HTTP request", slog.String("name", s.Name), slog.String("status", resp.Status),
			slog.Time("start_time", start), slog.Duration("duration", time.Since(start)),
		)
	}
}

// The returned [http.Response] is guaranteed to be non-nil, with a non-nil [http.Response.Body],
// and a usable [http.Response.Status] and [http.Response.StatusCode], even if the request failed.
// Note that the response body is stored in a local buffer, so reading it is guaranteed to be very
// fast and absolutely safe. If the request failed for any reason, the response body will be empty.
func (c *Collector) requestOnce(ctx context.Context) (*http.Response, bool) {
	var cancel context.CancelFunc
	if c.timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
	}
	if cancel != nil {
		defer cancel()
	}

	var body io.Reader
	if c.body != nil {
		body = bytes.NewReader(c.body) // Enables content-length & supports body reuse throughout redirects.
	}

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, c.method, c.url.String(), body)
	if err != nil {
		slog.Error("failed to construct HTTP request",
			slog.Any("error", err), slog.String("name", c.Name),
			slog.String("method", c.method), slog.String("url", c.url.Redacted()),
		)
		return newErrorResponse(http.StatusInternalServerError), false
	}

	req.Header = c.headers.Clone()
	if host := c.headers.Get("Host"); host != "" {
		req.Host = host // See [http.Request.Host] for details.
	}

	resp, err := c.client.Do(req)
	if err != nil {
		slog.Warn("failed to send HTTP request", slog.Any("error", err), slog.String("name", c.Name),
			slog.String("host", c.url.Host), slog.Time("start_time", start), slog.Duration("duration", time.Since(start)),
		)
		if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
			return newErrorResponse(http.StatusGatewayTimeout), true
		}
		return newErrorResponse(http.StatusBadGateway), true
	}

	if resp.StatusCode > MaxSuccessfulStatusCode {
		slog.Warn("HTTP server responded with error status", slog.String("name", c.Name), slog.String("status", resp.Status),
			slog.Time("start_time", start), slog.Duration("duration", time.Since(start)),
		)
	}

	resp = c.processResponse(resp)
	return resp, retryable(resp.StatusCode)
}

//bodyclose:handled
func (s *Sender) sendOnce(ctx context.Context, u *url.URL, h http.Header, fn getBodyFunc, cl int64) (*http.Response, bool) {
	var cancel context.CancelFunc
	if s.timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, s.timeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	go func() {
		select {
		case <-ctx.Done():
		case <-s.stop:
			cancel()
		}
	}()

	body, err := fn() // The function is guaranteed to be non-nil by [serializeData].
	if err != nil {
		slog.Error("cannot get reusable HTTP request body", slog.Any("error", err), slog.String("name", s.Name))
		return newErrorResponse(http.StatusInternalServerError), false
	}

	req, err := http.NewRequestWithContext(ctx, s.method, u.String(), body)
	if err != nil {
		slog.Error("failed to construct HTTP request",
			slog.Any("error", err), slog.String("name", s.Name),
			slog.String("method", s.method), slog.String("url", u.Redacted()),
		)
		return newErrorResponse(http.StatusInternalServerError), false
	}

	req.GetBody = fn // Enable body reuse throughout redirects.
	req.ContentLength = cl
	req.Header = h.Clone()

	// Customize the request's target address if the "Host" header is specified in the sender's own configured
	// headers (s.headers), but ignore the "Host" header in responses from collectors (h). Also note that "Host"
	// headers are automatically stripped (moved to [http.Request.Host]) from requests from receivers.
	if host := s.headers.Get("Host"); host != "" {
		req.Host = host // See [http.Request.Host] for details.
	}

	start := time.Now()
	resp, err := s.client.Do(req) //gosec:disable G704 // False positive.
	if err != nil {
		slog.Warn("failed to send HTTP request", slog.Any("error", err), slog.String("name", s.Name),
			slog.String("host", s.url.Host), slog.Time("start_time", start), slog.Duration("duration", time.Since(start)),
		)
		if errors.Is(err, context.Canceled) {
			return newErrorResponse(http.StatusServiceUnavailable), false
		}
		if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
			return newErrorResponse(http.StatusGatewayTimeout), true
		}
		return newErrorResponse(http.StatusBadGateway), true
	}

	// We don't care about responses from senders. Also, since Go 1.27,
	// [http.Response.Body] is drained automatically when closed (up to 256 KiB).
	_ = resp.Body.Close()

	if resp.StatusCode > MaxSuccessfulStatusCode {
		slog.Warn("HTTP server responded with error status", slog.String("name", s.Name), slog.String("status", resp.Status),
			slog.Time("start_time", start), slog.Duration("duration", time.Since(start)),
		)
	}

	return resp, retryable(resp.StatusCode)
}

// processResponse either reads or drains the given [http.Response.Body], based on [http.Response.StatusCode]
// and [http.Response.ContentLength]. Either way, it returns a copy of the [http.Response] with a guaranteed
// non-nil and locally-buffered [http.Response.Body] (although it may be empty based on the above).
//
// This decouples receiving data over an unreliable network from processing and sending it elsewhere.
// Either way, the original response body is consumed and closed, which is important for connection reuse.
//
//bodyclose:handled
func (c *Collector) processResponse(r *http.Response) *http.Response {
	// Since Go 1.27, [http.Response.Body] is drained automatically when closed (up to 256 KiB).
	defer r.Body.Close()

	if r.StatusCode > MaxSuccessfulStatusCode {
		return newErrorResponse(r.StatusCode)
	}
	if r.ContentLength > c.maxBodySize {
		slog.Warn("didn't read HTTP response body: too large", slog.String("name", c.Name),
			slog.Int64("content_length", r.ContentLength), slog.Int64("max_size", c.maxBodySize),
		)
		return newErrorResponse(http.StatusRequestEntityTooLarge)
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, c.maxBodySize+1))
	if err != nil {
		slog.Warn("failed to read HTTP response body", slog.Any("error", err), slog.String("name", c.Name))
		return newErrorResponse(http.StatusBadGateway)
	}
	if int64(len(body)) > c.maxBodySize {
		slog.Warn("HTTP response body is too large", slog.String("name", c.Name), slog.Int64("max_size", c.maxBodySize))
		return newErrorResponse(http.StatusRequestEntityTooLarge)
	}

	cpy := *r
	cpy.ContentLength = int64(len(body))
	cpy.Body = &reusableBody{Reader: bytes.NewReader(body), raw: body}
	return &cpy
}

//bodyclose:handled
func newErrorResponse(statusCode int) *http.Response {
	return &http.Response{
		Body:          http.NoBody,
		ContentLength: 0,
		StatusCode:    statusCode,
		Status:        fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode)),
	}
}

func retryable(statusCode int) bool {
	if statusCode < http.StatusBadRequest { // All 2xx and 3xx status codes.
		return false
	}
	switch statusCode {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return false
	case http.StatusMethodNotAllowed, http.StatusProxyAuthRequired, http.StatusGone, http.StatusLengthRequired:
		return false
	case http.StatusRequestEntityTooLarge, http.StatusRequestURITooLong, http.StatusUnsupportedMediaType:
		return false
	case http.StatusRequestHeaderFieldsTooLarge:
		return false
	case http.StatusNotImplemented:
		return false
	}
	return true
}

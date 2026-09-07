package http

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"github.com/daabr/versipellis/pkg/cache"
)

var (
	transportH2 = cache.NewFastCache[string, *http.Transport]()
	transportH3 = cache.NewFastCache[string, *http3.Transport]()

	maxBodyBytes int64 = 10 << 20 // 10 MiB.
)

// clientH2 constructs a client that supports both HTTP/1.1 and HTTP/2. It reuses [http.Transport]
// instances with the same TLS configuration, to optimize connection pooling. HTTP/2 requires TLS, so
// if the provided TLS configuration is nil, this function returns a client that only supports HTTP/1.1.
func clientH2(cfg *tls.Config, transportID string, timeout time.Duration) *http.Client {
	d := &net.Dialer{
		Timeout:   timeout / 2,
		KeepAlive: 30 * time.Second,
	}
	t := &http.Transport{
		Proxy:           http.ProxyFromEnvironment,
		DialContext:     d.DialContext,
		TLSClientConfig: cfg,

		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   100,
		TLSHandshakeTimeout:   timeout / 2,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	t.Protocols = &http.Protocols{}
	t.Protocols.SetHTTP1(true)
	t.Protocols.SetHTTP2(cfg != nil)
	t.ForceAttemptHTTP2 = t.Protocols.HTTP2()

	reusable, ok := transportH2.Add(transportID, t)
	if ok {
		slog.Debug("initializing a new HTTP/1.1 + HTTP/2 client")
	}
	return &http.Client{Transport: reusable, Timeout: timeout}
}

// clientH3 constructs an HTTP/3 client. It reuses [http3.Transport] instances with
// the same TLS configuration, to optimize connection pooling. HTTP/3 requires TLS, so
// unlike [clientH2], this function returns nil if the provided TLS configuration is nil.
func clientH3(cfg *tls.Config, transportID string, timeout time.Duration) *http.Client {
	if cfg == nil {
		slog.Warn("TLS configuration is required for HTTP/3 clients")
		return nil
	}

	t := &http3.Transport{
		TLSClientConfig: cfg,
		QUICConfig: &quic.Config{
			HandshakeIdleTimeout: 5 * time.Second,
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

func (c *Collector) requestWithRetries(ctx context.Context) *http.Response {
	resp := newErrorResponse(http.StatusGatewayTimeout)
	start := time.Now()

	for i := 0; i < c.retries+1 && ctx.Err() == nil; i++ {
		var retry bool
		resp, retry = c.requestOnce(ctx)
		if resp != nil && resp.StatusCode < http.StatusBadRequest {
			slog.Debug("HTTP request completed successfully",
				slog.String("name", c.Name), slog.Int("attempt", i+1), slog.String("status", resp.Status),
				slog.Time("start_time", start), slog.Duration("exec_duration", time.Since(start)),
			)
			return resp
		}

		if !retry {
			break
		}

		// Reminder: extend retries to a policy & make configurable in a separate PR.
	}
	return resp
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
		body = bytes.NewReader(c.body)
	}

	req, err := http.NewRequestWithContext(ctx, c.method, c.url.String(), body)
	if err != nil {
		slog.Error("failed to construct HTTP request",
			slog.Any("error", err), slog.String("name", c.Name),
			slog.String("method", c.method), slog.String("url", c.url.String()),
		)
		return newErrorResponse(http.StatusInternalServerError), false
	}

	req.Header = c.headers.Clone()
	if host := c.headers.Get("Host"); len(host) > 0 {
		req.Host = host // See [http.Request.Host] for details.
	}

	resp, err := c.client.Do(req)
	if err != nil {
		slog.Warn("failed to send HTTP request", slog.Any("error", err), slog.String("name", c.Name))
		return newErrorResponse(http.StatusBadGateway), true
	}

	if resp.StatusCode >= http.StatusBadRequest {
		slog.Warn("HTTP server responded with error status",
			slog.String("name", c.Name), slog.String("status", resp.Status),
		)
	}

	resp = c.processResponse(resp)
	return resp, retryable(resp.StatusCode)
}

// processResponse either reads or drains the given [http.Response.Body], based on [http.Response.StatusCode]
// and [http.Response.ContentLength]. Either way, it returns a copy of the [http.Response] with a guaranteed
// non-nil and locally-buffered [http.Response.Body] (although it may be empty based on the above).
//
// This decouples between the receiving of data over an unreliable network from processing and sending it elsewhere.
// Either way, the original response body is consumed and closed, which is important for connection reuse.
func (c *Collector) processResponse(r *http.Response) *http.Response {
	// Since Go 1.27, [http.Response.Body] is drained automatically when closed (up to 256 KiB).
	defer r.Body.Close()

	if r.StatusCode >= http.StatusBadRequest {
		return newErrorResponse(r.StatusCode)
	}
	if r.ContentLength > maxBodyBytes {
		slog.Warn("didn't read HTTP response body: too large",
			slog.String("name", c.Name), slog.Int64("content_length", r.ContentLength),
		)
		return newErrorResponse(http.StatusRequestEntityTooLarge)
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		slog.Warn("failed to read HTTP response body", slog.Any("error", err), slog.String("name", c.Name))
		return newErrorResponse(http.StatusBadGateway)
	}
	if int64(len(body)) > maxBodyBytes {
		slog.Warn("HTTP response body is too large", slog.String("name", c.Name), slog.Int64("max_size", maxBodyBytes))
		return newErrorResponse(http.StatusRequestEntityTooLarge)
	}

	cpy := *r
	cpy.ContentLength = int64(len(body))
	cpy.Body = io.NopCloser(bytes.NewReader(body))
	return &cpy
}

func newErrorResponse(statusCode int) *http.Response {
	return &http.Response{
		Body:          http.NoBody,
		ContentLength: 0,
		StatusCode:    statusCode,
		Status:        fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode)),
	}
}

func retryable(statusCode int) bool {
	if statusCode < http.StatusBadRequest {
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

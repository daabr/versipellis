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
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/net/http/httpguts"

	"github.com/daabr/versipellis/pkg/cache"
	"github.com/daabr/versipellis/pkg/config"
)

var (
	transportH2 = cache.NewFastCache[string, *http.Transport]()
	transportH3 = cache.NewFastCache[string, *http3.Transport]()
)

const (
	// MaxRedirects is the same as Go's default limit when [http.Client.CheckRedirect] is nil.
	MaxRedirects = 10

	// MaxSuccessfulStatusCode defines the highest status code considered successful.
	// Any status code above this value is considered an error, including 3xx
	// redirections that could not be handled automatically by the client.
	MaxSuccessfulStatusCode = 299
)

type (
	// Used when sending received requests, see [Sender.sendWithRetries].
	getBodyFunc func() (io.ReadCloser, error)

	// Used when sending collected responses, see [Collector.processResponse].
	bodyProvider interface {
		GetBody() (io.ReadCloser, error)
	}

	// Used when sending collected responses. See [Collector.processResponse] and [Sender.sendHTTPResponse].
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

// oauthClient constructs reusable clients for OAuth flows. Identity providers are usually unrelated to the
// component's own server, so this client ignores the component's TLS settings: it always verifies the server's
// certificate with the system's trusted CAs, it never presents an mTLS client certificate, and it supports only
// HTTP/1.1 and HTTP/2 (even when the component uses HTTP/3), like most identity providers. Its timeout is never
// 0 and at most [CloseTimeout], because token requests aren't covered by the cancellation of the component's
// own requests, so they must not stall the component or its shutdown indefinitely.
func oauthClient(minTLSVersion uint16, timeout time.Duration) *http.Client {
	if timeout <= 0 || timeout > CloseTimeout {
		timeout = CloseTimeout
	}
	// Can't collide with the transport IDs of components, which start with a hex checksum.
	transportID := fmt.Sprintf("oauth,%d,%d,%s", minTLSVersion, defaultMaxHeaderSize, timeout)
	client := clientH2(&tls.Config{MinVersion: minTLSVersion}, defaultMaxHeaderSize, timeout, transportID)
	client.CheckRedirect = oauthCheckRedirect
	return client
}

// oauthCheckRedirect is the [http.Client.CheckRedirect] function for [oauthClient]. POST requests to OAuth token
// endpoints include sensitive data in their bodies, so they can follow only 307 and 308 redirects. Furthermore,
// this function blocks scheme downgrades from HTTPS to HTTP and host changes. Errors specify the old and new
// URLs (with redacted secrets), so users can diagnose the issue and update URL configurations accordingly.
func oauthCheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= MaxRedirects {
		return fmt.Errorf("stopped after %d redirects", MaxRedirects)
	}

	method := via[0].Method
	status := 0
	if req.Response != nil {
		status = req.Response.StatusCode
	}
	switch {
	case method == http.MethodPost && status != http.StatusTemporaryRedirect && status != http.StatusPermanentRedirect:
		return fmt.Errorf("refusing to redirect OAuth POST with status %d to %s", status, req.URL.Redacted())
	case (via[0].URL.Scheme == "https" && req.URL.Scheme != "https") || !sameHost(req.URL.Host, via[0].URL.Host):
		return fmt.Errorf("refusing to redirect OAuth request from %s to %s", via[0].URL.Redacted(), req.URL.Redacted())
	default:
		return nil
	}
}

// sameHost compares two URL hosts (including ports). ASCII hosts are case-insensitive, but anything else must match
// exactly, because Unicode case folding equates distinct lookalike domains (e.g., the Kelvin sign and "k").
func sameHost(a, b string) bool {
	if isASCII(a) && isASCII(b) {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// checkRedirect returns an [http.Client.CheckRedirect] function that removes the configured authentication header
// from redirected requests, in the same way that [http.Client] handles the "Authorization" header and cookies.
//
// This function is not as restrictive as [oauthCheckRedirect] with the properties of the redirection URL
// because general requests may require more flexibility than what is considered as safe for OAuth flows.
// Furthermore, if our process or the original destination are compromised to enable rogue redirection
// then the attackers already have our current credentials anyway.
//
// Nevertheless, it still enforces extra precaution compared to Go's default behavior by removing the configured
// authentication header if the request's scheme is downgraded from encrypted HTTPS to unencrypted HTTP, because
// such redirects are usually caused by misconfigured TLS-terminating proxies rather than compromised servers.
func checkRedirect(authHeader string) func(*http.Request, []*http.Request) error {
	if authHeader == "" {
		return nil // Let Go use its own [http.Client].defaultCheckRedirect().
	}

	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= MaxRedirects {
			return fmt.Errorf("stopped after %d redirects", MaxRedirects)
		}

		// Once a redirect removes the header, it stays removed in all the following redirects,
		// even back to the original host, in the same way as the "sticky" logic in [http.Client].
		strip := func(r *http.Request) bool { return shouldntCopyHeaderOnRedirect(via[0].URL, r.URL) }
		if strip(req) || slices.ContainsFunc(via[1:], strip) {
			req.Header.Del(authHeader)
		}
		return nil
	}
}

// shouldntCopyHeaderOnRedirect is the inverse of the unexported function "shouldCopyHeaderOnRedirect" in
// [http.Client]. It also returns true on a URL scheme downgrade from encrypted HTTPS to unencrypted HTTP.
func shouldntCopyHeaderOnRedirect(initial, dest *url.URL) bool {
	if initial.Scheme == config.URLSchemeHTTPS && dest.Scheme == config.URLSchemeHTTP {
		return true
	}
	if initial.Host == dest.Host {
		return false
	}

	ihost, err1 := httpguts.PunycodeHostPort(initial.Hostname())
	dhost, err2 := httpguts.PunycodeHostPort(dest.Hostname())
	if err1 != nil || err2 != nil {
		return true
	}
	// If the [httpguts.PunycodeHostPort] error is nil, the output is guaranteed to be an ASCII string.
	return !isDomainOrSubdomain(strings.ToLower(dhost), strings.ToLower(ihost))
}

// isDomainOrSubdomain is copied from [http.Client]. Both domains must already be in canonical form.
func isDomainOrSubdomain(sub, parent string) bool {
	if sub == parent {
		return true
	}
	// If sub contains a ':' or '%', it's probably an IPv6 address (and is definitely not a hostname).
	// Don't check the suffix in this case, to avoid matching the contents of a IPv6 zone.
	// For example, "::1%.www.example.com" is not a subdomain of "www.example.com".
	if strings.ContainsAny(sub, ":%") {
		return false
	}
	// If sub is "foo.example.com" and parent is "example.com",
	// that means sub must end in "."+parent. Do it without allocating.
	return strings.HasSuffix(sub, parent) && sub[len(sub)-len(parent)-1] == '.'
}

func isASCII(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool { return r >= utf8.RuneSelf })
}

// requestWithRetries sends an HTTP request, retrying on transient errors based on [Collector.retries].
// Ongoing execution is governed by execCtx, giving attempt 0 a [Collector.timeout] grace period to complete
// even during shutdown. However, if collector shutdown has been initiated (i.e. schedCtx is canceled),
// further retrying is aborted, and so is waiting between attempts, to ensure prompt termination.
func (c *Collector) requestWithRetries(schedCtx, execCtx context.Context) *http.Response {
	resp := newErrorResponse(http.StatusServiceUnavailable)
	start := time.Now()

	for i := range c.retries.MaxAttempts {
		if (i > 0 && schedCtx.Err() != nil) || execCtx.Err() != nil {
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
		c.retries.waitBeforeRetry(execCtx, schedCtx.Done(), i)
	}
	return resp
}

// sendWithRetries sends an HTTP request, retrying on transient errors based on [Sender.retries]. It runs
// asynchronously in a separate goroutine and does not return any error to the caller. The request parameters
// were either cloned or constructed by the caller in order to prevent data races. The reject function is called
// if the request wasn't successful: a non-retryable error, too many retries, or preemption due to a shutdown.
func (s *Sender) sendWithRetries(
	ctx context.Context, u *url.URL, h http.Header, getBody getBodyFunc, size int64, reject func() string,
) {
	resp := newErrorResponse(http.StatusServiceUnavailable)
	start := time.Now()

	for i := range s.retries.MaxAttempts {
		if ctx.Err() != nil || (i > 0 && s.state.IsClosing()) {
			break
		}

		var retry bool
		resp, retry = s.sendOnce(ctx, u, h, getBody, size)
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
		s.retries.waitBeforeRetry(ctx, s.state.Closing, i)
	}

	if resp.StatusCode <= MaxSuccessfulStatusCode {
		return
	}
	sender := reject()
	slog.Warn("failed to send HTTP request", slog.String("name", s.Name), slog.String("status", resp.Status),
		slog.String("fallback", sender), slog.Time("start_time", start), slog.Duration("duration", time.Since(start)),
	)
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

	if req.Header, err = c.auth.SetHeader(ctx, c.headers, c.Name); err != nil {
		return authErrorResponse(err)
	}
	// Customize the request's target address if the "Host" header is
	// explicitly specified in the collector's own configured headers.
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
			// Request timed out.
		case <-s.state.Closed:
			cancel() // Sender shutting down.
		}
	}()

	// Set the authentication header before body, so that failing to obtain an OAuth access token won't leave an
	// unclosed body behind (nothing has been sent yet at this point, so the request is always safe to retry).
	hdr, err := s.auth.SetHeader(ctx, h, s.Name)
	if err != nil {
		return authErrorResponse(err)
	}

	body, err := fn() // The function is guaranteed to be non-nil.
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
	req.Header = hdr
	// Customize the request's target address if the "Host" header is explicitly specified
	// in the sender's own configured headers. Also note that "Host" headers are automatically
	// stripped in receiver-handled requests (moved to their [http.Request.Host]).
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

// authErrorResponse converts an [auth.Config.SetHeader] error into an error response and a retry decision
// in [Collector.requestOnce] and [Sender.sendOnce], in the same way they handle [http.Client.Do] errors.
func authErrorResponse(err error) (*http.Response, bool) {
	switch {
	case errors.Is(err, context.Canceled):
		return newErrorResponse(http.StatusServiceUnavailable), false
	case errors.Is(err, context.DeadlineExceeded):
		return newErrorResponse(http.StatusGatewayTimeout), true
	default:
		return newErrorResponse(http.StatusBadGateway), true
	}
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
	case http.StatusUnprocessableEntity, http.StatusRequestHeaderFieldsTooLarge, http.StatusNotImplemented:
		return false
	}
	return true
}

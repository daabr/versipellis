package http

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/daabr/versipellis/pkg/config"
	"github.com/daabr/versipellis/pkg/dest"
	"github.com/daabr/versipellis/pkg/flow"
)

const (
	contentTypeHeader = "Content-Type"
)

var defaultBatchLimits = flow.Limits{}

const (
	defaultConcurrencyLimit int64 = 100
	maxConcurrencyLimit     int64 = 200
)

// Sender contains all the configuration and state details for sending HTTP requests.
type Sender struct {
	Name string
	Type string

	url     *url.URL
	method  string
	headers http.Header
	timeout time.Duration
	tls     *tls.Config
	retries *retries

	transportID string
	client      *http.Client
	batch       *flow.Batcher[map[string]any]

	slots      chan struct{} // See [Sender.sendWithConcurrencyLimit].
	inProgress sync.WaitGroup
	lameDuck   atomic.Bool
	closeMu    sync.RWMutex
	closeOnce  sync.Once
	closing    chan struct{} // Removes delays between retries.
	stop       chan struct{} // Stops requests forcefully.
}

// NewSender creates a new [config.Sender] with the provided configuration, which was read
// from a TOML file. It checks the details and returns an error if any of them is invalid.
func NewSender(cfg map[string]any, name, baseType string) (*Sender, error) {
	s := &Sender{Name: name, Type: baseType, closing: make(chan struct{}), stop: make(chan struct{})}
	var err error

	if s.url, err = parseURL(config.Value(cfg, "url", ""), s.Type); err != nil {
		return nil, err
	}
	if s.method, err = parseMethod(config.Value(cfg, "method", http.MethodPost), "sending"); err != nil {
		return nil, err
	}
	if s.method == http.MethodGet {
		return nil, fmt.Errorf("HTTP method %q not supported for sending data", s.method)
	}
	if err := parseQuery(s.url, cfg["query"], "sender"); err != nil {
		return nil, err
	}
	if s.headers, err = parseHeaders(cfg["headers"], "sender"); err != nil {
		return nil, err
	}
	if s.retries, err = parseRetries(cfg["retries"], s.method, s.Name); err != nil {
		return nil, err
	}

	if s.tls, s.transportID, err = loadClientTLSConfig(cfg["tls"], s.Type); err != nil {
		return nil, err
	}
	if s.url.Scheme == httpScheme && cfg["tls"] != nil {
		if m, ok := cfg["tls"].(map[string]any); ok && len(m) > 0 {
			slog.Warn("TLS config details are ineffective because URL scheme is unencrypted HTTP",
				slog.String("name", s.Name), slog.String("url", s.url.Redacted()),
			)
		}
	}
	if s.timeout, err = time.ParseDuration(config.Value(cfg, "timeout", defaultRequestTimeout.String())); err != nil {
		return nil, fmt.Errorf("invalid timeout duration: %w", err)
	}
	// For us, 0 is the same as negative values, but not in Go.
	// This normalization simplifies HTTP client construction.
	s.timeout = max(s.timeout, 0)
	// HTTP transport configuration is affected by TLS, maximum header size, and timeout
	// settings, so this prevents clients with different configurations from sharing transports.
	s.transportID = fmt.Sprintf("%s,%d,%s", s.transportID, defaultMaxHeaderSize, s.timeout)

	switch s.Type {
	case config.SenderTypeHTTP:
		s.client = clientH2(s.tls.Clone(), defaultMaxHeaderSize, s.timeout, s.transportID)
	case config.SenderTypeHTTP3:
		s.client = clientH3(s.tls.Clone(), defaultMaxHeaderSize, s.timeout, s.transportID)
	default:
		return nil, fmt.Errorf("unexpected sender type %q", s.Type)
	}

	concurrency := config.Value(cfg, "concurrency_limit", defaultConcurrencyLimit)
	if concurrency < 1 {
		slog.Warn("normalizing sender concurrency limit", slog.String("name", s.Name),
			slog.Int64("below_min", concurrency), slog.Int64("new_max_value", maxConcurrencyLimit),
		)
		concurrency = maxConcurrencyLimit
	}
	concurrencyLimit := config.BoundedInt(concurrency, 1, maxConcurrencyLimit, s.Name, "sender concurrency limit")
	s.slots = make(chan struct{}, concurrencyLimit)

	var limits flow.Limits
	if limits, err = config.BatchLimits(cfg["batch"], s.Name, defaultBatchLimits); err != nil {
		return nil, fmt.Errorf("HTTP batching limits: %w", err)
	}
	opts := new(flow.Options[map[string]any]{Guard: s.guard})
	if s.batch, err = flow.NewBatcher(limits, s.sendStructured, opts); err != nil {
		return nil, fmt.Errorf("HTTP batching: %w", err)
	}

	return s, nil
}

// guard synchronizes dispatches of delayed batches (see [flow.Options.Guard]) with [Sender.Close].
func (s *Sender) guard(flush func()) {
	s.closeMu.RLock()
	defer s.closeMu.RUnlock()

	if !s.lameDuck.Load() {
		flush() // Otherwise, Close has already taken the pending batch.
	}
}

// Send any data as an HTTP request to the configured [Sender], retrying on transient errors
// based on [Sender.retries]. Byte blobs are sent as raw payloads, full [http.Request]s and
// [http.Response]s are proxied with their headers and body preserved. Other data types are
// encoded, if possible. This method supports batching based on user-configured [flow.Limits].
//
// Concurrency & lifecycle: the entire chunk is either accepted or rejected, because this method holds a read lock
// of [Sender.closeMu] while it checks [Sender.lameDuck] and registers work in [Sender.inProgress], so [Sender.Close]
// can't start waiting in the middle. Therefore, nothing under this lock may block, and the actual I/O is asynchronous.
func (s *Sender) Send(ctx context.Context, data flow.Chunk) {
	if flow.IsEmpty(data) {
		return
	}

	s.closeMu.RLock()
	defer s.closeMu.RUnlock()

	if s.lameDuck.Load() {
		slog.Warn("cannot send HTTP request: shutdown in progress",
			slog.String("name", s.Name), slog.Int("batch_size", data.Len()),
		)
		dest.Discard.Send(ctx, data)
		return
	}

	ctx = context.WithoutCancel(ctx)
	switch chunk := data.(type) {
	case flow.Structured:
		s.batch.AddChunk(ctx, chunk) // May dispatch a full batch synchronously, still under the read lock.

	case flow.Blobs:
		for _, blob := range chunk {
			s.sendBlob(ctx, blob)
		}
	case flow.HTTPRequests:
		for _, req := range chunk {
			s.sendHTTPRequest(ctx, req)
		}
	case flow.HTTPResponses:
		for _, resp := range chunk {
			s.sendHTTPResponse(ctx, resp)
		}
	default:
		slog.Error("unhandled data type in HTTP sender", slog.String("name", s.Name),
			slog.String("data_type", fmt.Sprintf("%T", chunk)))
	}
}

// Close waits (up to [CloseTimeout] or a context deadline) for requests that are in progress to finish, and
// rejects new ones. If pending requests don't finish in time, the sender will forcefully close their connections.
func (s *Sender) Close(ctx context.Context) {
	s.closeOnce.Do(func() {
		s.closeMu.Lock()
		s.lameDuck.Store(true)
		s.closeMu.Unlock()

		timeout := s.timeout
		if timeout <= 0 || timeout > CloseTimeout {
			timeout = CloseTimeout // Ensure the timeout is within acceptable bounds.
		}
		shutdownCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		done := make(chan struct{})
		go func() {
			close(s.closing)
			defer close(done)

			s.batch.Flush(ctx) // Forceful shutdowns abort its requests too, via [Sender.stop].
			s.inProgress.Wait()
		}()

		select {
		case <-done:
			// All done.
		case <-shutdownCtx.Done():
			slog.Warn("closing HTTP sender forcefully", slog.Any("error", shutdownCtx.Err()),
				slog.String("name", s.Name), slog.Duration("timeout", timeout),
			)
			// Irrelevant for the done channel case: it's closed after [Sender.inProgress.Wait]
			// returns, i.e. nothing is currently being sent that needs to be aborted, and new
			// [Sender.Send] calls cannot start because [Sender.lameDuck] is already true.
			close(s.stop)
		}
	})
}

// sendWithConcurrencyLimit registers a new outgoing request as a goroutine in [Sender.inProgress], to be sent
// if/when a slot is available in [Sender.slots], so the number of concurrent active requests (including preparation
// and retries) is bounded, but the caller never blocks: it may be holding a read lock of [Sender.closeMu]. Waiting
// goroutines are cheap, and their payloads were already in memory anyway.
//
// If a slot is available immediately, it's acquired synchronously, so the request is sent even if [Sender.Close]
// begins before its goroutine starts (e.g., batches that Close flushes). Only requests that are blocked by the
// limit wait for a slot, and if [Sender.Close] begins first, they're dropped instead of delaying the shutdown.
func (s *Sender) sendWithConcurrencyLimit(fn func()) {
	select {
	case s.slots <- struct{}{}: // Non-blocking, so it's safe under the read lock.
		s.inProgress.Go(func() {
			defer func() { <-s.slots }()
			fn()
		})
		return
	default:
	}

	s.inProgress.Go(func() {
		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
		case <-s.closing:
		}

		// Check again, because if a slot was released during [Sender.Close], the choice above was random.
		select {
		case <-s.closing:
			slog.Warn("HTTP request dropped while waiting for concurrency slot", slog.String("name", s.Name))
		default:
			fn()
		}
	})
}

func (s *Sender) sendBlob(ctx context.Context, body []byte) {
	if len(body) == 0 {
		slog.Error("cannot send HTTP request with no payload",
			slog.String("name", s.Name), slog.String("format", "raw_bytes"),
		)
		return
	}

	outHdr := s.headers.Clone()
	if outHdr.Get(contentTypeHeader) == "" {
		outHdr.Set(contentTypeHeader, http.DetectContentType(body))
	}

	s.sendWithConcurrencyLimit(func() {
		s.sendWithRetries(ctx, s.url.Clone(), outHdr, retryableBody(body), int64(len(body)))
	})
}

// sendStructured is called only via [Sender.batch]: from [Sender.Send] and [Sender.guard], which hold a read lock
// of [Sender.closeMu] after checking [Sender.lameDuck], and from [Sender.Close] before it waits for requests that are
// in progress. Either way, it may register new work without checking [Sender.lameDuck] again, and it must not acquire
// a recursive read lock, which may deadlock with Close. Encoding happens asynchronously, so it doesn't hold the lock,
// and only after acquiring a concurrency slot, so the number of encoded payloads in memory is bounded too.
func (s *Sender) sendStructured(ctx context.Context, data []map[string]any) {
	s.sendWithConcurrencyLimit(func() {
		body, err := flow.FormatNDJSON.Encode(data)
		if err != nil {
			slog.Error("failed to encode HTTP request payload", slog.Any("error", err),
				slog.String("name", s.Name), slog.Int("batch_size", len(data)), slog.String("format", "ndjson"),
			)
			return
		}

		outHdr := s.headers.Clone()
		if outHdr.Get(contentTypeHeader) == "" {
			outHdr.Set(contentTypeHeader, flow.FormatNDJSON.ContentType())
		}

		s.sendWithRetries(ctx, s.url.Clone(), outHdr, retryableBody(body), int64(len(body)))
	})
}

func (s *Sender) sendHTTPRequest(ctx context.Context, req *http.Request) {
	if req == nil {
		slog.Error("cannot send empty HTTP request", slog.String("name", s.Name))
		return
	}

	outURL := s.url.Clone()
	copyQuery(req.URL, outURL)

	outHdr := s.headers.Clone()
	copyHeaders(req.Header, outHdr, true)

	if req.Body == nil && req.GetBody == nil {
		s.sendWithConcurrencyLimit(func() {
			s.sendWithRetries(ctx, outURL, outHdr, retryableBody(nil), 0)
		})
		return
	}
	if req.GetBody != nil { // Optimization to avoid duplicate memory allocations for the request body.
		if req.Body != nil {
			_ = req.Body.Close()
		}
		s.sendWithConcurrencyLimit(func() {
			s.sendWithRetries(ctx, outURL, outHdr, req.GetBody, req.ContentLength)
		})
		return
	}
	// Requests from receivers always have a GetBody function, so this is only a fallback. Either way, bodies are
	// buffered in memory before they're passed to [Sender.Send], so reading them under its read lock doesn't block.
	defer req.Body.Close() // Redundant but harmless (see [http.Request.Body] for server requests).
	b, err := io.ReadAll(req.Body)
	if err != nil {
		slog.Error("failed to read HTTP request body", slog.Any("error", err), slog.String("name", s.Name))
		return
	}

	s.sendWithConcurrencyLimit(func() {
		s.sendWithRetries(ctx, outURL, outHdr, retryableBody(b), int64(len(b)))
	})
}

func (s *Sender) sendHTTPResponse(ctx context.Context, resp *http.Response) {
	if resp == nil {
		slog.Error("cannot send empty HTTP response", slog.String("name", s.Name))
		return
	}
	if resp.Body == nil {
		// Received responses without content have no value, so we don't relay them,
		// in contrast to received requests without a body which may still carry meaning.
		// Regardless, it's not an error that needs to be reported, unlike a nil object.
		return
	}

	outHdr := s.headers.Clone()
	copyHeaders(resp.Header, outHdr, false)

	if b, ok := resp.Body.(bodyProvider); ok {
		// Memory optimization, due to the same reason as in [Sender.sendHTTPRequest],
		// working around the fact that [http.Response] doesn't have a GetBody() method.
		s.sendWithConcurrencyLimit(func() {
			s.sendWithRetries(ctx, s.url.Clone(), outHdr, b.GetBody, resp.ContentLength)
		})
		return
	}
	// Responses from collectors always have a reusable body, so this is only a fallback. Either way, bodies are
	// buffered in memory before they're passed to [Sender.Send], so reading them under its read lock doesn't block.
	defer resp.Body.Close() // It's important to call this before [io.ReadAll], but only in this method.
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Error("failed to read HTTP response body", slog.Any("error", err), slog.String("name", s.Name))
		return
	}

	s.sendWithConcurrencyLimit(func() {
		s.sendWithRetries(ctx, s.url.Clone(), outHdr, retryableBody(b), int64(len(b)))
	})
}

func retryableBody(b []byte) getBodyFunc {
	return func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(b)), nil
	}
}

// copyQuery copies query parameters from the source URL to
// the destination URL, without overwriting existing keys.
func copyQuery(srcURL, dstURL *url.URL) {
	if srcURL == nil || dstURL == nil || srcURL.RawQuery == "" {
		return
	}

	if dstURL.RawQuery == "" {
		dstURL.RawQuery = srcURL.RawQuery
		return
	}

	src, dst := srcURL.Query(), dstURL.Query()
	for k := range src {
		if _, found := dst[k]; !found {
			dst[k] = src[k]
		}
	}

	dstURL.RawQuery = dst.Encode()
}

// copyHeaders copies safe header key-value pairs from the source
// to the destination, without overwriting existing keys.
func copyHeaders(src, dst http.Header, request bool) {
	if len(src) == 0 {
		return
	}

	for _, k := range safeHeaders(src, request) {
		canon := http.CanonicalHeaderKey(k)
		if _, found := dst[canon]; !found {
			dst[canon] = slices.Clone(src[k])
		}
	}
}

// See https://datatracker.ietf.org/doc/html/rfc2616#section-13.5.1
// and https://datatracker.ietf.org/doc/html/rfc6797#section-6.1.
var filteredHeaders = map[string]struct{}{
	"Connection":                {},
	"Keep-Alive":                {},
	"Proxy-Authenticate":        {},
	"Proxy-Authorization":       {},
	"Proxy-Connection":          {},
	"Te":                        {},
	"Trailer":                   {},
	"Transfer-Encoding":         {},
	"Upgrade":                   {},
	"Strict-Transport-Security": {},
}

// Reminder: revisit this when we support additional non-default encoding types.
var filteredRequestHeaders = map[string]struct{}{
	"Content-Length": {},
}

// Reminder: revisit this when we support additional non-default encoding types.
var filteredResponseHeaders = map[string]struct{}{
	"Content-Encoding": {},
	"Content-Length":   {},
	"Set-Cookie":       {},
}

// Forwarding a received request/response's [http.Request.Header] as-is
// may propagate irrelevant hop-by-hop headers, which would lead to bugs.
// See https://nathandavison.com/blog/abusing-http-hop-by-hop-request-headers
// and https://cs.opensource.google/go/go/+/master:src/net/http/httputil/reverseproxy.go.
// We call this function only before sending over HTTP for better debugging through other senders.
func safeHeaders(headers http.Header, request bool) []string {
	if len(headers) == 0 {
		return nil
	}

	// https://datatracker.ietf.org/doc/html/rfc9110#name-connection
	var connHeaders map[string]struct{}
	for _, vs := range headers.Values("Connection") {
		for v := range strings.SplitSeq(vs, ",") {
			if connHeaders == nil {
				connHeaders = make(map[string]struct{})
			}
			connHeaders[http.CanonicalHeaderKey(strings.TrimSpace(v))] = struct{}{}
		}
	}

	var safe []string
	for k := range headers {
		canon := http.CanonicalHeaderKey(k)
		if _, found := filteredHeaders[canon]; found {
			continue
		}
		if _, found := connHeaders[canon]; found {
			continue
		}
		if request {
			if _, found := filteredRequestHeaders[canon]; found {
				continue
			}
		} else {
			if _, found := filteredResponseHeaders[canon]; found {
				continue
			}
		}
		safe = append(safe, k)
	}

	return safe
}

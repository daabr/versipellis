package http

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json/jsontext"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/daabr/versipellis/pkg/codec"
	"github.com/daabr/versipellis/pkg/config"
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
	Name     string
	Type     string
	fallback config.SendFunc

	url     *url.URL
	method  string
	headers http.Header
	timeout time.Duration
	format  config.Format
	tls     *tls.Config
	retries *retries

	transportID string
	client      *http.Client
	state       config.State

	// See [initBatchers].
	jsonBatches batchersMap[jsontext.Value]
	rowBatches  batchersMap[flow.Row]

	slots chan struct{}
}

// NewSender creates a new [config.Sender] with the provided configuration, which was read
// from a TOML file. It checks the details and returns an error if any of them is invalid.
func NewSender(cfg map[string]any, name, baseType string, dlq config.Sender) (*Sender, error) {
	s := &Sender{Name: name, Type: baseType}
	s.state.Start()
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
	if s.format, err = codec.ParseStructuredFormat(cfg); err != nil {
		return nil, fmt.Errorf("format setting: %w", err)
	}
	if dlq != nil {
		s.fallback = dlq.Send
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

	var limits flow.Limits
	if limits, err = config.BatchLimits(cfg["batch"], s.Name, defaultBatchLimits); err != nil {
		return nil, fmt.Errorf("HTTP batching limits: %w", err)
	}
	// Both kinds of batchers have the same limits and options, so if one fails, the other would fail too.
	if s.jsonBatches, err = initBatchers(s, limits, codec.Formatter.EncodeJSON); err == nil {
		s.rowBatches, err = initBatchers(s, limits, codec.Formatter.EncodeRows)
	}
	if err != nil {
		return nil, err
	}

	return s, nil
}

type (
	batchersMap[T flow.StructuredItem]   = map[config.Format]*flow.Batcher[T]
	formatterFunc[T flow.StructuredItem] = func(codec.Formatter, flow.ChunkOf[T]) ([]byte, error)
)

// initBatchers creates batchers for a single kind of structured data (e.g., [flow.JSON] or [flow.Rows]),
// which encode their batches with the given function (e.g., [codec.Formatter.EncodeJSON]) and send them.
// Data in different formats can't be joined in a single payload, so each structured format needs to have its
// own batcher (while non-structured data is sent directly without batching). However, an explicit sender format
// applies to all the data and overrides upstream format settings, so in that case only one batcher is needed.
func initBatchers[T flow.StructuredItem](s *Sender, l flow.Limits, encode formatterFunc[T]) (batchersMap[T], error) {
	opts := new(flow.Options[T]{Guard: s.state.IfNotClosing})
	batchers := make(batchersMap[T])
	for format, fmtr := range codec.StructuredFormats() {
		if s.format != codec.FormatDefault && format != s.format {
			continue
		}

		dispatch := func(ctx context.Context, items []T) {
			chunk := flow.ChunkOf[T](items)
			s.sendStructured(ctx, chunk, format, fmtr.ContentType(), func() ([]byte, error) {
				return encode(fmtr, chunk)
			})
		}
		b, err := flow.NewBatcher(l, dispatch, opts)
		if err != nil {
			return nil, fmt.Errorf("HTTP batching (%s format): %w", format, err)
		}
		batchers[format] = b
	}

	return batchers, nil
}

// outputFormat returns the format in which the sender should send the given data, which applies to
// its batching, encoding, and rejections alike. For structured data, it's the sender's own explicit
// format, or the given upstream format, or NDJSON ([NewSender] ensures that its own format is either
// default or structured). Blobs keep the upstream format (e.g., an encoded payload's format),
// but they're opaque bytes by definition, so the default format becomes [codec.FormatRaw].
// Other data (i.e. HTTP messages) keeps the upstream format, because it's sent as-is.
func (s *Sender) outputFormat(data flow.Chunk, upstream config.Format) config.Format {
	switch data.(type) {
	case flow.Blobs:
		if upstream == codec.FormatDefault {
			return codec.FormatRaw
		}
		return upstream
	case flow.JSON, flow.Rows:
		if s.format != codec.FormatDefault {
			return s.format
		}
		format, _ := codec.Resolve(upstream, codec.FormatNDJSON)
		return format
	default:
		return upstream
	}
}

// Send any data as an HTTP request to the configured [Sender], retrying on transient errors based on [Sender.retries].
// Byte blobs are sent as raw payloads, full [http.Request]s and [http.Response]s are proxied with their headers and
// body preserved. JSON objects and rows are encoded in the sender's configured format, or the source's format, or
// as NDJSON - in this order of precedence. This method supports batching based on user-configured [flow.Limits].
//
// Concurrency & lifecycle: the entire chunk is either accepted or rejected, because this method holds a read lock
// of [config.State.CloseMu] while it checks the closing state and registers work, so [Sender.Close] can't start
// waiting in the middle. Therefore, nothing under this lock may block, and the actual I/O is asynchronous.
func (s *Sender) Send(ctx context.Context, data flow.Chunk, format config.Format) {
	if flow.IsEmpty(data) {
		return
	}

	format = s.outputFormat(data, format)
	if s.state.IfClosingElseLock() {
		sender := deadLetter(ctx, s.fallback, data, format)
		slog.Warn("cannot send HTTP request: shutdown in progress", slog.String("name", s.Name),
			slog.Int("batch_size", data.Len()), slog.String("fallback", sender),
		)
		return
	}
	defer s.state.CloseMu.RUnlock()

	ctx = context.WithoutCancel(ctx)
	switch chunk := data.(type) {
	// Structured data may dispatch a full batch synchronously, still under the read lock.
	case flow.JSON:
		s.jsonBatches[format].AddChunk(ctx, chunk)
	case flow.Rows:
		s.rowBatches[format].AddChunk(ctx, chunk)

	case flow.Blobs:
		for _, blob := range chunk {
			s.sendBlob(ctx, blob, format)
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

// Close waits (up to [CloseTimeout] + [AbortTimeout]) for ongoing requests to finish, and rejects new ones. If pending
// requests don't finish in time, the sender will forcefully close their connections, and divert them to the DLQ.
func (s *Sender) Close(ctx context.Context) {
	if !s.state.IsStarted() {
		return
	}

	s.state.CloseOnce.Do(func() {
		s.state.BeginClose()

		done := make(chan struct{})
		go func() {
			defer close(done)

			// Forceful shutdowns abort the requests of these batches too, via [config.State.FinishClose].
			for _, b := range s.jsonBatches {
				b.Flush(ctx)
			}
			for _, b := range s.rowBatches {
				b.Flush(ctx)
			}
			s.state.InProgress.Wait()
		}()

		timeout := s.timeout
		if timeout <= 0 || timeout > CloseTimeout {
			timeout = CloseTimeout // Ensure the timeout is within acceptable bounds.
		}

		select {
		case <-done:
			// All done.
		case <-time.After(timeout):
			slog.Warn("closing HTTP sender forcefully", slog.String("name", s.Name), slog.Duration("timeout", timeout))
		}

		// Mark the sender as closed. After a timeout, this is also a signal for straggler requests to abort.
		s.state.FinishClose()

		// Wait for aborted workers, if there are any, to divert their data to the DLQ before they actually stop.
		// The DLQ starts closing only after all other senders are closed, in order to support this.
		select {
		case <-done:
			// Immediate if the previous select block didn't encounter a timeout,
			// so no need to nest this select block inside the previous one.
		case <-time.After(AbortTimeout):
			slog.Error("aborted HTTP sender didn't stop immediately", slog.String("name", s.Name))
		}
	})
}

// sendWithConcurrencyLimit registers a new outgoing request as a goroutine in [config.State.InProgress],
// to be sent if/when a slot is available in [Sender.slots], so the number of concurrent active requests
// (including preparation and retries) is bounded, but the caller never blocks: it may be holding a read lock
// of [config.State.CloseMu]. Waiting goroutines are cheap, and their payloads were already in memory anyway.
//
// If a slot is available immediately, it's acquired synchronously, so the request is sent even if [Sender.Close]
// begins before its goroutine starts (e.g., batches that Close flushes). Only requests that are blocked by the limit
// wait for a slot. If [Sender.Close] begins first, they're rejected (sent to the DLQ) so as not to delay the shutdown.
func (s *Sender) sendWithConcurrencyLimit(send func(), reject func() string) {
	select {
	case s.slots <- struct{}{}: // Non-blocking, so it's safe under the read lock.
		s.state.InProgress.Go(func() {
			defer func() { <-s.slots }()
			send()
		})
		return
	default:
	}

	s.state.InProgress.Go(func() {
		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
		case <-s.state.Closing:
		}

		// Check again, because if a slot was released during [Sender.Close], the choice above was random.
		select {
		case <-s.state.Closing:
			sender := reject()
			slog.Warn("HTTP request not sent: shutdown began while waiting for concurrency slot",
				slog.String("name", s.Name), slog.String("fallback", sender),
			)
		default:
			send()
		}
	})
}

func (s *Sender) sendBlob(ctx context.Context, body []byte, format config.Format) {
	if len(body) == 0 { // Upstream invariant violation: shouldn't have been sent at all.
		slog.Error("cannot send HTTP request with no payload",
			slog.String("name", s.Name), slog.String("format", "raw_bytes"),
		)
		return
	}

	outHdr := s.headers.Clone()
	if outHdr.Get(contentTypeHeader) == "" {
		outHdr.Set(contentTypeHeader, http.DetectContentType(body))
	}

	reject := func() string { return deadLetter(ctx, s.fallback, flow.Blobs{body}, format) }
	s.sendWithConcurrencyLimit(func() {
		s.sendWithRetries(ctx, s.url.Clone(), outHdr, retryableBody(body), int64(len(body)), reject)
	}, reject)
}

// sendStructured is called only via [initBatchers]: from [Sender.Send] and the batchers' guard
// ([config.State.IfNotClosing]), which hold a read lock of [config.State.CloseMu] after checking
// [config.State.IsClosing], and [Sender.Close] before it waits for ongoing requests to finish. Either
// way, it may register new work without checking [config.State.IsClosing] again, and it must not acquire
// a recursive read lock, which may deadlock with Close. Encoding happens asynchronously, so it doesn't hold
// the lock, and only after acquiring a concurrency slot, so the number of encoded payloads in memory is bounded
// too. If encoding fails, the data is passed as-is to the sender's fallback (the DLQ), which encodes it on its
// own terms. If encoding succeeds, delivery failures pass the encoded payload to the fallback as [flow.Blobs]
// in the resolved format, because the original data is no longer available at that point.
func (s *Sender) sendStructured(
	ctx context.Context, data flow.Chunk, format config.Format, contentType string, encode func() ([]byte, error),
) {
	s.sendWithConcurrencyLimit(func() {
		body, err := encode()
		if err != nil {
			sender := deadLetter(ctx, s.fallback, data, format)
			slog.Warn("failed to encode HTTP request payload", slog.Any("error", err), slog.String("name", s.Name),
				slog.Int("batch_size", data.Len()), slog.String("format", string(format)), slog.String("fallback", sender),
			)
			return
		}
		// Only the encoded body is needed from here on (including retries), so release the original items.
		data, encode = nil, nil

		outHdr := s.headers.Clone()
		if outHdr.Get(contentTypeHeader) == "" {
			outHdr.Set(contentTypeHeader, contentType)
		}

		s.sendWithRetries(ctx, s.url.Clone(), outHdr, retryableBody(body), int64(len(body)), func() string {
			return deadLetter(ctx, s.fallback, flow.Blobs{body}, format)
		})
	}, func() string {
		return deadLetter(ctx, s.fallback, data, format)
	})
}

func (s *Sender) sendHTTPRequest(ctx context.Context, req *http.Request) {
	if req == nil { // Upstream invariant violation: shouldn't have been sent at all.
		slog.Error("cannot send empty HTTP request", slog.String("name", s.Name))
		return
	}

	outURL := s.url.Clone()
	copyQuery(req.URL, outURL)

	outHdr := s.headers.Clone()
	copyHeaders(req.Header, outHdr, true)

	if req.Body == nil && req.GetBody == nil {
		reject := func() string { return s.rejectRequest(ctx, req, nil) }
		s.sendWithConcurrencyLimit(func() {
			s.sendWithRetries(ctx, outURL, outHdr, retryableBody(nil), 0, reject)
		}, reject)
		return
	}
	if req.GetBody != nil { // Optimization to avoid duplicate memory allocations for the request body.
		if req.Body != nil {
			_ = req.Body.Close()
		}
		reject := func() string { return s.rejectRequest(ctx, req, req.GetBody) }
		s.sendWithConcurrencyLimit(func() {
			s.sendWithRetries(ctx, outURL, outHdr, req.GetBody, req.ContentLength, reject)
		}, reject)
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

	reject := func() string { return s.rejectRequest(ctx, req, retryableBody(b)) }
	s.sendWithConcurrencyLimit(func() {
		s.sendWithRetries(ctx, outURL, outHdr, retryableBody(b), int64(len(b)), reject)
	}, reject)
}

// rejectRequest passes an HTTP request that wasn't delivered to the fallback sender (see [deadLetter]). Its original
// body may have been consumed or closed already, so it's replaced with a fresh one from getBody, if there is one.
func (s *Sender) rejectRequest(ctx context.Context, req *http.Request, getBody getBodyFunc) string {
	if getBody != nil {
		if body, err := getBody(); err == nil {
			req.Body = body
		}
	}
	return deadLetter(ctx, s.fallback, flow.HTTPRequests{req}, codec.FormatDefault)
}

func (s *Sender) sendHTTPResponse(ctx context.Context, resp *http.Response) {
	if resp == nil { // Upstream invariant violation: shouldn't have been sent at all.
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
		// Memory optimization, due to the same reason as in [Sender.sendHTTPRequest], but
		// working around the fact that [http.Response] doesn't have a GetBody() method.
		reject := func() string { return s.rejectResponse(ctx, resp, b.GetBody) }
		s.sendWithConcurrencyLimit(func() {
			s.sendWithRetries(ctx, s.url.Clone(), outHdr, b.GetBody, resp.ContentLength, reject)
		}, reject)
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

	reject := func() string { return s.rejectResponse(ctx, resp, retryableBody(b)) }
	s.sendWithConcurrencyLimit(func() {
		s.sendWithRetries(ctx, s.url.Clone(), outHdr, retryableBody(b), int64(len(b)), reject)
	}, reject)
}

// rejectResponse is like [Sender.rejectRequest], for HTTP responses.
func (s *Sender) rejectResponse(ctx context.Context, resp *http.Response, getBody getBodyFunc) string {
	if body, err := getBody(); err == nil {
		resp.Body = body
	}
	return deadLetter(ctx, s.fallback, flow.HTTPResponses{resp}, codec.FormatDefault)
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

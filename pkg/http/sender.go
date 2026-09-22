package http

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
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
)

const (
	contentTypeHeader = "Content-Type"
	jsonContentType   = "application/json"
)

// Destination contains all the configuration and state details for sending HTTP requests.
type Destination struct {
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
	batch       atomic.Bool

	inProgress sync.WaitGroup
}

// NewDestination creates a new [Destination] from the given configuration, which was read
// from a TOML file. It checks the details and returns an error if any of them is invalid.
func NewDestination(cfg map[string]any, name, baseType string) (*Destination, error) {
	var err error
	d := &Destination{
		Name: name,
		Type: baseType,
	}

	if d.url, err = parseURL(config.Value(cfg, "url", ""), d.Type); err != nil {
		return nil, err
	}
	if d.method, err = parseMethod(config.Value(cfg, "method", http.MethodPost), "sending"); err != nil {
		return nil, err
	}
	if d.method == http.MethodGet {
		return nil, fmt.Errorf("HTTP method %q not supported for sending data", d.method)
	}
	if err := parseQuery(d.url, cfg["query"], "sender"); err != nil {
		return nil, err
	}
	if d.headers, err = parseHeaders(cfg["headers"], "sender"); err != nil {
		return nil, err
	}
	if d.retries, err = parseRetries(cfg["retries"], d.method, d.Name); err != nil {
		return nil, err
	}

	if d.tls, d.transportID, err = loadClientTLSConfig(cfg["tls"], d.Type); err != nil {
		return nil, err
	}
	if d.url.Scheme == httpScheme && cfg["tls"] != nil {
		if m, ok := cfg["tls"].(map[string]any); ok && len(m) > 0 {
			slog.Warn("TLS config details are ineffective because URL scheme is unencrypted HTTP",
				slog.String("name", d.Name), slog.String("url", d.url.Redacted()),
			)
		}
	}
	if d.timeout, err = time.ParseDuration(config.Value(cfg, "timeout", defaultRequestTimeout.String())); err != nil {
		return nil, fmt.Errorf("invalid timeout duration: %w", err)
	}
	// For us, 0 is the same as negative values, but not in Go.
	// This normalization simplifies HTTP client construction.
	d.timeout = max(d.timeout, 0)
	// HTTP transport configuration is affected by TLS, maximum header size, and timeout
	// settings, so this prevents clients with different configurations from sharing transports.
	d.transportID = fmt.Sprintf("%s,%d,%s", d.transportID, defaultMaxHeaderSize, d.timeout)

	switch d.Type {
	case config.SenderTypeHTTP:
		d.client = clientH2(d.tls.Clone(), defaultMaxHeaderSize, d.timeout, d.transportID)
	case config.SenderTypeHTTP3:
		d.client = clientH3(d.tls.Clone(), defaultMaxHeaderSize, d.timeout, d.transportID)
	default:
		return nil, fmt.Errorf("unexpected sender type %q", d.Type)
	}

	return d, nil
}

// Send sends any data as an HTTP request to the configured [Destination], retrying on transient errors
// based on [Destination.retries]. Byte buffers are sent as raw payloads, received [http.Request]s and
// [http.Response]s are proxied with their headers and content preserved. Other data types are encoded
// as JSON, if possible. Nil data is treated as a sentinel marking the beginning and the end of batches.
func (d *Destination) Send(ctx context.Context, data any) {
	// Don't send nil data, it is used as a sentinel for batches.
	// Reminder: not fully implemented yet (batch size & batching duration limits).
	if data == nil {
		b := d.batch.Load()
		d.batch.CompareAndSwap(b, !b) // Reminder: consider concurrency (how to handle overlapping batches).

		return
	}

	outURL := d.url.Clone()
	outHdr := d.headers.Clone()
	getBody, contentLength, err := serializeData(data, outURL, outHdr)
	if err != nil {
		slog.Error("failed to serialize payload for HTTP request", slog.Any("error", err),
			slog.String("name", d.Name), slog.String("url", outURL.Redacted()),
		)
		return
	}

	d.inProgress.Go(func() {
		d.sendWithRetries(ctx, outURL, outHdr, getBody, contentLength)
	})
}

func serializeData(data any, outURL *url.URL, outHdr http.Header) (getBodyFunc, int64, error) {
	switch t := data.(type) {
	case []byte:
		if len(t) == 0 {
			return nil, 0, errors.New("no payload to send")
		}
		if outHdr.Get(contentTypeHeader) == "" {
			outHdr.Set(contentTypeHeader, http.DetectContentType(t))
		}
		// Mutation of the original byte slice is not a concern because it's already abandoned by the data
		// source. On the other hand, GC pressure due to duplicating huge blobs is something we need to avoid.
		return func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(t)), nil }, int64(len(t)), nil

	case *http.Request:
		if t == nil {
			return nil, 0, errors.New("cannot forward nil HTTP request")
		}
		copyHeaders(t.Header, outHdr, true)
		copyQuery(t.URL, outURL)
		if t.Body == nil && t.GetBody == nil {
			return func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(nil)), nil }, 0, nil
		}
		if t.GetBody != nil { // Optimization to avoid duplicate memory allocations for the request body.
			return t.GetBody, t.ContentLength, nil
		}
		defer t.Body.Close() // Redundant but harmless (see [http.Request.Body] for server requests).
		b, err := io.ReadAll(t.Body)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to read HTTP request body: %w", err)
		}
		return func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }, int64(len(b)), nil

	case *http.Response:
		if t == nil {
			return nil, 0, errors.New("cannot relay nil HTTP response")
		}
		copyHeaders(t.Header, outHdr, false)
		if t.Body == nil {
			return func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(nil)), nil }, 0, nil
		}
		if b, ok := t.Body.(bodyProvider); ok {
			// Memory optimization, due to the same reason as above (to avoid duplicate allocations for response
			// bodies during retries), working around the fact that [http.Response] doesn't have a GetBody() method.
			return b.GetBody, t.ContentLength, nil
		}
		defer t.Body.Close() // It's important to call this before [io.ReadAll], but only here, not above.
		b, err := io.ReadAll(t.Body)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to read HTTP response body: %w", err)
		}
		return func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }, int64(len(b)), nil
	}

	// Fall-back to JSON encoding for other data types.
	buf := new(bytes.Buffer)
	encoder := json.NewEncoder(buf)
	encoder.SetEscapeHTML(false) // Passing raw data, not rendering it, so don't alter it.

	if err := encoder.Encode(data); err != nil {
		return nil, 0, fmt.Errorf("failed to serialize JSON: %w", err)
	}
	if outHdr.Get(contentTypeHeader) == "" {
		outHdr.Set(contentTypeHeader, jsonContentType)
	}
	body := buf.Bytes()
	return func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }, int64(len(body)), nil
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

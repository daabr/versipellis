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

	if d.url, err = parseURL(config.Value(cfg, "url", ""), d.Type, "sender"); err != nil {
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
				slog.String("name", d.Name), slog.String("url", d.url.String()),
			)
		}
	}
	if d.timeout, err = time.ParseDuration(config.Value(cfg, "timeout", defaultRequestTimeout.String())); err != nil {
		return nil, fmt.Errorf("invalid timeout duration: %w", err)
	}
	// For us, 0 is the same as negative values, but not in Go. This normalization simplifies HTTP client construction.
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

	currentURL := d.url.Clone()
	headers := d.headers.Clone()
	payload, err := serializeData(data, currentURL, headers)
	if err != nil {
		slog.Error("failed to serialize payload for HTTP request", slog.Any("error", err),
			slog.String("name", d.Name), slog.String("url", currentURL.String()),
		)
		return
	}

	d.inProgress.Go(func() {
		d.sendWithRetries(ctx, currentURL, headers, payload)
	})
}

func serializeData(data any, u *url.URL, headers http.Header) ([]byte, error) {
	switch t := data.(type) {
	case []byte:
		if len(t) == 0 {
			return nil, errors.New("no payload to send")
		}
		if headers.Get(contentTypeHeader) == "" {
			headers.Set(contentTypeHeader, http.DetectContentType(t))
		}
		return bytes.Clone(t), nil

	case *http.Request:
		if t == nil {
			return nil, errors.New("cannot forward nil HTTP request")
		}
		copyHeaders(t.Header, headers)
		copyQuery(t.URL, u)
		if t.Body == nil {
			return nil, nil
		}
		defer t.Body.Close()
		body, err := io.ReadAll(t.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read HTTP request body: %w", err)
		}
		return body, nil

	case *http.Response:
		if t == nil {
			return nil, errors.New("cannot relay nil HTTP response")
		}
		copyHeaders(t.Header, headers)
		if t.Body == nil {
			return nil, nil
		}
		defer t.Body.Close()
		body, err := io.ReadAll(t.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read HTTP response body: %w", err)
		}
		return body, nil
	}

	// Fall-back to JSON encoding for other data types.
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false) // Passing raw data, not rendering it, so don't alter it.

	if err := encoder.Encode(data); err != nil {
		return nil, fmt.Errorf("failed to serialize JSON: %w", err)
	}

	if headers.Get(contentTypeHeader) == "" {
		headers.Set(contentTypeHeader, jsonContentType)
	}
	return buf.Bytes(), nil
}

// copyHeaders copies header key-value pairs from the source to the destination, without overwriting existing keys.
func copyHeaders(src, dst http.Header) {
	for k := range src {
		if _, found := dst[k]; !found {
			dst[k] = slices.Clone(src[k])
		}
	}
}

// copyQuery copies query parameters from the source URL to the destination URL, without overwriting existing keys.
func copyQuery(srcURL, dstURL *url.URL) {
	src, dst := srcURL.Query(), dstURL.Query()
	for k := range src {
		if _, found := dst[k]; !found {
			dst[k] = src[k]
		}
	}
	dstURL.RawQuery = dst.Encode()
}

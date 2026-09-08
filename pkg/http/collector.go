package http

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http/httpguts"

	"github.com/daabr/versipellis/pkg/config"
)

const (
	// CloseTimeout is the maximum duration to wait for in-flight requests to finish, during [Collector.Close].
	// If this timeout is reached, the collector will forcefully shut down the connection, as well as any
	// idle ones. It is intentionally short and not configurable, to enable quick process restarts.
	CloseTimeout = 5 * time.Second

	defaultRequestTimeout = 5 * time.Second
)

// Collector contains all the configuration and state details for sending HTTP requests.
type Collector struct {
	config.BaseCollector

	url     *url.URL
	method  string
	headers http.Header
	body    []byte
	timeout time.Duration
	retries int // Reminder: extend this to a policy struct & make it configurable in a separate PR.

	client      *http.Client
	transportID string // Reminder: add configurable TLS in a separate PR.

	cancel    context.CancelFunc
	done      <-chan struct{}
	inFlight  sync.WaitGroup
	closeOnce sync.Once
}

// Base returns a copy of the collector's static and generic configuration details.
// Specifically, it does not copy references such as the Schedule and Sender fields.
func (c *Collector) Base() *config.BaseCollector {
	return &config.BaseCollector{
		Type:        c.Type,
		Name:        c.Name,
		Cronspec:    c.Cronspec,
		Trigger:     c.Trigger,
		Destination: c.Destination,
	}
}

// NewCollector creates a new [Collector] from the given configuration, which was read from
// a TOML file. It checks the details and returns an error if any of them is invalid.
func NewCollector(base *config.BaseCollector, cfg map[string]any) (*Collector, error) {
	switch {
	case base == nil:
		return nil, errors.New("base collector cannot be nil")
	case base.Type != config.CollectorTypeHTTP && base.Type != config.CollectorTypeHTTP3:
		msg := "collector type is %q, but must be %q or %q"
		return nil, fmt.Errorf(msg, base.Type, config.CollectorTypeHTTP, config.CollectorTypeHTTP3)
	case cfg == nil:
		return nil, fmt.Errorf("[collector.%s] TOML config section is missing", base.Type)
	}

	var err error
	c := &Collector{BaseCollector: *base, retries: 3} // Reminder: configurable retries policy in a separate PR.
	httpCfg, ok := cfg[base.Type].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("[collector.%s] isn't a valid TOML config section", base.Type)
	}

	if c.url, err = parseURL(config.Value(httpCfg, "url", ""), c.Type); err != nil {
		return nil, err
	}
	if c.method, err = parseMethod(config.Value(httpCfg, "method", http.MethodGet)); err != nil {
		return nil, err
	}
	if err := parseQuery(c.url, httpCfg["query"]); err != nil {
		return nil, err
	}
	if c.headers, err = parseHeaders(httpCfg["headers"]); err != nil {
		return nil, err
	}
	if c.body, err = loadBody(httpCfg, c.method); err != nil {
		return nil, err
	}
	if c.timeout, err = time.ParseDuration(config.Value(httpCfg, "timeout", defaultRequestTimeout.String())); err != nil {
		return nil, fmt.Errorf("invalid timeout duration: %w", err)
	}
	// For us, 0 is the same as negative values, but not for Go, so this normalization simplifies HTTP client construction.
	c.timeout = max(c.timeout, 0)

	return c, nil
}

func parseURL(rawURL string, protoVer string) (*url.URL, error) {
	if rawURL == "" {
		return nil, errors.New("HTTP collector URL must be specified")
	}

	u, err := url.Parse(rawURL)
	switch {
	case err != nil:
		return nil, fmt.Errorf("invalid HTTP collector URL: %w", err)
	case !u.IsAbs():
		return nil, errors.New("HTTP collector URL must be absolute (start with a scheme)")
	}

	scheme := strings.ToLower(u.Scheme)
	switch {
	case scheme != "https" && protoVer == config.CollectorTypeHTTP3:
		return nil, errors.New("HTTP collector URL must have an HTTPS scheme for HTTP/3")
	case scheme != "https" && scheme != "http":
		return nil, errors.New("HTTP collector URL must have an HTTP/S scheme")
	case u.Opaque != "":
		return nil, fmt.Errorf(`HTTP collector URL must have "//" after the "%s:" scheme`, u.Scheme)
	case u.Hostname() == "":
		return nil, errors.New("HTTP collector URL must have a host address")
	case u.Port() != "":
		// [url.Parse] returns an error for negative and non-numeric values, but not out-of-range numbers.
		if port, err := strconv.Atoi(u.Port()); err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("HTTP collector URL has an invalid port number: %q", u.Port())
		}
	}

	return u, nil
}

func parseMethod(rawMethod string) (string, error) {
	switch m := strings.ToUpper(rawMethod); m {
	case http.MethodGet, http.MethodPatch, http.MethodPost, http.MethodPut:
		return m, nil
	case http.MethodConnect, http.MethodDelete, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return "", fmt.Errorf("HTTP method %q not supported for data collection", m)
	default:
		return "", fmt.Errorf("invalid HTTP method %q", rawMethod)
	}
}

// parseQuery adds "query" key-value pairs (if there are any) to the URL's query.
// It overrides any existing parameters from the original URL with the same name,
// and returns an error if the type of any configured value isn't a string.
func parseQuery(u *url.URL, cfg any) error {
	if cfg == nil {
		return nil
	}
	table, ok := cfg.(map[string]any)
	if !ok {
		return fmt.Errorf(`HTTP collector's "query" must be a table of string key-value pairs, got %T`, cfg)
	}

	values := u.Query()
	for key, rawValue := range table {
		if v, ok := rawValue.(string); ok {
			values.Set(key, v)
			continue
		}
		return fmt.Errorf("query parameter %q must be a string, got %T", key, rawValue)
	}

	u.RawQuery = values.Encode()
	return nil
}

func parseHeaders(cfg any) (http.Header, error) {
	if cfg == nil {
		return nil, nil
	}
	table, ok := cfg.(map[string]any)
	if !ok {
		return nil, fmt.Errorf(`HTTP collector "headers" must be a table of string key-value pairs, got %T`, cfg)
	}

	headers := make(http.Header, len(table))
	for key, value := range table {
		if !httpguts.ValidHeaderFieldName(key) {
			return nil, fmt.Errorf("invalid HTTP header field name %q", key)
		}
		v, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("HTTP header field value %q must be a string, got %T", key, value)
		}
		if !httpguts.ValidHeaderFieldValue(v) {
			return nil, fmt.Errorf("invalid HTTP header field value for header %q", key)
		}
		headers.Set(key, v)
	}

	return headers, nil
}

func loadBody(cfg map[string]any, method string) ([]byte, error) {
	inline := config.Value(cfg, "body", "") // Not trimming leading/trailing whitespaces because this payload may be signed.
	path := strings.TrimSpace(config.Value(cfg, "body_file", ""))

	switch {
	case inline == "" && path == "":
		return nil, nil
	case method == http.MethodGet:
		return nil, errors.New("HTTP GET requests cannot have a body")
	case inline != "" && path != "":
		return nil, errors.New("both HTTP body string and HTTP body file provided, specify only one")
	case inline != "" && path == "":
		return []byte(inline), nil
	}

	body, err := os.ReadFile(path) //gosec:disable G304 // Path is configurable by design.
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, errors.New("specified HTTP body file is empty: " + path)
	}
	return body, nil // Not trimming leading/trailing whitespaces because this payload may be binary.
}

// Start connects to the configured HTTP server and starts sending requests to it.
// This function returns immediately, and the collector runs asynchronously in the background.
// This function is idempotent: only the first call will actually start a goroutine. However,
// it is not meant to be safe for concurrency, initialize collectors only in the main goroutine.
func (c *Collector) Start(ctx context.Context) bool {
	if c == nil {
		slog.Error("HTTP collector is misconfigured")
		return false
	}
	if c.cancel != nil {
		slog.Error("HTTP collector already started") // This is a programming error...
		return true                                  // ...But a harmless one.
	}

	cfg := &tls.Config{} // Reminder: add configurable TLS in a separate PR.

	switch c.Type {
	case config.CollectorTypeHTTP:
		c.client = clientH2(cfg, c.transportID, c.timeout)
	case config.CollectorTypeHTTP3:
		c.client = clientH3(cfg, c.transportID, c.timeout)
	}
	ctx, c.cancel = context.WithCancel(ctx)
	c.done = ctx.Done()

	slog.Info("starting to send HTTP requests", slog.String("name", c.Name), slog.String("schedule", c.Cronspec))
	go c.scheduleNextRequest(ctx, time.Now())
	return true
}

func (c *Collector) scheduleNextRequest(ctx context.Context, prev time.Time) {
	defer c.Close()
	for {
		nextStart := c.Schedule.Next(prev)
		if nextStart.IsZero() {
			if c.Schedule.RunsOnlyOnce() {
				slog.Info("HTTP collector finished one-time execution",
					slog.String("name", c.Name), slog.String("schedule", c.Cronspec),
				)
			} else {
				slog.Error("HTTP collector stopped due to scheduler bug - no next instance",
					slog.String("name", c.Name), slog.String("schedule", c.Cronspec),
				)
			}
			return
		}
		if now := time.Now(); !c.Schedule.RunsOnlyOnce() && now.After(nextStart) {
			slog.Warn("HTTP collector is behind schedule, skipping missed execution",
				slog.String("name", c.Name), slog.String("schedule", c.Cronspec),
				slog.Time("skipped", nextStart), slog.Duration("gap", now.Sub(nextStart)),
			)
			prev = nextStart
			continue

			// Reminder: wait according to configurable retries policy in a separate PR.
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(nextStart)):
			c.sendRequest(ctx)
			prev = nextStart
		}
	}
}

func (c *Collector) sendRequest(ctx context.Context) {
	c.inFlight.Add(1)
	defer c.inFlight.Done()

	resp := c.requestWithRetries(ctx) //nolint:bodyclose // See [Collector.requestOnce] and [Collector.processResponse].

	if resp.StatusCode < http.StatusBadRequest && c.Sender != nil {
		resp.Header = fixHeaders(resp.Header)
		if err := c.Sender(ctx, resp); err != nil {
			slog.Warn("failed to process HTTP response", slog.Any("error", err), slog.String("name", c.Name))
		}
	}
}

// Forwarding a received response's [http.Response.Header] as-is in an outgoing request
// can propagate hop-by-hop or response-specific headers, which would lead to bugs. See
// https://nathandavison.com/blog/abusing-http-hop-by-hop-request-headers and [httputil].
func fixHeaders(h http.Header) http.Header {
	headers := h.Clone()
	for k := range headers {
		switch k {
		// https://datatracker.ietf.org/doc/html/rfc9110#name-connection
		case "Connection":
			for _, vs := range headers[k] {
				for v := range strings.SplitSeq(vs, ",") {
					headers.Del(strings.TrimSpace(v))
				}
			}
			headers.Del(k)
		// https://datatracker.ietf.org/doc/html/rfc2616#section-13.5.1
		// https://datatracker.ietf.org/doc/html/rfc6797#section-6.1
		case "Keep-Alive", "Te", "Trailer", "Transfer-Encoding", "Upgrade", "Strict-Transport-Security":
			headers.Del(k)
		// Reminder: revisit this case when we support additional non-default encoding types.
		case "Content-Encoding", "Content-Length", "Set-Cookie":
			headers.Del(k)
		}
	}
	return headers
}

// Done returns a channel that signals and gets closed when the collector has finished its work and is no longer running.
func (c *Collector) Done() <-chan struct{} {
	return c.done
}

// Close waits (up to [CloseTimeout]) for requests that are currently in flight to finish, and prevents new ones
// from starting. It then signals through the [Collector.Done] channel that the collector isn't executing requests
// anymore. It is safe (though useless) to call multiple times, even if [Collector.Start] was never called,
// but either way it's meant to be called only in the same goroutine as [Collector.scheduleNextRequest].
func (c *Collector) Close() {
	if c == nil || c.cancel == nil {
		return
	}

	c.closeOnce.Do(func() {
		defer c.cancel()

		if c.client == nil {
			return
		}

		done := make(chan struct{})
		go func() {
			defer close(done)
			c.inFlight.Wait()
		}()

		select {
		case <-done:
			// All done.
		case <-time.After(CloseTimeout):
			slog.Warn("closing HTTP collector forcefully",
				slog.String("name", c.Name), slog.Duration("timeout", CloseTimeout),
			)
		}

		c.client.CloseIdleConnections()
	})
}

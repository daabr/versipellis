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
	closeTimeout = 5 * time.Second

	defaultRequestTimeout = 5 * time.Second

	maxByteSize int64 = 1073741824 // 1 GiB (which is already quite large for a single HTTP request).
)

// Collector contains all the configuration and state details for sending HTTP requests.
type Collector struct {
	config.BaseCollector

	url           *url.URL
	method        string
	headers       http.Header
	body          []byte
	maxBodySize   int64
	maxHeaderSize int64
	timeout       time.Duration
	tls           *tls.Config
	retries       *retries

	transportID string
	client      *http.Client

	cancelSched context.CancelFunc
	cancelExec  context.CancelFunc
	closeDone   chan struct{}
	inProgress  sync.WaitGroup
	closeOnce   sync.Once
}

// Base returns a copy of the collector's static and generic configuration details.
// Specifically, it does not copy references such as the Schedule and Sender fields.
func (c *Collector) Base() *config.BaseCollector {
	return &config.BaseCollector{
		Type:        c.Type,
		Name:        c.Name,
		Cronspec:    c.Cronspec,
		Trigger:     c.Trigger,
		Concurrency: c.Concurrency,
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
	case cfg == nil || cfg[base.Type] == nil:
		return nil, fmt.Errorf("[collector.%s] TOML config section is missing", base.Type)
	}

	var err error
	c := &Collector{BaseCollector: *base}
	httpCfg, ok := cfg[c.Type].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("[collector.%s] isn't a valid TOML config section", c.Type)
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
	if c.retries, err = parseRetries(httpCfg["retries"], c.method, c.Name); err != nil {
		return nil, err
	}

	if c.tls, c.transportID, err = loadClientTLSConfig(httpCfg["tls"], c.Type); err != nil {
		return nil, err
	}
	if c.url.Scheme == "http" && httpCfg["tls"] != nil {
		if m, ok := httpCfg["tls"].(map[string]any); ok && len(m) > 0 {
			slog.Warn("TLS config details are ineffective because URL scheme is unencrypted HTTP",
				slog.String("name", c.Name), slog.String("url", c.url.String()),
			)
		}
	}

	c.maxBodySize = parseByteSize(httpCfg, "max_body_size", c.Name, defaultMaxBodySize)
	c.maxHeaderSize = parseByteSize(httpCfg, "max_headers_size", c.Name, defaultMaxHeaderSize)
	if c.timeout, err = time.ParseDuration(config.Value(httpCfg, "timeout", defaultRequestTimeout.String())); err != nil {
		return nil, fmt.Errorf("invalid timeout duration: %w", err)
	}
	// For us, 0 is the same as negative values, but not in Go. This normalization simplifies HTTP client construction.
	c.timeout = max(c.timeout, 0)
	// HTTP transport configuration is affected by TLS, maximum header size, and timeout
	// settings, so this prevents clients with different configurations from sharing transports.
	c.transportID = fmt.Sprintf("%s,%d,%s", c.transportID, c.maxHeaderSize, c.timeout)

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

	u.Scheme = strings.ToLower(u.Scheme)
	switch {
	case u.Scheme != "https" && protoVer == config.CollectorTypeHTTP3:
		return nil, errors.New("HTTP collector URL must have an HTTPS scheme for HTTP/3")
	case u.Scheme != "https" && u.Scheme != "http":
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
func parseQuery(u *url.URL, rawCfg any) error {
	if rawCfg == nil {
		return nil
	}
	cfg, ok := rawCfg.(map[string]any)
	if !ok {
		return fmt.Errorf(`HTTP collector's "query" must be a table of string key-value pairs, got %T`, rawCfg)
	}
	if len(cfg) == 0 {
		return nil
	}

	values := u.Query()
	for key, rawValue := range cfg {
		if v, ok := rawValue.(string); ok {
			values.Set(key, v)
			continue
		}
		return fmt.Errorf("query parameter %q must be a string, got %T", key, rawValue)
	}

	u.RawQuery = values.Encode()
	return nil
}

func parseHeaders(rawCfg any) (http.Header, error) {
	if rawCfg == nil {
		return nil, nil
	}
	cfg, ok := rawCfg.(map[string]any)
	if !ok {
		return nil, fmt.Errorf(`HTTP collector "headers" must be a table of string key-value pairs, got %T`, rawCfg)
	}

	headers := make(http.Header, len(cfg))
	for key, value := range cfg {
		if !httpguts.ValidHeaderFieldName(key) {
			return nil, fmt.Errorf("invalid HTTP header name %q", key)
		}
		v, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("HTTP header value for %q must be a string, got %T", key, value)
		}
		if !httpguts.ValidHeaderFieldValue(v) {
			return nil, fmt.Errorf("invalid HTTP header value for %q", key)
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

func parseByteSize(cfg map[string]any, key, name string, defaultValue int64) int64 {
	value := config.Value(cfg, key, defaultValue)
	if value <= 0 {
		description := strings.ReplaceAll(key, "_", " ")
		slog.Warn(description+" has an invalid (non-positive) value, using default value",
			slog.String("name", name), slog.Int64("actual", value), slog.Int64("default", defaultValue),
		)
		value = defaultValue
	}
	if value > maxByteSize {
		description := strings.ReplaceAll(key, "_", " ")
		slog.Warn("forcing upper bound on "+description, slog.String("name", name),
			slog.Int64("above_max", value), slog.Int64("new_value", maxByteSize),
		)
		value = maxByteSize
	}

	return value
}

// Start connects to the configured HTTP server and starts sending requests to it. This function
// returns immediately, and the collector runs asynchronously in the background. This function is
// idempotent: only the first call will actually start a goroutine. However, it is not meant to be
// safe for concurrency, initialize collectors only in the main goroutine. Lastly, the collector
// obeys the cancellation of the provided context, but with a grace period of [Collector.timeout].
func (c *Collector) Start(ctx context.Context) bool {
	if c == nil {
		slog.Error("HTTP collector is misconfigured")
		return false
	}
	if c.cancelSched != nil {
		slog.Error("HTTP collector already started") // This is a programming error...
		return true                                  // ...But a harmless one.
	}

	switch c.Type {
	case config.CollectorTypeHTTP:
		c.client = clientH2(c.tls.Clone(), c.maxHeaderSize, c.timeout, c.transportID)
	case config.CollectorTypeHTTP3:
		c.client = clientH3(c.tls.Clone(), c.maxHeaderSize, c.timeout, c.transportID)
	}

	var schedCtx, execCtx context.Context
	schedCtx, c.cancelSched = context.WithCancel(ctx)
	execCtx, c.cancelExec = context.WithCancel(context.WithoutCancel(ctx))
	c.closeDone = make(chan struct{})

	slog.Info("starting to send HTTP requests",
		slog.String("name", c.Name), slog.String("schedule", c.Cronspec),
	)
	go c.scheduleNext(schedCtx, execCtx, time.Now())
	return true
}

func (c *Collector) scheduleNext(ctx, execCtx context.Context, prev time.Time) {
	sem := make(chan struct{}, max(c.Concurrency, 1))
	defer c.Close()

	for {
		nextStart := c.Schedule.Next(prev)
		if nextStart.IsZero() {
			if c.Schedule.RunsOnlyOnce() {
				done := make(chan struct{})
				go func() {
					defer close(done)
					c.inProgress.Wait()
				}()
				select {
				case <-ctx.Done():
				case <-done:
				}
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
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(nextStart)):
			c.checkConcurrency(ctx, execCtx, sem, nextStart)
			prev = nextStart
		}
	}
}

func (c *Collector) checkConcurrency(ctx, execCtx context.Context, sem chan struct{}, scheduled time.Time) {
	if ctx.Err() != nil { // Instead of ctx.Done() in the select block below - to check ctx before sem.
		return
	}
	select {
	case sem <- struct{}{}:
		c.inProgress.Go(func() {
			defer func() { <-sem }()
			c.sendRequest(ctx, execCtx)
		})
	default:
		slog.Warn("HTTP collector is at its concurrency limit, skipping request",
			slog.String("name", c.Name), slog.Int("limit", cap(sem)), slog.Time("skipped", scheduled),
		)
	}
}

// sendRequest sends a single scheduled HTTP request and forwards its response to the sender.
// SchedCtx indicates if collector scheduling is active (aborting future retries on shutdown),
// while execCtx governs the in-flight network call up to [Collector.timeout].
func (c *Collector) sendRequest(schedCtx, execCtx context.Context) {
	resp := c.requestWithRetries(schedCtx, execCtx) //nolint:bodyclose // See [Collector.requestOnce].

	if resp.StatusCode < http.StatusBadRequest && c.Sender != nil {
		resp.Header = fixHeaders(resp.Header)
		resp.Close = false
		resp.Trailer = nil
		resp.TransferEncoding = nil
		if err := c.Sender(execCtx, resp); err != nil {
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

// Done returns a channel that closes when shutdown completes or its grace period expires.
func (c *Collector) Done() <-chan struct{} {
	return c.closeDone
}

// Close waits (up to [Collector.timeout]) for requests that are in progress to finish, after new ones are no longer being
// scheduled. It is safe to call multiple times, even if [Collector.Start] wasn't called, but it's meant to be called only
// at the end of the [Collector.scheduleNext] goroutine. If there are still pending requests after the timeout, the collector
// will forcefully close their connections. It then signals through the [Collector.Done] channel that it's ready to shut down.
func (c *Collector) Close() {
	if c == nil || c.cancelSched == nil {
		return
	}

	c.closeOnce.Do(func() {
		c.cancelSched()

		done := make(chan struct{})
		go func() {
			defer close(done)
			c.inProgress.Wait()
		}()

		timeout := c.timeout
		if timeout <= 0 || timeout > closeTimeout {
			timeout = closeTimeout // Ensure the timeout is within acceptable bounds.
		}

		select {
		case <-done:
			// All done.
		case <-time.After(timeout):
			slog.Warn("closing HTTP collector forcefully",
				slog.String("name", c.Name), slog.Duration("timeout", timeout),
			)
			if c.cancelExec != nil {
				c.cancelExec()
			}
		}

		if c.closeDone != nil {
			close(c.closeDone)
		}
	})
}

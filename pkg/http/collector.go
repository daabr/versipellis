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
	"strings"
	"time"

	"github.com/daabr/versipellis/pkg/codec"
	"github.com/daabr/versipellis/pkg/config"
	"github.com/daabr/versipellis/pkg/flow"
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
	format        config.Format
	tls           *tls.Config
	retries       *retries

	transportID string
	client      *http.Client
	state       config.State
}

// Base returns a copy of the collector's static and generic configuration details.
// Specifically, it does not copy references such as the Schedule and Send fields.
func (c *Collector) Base() *config.BaseCollector {
	return &config.BaseCollector{
		Type:        c.Type,
		Name:        c.Name,
		Destination: c.Destination,

		Cronspec:    c.Cronspec,
		Trigger:     c.Trigger,
		Concurrency: c.Concurrency,
	}
}

// NewCollector creates a new [Collector] from the given configuration, which was
// read from a TOML file. It checks the details and returns an error if any of them
// is semantically invalid, but the caller is responsible for providing usable input.
func NewCollector(base *config.BaseCollector, cfg map[string]any) (*Collector, error) {
	c := &Collector{BaseCollector: *base}
	var err error

	if c.url, err = parseURL(config.Value(cfg, "url", ""), c.Type); err != nil {
		return nil, err
	}
	if c.method, err = parseMethod(config.Value(cfg, "method", http.MethodGet), "collection"); err != nil {
		return nil, err
	}
	if err := parseQuery(c.url, cfg["query"], "collector"); err != nil {
		return nil, err
	}
	if c.headers, err = parseHeaders(cfg["headers"], "collector"); err != nil {
		return nil, err
	}
	if c.body, err = loadBody(cfg, c.method); err != nil {
		return nil, err
	}
	if c.format, err = codec.ParseFormat(cfg); err != nil {
		return nil, fmt.Errorf("format setting: %w", err)
	}
	c.maxBodySize = parseByteSize(cfg, "max_body_size", c.Name, defaultMaxBodySize)
	c.maxHeaderSize = parseByteSize(cfg, "max_headers_size", c.Name, defaultMaxHeaderSize)

	if c.timeout, err = time.ParseDuration(config.Value(cfg, "timeout", defaultRequestTimeout.String())); err != nil {
		return nil, fmt.Errorf("invalid timeout duration: %w", err)
	}
	// For us, 0 is the same as negative values, but not in Go. This normalization simplifies HTTP client construction.
	c.timeout = max(c.timeout, 0)

	if c.tls, c.transportID, err = loadClientTLSConfig(cfg["tls"], c.Type); err != nil {
		return nil, err
	}
	if c.url.Scheme == httpScheme && cfg["tls"] != nil {
		if m, ok := cfg["tls"].(map[string]any); ok && len(m) > 0 {
			slog.Warn("TLS config details are ineffective because URL scheme is unencrypted HTTP",
				slog.String("name", c.Name), slog.String("url", c.url.Redacted()),
			)
		}
	}
	// HTTP transport configuration is affected by TLS, maximum header size, and timeout
	// settings, so this prevents clients with different configurations from sharing transports.
	c.transportID = fmt.Sprintf("%s,%d,%s", c.transportID, c.maxHeaderSize, c.timeout)

	if c.retries, err = parseRetries(cfg["retries"], c.method, c.Name); err != nil {
		return nil, err
	}

	return c, nil
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

// Start connects to the configured HTTP server and starts sending requests to it. This method returns
// immediately, and the collector runs asynchronously in the background. This method is not safe for
// concurrency, initialize collectors only in the main goroutine. Lastly, the collector obeys the
// cancellation of the provided context, but with a grace period of [Collector.timeout].
func (c *Collector) Start(ctx context.Context) bool {
	switch c.Type {
	case config.CollectorTypeHTTP:
		c.client = clientH2(c.tls.Clone(), c.maxHeaderSize, c.timeout, c.transportID)
	case config.CollectorTypeHTTP3:
		c.client = clientH3(c.tls.Clone(), c.maxHeaderSize, c.timeout, c.transportID)
	}

	schedCtx, execCtx := c.state.StartWith(ctx)
	slog.Info("starting to send HTTP requests", slog.String("name", c.Name), slog.String("schedule", c.Cronspec))
	go c.scheduleNext(schedCtx, execCtx, time.Now())
	return true
}

func (c *Collector) scheduleNext(schedCtx, execCtx context.Context, prev time.Time) {
	sem := make(chan struct{}, max(c.Concurrency, 1))
	defer c.Close(execCtx)

	for {
		nextStart := c.Schedule.Next(prev)
		if nextStart.IsZero() {
			if c.Schedule.RunsOnlyOnce() {
				done := make(chan struct{})
				go func() {
					defer close(done)
					c.state.InProgress.Wait()
				}()
				select {
				case <-schedCtx.Done():
				case <-done:
					slog.Info("HTTP collector finished one-time execution",
						slog.String("name", c.Name), slog.String("schedule", c.Cronspec),
					)
				}
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
		case <-schedCtx.Done():
			return
		case <-time.After(time.Until(nextStart)):
			c.requestWithConcurrencyLimit(schedCtx, execCtx, sem, nextStart)
			prev = nextStart
		}
	}
}

func (c *Collector) requestWithConcurrencyLimit(schedCtx, execCtx context.Context, sem chan struct{}, scheduled time.Time) {
	// Checking schedCtx.Err() here instead of using schedCtx.Done() in the select
	// block below, to guarantee that we check the closing state before the semaphore.
	if schedCtx.Err() != nil {
		return
	}

	select {
	case sem <- struct{}{}:
		c.state.InProgress.Go(func() {
			defer func() { <-sem }()

			resp := c.requestWithRetries(schedCtx, execCtx)
			if resp.StatusCode <= MaxSuccessfulStatusCode {
				c.sendResponse(execCtx, resp)
			}
		})
	default:
		slog.Warn("HTTP collector is at its concurrency limit, skipping request",
			slog.String("name", c.Name), slog.Int("limit", cap(sem)), slog.Time("skipped", scheduled),
		)
	}
}

// sendResponse passes a collected HTTP response to the collector's destination: as-is by default, or only
// its body, in a specific format (when one is configured). If the body can't be converted to that format,
// the entire HTTP response is sent to the Dead-Letter Queue. Either way, all I/O is asynchronous.
func (c *Collector) sendResponse(ctx context.Context, resp *http.Response) {
	if c.format == codec.FormatDefault {
		c.Send(ctx, flow.HTTPResponses{resp}, c.format)
		return
	}

	var chunk flow.Chunk
	body, err := responseBody(resp)
	if err == nil {
		chunk, err = bodyChunk(body, c.format)
	}
	if err != nil {
		sender := deadLetter(ctx, c.Fallback, flow.HTTPResponses{resp}, codec.FormatDefault)
		slog.Warn("processing error in HTTP response body", slog.Any("error", err), slog.String("name", c.Name),
			slog.String("format", string(c.format)), slog.String("fallback", sender),
		)
		return
	}

	if resp.Body != nil {
		_ = resp.Body.Close() // The rest of the response is discarded, but the chunk may still reference its body.
	}
	if chunk != nil {
		c.Send(ctx, chunk, c.format)
	}
}

// Done returns a channel that closes when shutdown completes or its grace period expires.
func (c *Collector) Done() <-chan struct{} {
	return c.state.Closed
}

// Close waits (up to [CloseTimeout] + [AbortTimeout]) for ongoing requests to finish, once new ones can
// no longer be scheduled. It is safe to call this multiple times, even if [Collector.Start] wasn't called.
// However, it's meant to be called only once, at the end of the [Collector.scheduleNext] goroutine.
// If there are still pending requests after the timeout, the collector will forcefully close their
// connections. It then signals through the [Collector.Done] channel that it's ready to shut down.
func (c *Collector) Close(context.Context) {
	if !c.state.IsStarted() {
		return
	}

	c.state.CloseOnce.Do(func() {
		c.state.BeginClose()

		done := make(chan struct{})
		go func() {
			defer close(done)
			c.state.InProgress.Wait()
		}()

		timeout := c.timeout
		if timeout <= 0 || timeout > CloseTimeout {
			timeout = CloseTimeout // Ensure the timeout is within acceptable bounds.
		}

		select {
		case <-done:
			// All done.
		case <-time.After(timeout):
			slog.Warn("closing HTTP collector forcefully", slog.String("name", c.Name), slog.Duration("timeout", timeout))
			c.state.StopStragglers()
		}

		// Wait for aborted workers to actually stop, if there are any.
		select {
		case <-done:
			// Immediate if the previous select block didn't encounter a timeout,
			// so no need to nest this select block inside the previous one.
		case <-time.After(AbortTimeout):
			slog.Error("aborted HTTP collector didn't stop immediately", slog.String("name", c.Name))
		}

		c.state.FinishClose()
	})
}

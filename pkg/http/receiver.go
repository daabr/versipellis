package http

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/quic-go/quic-go/http3"

	"github.com/daabr/versipellis/pkg/codec"
	"github.com/daabr/versipellis/pkg/config"
	"github.com/daabr/versipellis/pkg/flow"
)

// Receiver contains all the configuration and state details for receiving HTTP requests. It's a
// composite container for HTTP/1.1 and HTTP/2 webhooks (over TCP), as well as HTTP/3 (over QUIC/UDP).
type Receiver struct {
	config.BaseReceiver

	address       string
	maxBodySize   int64
	maxHeaderSize int64
	timeout       time.Duration
	format        config.Format
	tls           *tls.Config

	tcp   *http.Server
	udp   *http3.Server
	state config.State
}

// Base returns a copy of the receiver's static and generic configuration details.
// Specifically, it does not copy references such as the Send field.
func (r *Receiver) Base() *config.BaseReceiver {
	return &config.BaseReceiver{Type: r.Type, Name: r.Name, Destination: r.Destination}
}

// NewReceiver creates a new [Receiver] from the given configuration, which was
// read from a TOML file. It checks the details and returns an error if any of them
// is semantically invalid, but the caller is responsible for providing usable input.
func NewReceiver(base *config.BaseReceiver, cfg map[string]any) (*Receiver, error) {
	r := &Receiver{BaseReceiver: *base}

	var err error
	if r.address, err = parseAddress(config.Value(cfg, "address", ""), base.Type); err != nil {
		return nil, err
	}
	if r.format, err = codec.ParseFormat(cfg); err != nil {
		return nil, fmt.Errorf("format setting: %w", err)
	}

	r.maxBodySize = parseByteSize(cfg, "max_body_size", r.Name, defaultMaxBodySize)
	r.maxHeaderSize = parseByteSize(cfg, "max_headers_size", r.Name, defaultMaxHeaderSize)

	if r.timeout, err = time.ParseDuration(config.Value(cfg, "timeout", defaultRequestTimeout.String())); err != nil {
		return nil, fmt.Errorf("invalid timeout duration: %w", err)
	}
	// For us, 0 is the same as negative values, but not in Go.
	// This normalization simplifies HTTP server construction.
	r.timeout = max(r.timeout, 0)

	if r.tls, err = loadServerTLSConfig(cfg["tls"], r.Type); err != nil {
		return nil, err
	}
	if r.tls == nil && r.Type == config.ReceiverTypeHTTP3 {
		return nil, errors.New("HTTP/3 requires TLS configuration")
	}

	return r, nil
}

// parseAddress technically requires network access, but since the address
// is a local IP address it shouldn't fail even in isolated environments.
func parseAddress(addr, protoVer string) (string, error) {
	switch {
	case addr == "":
		return "", errors.New("address field required but not found")
	case protoVer == config.ReceiverTypeHTTP:
		tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
		if err != nil {
			return "", fmt.Errorf("invalid TCP address %q: %w", addr, err)
		}
		return tcpAddr.String(), nil
	case protoVer == config.ReceiverTypeHTTP3:
		udpAddr, err := net.ResolveUDPAddr("udp", addr)
		if err != nil {
			return "", fmt.Errorf("invalid UDP address %q: %w", addr, err)
		}
		return udpAddr.String(), nil
	default:
		return "", fmt.Errorf("unexpected receiver type %q", protoVer)
	}
}

// Start listens for incoming HTTP/1.1, HTTP/2, and HTTP/3 requests on the configured local TCP or UDP
// address. This method returns immediately, and the servers run asynchronously in the background.
// The input context is used only when starting them, not to control their entire lifecycle.
func (r *Receiver) Start(ctx context.Context) bool {
	r.state.Start()
	handler := http.Handler(r)
	if r.timeout > 0 {
		handler = http.TimeoutHandler(r, r.timeout, "")
	}
	mux := http.NewServeMux()
	for _, method := range []string{http.MethodGet, http.MethodPatch, http.MethodPost, http.MethodPut} {
		mux.Handle(method+" /", handler)
	}

	if r.Type == config.ReceiverTypeHTTP {
		r.tcp = r.newTCPServer(mux)
		ln, ok := r.listenTCP(ctx)
		if !ok {
			return false
		}
		go r.serveTCP(r.tcp, ln)
		versions := "HTTP/1.1"
		if r.tls != nil {
			versions += " and HTTP/2"
		}
		slog.Info(fmt.Sprintf("listening for %s requests", versions), slog.String("name", r.Name),
			slog.String("tcp_addr", r.address), slog.String("path", "/"),
		)
	}

	if r.Type == config.ReceiverTypeHTTP3 {
		r.udp = r.newUDPServer(mux)
		conn, ok := r.listenUDP(ctx)
		if !ok {
			return false
		}
		go r.serveUDP(r.udp, conn)
		slog.Info("listening for HTTP/3 requests", slog.String("name", r.Name),
			slog.String("udp_addr", r.address), slog.String("path", "/"),
		)
	}

	return true
}

// ServeHTTP implements the [http.Handler] interface for the [Receiver] type.
func (r *Receiver) ServeHTTP(w http.ResponseWriter, inReq *http.Request) {
	start := time.Now()

	if r.state.IfClosingElseLock() {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	r.state.InProgress.Add(1)
	r.state.CloseMu.RUnlock()
	defer r.state.InProgress.Done()

	// Reminder: need special handling for "multipart/form-data" requests, using [http.Request.MultipartReader],
	// to stream multi-gigabyte files efficiently instead of buffering them naively.
	body, ok := readRequest(w, inReq, r.Name, r.maxBodySize)
	if !ok {
		return // [readRequest] already called [w.WriteHeader] internally.
	}

	if r.Send != nil && !r.sendRequest(inReq, body) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		return
	}

	w.WriteHeader(http.StatusAccepted)
	slog.Debug("HTTP request received successfully", slog.String("name", r.Name),
		slog.String("proto", inReq.Proto), slog.String("method", inReq.Method),
		slog.String("remote_addr", inReq.RemoteAddr), slog.Int("content_length", len(body)),
		slog.Time("start_time", start), slog.Duration("duration", time.Since(start)),
	)
}

// sendRequest passes a received HTTP request to the receiver's destination: as-is by default, or only its body, in a
// specific format (when one is configured). If the body can't be converted to that format, the entire HTTP request is
// sent to the Dead-Letter Queue. Either way, all I/O is asynchronous, so the result is returned to the HTTP client.
func (r *Receiver) sendRequest(inReq *http.Request, body []byte) bool {
	ctx := inReq.Context()
	if r.format == codec.FormatDefault {
		r.Send(ctx, flow.HTTPRequests{outgoingRequest(inReq, body)}, r.format)
		return true
	}

	chunk, err := bodyChunk(body, r.format)
	if err != nil {
		sender := deadLetter(ctx, r.Fallback, flow.HTTPRequests{outgoingRequest(inReq, body)}, codec.FormatDefault)
		slog.Warn("processing error in received HTTP request body", slog.Any("error", err),
			slog.String("name", r.Name), slog.String("format", string(r.format)), slog.String("fallback", sender),
		)
		return false
	}
	if chunk != nil {
		r.Send(ctx, chunk, r.format)
	}
	return true
}

// outgoingRequest clones a received HTTP request, with its locally-buffered body, so it can be passed to senders.
func outgoingRequest(inReq *http.Request, body []byte) *http.Request {
	// We ignore the incoming request's method and path, to use the sender's preconfigured values.
	outReq := inReq.Clone(inReq.Context())

	// Update the "Content-Length" header, if needed.
	if bodyLen := int64(len(body)); bodyLen != outReq.ContentLength {
		outReq.ContentLength = bodyLen
	}

	// Memory optimization to avoid duplicate allocations: preserve access to the body's underlying
	// []byte slice to support sender retries (which [io.ReadCloser] does not enable on its own).
	outReq.Body = io.NopCloser(bytes.NewReader(body))
	outReq.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}

	return outReq
}

// readRequest reads the provided [http.Request.Body], and returns a locally-buffered copy,
// to decouple receiving data over an unreliable network from processing and sending it
// elsewhere. This is safe because [http.Request.Body] is never nil in server requests.
func readRequest(w http.ResponseWriter, r *http.Request, name string, maxBytes int64) ([]byte, bool) {
	if r.ContentLength > maxBytes {
		slog.Warn("didn't read HTTP request body: too large", slog.String("name", name),
			slog.String("proto", r.Proto), slog.String("method", r.Method), slog.String("remote_addr", r.RemoteAddr),
			slog.Int64("content_length", r.ContentLength), slog.Int64("max_size", maxBytes),
		)
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return nil, false
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBytes))
	if err != nil {
		slog.Warn("failed to read HTTP request body", slog.Any("error", err), slog.String("name", name),
			slog.String("proto", r.Proto), slog.String("method", r.Method), slog.String("remote_addr", r.RemoteAddr),
		)
		if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return nil, false
		}
		w.WriteHeader(http.StatusBadRequest)
		return nil, false
	}

	return body, true // The caller will write the response status, after processing the body.
}

// Close waits (up to [CloseTimeout]) to allow ongoing requests to finish before it gracefully
// shuts down the server. It is safe to call multiple times, even if [Receiver.Start] wasn't called.
// However, it's meant to be called only once, within the main goroutine that called [Receiver.Start].
func (r *Receiver) Close(ctx context.Context) {
	if !r.state.IsStarted() {
		return
	}

	r.state.CloseOnce.Do(func() {
		r.state.BeginClose()

		timeout := r.timeout
		if timeout <= 0 || timeout > CloseTimeout {
			timeout = CloseTimeout // Ensure the timeout is within acceptable bounds.
		}
		shutdownCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		if r.tcp != nil {
			slog.Debug("shutting down HTTP server", slog.String("name", r.Name), slog.String("tcp_addr", r.address))
			err := r.tcp.Shutdown(shutdownCtx) // Blocking up to timeout.
			if err != nil && !isAnyError(err, http.ErrServerClosed, context.Canceled, context.DeadlineExceeded) {
				slog.Error("HTTP server shutdown error", slog.Any("error", err),
					slog.String("name", r.Name), slog.String("tcp_addr", r.address),
				)
			}
			_ = r.tcp.Close() // In case of a [context.DeadlineExceeded] (redundant but harmless otherwise).
			r.tcp = nil
		}

		if r.udp != nil {
			slog.Debug("shutting down HTTP/3 server", slog.String("name", r.Name), slog.String("udp_addr", r.address))
			err := r.udp.Shutdown(shutdownCtx) // Blocking up to timeout.
			if err != nil && !isAnyError(err, context.Canceled, context.DeadlineExceeded) {
				slog.Error("HTTP/3 server shutdown error", slog.Any("error", err),
					slog.String("name", r.Name), slog.String("udp_addr", r.address),
				)
			}
			_ = r.udp.Close() // In case of a [context.DeadlineExceeded] (redundant but harmless otherwise).
			r.udp = nil
		}

		done := make(chan struct{})
		go func() {
			defer close(done)
			r.state.InProgress.Wait()
		}()

		select {
		case <-done:
			// All done.
		case <-shutdownCtx.Done():
			slog.Warn("closing HTTP receiver forcefully", slog.String("name", r.Name), slog.Duration("timeout", timeout))
		}

		r.state.FinishClose()
	})
}

func isAnyError(err error, targets ...error) bool {
	for _, target := range targets {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

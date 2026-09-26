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
	"sync"
	"time"

	"github.com/quic-go/quic-go/http3"

	"github.com/daabr/versipellis/pkg/config"
)

// Receiver contains all the configuration and state details for receiving HTTP requests. It's a
// composite container for HTTP/1.1 and HTTP/2 webhooks (over TCP), as well as HTTP/3 (over QUIC/UDP).
type Receiver struct {
	config.BaseReceiver

	address       string
	maxBodySize   int64
	maxHeaderSize int64
	timeout       time.Duration
	tls           *tls.Config

	tcp *http.Server
	udp *http3.Server

	closeOnce sync.Once
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
// address. This function returns immediately, and the server runs asynchronously in the background.
// The input context is used only for starting them, not to control their entire lifecycle.
func (r *Receiver) Start(ctx context.Context) bool {
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

	// Reminder: need special handling for "multipart/form-data" requests, using [http.Request.MultipartReader],
	// to stream multi-gigabyte files efficiently instead of buffering them naively.
	body, ok := readRequest(w, inReq, r.Name, r.maxBodySize)
	if !ok {
		return
	}

	if r.Send != nil {
		// Note that we ignore the incoming request's method and path, to use the sender's preconfigured values.
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

		r.Send(context.WithoutCancel(inReq.Context()), outReq) // Returns quickly (usually asynchronous internally).
	}

	slog.Debug("HTTP request received successfully", slog.String("name", r.Name),
		slog.String("proto", inReq.Proto), slog.String("method", inReq.Method),
		slog.String("remote_addr", inReq.RemoteAddr), slog.Int("content_length", len(body)),
		slog.Time("start_time", start), slog.Duration("duration", time.Since(start)),
	)
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

	w.WriteHeader(http.StatusAccepted)
	return body, true
}

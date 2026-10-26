package http

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net"
	"net/http"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

func (r *Receiver) newTCPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Addr:      r.address,
		Handler:   handler,
		TLSConfig: r.tls,

		ReadTimeout:    r.timeout,
		WriteTimeout:   r.timeout,
		IdleTimeout:    r.timeout,
		MaxHeaderBytes: int(min(r.maxHeaderSize, math.MaxInt32)),

		ErrorLog:  slog.NewLogLogger(slog.Default().Handler(), slog.LevelWarn),
		ConnState: r.connState,
	}
}

func (r *Receiver) newUDPServer(handler http.Handler) *http3.Server {
	return &http3.Server{
		Addr:      r.address,
		Handler:   handler,
		TLSConfig: r.tls,
		QUICConfig: &quic.Config{
			HandshakeIdleTimeout: min(r.timeout, defaultRequestTimeout),
			MaxIdleTimeout:       30 * time.Second,
			KeepAlivePeriod:      15 * time.Second,
		},

		IdleTimeout:    30 * time.Second,
		MaxHeaderBytes: int(min(r.maxHeaderSize, math.MaxInt32)),

		Logger: slog.Default(),
	}
}

func (r *Receiver) listenTCP(ctx context.Context) (net.Listener, bool) {
	ln, err := new(net.ListenConfig).Listen(ctx, "tcp", r.tcp.Addr)
	if err != nil {
		slog.Error("HTTP server error: failed to listen", slog.Any("error", err),
			slog.String("name", r.Name), slog.String("tcp_addr", r.tcp.Addr),
		)
		r.tcp = nil
		return nil, false
	}

	r.address = ln.Addr().String()
	return ln, true
}

func (r *Receiver) listenUDP(ctx context.Context) (net.PacketConn, bool) {
	conn, err := new(net.ListenConfig).ListenPacket(ctx, "udp", r.udp.Addr)
	if err != nil {
		slog.Error("HTTP/3 server error: failed to listen", slog.Any("error", err),
			slog.String("name", r.Name), slog.String("udp_addr", r.udp.Addr),
		)
		r.udp = nil
		return nil, false
	}

	r.address = conn.LocalAddr().String()
	return conn, true
}

func (r *Receiver) serveTCP(tcp *http.Server, ln net.Listener) bool {
	defer ln.Close()

	var err error
	if r.tls == nil {
		err = tcp.Serve(ln)
	} else {
		err = tcp.ServeTLS(ln, "", "")
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("HTTP server error", slog.Any("error", err),
			slog.String("name", r.Name), slog.String("tcp_addr", r.address),
		)
		return false
	}

	return true
}

func (r *Receiver) serveUDP(udp *http3.Server, conn net.PacketConn) bool {
	defer conn.Close()

	if err := udp.Serve(conn); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("HTTP/3 server error", slog.Any("error", err),
			slog.String("name", r.Name), slog.String("udp_addr", r.address),
		)
		return false
	}
	return true
}

func (r *Receiver) connState(_ net.Conn, state http.ConnState) {
	slog.Debug("HTTP server connection state change", slog.String("name", r.Name), slog.String("state", state.String()))
}

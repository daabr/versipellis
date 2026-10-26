package http

import (
	"crypto/tls"
	"net/http"
	"testing"
)

func TestReceiverListenTCPError(t *testing.T) {
	t.Parallel()

	r := &Receiver{}
	r.tcp = r.newTCPServer(http.DefaultServeMux)
	r.tcp.Addr = ":invalid"

	if ln, ok := r.listenTCP(t.Context()); ok {
		t.Errorf("Receiver.listenTCP() ok = true, want false")
		t.Cleanup(func() { _ = ln.Close() })
	}
}

func TestReceiverListenUDPError(t *testing.T) {
	t.Parallel()

	r := &Receiver{}
	r.udp = r.newUDPServer(http.DefaultServeMux)
	r.udp.Addr = ":invalid"

	if conn, ok := r.listenUDP(t.Context()); ok {
		t.Errorf("Receiver.listenUDP() ok = true, want false")
		t.Cleanup(func() { _ = conn.Close() })
	}
}

func TestReceiverServeTCPError(t *testing.T) {
	t.Parallel()

	r := &Receiver{}
	r.tcp = r.newTCPServer(http.DefaultServeMux)
	r.tls = &tls.Config{MinVersion: 1} //gosec:disable G402 // Intentional unit test to cause an error.

	ln, ok := r.listenTCP(t.Context())
	if !ok {
		t.Errorf("Receiver.listenTCP() ok = false, want true")
	}
	t.Cleanup(func() { _ = ln.Close() })

	if r.serveTCP(r.tcp, ln) {
		t.Errorf("Receiver.serveTCP() ok = true, want false")
	}
}

func TestReceiverServeUDPError(t *testing.T) {
	t.Parallel()

	r := &Receiver{}
	r.udp = r.newUDPServer(http.DefaultServeMux)
	r.tls = &tls.Config{MinVersion: 1} //gosec:disable G402 // Intentional unit test to cause an error.

	conn, ok := r.listenUDP(t.Context())
	if !ok {
		t.Errorf("Receiver.listenUDP() ok = false, want true")
	}
	t.Cleanup(func() { _ = conn.Close() })

	if r.serveUDP(r.udp, conn) {
		t.Errorf("Receiver.serveUDP() ok = true, want false")
	}
}

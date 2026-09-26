package http

import (
	"context"
	"crypto/tls"
	"errors"
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

func TestReceiverClose(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		receiver *Receiver
		wantOK   bool
	}{
		{
			name:     "adjust_timeout",
			receiver: &Receiver{},
			wantOK:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.receiver.Close(t.Context())
			// No panic = success.
		})
	}
}

func TestIsAnyError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		err     error
		targets []error
		want    bool
	}{
		{
			name:    "nil_error",
			err:     nil,
			targets: []error{nil},
			want:    true,
		},
		{
			name:    "non_nil_error_matches_target",
			err:     http.ErrServerClosed,
			targets: []error{context.DeadlineExceeded, http.ErrServerClosed},
			want:    true,
		},
		{
			name:    "non_nil_error_does_not_match_target",
			err:     errors.New("some error"),
			targets: []error{errors.New("another error")},
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := isAnyError(tt.err, tt.targets...); got != tt.want {
				t.Errorf("isAnyError() = %v, want %v", got, tt.want)
			}
		})
	}
}

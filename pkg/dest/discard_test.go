package dest

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDiscard(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data any
	}{
		{
			name: "nil",
			data: nil,
		},
		{
			name: "string",
			data: "test",
		},
		{
			name: "nil_http_request",
			data: (*http.Request)(nil),
		},
		{
			name: "http_request_without_body",
			data: &http.Request{},
		},
		{
			name: "http_request_with_body",
			data: &http.Request{Body: io.NopCloser(strings.NewReader("test"))},
		},
		{
			name: "nil_http_response",
			data: (*http.Response)(nil),
		},
		{
			name: "http_response_without_body",
			data: &http.Response{},
		},
		{
			name: "http_response_with_body",
			data: &http.Response{Body: io.NopCloser(strings.NewReader("test"))},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// The most we can check is that this doesn't panic.
			d := newDiscard()
			d.Send(t.Context(), tt.data)
			d.Close(t.Context())
		})
	}
}

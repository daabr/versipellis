package dest

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/daabr/versipellis/pkg/flow"
)

func TestDiscard(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data flow.Chunk
	}{
		{
			name: "nil_chunk",
			data: nil,
		},
		{
			name: "nil_http_request",
			data: flow.HTTPRequests([]*http.Request{nil}),
		},
		{
			name: "http_request_without_body",
			data: flow.HTTPRequests([]*http.Request{{}}),
		},
		{
			name: "http_request_with_body",
			data: flow.HTTPRequests([]*http.Request{{Body: io.NopCloser(strings.NewReader("test"))}}),
		},
		{
			name: "nil_http_response",
			data: flow.HTTPResponses([]*http.Response{nil}),
		},
		{
			name: "http_response_without_body",
			data: flow.HTTPResponses([]*http.Response{{}}),
		},
		{
			name: "http_response_with_body",
			data: flow.HTTPResponses([]*http.Response{{Body: io.NopCloser(strings.NewReader("test"))}}),
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

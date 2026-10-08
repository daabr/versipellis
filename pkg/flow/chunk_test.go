package flow_test

import (
	"net/http"
	"testing"

	"github.com/daabr/versipellis/pkg/flow"
)

func TestChunk(t *testing.T) {
	t.Parallel()

	// A sender's Send method receives chunks of any kind through the sealed, non-generic interface.
	kind := func(c flow.Chunk) string {
		switch c.(type) {
		case flow.Blobs:
			return "blobs"
		case flow.Structured:
			return "structured"
		case flow.HTTPRequests:
			return "reqs"
		case flow.HTTPResponses:
			return "resps"
		default:
			return "unexpected"
		}
	}

	tests := []struct {
		name     string
		chunk    flow.Chunk
		wantKind string
		wantLen  int
	}{
		{
			name:     "blob",
			chunk:    flow.Blobs{[]byte("a")},
			wantKind: "blobs",
			wantLen:  1,
		},
		{
			name:     "blobs",
			chunk:    flow.Blobs{[]byte("abc"), []byte("def")},
			wantKind: "blobs",
			wantLen:  2,
		},
		{
			name:     "nil_map",
			chunk:    flow.Structured(nil),
			wantKind: "structured",
			wantLen:  0,
		},
		{
			name:     "map",
			chunk:    flow.Structured{{"a": 1}, {"b": 2}},
			wantKind: "structured",
			wantLen:  2,
		},
		{
			name:     "http_request",
			chunk:    flow.HTTPRequests{new(http.Request)},
			wantKind: "reqs",
			wantLen:  1,
		},
		{
			name:     "http_responses",
			chunk:    flow.HTTPResponses{new(http.Response), new(http.Response)},
			wantKind: "resps",
			wantLen:  2,
		},
		{
			name:     "fake_data_type",
			chunk:    flow.FakeDataType{},
			wantKind: "unexpected",
			wantLen:  1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := kind(tt.chunk); got != tt.wantKind {
				t.Errorf("chunk kind = %q, want %q", got, tt.wantKind)
			}
			if got := tt.chunk.Len(); got != tt.wantLen {
				t.Errorf("Chunk.Len() = %d, want %d", got, tt.wantLen)
			}
		})
	}
}

func TestIsEmpty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		chunk flow.Chunk
		want  bool
	}{
		{
			name:  "nil_interface", // Calling Len on it would panic.
			chunk: nil,
			want:  true,
		},
		{
			name:  "nil_chunk",
			chunk: flow.Structured(nil),
			want:  true,
		},
		{
			name:  "empty_chunk",
			chunk: flow.HTTPRequests{},
			want:  true,
		},
		{
			name:  "non_empty_chunk",
			chunk: flow.Blobs{[]byte("a")},
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := flow.IsEmpty(tt.chunk); got != tt.want {
				t.Errorf("flow.IsEmpty() = %v, want %v", got, tt.want)
			}
		})
	}
}

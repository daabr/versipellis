package http

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/daabr/versipellis/pkg/config"
)

func TestNewDestination(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      map[string]any
		baseType string
		wantErr  bool
	}{
		{
			name:     "nil_config",
			cfg:      nil,
			baseType: config.SenderTypeHTTP,
			wantErr:  true,
		},
		{
			name:     "missing_url",
			cfg:      map[string]any{},
			baseType: config.SenderTypeHTTP,
			wantErr:  true,
		},
		{
			name:     "invalid_url",
			cfg:      map[string]any{"url": "not-a-url"},
			baseType: config.SenderTypeHTTP,
			wantErr:  true,
		},
		{
			name:     "invalid_method",
			cfg:      map[string]any{"url": "https://example.com", "method": "BLAH"},
			baseType: config.SenderTypeHTTP,
			wantErr:  true,
		},
		{
			name:     "get_method_not_allowed",
			cfg:      map[string]any{"url": "https://example.com", "method": "GET"},
			baseType: config.SenderTypeHTTP,
			wantErr:  true,
		},
		{
			name:     "invalid_query",
			cfg:      map[string]any{"url": "https://example.com", "query": "invalid"},
			baseType: config.SenderTypeHTTP,
			wantErr:  true,
		},
		{
			name:     "invalid_headers",
			cfg:      map[string]any{"url": "https://example.com", "headers": "invalid"},
			baseType: config.SenderTypeHTTP,
			wantErr:  true,
		},
		{
			name:     "http_url_with_tls_config",
			cfg:      map[string]any{"url": "http://example.com", "tls": map[string]any{"min_version": "1.3"}},
			baseType: config.SenderTypeHTTP,
			wantErr:  false, // Warning log.
		},
		{
			name:     "invalid_timeout",
			cfg:      map[string]any{"url": "https://example.com", "timeout": "not-a-duration"},
			baseType: config.SenderTypeHTTP,
			wantErr:  true,
		},
		{
			name:     "invalid_retries",
			cfg:      map[string]any{"url": "https://example.com", "retries": "invalid"},
			baseType: config.SenderTypeHTTP,
			wantErr:  true,
		},
		{
			name:     "invalid_tls",
			cfg:      map[string]any{"url": "https://example.com", "tls": "invalid"},
			baseType: config.SenderTypeHTTP,
			wantErr:  true,
		},
		{
			name:     "unexpected_base_type",
			cfg:      map[string]any{"url": "https://example.com/path"},
			baseType: "unexpected_type",
			wantErr:  true,
		},
		{
			name:     "valid_minimal_config",
			cfg:      map[string]any{"url": "https://example.com/path"},
			baseType: config.SenderTypeHTTP,
			wantErr:  false,
		},
		{
			name: "valid_full_config",
			cfg: map[string]any{
				"url":     "https://example.com/path",
				"method":  http.MethodPatch,
				"timeout": "10s",
				"headers": map[string]any{
					"Authorization": "Bearer token123",
				},
				"retries": map[string]any{
					"type":         retryTypeStatic,
					"max_attempts": int64(2),
					"interval":     "100ms",
				},
			},
			baseType: config.SenderTypeHTTP,
			wantErr:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, gotErr := NewDestination(tt.cfg, tt.name, tt.baseType)
			if (gotErr != nil) != tt.wantErr {
				t.Errorf("NewDestination() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
		})
	}
}

func TestNewDestinationHTTP3(t *testing.T) {
	t.Parallel()

	_, err := NewDestination(
		map[string]any{"url": "https://example.com"}, "TestNewDestinationHTTP3", config.SenderTypeHTTP3,
	)
	if err != nil {
		t.Fatalf("NewDestination() error = %v, wantErr %v", err, false)
	}
}

func TestDestinationSendBatch(t *testing.T) {
	t.Parallel()

	var counter atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		switch {
		case err != nil:
			t.Errorf("failed to read request body: %v", err)
		case len(body) == 0:
			t.Errorf("received empty request body")
		default:
			counter.Add(1)
		}
	}))
	t.Cleanup(server.Close)

	d, err := NewDestination(map[string]any{"url": server.URL}, "TestDestinationSendBatch", config.SenderTypeHTTP)
	if d == nil {
		t.Fatalf("NewDestination() error = %v", err)
	}

	// Aside: cover the logic for not sending an empty byte slice.
	d.Send(t.Context(), []byte{})

	// Step 1: start batch.
	d.Send(t.Context(), nil)

	// Reminder: join multiple parts into a single batch.

	// Step 3: finalize & send batch.
	d.Send(t.Context(), nil)

	want := int64(0) // Reminder: change to 1 when batches are implemented.
	if got := counter.Load(); got != want {
		t.Errorf("counter = %d, want %d", got, want)
	}
}

func TestSerializeDataHTTPRequest(t *testing.T) {
	t.Parallel()

	u, _ := url.Parse("http://example.com/?p1=good")

	tests := []struct {
		name      string
		req       *http.Request
		hdr       http.Header
		wantBody  bool
		wantErr   bool
		wantHdr   http.Header
		wantQuery string
	}{
		{
			name:     "nil",
			req:      nil,
			hdr:      http.Header{},
			wantBody: false,
			wantErr:  true,
		},
		{
			name: "modify_some_headers_and_params",
			req: &http.Request{
				Header: http.Header{"H1": {"bad"}, "H2": {"good"}},
				URL:    &url.URL{RawQuery: "p1=bad&p2=good"},
			},
			hdr:       http.Header{"H1": {"good"}},
			wantBody:  false,
			wantErr:   false,
			wantHdr:   http.Header{"H1": {"good"}, "H2": {"good"}},
			wantQuery: "p1=good&p2=good",
		},
		{
			name:      "req_with_body",
			req:       &http.Request{URL: &url.URL{}},
			hdr:       http.Header{},
			wantBody:  true,
			wantErr:   false,
			wantHdr:   http.Header{},
			wantQuery: "p1=good",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotURL := u.Clone()
			gotHdr := tt.hdr.Clone()
			if tt.wantBody {
				tt.req.Body = io.NopCloser(strings.NewReader("test"))
			}

			gotBody, gotErr := serializeData(tt.req, gotURL, gotHdr)

			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("serializeData() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
			gotQuery := gotURL.Query()

			wantBody := ""
			if tt.wantBody {
				wantBody = "test"
			}
			if tt.wantBody != (string(gotBody) == "test") {
				t.Errorf("body = %v, want %q", gotBody, wantBody)
			}
			if tt.req != nil && !reflect.DeepEqual(gotHdr, tt.wantHdr) {
				t.Errorf("hdr = %+v, want %+v", gotHdr, tt.wantHdr)
			}
			if tt.req != nil && gotQuery.Encode() != tt.wantQuery {
				t.Errorf("query = %+v, want %q", gotQuery, tt.wantQuery)
			}
		})
	}
}

func TestSerializeDataHTTPResponse(t *testing.T) {
	t.Parallel()

	u, _ := url.Parse("http://example.com/")

	tests := []struct {
		name     string
		resp     *http.Response
		hdr      http.Header
		wantBody bool
		wantErr  bool
		wantHdr  http.Header
	}{
		{
			name:     "nil",
			resp:     nil,
			hdr:      http.Header{},
			wantBody: false,
			wantErr:  true,
		},
		{
			name: "modify_some_headers",
			resp: &http.Response{
				Header: http.Header{"H1": {"bad"}, "H2": {"good"}},
			},
			hdr:      http.Header{"H1": {"good"}},
			wantBody: false,
			wantErr:  false,
			wantHdr:  http.Header{"H1": {"good"}, "H2": {"good"}},
		},
		{
			name:     "resp_with_body",
			resp:     &http.Response{},
			hdr:      http.Header{},
			wantBody: true,
			wantErr:  false,
			wantHdr:  http.Header{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotURL := u.Clone()
			gotHdr := tt.hdr.Clone()
			if tt.wantBody {
				tt.resp.Body = io.NopCloser(strings.NewReader("test"))
			}

			gotBody, gotErr := serializeData(tt.resp, gotURL, gotHdr)

			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("serializeData() error = %v, wantErr %v", gotErr, tt.wantErr)
			}

			wantBody := ""
			if tt.wantBody {
				wantBody = "test"
			}
			if tt.wantBody != (string(gotBody) == "test") {
				t.Errorf("body = %v, want %q", gotBody, wantBody)
			}
			if tt.resp != nil && !reflect.DeepEqual(gotHdr, tt.wantHdr) {
				t.Errorf("headers = %+v, want %+v", gotHdr, tt.wantHdr)
			}
		})
	}
}

func TestSerializeDataJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		data     any
		wantBody string
		wantErr  bool
	}{
		{
			name:     "nil",
			data:     nil,
			wantErr:  false,
			wantBody: "null\n",
		},
		{
			name:     "map",
			data:     map[string]any{"key": "value"},
			wantBody: `{"key":"value"}` + "\n",
			wantErr:  false,
		},
		{
			name:     "json_with_unencoded_html",
			data:     map[string]any{"html": "& < >"},
			wantBody: `{"html":"& < >"}` + "\n",
			wantErr:  false,
		},
		{
			name:    "json_error",
			data:    map[string]any{"channel": make(chan struct{})}, // Go channels cannot be encoded as JSON.
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			hdr := http.Header{}
			got, gotErr := serializeData(tt.data, nil, hdr)
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("serializeData() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
			if tt.wantErr {
				return
			}

			if gotBody := string(got); gotBody != tt.wantBody {
				t.Errorf("serializeData() body = %q, want %q", gotBody, tt.wantBody)
			}
			wantHdr := http.Header{"Content-Type": {"application/json"}}
			if !reflect.DeepEqual(hdr, wantHdr) {
				t.Errorf("headers = %+v, want %+v", hdr, wantHdr)
			}
		})
	}
}

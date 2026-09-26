package dest

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestStdout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data any
		want string
	}{
		{
			name: "nil",
			data: nil,
			want: "",
		},
		{
			name: "int",
			data: 42,
			want: "42\n",
		},
		{
			name: "json",
			data: map[string]any{"key": "value", "number": 42, "list": []any{1, 2, 3}},
			want: `{"key":"value","list":[1,2,3],"number":42}` + "\n",
		},
		{
			name: "ndjson",
			data: []map[string]any{
				{"key1": "value1"},
				{"key2": "value2"},
			},
			want: `{"key1":"value1"}` + "\n" + `{"key2":"value2"}` + "\n",
		},
		{
			name: "not_json",
			data: map[string]any{"channel": make(chan struct{})}, // Go channels cannot be encoded as JSON.
			want: "",                                             // Log this, but don't pollute [os.Stdout] with non-JSON text.
		},
		{
			name: "not_ndjson",
			data: []map[string]any{
				{"key1": "value1"},
				{"channel": make(chan struct{})}, // Go channels cannot be encoded as JSON.
				{"key1": "value1"},
			},
			want: `{"key1":"value1"}` + "\n", // Fail on first error.
		},
		// After the "not_[nd]json" test cases, to ensure it doesn't leave [encoder] in a broken state.
		{
			name: "string",
			data: "just a string",
			want: `"just a string"` + "\n",
		},
		{
			name: "unencoded_html",
			data: "& < >",
			want: `"& < >"` + "\n",
		},
		{
			name: "http_request_with_body",
			data: func() *http.Request {
				body := strings.NewReader("body")
				req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://example.com", body)
				return req
			}(),
			want: "POST / HTTP/1.1\r\nHost: example.com\r\nUser-Agent: Go-http-client/1.1\r\nContent-Length: 4\r\n\r\nbody",
		},
		{
			name: "http_request_without_body",
			data: func() *http.Request {
				req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://example.com", http.NoBody)
				return req
			}(),
			want: "POST / HTTP/1.1\r\nHost: example.com\r\nUser-Agent: Go-http-client/1.1\r\nContent-Length: 0\r\n\r\n",
		},
		{
			name: "http_response",
			data: func() *http.Response {
				body := strings.NewReader("body")
				req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com/path", body)
				return &http.Response{
					Request:    req,
					Status:     "200 OK",
					StatusCode: http.StatusOK,
					Proto:      "HTTP/1.1",
					ProtoMajor: 1,
					ProtoMinor: 1,
					Body:       io.NopCloser(body),
				}
			}(), //nolint:bodyclose // Closed by [stdoutSender.Send] in unit test.
			want: "HTTP/1.1 200 OK\r\nConnection: close\r\n\r\nbody",
		},
		{
			name: "nil_http_request",
			data: (*http.Request)(nil),
			want: "",
		},
		{
			name: "nil_http_response",
			data: (*http.Response)(nil),
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fakeStdout := new(strings.Builder)
			s := new(stdoutSender{writer: fakeStdout})
			s.Send(t.Context(), tt.data)
			s.Close(t.Context())

			if got := fakeStdout.String(); got != tt.want {
				t.Errorf("Stdout(%s) stdout = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestStdoutConcurrency(t *testing.T) {
	t.Parallel()

	fakeStdout := new(bytes.Buffer)
	sender := new(stdoutSender{writer: fakeStdout})
	t.Cleanup(func() { sender.Close(t.Context()) })

	const concurrencyFactor = 100

	var wg sync.WaitGroup
	for goroutine := range concurrencyFactor {
		wg.Go(func() {
			for call := range concurrencyFactor {
				sender.Send(t.Context(), map[string]any{"goroutine": goroutine, "call": call})
			}
		})
	}
	wg.Wait()

	seen := [concurrencyFactor][concurrencyFactor]int{}
	gotLines := 0

	output, err := fakeStdout.ReadBytes('\n')
	for err == nil {
		gotLines++
		gotLine := new(struct {
			G int `json:"goroutine"`
			C int `json:"call"`
		})
		if err = json.Unmarshal(output, gotLine); err != nil {
			t.Fatalf("invalid JSON in line %d (%s): %v", gotLines, output, err)
		}

		if n := concurrencyFactor; gotLine.G < 0 || gotLine.G >= n || gotLine.C < 0 || gotLine.C >= n {
			t.Fatalf("invalid index(es) in line %d: %+v", gotLines, gotLine)
		}
		seen[gotLine.G][gotLine.C]++

		output, err = fakeStdout.ReadBytes('\n')
	}
	if !errors.Is(err, io.EOF) {
		t.Fatalf("error reading from buffer: %v", err)
	}

	if wantLines := concurrencyFactor * concurrencyFactor; gotLines != wantLines {
		t.Errorf("got %d lines in total, want %d", gotLines, wantLines)
	}
	for g := range concurrencyFactor {
		for c := range concurrencyFactor {
			if seen[g][c] != 1 {
				t.Errorf("goroutine %d, call %d: seen %d times, want 1", g+1, c+1, seen[g][c])
			}
		}
	}
}

func TestStdoutSendDuringClose(t *testing.T) {
	t.Parallel()

	fakeStdout := new(strings.Builder)
	s := new(stdoutSender{writer: fakeStdout})
	s.Close(t.Context())

	s.Send(t.Context(), "should be dropped")
	if got := fakeStdout.String(); got != "" {
		t.Errorf("Send() after Close() wrote %q, want empty", got)
	}
}

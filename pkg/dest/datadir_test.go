package dest

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestDeadLetterQueue(t *testing.T) {
	tests := []struct {
		name     string
		data     any
		want     string
		wantSkip bool
	}{
		{
			name:     "nil",
			data:     nil,
			wantSkip: true,
		},
		{
			name: "bytes",
			data: []byte("payload"),
			want: "payload",
		},
		{
			name: "json",
			data: map[string]any{"key": "value", "number": 42, "list": []any{1, 2, 3}},
			want: `{"key":"value","list":[1,2,3],"number":42}` + "\n",
		},
		{
			name: "json_with_unencoded_html",
			data: map[string]any{"html": "& < >"},
			want: `{"html":"& < >"}` + "\n",
		},
		{
			name:     "not_json",
			data:     map[string]any{"channel": make(chan struct{})}, // Go channels cannot be encoded as JSON.
			wantSkip: true,
		},
		{
			name:     "nil_http_request",
			data:     (*http.Request)(nil),
			wantSkip: true,
		},
		{
			name: "http_request_without_body",
			data: func() *http.Request {
				req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com", nil)
				return req
			}(),
			want: "GET / HTTP/1.1\r\nHost: example.com\r\nUser-Agent: Go-http-client/1.1\r\n\r\n",
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
			name:     "nil_http_response",
			data:     (*http.Response)(nil),
			wantSkip: true,
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
			}(), //nolint:bodyclose // Unit test.
			want: "HTTP/1.1 200 OK\r\nConnection: close\r\n\r\nbody",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())

			DeadLetterQueue(t.Context(), tt.data)
			dlqInProgress.Wait()

			got := ""
			err := filepath.WalkDir(dataDir, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				f, err := os.ReadFile(path) //gosec:disable G122 G304 // Unit test.
				if err != nil {
					return err
				}
				got = string(f)
				return nil
			})
			if (err != nil) != tt.wantSkip {
				t.Fatalf("filepath.WalkDir(%s) error = %v, file = %q, wantSkip %v, ", tt.name, err, got, tt.wantSkip)
			}

			if tt.wantSkip && got != "" {
				t.Errorf("DeadLetterQueue(%s) = %q, want to skip", tt.name, got)
			} else if got != tt.want {
				t.Errorf("DeadLetterQueue(%s) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

const (
	goroutines        = 100
	callsPerGoroutine = 100
)

func TestDeadLetterQueueConcurrency(t *testing.T) {
	t.Chdir(t.TempDir())

	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Go(func() {
			for i := range callsPerGoroutine {
				DeadLetterQueue(t.Context(), map[string]any{"goroutine": g, "call": i})
			}
		})
	}
	wg.Wait()
	dlqInProgress.Wait()

	seen := [goroutines][callsPerGoroutine]int{}
	gotLines := 0

	err := filepath.WalkDir(dataDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		f, err := os.ReadFile(path) //gosec:disable G122 G304 // Purely for testing.
		if err != nil {
			return err
		}
		got := new(struct {
			Goroutine int `json:"goroutine"`
			Call      int `json:"call"`
		})
		if err := json.Unmarshal(f, got); err != nil {
			return err //nolint:wrapcheck // Purely for testing.
		}

		gotLines++
		if got.Goroutine < 0 || got.Goroutine >= goroutines || got.Call < 0 || got.Call >= callsPerGoroutine {
			t.Errorf("invalid index(es) in line %d: %+v", gotLines, got)
		}

		seen[got.Goroutine][got.Call]++
		if seen[got.Goroutine][got.Call] > 1 {
			t.Errorf("instance no. %d of: goroutine %d, call %d", seen[got.Goroutine][got.Call], got.Goroutine, got.Call)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("filepath.WalkDir(%s) error = %v", t.Name(), err)
	}

	wantLines := goroutines * callsPerGoroutine
	if gotLines != wantLines {
		t.Errorf("got %d lines in total, want %d", gotLines, wantLines)
	}
}

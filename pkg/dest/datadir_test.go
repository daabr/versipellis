package dest

import (
	"encoding/json"
	"io"
	"io/fs"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDeadLetterQueue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		lameDuck bool
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
			name:     "empty_byte_slice",
			data:     []byte(""),
			wantSkip: true,
		},
		{
			name:     "send_during_close",
			lameDuck: true,
			data:     []byte("payload"),
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
			t.Parallel()

			tempDir := t.TempDir()
			dlq := InitDeadLetterQueue(tempDir)
			if dlq == nil {
				t.Fatalf("failed to initialize DeadLetterQueue")
			}
			if tt.lameDuck {
				dlq.lameDuck.Store(true)
			}
			dlq.Send(t.Context(), tt.data)
			dlq.Close(t.Context())

			got := ""
			err := filepath.WalkDir(tempDir, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				f, err := os.ReadFile(path) //gosec:disable G122 G304 // Unit test.
				if err != nil {
					t.Fatalf("failed to read file %q: %v", path, err)
				}
				got = string(f)
				return nil
			})
			if err != nil {
				t.Fatalf("filepath.WalkDir(%s) error = %v", tt.name, err)
			}

			if tt.wantSkip && got != "" {
				t.Errorf("DeadLetterQueue(%s) = %q, want no file", tt.name, got)
			} else if got != tt.want {
				t.Errorf("DeadLetterQueue(%s) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestDeadLetterQueueConcurrency(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	dlq := InitDeadLetterQueue(tempDir)
	if dlq == nil {
		t.Fatalf("failed to initialize DeadLetterQueue")
	}
	t.Cleanup(func() { dlq.Close(t.Context()) })

	const concurrencyFactor = 10

	var wg sync.WaitGroup
	for goroutine := range concurrencyFactor {
		wg.Go(func() {
			for call := range concurrencyFactor {
				dlq.Send(t.Context(), map[string]any{"goroutine": goroutine, "call": call})
			}
		})
	}
	wg.Wait()
	dlq.inProgress.Wait()

	seen := [concurrencyFactor][concurrencyFactor]int{}
	gotFiles := 0

	err := filepath.WalkDir(tempDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		f, err := os.ReadFile(path) //gosec:disable G122 G304 // Self-generated path for testing.
		if err != nil {
			return err
		}

		gotFile := new(struct {
			G int `json:"goroutine"`
			C int `json:"call"`
		})
		if err = json.Unmarshal(f, gotFile); err != nil {
			t.Fatalf("invalid JSON in file %q: %v", path, err)
		}

		if n := concurrencyFactor; gotFile.G < 0 || gotFile.G >= n || gotFile.C < 0 || gotFile.C >= n {
			t.Fatalf("invalid index(es) in file %q: %+v", path, gotFile)
		}

		seen[gotFile.G][gotFile.C]++
		gotFiles++
		return nil
	})
	if err != nil {
		t.Fatalf("filepath.WalkDir(%s) error = %v", t.Name(), err)
	}

	if wantFiles := concurrencyFactor * concurrencyFactor; gotFiles != wantFiles {
		t.Errorf("got %d files in total, want %d", gotFiles, wantFiles)
	}
	for g := range concurrencyFactor {
		for c := range concurrencyFactor {
			if seen[g][c] != 1 {
				t.Errorf("goroutine %d, call %d: seen %d times, want 1", g+1, c+1, seen[g][c])
			}
		}
	}
}

func TestAsyncWriteFileErrors(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	dlq := InitDeadLetterQueue(tempDir)
	if dlq == nil {
		t.Fatalf("failed to initialize DeadLetterQueue")
	}
	t.Cleanup(func() { dlq.Close(t.Context()) })

	tests := []struct {
		name      string
		dirPerms  os.FileMode
		filePerms os.FileMode
		wantOK    bool
	}{
		{"valid_perms", dirPermissions, filePermissions, true},
		{"invalid_dir_perms", os.FileMode(math.MaxUint32), filePermissions, false},
		{"invalid_file_perms", dirPermissions, os.FileMode(math.MaxUint32), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotOK := dlq.asyncWriteFile([]byte("data"), time.Now().UTC(), tt.dirPerms, tt.filePerms)
			if gotOK != tt.wantOK {
				t.Errorf("asyncWriteFile() = %v, want %v", gotOK, tt.wantOK)
			}
		})
	}
}

package dest

import (
	"encoding/json/v2"
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
		name        string
		lameDuck    bool
		data        any
		wantFiles   int
		wantContent string
	}{
		{
			name:      "nil",
			data:      nil,
			wantFiles: 0,
		},
		{
			name:      "empty_byte_slice",
			data:      []byte(""),
			wantFiles: 0,
		},
		{
			name:      "send_during_close",
			lameDuck:  true,
			data:      []byte("payload"),
			wantFiles: 0,
		},
		{
			name:        "bytes",
			data:        []byte("payload"),
			wantFiles:   1,
			wantContent: "payload",
		},
		{
			name:        "json",
			data:        map[string]any{"key": "value", "number": 42, "list": []any{1, 2, 3}},
			wantFiles:   1,
			wantContent: `{"key":"value","list":[1,2,3],"number":42}`,
		},
		{
			name:        "json_with_unencoded_html",
			data:        map[string]any{"html": "& < >"},
			wantFiles:   1,
			wantContent: `{"html":"& < >"}`,
		},
		{
			name:      "not_json",
			data:      map[string]any{"channel": make(chan struct{})}, // Go channels cannot be encoded as JSON.
			wantFiles: 0,
		},
		{
			name:      "nil_http_request",
			data:      (*http.Request)(nil),
			wantFiles: 0,
		},
		{
			name: "http_request_without_body",
			data: func() *http.Request {
				req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com", http.NoBody)
				return req
			}(),
			wantFiles:   1,
			wantContent: "GET / HTTP/1.1\r\nHost: example.com\r\nUser-Agent: Go-http-client/1.1\r\n\r\n",
		},
		{
			name: "http_request_with_body",
			data: func() *http.Request {
				body := strings.NewReader("body")
				req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://example.com", body)
				return req
			}(),
			wantFiles:   1,
			wantContent: "POST / HTTP/1.1\r\nHost: example.com\r\nUser-Agent: Go-http-client/1.1\r\nContent-Length: 4\r\n\r\nbody",
		},
		{
			name:      "nil_http_response",
			data:      (*http.Response)(nil),
			wantFiles: 0,
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
			}(), //nolint:bodyclose // Closed by [DeadLetterQueue.Send] in unit test.
			wantFiles:   1,
			wantContent: "HTTP/1.1 200 OK\r\nConnection: close\r\n\r\nbody",
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

			gotFiles := 0
			gotContent := ""
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
				gotFiles++
				gotContent = string(f)
				return nil
			})
			if err != nil {
				t.Fatalf("filepath.WalkDir(%s) error = %v", tt.name, err)
			}

			if gotFiles != tt.wantFiles {
				t.Errorf("DeadLetterQueue(%s) = %d files, want %d files", tt.name, gotFiles, tt.wantFiles)
			} else if gotContent != tt.wantContent {
				t.Errorf("DeadLetterQueue(%s) = %q, want %q", tt.name, gotContent, tt.wantContent)
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

			tempDir := t.TempDir()
			dlq := InitDeadLetterQueue(tempDir)
			if dlq == nil {
				t.Fatalf("failed to initialize DeadLetterQueue")
			}
			t.Cleanup(func() { dlq.Close(t.Context()) })

			gotOK := dlq.asyncWriteFile([]byte("data"), time.Now().UTC(), tt.dirPerms, tt.filePerms)
			if gotOK != tt.wantOK {
				t.Errorf("asyncWriteFile() = %v, want %v", gotOK, tt.wantOK)
			}
		})
	}
}

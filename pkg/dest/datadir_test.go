package dest

import (
	"cmp"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"io/fs"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/daabr/versipellis/pkg/codec"
	"github.com/daabr/versipellis/pkg/config"
	"github.com/daabr/versipellis/pkg/flow"
)

func TestDeadLetterQueue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		lameDuck    bool
		data        flow.Chunk
		format      config.Format
		wantFiles   int
		wantContent string
		wantExt     string
	}{
		{
			name:      "nil_chunk",
			data:      nil,
			wantFiles: 0,
		},
		{
			name:      "empty_byte_slice",
			data:      flow.Blobs([][]byte{[]byte("")}),
			wantFiles: 0,
		},
		{
			name:      "send_during_close",
			lameDuck:  true,
			data:      flow.Blobs([][]byte{[]byte("payload")}),
			wantFiles: 0,
		},
		{
			name:        "byte_slice",
			data:        flow.Blobs([][]byte{[]byte("payload")}),
			wantFiles:   1,
			wantContent: "payload",
		},
		{
			name:        "byte_slice_raw_format",
			data:        flow.Blobs([][]byte{[]byte("payload")}),
			format:      codec.FormatRaw,
			wantFiles:   1,
			wantContent: "payload",
			wantExt:     ".raw",
		},
		{
			name:        "printable_unicode_byte_slice",
			data:        flow.Blobs([][]byte{[]byte("πayload")}),
			wantFiles:   1,
			wantContent: "πayload",
		},
		{
			name:        "non_printable_unicode_byte_slice",
			data:        flow.Blobs([][]byte{[]byte("\x00\x01\x02")}),
			wantFiles:   1,
			wantContent: "\x00\x01\x02",
		},
		{
			name:        "byte_slices",
			data:        flow.Blobs([][]byte{[]byte("\x00\x01\x02"), []byte("\x00\x01\x02"), []byte("\x00\x01\x02")}),
			wantFiles:   3,
			wantContent: "\x00\x01\x02\x00\x01\x02\x00\x01\x02",
		},
		{
			name:        "json_objects",
			wantExt:     ".json",
			data:        flow.JSON{jsontext.Value(`{"key":"value","number":42,"list":[1,2,3]}`)},
			wantFiles:   1,
			wantContent: `{"key":"value","number":42,"list":[1,2,3]}`,
		},
		{
			name:        "json_objects_batch",
			wantExt:     ".json",
			data:        flow.JSON{jsontext.Value(`{"z":1}`), jsontext.Value("{\n \"a\": 2\n}")},
			wantFiles:   1,
			wantContent: `[{"z":1},{"a":2}]`,
		},
		{
			name:        "json_objects_ndjson_format",
			wantExt:     ".ndjson",
			data:        flow.JSON{jsontext.Value(`{"a":1}`), jsontext.Value(`{"b":2}`)},
			format:      codec.FormatNDJSON,
			wantFiles:   1,
			wantContent: `{"a":1}` + "\n" + `{"b":2}` + "\n",
		},
		{
			name:      "invalid_json_object",
			data:      flow.JSON{jsontext.Value(`{"a":}`)},
			wantFiles: 0,
		},
		{
			name:        "rows",
			wantExt:     ".json",
			data:        flow.Rows{{Cols: []string{"key", "number"}, Vals: []any{"value", 42}}},
			wantFiles:   1,
			wantContent: `{"key":"value","number":42}`,
		},
		{
			name:        "rows_batch_with_unencoded_html",
			wantExt:     ".json",
			data:        flow.Rows{{Cols: []string{"html"}, Vals: []any{"& < >"}}, {Cols: []string{"n"}, Vals: []any{1}}},
			wantFiles:   1,
			wantContent: `[{"html":"& < >"},{"n":1}]`,
		},
		{
			name:        "rows_json_array_format",
			wantExt:     ".json_array",
			data:        flow.Rows{{Cols: []string{"a"}, Vals: []any{1}}},
			format:      codec.FormatJSONArray,
			wantFiles:   1,
			wantContent: `[{"a":1}]`,
		},
		{
			name:        "rows_ndjson_format",
			wantExt:     ".ndjson",
			data:        flow.Rows{{Cols: []string{"a"}, Vals: []any{1}}, {Cols: []string{"b"}, Vals: []any{2}}},
			format:      codec.FormatNDJSON,
			wantFiles:   1,
			wantContent: `{"a":1}` + "\n" + `{"b":2}` + "\n",
		},
		{
			name:        "rows_raw_format_falls_back_to_json",
			wantExt:     ".json",
			data:        flow.Rows{{Cols: []string{"a"}, Vals: []any{1}}, {Cols: []string{"b"}, Vals: []any{2}}},
			format:      codec.FormatRaw,
			wantFiles:   1,
			wantContent: `[{"a":1},{"b":2}]`,
		},
		{
			// Go channels cannot be encoded as JSON.
			name:      "unencodable_row",
			data:      flow.Rows{{Cols: []string{"channel"}, Vals: []any{make(chan struct{})}}},
			wantFiles: 0,
		},
		{
			name:    "unencodable_row_in_batch_falls_back_to_readable_ndjson",
			wantExt: ".lossy_ndjson",
			data: flow.Rows{
				{Cols: []string{"a"}, Vals: []any{"ok"}},
				{Cols: []string{"a"}, Vals: []any{make(chan struct{})}},
			},
			wantFiles:   1,
			wantContent: `{"a": "ok"}` + "\n",
		},
		{
			name:    "invalid_utf8_row_falls_back_to_readable_ndjson",
			wantExt: ".lossy_ndjson",
			data: flow.Rows{
				{Cols: []string{"a"}, Vals: []any{"ok"}},
				{Cols: []string{"a"}, Vals: []any{"\xff"}},
			},
			wantFiles:   1,
			wantContent: `{"a": "ok"}` + "\n" + "{\"a\": \"\uFFFD\"}\n",
		},
		{
			name:      "nil_http_request",
			data:      flow.HTTPRequests([]*http.Request{nil}),
			wantFiles: 0,
		},
		{
			name:    "http_request_without_body",
			wantExt: ".http_req",
			data: flow.HTTPRequests([]*http.Request{
				func() *http.Request {
					req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com", http.NoBody)
					return req
				}(),
			}),
			wantFiles:   1,
			wantContent: "GET / HTTP/1.1\r\nHost: example.com\r\nUser-Agent: Go-http-client/1.1\r\n\r\n",
		},
		{
			name:    "http_request_with_body",
			wantExt: ".http_req",
			data: flow.HTTPRequests([]*http.Request{
				func() *http.Request {
					body := strings.NewReader("body")
					req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://example.com", body)
					return req
				}(),
			}),
			wantFiles:   1,
			wantContent: "POST / HTTP/1.1\r\nHost: example.com\r\nUser-Agent: Go-http-client/1.1\r\nContent-Length: 4\r\n\r\nbody",
		},
		{
			name:      "http_request_write_error", // No URL or host.
			data:      flow.HTTPRequests([]*http.Request{{Method: http.MethodGet, Body: http.NoBody}}),
			wantFiles: 0,
		},
		{
			name:      "nil_http_response",
			data:      flow.HTTPResponses([]*http.Response{nil}),
			wantFiles: 0,
		},
		{
			name: "http_response_write_error",
			data: flow.HTTPResponses([]*http.Response{{
				StatusCode: http.StatusOK,
				ProtoMajor: 1,
				ProtoMinor: 1,
				Body:       io.NopCloser(iotest.ErrReader(errors.New("read error"))),
			}}),
			wantFiles: 0,
		},
		{
			name:    "http_response",
			wantExt: ".http_resp",
			data: flow.HTTPResponses([]*http.Response{
				func() *http.Response {
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
			}),
			wantFiles:   1,
			wantContent: "HTTP/1.1 200 OK\r\nConnection: close\r\n\r\nbody",
		},
		{
			name:    "http_request_with_credentials",
			wantExt: ".http_req",
			data: flow.HTTPRequests([]*http.Request{
				func() *http.Request {
					// URL userinfo is never written by [http.Request.Write], so it's not redacted explicitly.
					u := "http://user:pass@example.com/p?a=1&api_key=k&sig=s&b=%20" //gosec:disable G101 // Unit test.
					req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, u, strings.NewReader("token=body"))
					req.Header.Set("Authorization", "Bearer abc.def")
					req.Header.Set("Cookie", "a=1; b=2")
					req.Header.Set("X-Api-Key", "k")
					req.Header.Set("X-Custom", "keep")
					return req
				}(),
			}),
			wantFiles: 1,
			wantContent: "POST /p?a=1&api_key=REDACTED&sig=REDACTED&b=%20 HTTP/1.1\r\nHost: example.com\r\n" +
				"User-Agent: Go-http-client/1.1\r\nContent-Length: 10\r\nAuthorization: Bearer REDACTED\r\n" +
				"Cookie: a=REDACTED; b=REDACTED\r\nX-Api-Key: REDACTED\r\nX-Custom: keep\r\n\r\ntoken=body",
		},
		{
			name:    "http_response_with_credentials",
			wantExt: ".http_resp",
			data: flow.HTTPResponses([]*http.Response{
				{
					Status:     "200 OK",
					StatusCode: http.StatusOK,
					Proto:      "HTTP/1.1",
					ProtoMajor: 1,
					ProtoMinor: 1,
					Header: http.Header{
						"Content-Type": {"text/plain"},
						"Set-Cookie":   {"sid=1; Path=/; HttpOnly", "theme=dark"},
					},
					Body: io.NopCloser(strings.NewReader("body")),
				},
			}),
			wantFiles: 1,
			wantContent: "HTTP/1.1 200 OK\r\nConnection: close\r\nContent-Type: text/plain\r\n" +
				"Set-Cookie: sid=REDACTED; Path=/; HttpOnly\r\nSet-Cookie: theme=REDACTED\r\n\r\nbody",
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
			dlq.Send(t.Context(), tt.data, cmp.Or(tt.format, codec.FormatDefault))
			dlq.Close(t.Context())

			gotFiles := 0
			var gotContent strings.Builder
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
				gotExt := filepath.Ext(path)
				if gotExt != tt.wantExt {
					t.Errorf("DeadLetterQueue(%s) file extension = %q, want %q", tt.name, gotExt, tt.wantExt)
				}
				gotFiles++
				gotContent.Write(f)
				return nil
			})
			if err != nil {
				t.Fatalf("filepath.WalkDir(%s) error = %v", tt.name, err)
			}

			if gotFiles != tt.wantFiles {
				t.Errorf("DeadLetterQueue(%s) = %d files, want %d files", tt.name, gotFiles, tt.wantFiles)
			} else if gotContent.String() != tt.wantContent {
				t.Errorf("DeadLetterQueue(%s) = %q, want %q", tt.name, gotContent.String(), tt.wantContent)
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
				data := flow.Rows{{Cols: []string{"goroutine", "call"}, Vals: []any{goroutine, call}}}
				dlq.Send(t.Context(), data, codec.FormatDefault)
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

		gotFile := struct {
			G int `json:"goroutine"`
			C int `json:"call"`
		}{}
		if err = json.Unmarshal(f, &gotFile); err != nil {
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

func TestInitDeadLetterQueueError(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, filePermissions); err != nil {
		t.Fatalf("failed to create file: %v", err)
	}

	if dlq := InitDeadLetterQueue(file); dlq != nil {
		t.Errorf("InitDeadLetterQueue(file) = %v, want nil", dlq)
	}
}

func TestEncodeNil(t *testing.T) {
	t.Parallel()

	if got, ext := encode(nil, codec.FormatJSON); got != nil || ext != "" {
		t.Errorf("encode(nil) = %v, %q, want nil, %q", got, ext, "")
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

			gotOK := dlq.asyncWriteFile([]byte("data"), "", time.Now().UTC(), tt.dirPerms, tt.filePerms)
			if gotOK != tt.wantOK {
				t.Errorf("asyncWriteFile() = %v, want %v", gotOK, tt.wantOK)
			}
		})
	}
}

func TestFileExtension(t *testing.T) {
	t.Parallel()

	tests := []struct {
		format config.Format
		want   string
	}{
		{"", ""},
		{codec.FormatDefault, ""},
		{codec.FormatJSON, ".json"},
		{codec.FormatJSONArray, ".json_array"},
		{codec.FormatNDJSON, ".ndjson"},
		{codec.FormatRaw, ".raw"},
		// Non-word characters are removed (Go's \W is ASCII-only), so formats can't affect the file's path.
		{"../", ""},
		{"../x", ".x"},
		{"a/b\\c", ".abc"},
		{"json-é 2", ".json2"},
		{"JSON", ".json"},
	}
	for _, tt := range tests {
		t.Run(string(tt.format), func(t *testing.T) {
			t.Parallel()

			if got := fileExtension(tt.format); got != tt.want {
				t.Errorf("fileExtension(%q) = %q, want %q", tt.format, got, tt.want)
			}
		})
	}
}

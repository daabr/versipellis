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
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"

	"github.com/daabr/versipellis/pkg/codec"
	"github.com/daabr/versipellis/pkg/config"
	"github.com/daabr/versipellis/pkg/flow"
)

func TestDeadLetterQueue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		closeFirst  bool
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
			name:       "send_during_close",
			closeFirst: true,
			data:       flow.Blobs([][]byte{[]byte("payload")}),
			wantFiles:  0,
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
			dlq, ok := InitDeadLetterQueue(tempDir, nil)
			if !ok {
				t.Fatalf("failed to initialize DeadLetterQueue")
			}
			if tt.closeFirst {
				dlq.Close(t.Context()) // The second call below is a no-op.
			}
			dlq.Send(t.Context(), tt.data, cmp.Or(tt.format, codec.FormatDefault))
			dlq.Close(t.Context())
			select {
			case <-dlq.state.Closed:
			default:
				t.Error("DeadLetterQueue.Close() didn't close State.Closed")
			}

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
	dlq, ok := InitDeadLetterQueue(tempDir, nil)
	if !ok {
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
	dlq.state.InProgress.Wait()

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
	if dlq, ok := InitDeadLetterQueue(file, nil); ok || dlq != nil {
		t.Errorf("InitDeadLetterQueue(file, nil) = %v, %v, want nil, false", dlq, ok)
	}

	// An invalid configuration fails before creating the directory.
	dir := filepath.Join(t.TempDir(), "dlq")
	if dlq, ok := InitDeadLetterQueue(dir, "invalid"); ok || dlq != nil {
		t.Errorf("InitDeadLetterQueue(dir, \"invalid\") = %v, %v, want nil, false", dlq, ok)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("InitDeadLetterQueue() with an invalid configuration created the directory: %v", err)
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

	disabledSizeLimits := map[string]any{"max_size": int64(0), "warning_size": int64(0)}

	tests := []struct {
		name      string
		cfg       map[string]any
		dirPerms  os.FileMode
		filePerms os.FileMode
		want      writeResult
		wantSize  int64
	}{
		{"valid_perms", nil, dirPermissions, filePermissions, writeOK, 4},
		{"invalid_dir_perms", nil, os.FileMode(math.MaxUint32), filePermissions, writeRetry, 0},
		{"invalid_file_perms", nil, dirPermissions, os.FileMode(math.MaxUint32), writeRetry, 0},
		{"max_size_reached", map[string]any{"max_size": int64(4)}, dirPermissions, filePermissions, writeOK, 4},
		{"max_size_exceeded", map[string]any{"max_size": int64(3)}, dirPermissions, filePermissions, writeAbort, 0},
		{"size_limits_disabled", disabledSizeLimits, dirPermissions, filePermissions, writeOK, 0}, // Not accounted at all.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tempDir := t.TempDir()
			dlq, ok := InitDeadLetterQueue(tempDir, tt.cfg)
			if !ok {
				t.Fatalf("failed to initialize DeadLetterQueue")
			}
			t.Cleanup(func() { dlq.Close(t.Context()) })

			data := []byte("data")
			got := dlq.asyncWriteFile(data, ".ext", time.Now().UTC(), tt.dirPerms, tt.filePerms)
			if got != tt.want {
				t.Errorf("asyncWriteFile() = %v, want %v", got, tt.want)
			}

			// Only successful writes are accounted for, even if a measurement runs concurrently.
			if gotSize := lockedTotalSize(dlq); gotSize != tt.wantSize {
				t.Errorf("asyncWriteFile() accounted size = %d, want %d", gotSize, tt.wantSize)
			}

			// Successful writes leave only the renamed file, failed writes don't leave temporary files.
			var gotFiles []string
			err := filepath.WalkDir(tempDir, func(path string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					gotFiles = append(gotFiles, filepath.Base(path))
				}
				return err
			})
			if err != nil {
				t.Fatalf("filepath.WalkDir(%s) error = %v", tt.name, err)
			}
			wantFiles := 0
			if tt.want == writeOK {
				wantFiles = 1
			}
			if len(gotFiles) != wantFiles {
				t.Fatalf("asyncWriteFile() files = %q, want %d files", gotFiles, wantFiles)
			}
			if wantFiles > 0 && filepath.Ext(gotFiles[0]) != ".ext" {
				t.Errorf("asyncWriteFile() file name = %q, want extension %q", gotFiles[0], ".ext")
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

// TestDLQSizeRemeasure checks that the size of all the files in the DLQ directory is measured at startup,
// and again only after [remeasureMinDelay], which also notices external changes.
func TestDLQSizeRemeasure(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		writeTestFile(t, filepath.Join(dir, "external.bin"), 100)
		writeTestFile(t, filepath.Join(dir, "sub", "leftover.json"+incompleteExt), 50)

		dlq, ok := InitDeadLetterQueue(dir, map[string]any{"warning_size": int64(120)})
		if !ok {
			t.Fatal("failed to initialize DeadLetterQueue")
		}
		defer dlq.Close(t.Context())

		synctest.Wait()
		assertDLQSize(t, dlq, "initial measurement", 150, true)

		writeTestFile(t, filepath.Join(dir, "external2.bin"), 40)
		synctest.Sleep(remeasureMinDelay - 1)
		assertDLQSize(t, dlq, "before re-measurement", 150, true)

		synctest.Sleep(1)
		synctest.Wait()
		assertDLQSize(t, dlq, "after re-measurement", 190, true)

		for _, name := range []string{"external.bin", "external2.bin", "sub"} {
			if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
				t.Fatalf("os.RemoveAll() error = %v", err)
			}
		}
		synctest.Sleep(remeasureMinDelay)
		synctest.Wait()
		assertDLQSize(t, dlq, "after deletions", 0, false)
	})
}

// TestDLQSizeRemeasureBackoff checks that the delay between measurements doubles while the total size
// doesn't change, and that new writes reset it to the minimum, even during a longer wait.
func TestDLQSizeRemeasureBackoff(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		dlq, ok := InitDeadLetterQueue(dir, map[string]any{"warning_size": int64(1000)})
		if !ok {
			t.Fatal("failed to initialize DeadLetterQueue")
		}
		defer dlq.Close(t.Context())

		// Unchanged measurements after 1x and 2x the minimal delay, so the next one is after 4x.
		synctest.Wait()
		synctest.Sleep(remeasureMinDelay)
		synctest.Sleep(2 * remeasureMinDelay)
		synctest.Wait()

		writeTestFile(t, filepath.Join(dir, "external1"), 10)
		synctest.Sleep(4*remeasureMinDelay - 1)
		synctest.Wait()
		assertDLQSize(t, dlq, "before the backed-off measurement", 0, false)
		synctest.Sleep(1)
		synctest.Wait()
		assertDLQSize(t, dlq, "after the backed-off measurement", 10, false)

		// That change reset the delay, then it doubles twice again, and a write cuts the 4x wait short.
		synctest.Sleep(remeasureMinDelay)
		synctest.Sleep(2 * remeasureMinDelay)
		synctest.Sleep(remeasureMinDelay)
		dlq.Send(t.Context(), flow.Blobs{[]byte("12345")}, codec.FormatRaw)
		synctest.Wait()
		writeTestFile(t, filepath.Join(dir, "external2"), 20)

		synctest.Sleep(remeasureMinDelay - 1)
		synctest.Wait()
		assertDLQSize(t, dlq, "before the measurement after a write", 15, false) // Accounted write only.
		synctest.Sleep(1)
		synctest.Wait()
		assertDLQSize(t, dlq, "after the measurement after a write", 35, false)
	})
}

func TestNextRemeasureDelay(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		delay   time.Duration
		changed bool
		want    time.Duration
	}{
		{"changed_at_min", remeasureMinDelay, true, remeasureMinDelay},
		{"changed_after_backoff", 8 * remeasureMinDelay, true, remeasureMinDelay},
		{"unchanged_1x", remeasureMinDelay, false, 2 * remeasureMinDelay},
		{"unchanged_2x", 2 * remeasureMinDelay, false, 4 * remeasureMinDelay},
		{"unchanged_4x", 4 * remeasureMinDelay, false, 8 * remeasureMinDelay},
		{"unchanged_8x", 8 * remeasureMinDelay, false, 8 * remeasureMinDelay}, // 16x would exceed the maximum.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := nextRemeasureDelay(tt.delay, tt.changed); got != tt.want {
				t.Errorf("nextRemeasureDelay(%v, %v) = %v, want %v", tt.delay, tt.changed, got, tt.want)
			}
		})
	}
}

func writeTestFile(t *testing.T, path string, size int) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), dirPermissions); err != nil {
		t.Fatalf("os.MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", size)), filePermissions); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
}

func assertDLQSize(t *testing.T, dlq *DeadLetterQueue, when string, wantSize int64, wantWarned bool) {
	t.Helper()

	if got := lockedTotalSize(dlq); got != wantSize {
		t.Errorf("DLQ size %s = %d, want %d", when, got, wantSize)
	}
	dlq.sizeMu.Lock()
	defer dlq.sizeMu.Unlock()
	if dlq.warned != wantWarned {
		t.Errorf("DLQ size warning state %s = %v, want %v", when, dlq.warned, wantWarned)
	}
}

// lockedTotalSize returns the total size of the DLQ directory, as accounted so far.
func lockedTotalSize(dlq *DeadLetterQueue) int64 {
	dlq.sizeMu.Lock()
	defer dlq.sizeMu.Unlock()
	return dlq.totalSize
}

// newTestDLQ returns a [DeadLetterQueue] for the given directory, without starting its background measurements.
func newTestDLQ(t *testing.T, dir string, maxSize, warnSize int64) *DeadLetterQueue {
	t.Helper()

	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("os.OpenRoot() error = %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return &DeadLetterQueue{maxSize: maxSize, warnSize: warnSize, root: r, newFiles: make(map[string]newFile)}
}

func TestDLQSizeWarning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		warningSize int64
		sizes       []int64 // Sequence of reservations, each followed by a check of the warning state.
		want        []bool
	}{
		{"disabled", 0, []int64{1, 100}, []bool{false, false}},
		{"below_threshold", 100, []int64{50, 49}, []bool{false, false}},
		{"reached_threshold", 100, []int64{50, 50}, []bool{false, true}},
		{"latched_above_threshold", 100, []int64{50, 51, 100}, []bool{false, true, true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			d := newTestDLQ(t, t.TempDir(), 1000, tt.warningSize)
			for i, size := range tt.sizes {
				if !d.reserve(strconv.Itoa(i), size) {
					t.Fatalf("reserve() #%d = false, want true", i+1)
				}
				if d.warned != tt.want[i] {
					t.Errorf(
						"warning state after reserve() #%d with total %d = %v, want %v",
						i+1,
						d.totalSize,
						d.warned,
						tt.want[i],
					)
				}
			}
		})
	}
}

// TestDLQSizeWarningReset checks that the warning state is reset only when a measurement
// shows that the total size is under the threshold, and that the maximum size is enforced.
func TestDLQSizeWarningReset(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	d := newTestDLQ(t, dir, 250, 150)
	for _, name := range []string{"a", "b"} {
		if !d.reserve(name, 100) {
			t.Fatalf("reserve(%q) = false, want true", name)
		}
		writeTestFile(t, filepath.Join(dir, name), 100)
		d.release(name, true)
	}
	assertDLQSize(t, d, "after crossing the warning size", 200, true)

	if d.reserve("c", 100) {
		t.Error("reserve() beyond the maximum size = true, want false")
	}
	if !d.reserve("d", 50) {
		t.Error("reserve() up to the maximum size = false, want true")
	}
	d.release("d", false) // A failed write releases its reserved size.
	assertDLQSize(t, d, "after a failed write", 200, true)

	d.measureSize()
	assertDLQSize(t, d, "after measuring the same size", 200, true)

	if err := os.Remove(filepath.Join(dir, "b")); err != nil {
		t.Fatalf("os.Remove() error = %v", err)
	}
	d.measureSize()
	assertDLQSize(t, d, "after measuring a smaller size", 100, false)
}

func TestDLQSizeRelease(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		measuring   bool
		written     bool
		wantTracked bool
		wantSize    int64
	}{
		{"failed", false, false, false, 0},
		{"failed_while_measuring", true, false, false, 0},
		{"written", false, true, false, 10},
		{"written_while_measuring", true, true, true, 10}, // Tracked until the end of the measurement.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			d := newTestDLQ(t, t.TempDir(), 0, 100)
			if !d.reserve("dir/file", 10) {
				t.Fatal("reserve() = false, want true")
			}
			d.measuring = tt.measuring
			d.release("dir/file", tt.written)

			file, tracked := d.newFiles["dir/file"]
			if tracked != tt.wantTracked {
				t.Errorf("release() tracked = %v, want %v", tracked, tt.wantTracked)
			}
			if tracked && !file.done {
				t.Error("release() of a written file during a measurement didn't mark it as done")
			}
			if d.totalSize != tt.wantSize {
				t.Errorf("release() total size = %d, want %d", d.totalSize, tt.wantSize)
			}
		})
	}
}

// TestDLQSizeMeasureTracking checks that measurements count tracked files exactly once, by their reserved sizes.
func TestDLQSizeMeasureTracking(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "external"), 1)
	writeTestFile(t, filepath.Join(dir, "dir", "in_flight"+incompleteExt), 5) // Partially written.
	writeTestFile(t, filepath.Join(dir, "dir", "finished"), 20)

	d := newTestDLQ(t, dir, 0, 100)
	d.newFiles["dir/in_flight"] = newFile{size: 10}
	d.newFiles["dir/finished"] = newFile{size: 20, done: true} // Finished during the previous measurement.
	d.measureSize()

	if got, want := d.totalSize, int64(1+10+20); got != want {
		t.Errorf("total size after measurement = %d, want %d", got, want)
	}
	if _, found := d.newFiles["dir/finished"]; found {
		t.Error("a file that finished during the measurement is still tracked after it")
	}
	if _, found := d.newFiles["dir/in_flight"]; !found {
		t.Error("an in-flight file isn't tracked after the measurement")
	}
}

func TestDeadLetterQueueMaxSizeConcurrency(t *testing.T) {
	t.Parallel()

	const fileSize, maxFiles = 100, 10

	dir := t.TempDir()
	dlq, ok := InitDeadLetterQueue(dir, map[string]any{"max_size": int64(fileSize * maxFiles)})
	if !ok {
		t.Fatal("failed to initialize DeadLetterQueue")
	}
	t.Cleanup(func() { dlq.Close(t.Context()) })

	var wg sync.WaitGroup
	for range 4 * maxFiles {
		wg.Go(func() {
			dlq.Send(t.Context(), flow.Blobs{[]byte(strings.Repeat("x", fileSize))}, codec.FormatRaw)
		})
	}
	wg.Wait()
	dlq.state.InProgress.Wait()

	files := 0
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files++
		}
		return err
	})
	if err != nil {
		t.Fatalf("filepath.WalkDir() error = %v", err)
	}
	if files != maxFiles {
		t.Errorf("DLQ files = %d, want %d", files, maxFiles)
	}
	if got := lockedTotalSize(dlq); got != fileSize*maxFiles {
		t.Errorf("DLQ size = %d, want %d", got, fileSize*maxFiles)
	}
}

// TestDLQMaxSizeBeforeMeasurement checks that writes wait for the first measurement of the DLQ directory, so a
// directory that's already full can't grow beyond the maximum size, and that they're discarded if the DLQ closes first.
func TestDLQMaxSizeBeforeMeasurement(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		closeFirst bool
	}{
		{"measured_first", false},
		{"closed_first", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			writeTestFile(t, filepath.Join(dir, "existing"), 100) // Already full.

			synctest.Test(t, func(t *testing.T) {
				d := newTestDLQ(t, dir, 100, 0)
				d.measured = make(chan struct{})
				d.state.Start()

				d.Send(t.Context(), flow.Blobs{[]byte("new data")}, codec.FormatRaw)
				synctest.Wait()
				if got := lockedTotalSize(d); got != 0 {
					t.Errorf("DLQ size before the first measurement = %d, want 0 (no reservations)", got)
				}

				start := time.Now()
				if tt.closeFirst {
					d.Close(t.Context())
					if elapsed := time.Since(start); elapsed > 0 {
						t.Errorf("Close() with a pending write took %v, want no wait", elapsed)
					}
				} else {
					d.measureSize()
					close(d.measured)
					d.state.InProgress.Wait()
					assertDLQSize(t, d, "after the first measurement", 100, false)
				}
			})

			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("os.ReadDir() error = %v", err)
			}
			if len(entries) != 1 || entries[0].Name() != "existing" {
				t.Errorf("DLQ directory entries = %v, want only the existing file", entries)
			}
		})
	}
}

// TestDLQMeasurementInterruptedByClose checks that a first measurement that was interrupted by
// shutdown doesn't release pending writes, which would then bypass the maximum size.
func TestDLQMeasurementInterruptedByClose(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "existing"), 100)

	d := newTestDLQ(t, dir, 100, 0)
	d.measured = make(chan struct{})
	d.state.Start()
	d.state.BeginClose()
	d.measureSizeLoop() // Returns immediately because the DLQ is closing.

	select {
	case <-d.measured:
		t.Error("measureSizeLoop() released pending writes after an interrupted measurement")
	default:
	}
}

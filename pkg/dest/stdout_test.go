package dest

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/daabr/versipellis/pkg/codec"
	"github.com/daabr/versipellis/pkg/flow"
)

func TestStdout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data flow.Chunk
		want string
	}{
		{
			name: "nothing",
			data: nil,
			want: "",
		},
		{
			name: "int",
			data: flow.Rows{{Cols: []string{"int"}, Vals: []any{42}}},
			want: `{"int": 42}` + "\n",
		},
		{
			name: "float",
			data: flow.Rows{{Cols: []string{"float"}, Vals: []any{3.14}}},
			want: `{"float": 3.14}` + "\n",
		},
		{
			name: "string",
			data: flow.Rows{{Cols: []string{"string"}, Vals: []any{"just a string"}}},
			want: `{"string": "just a string"}` + "\n",
		},
		{
			name: "unencoded_html",
			data: flow.Rows{{Cols: []string{"html"}, Vals: []any{"& < >"}}},
			want: `{"html": "& < >"}` + "\n",
		},
		{
			name: "empty_byte_slice_1",
			data: flow.Blobs([][]byte{}),
			want: "",
		},
		{
			name: "empty_byte_slice_2",
			data: flow.Blobs([][]byte{[]byte("")}),
			want: "",
		},
		{
			name: "nil_byte_slice",
			data: flow.Blobs([][]byte{nil}),
			want: "",
		},
		{
			name: "printable_ascii_byte_slice",
			data: flow.Blobs([][]byte{[]byte("payload")}),
			want: "payload\n",
		},
		{
			name: "printable_unicode_byte_slice",
			data: flow.Blobs([][]byte{[]byte("πayload")}),
			want: "πayload\n",
		},
		{
			name: "non_printable_unicode_byte_slice",
			data: flow.Blobs([][]byte{[]byte("\x00\x01\x02")}),
			want: "\x00\x01\x02\n",
		},
		{
			name: "multiple_byte_slices",
			data: flow.Blobs([][]byte{[]byte("first"), []byte("second")}),
			want: "first\nsecond\n",
		},
		{
			name: "printable_and_non_printable_side_by_side",
			data: flow.Rows{{Cols: []string{"text", "base64"}, Vals: []any{[]byte("a\vb"), []byte{0xff, 0x00}}}},
			want: `{"text": "a\u000bb", "base64": "/wA="}` + "\n", // "\v" is U+000B.
		},
		{
			name: "http_request_with_body",
			data: flow.HTTPRequests([]*http.Request{func() *http.Request {
				body := strings.NewReader("body")
				req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://example.com", body)
				return req
			}()}),
			want: "POST / HTTP/1.1\r\nHost: example.com\r\nUser-Agent: Go-http-client/1.1\r\nContent-Length: 4\r\n\r\nbody",
		},
		{
			name: "http_request_without_body",
			data: flow.HTTPRequests([]*http.Request{func() *http.Request {
				req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://example.com", http.NoBody)
				return req
			}()}),
			want: "POST / HTTP/1.1\r\nHost: example.com\r\nUser-Agent: Go-http-client/1.1\r\nContent-Length: 0\r\n\r\n",
		},
		{
			name: "http_response",
			data: flow.HTTPResponses([]*http.Response{func() *http.Response {
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
			}()}), //nolint:bodyclose // Closed by [stdoutSender.Send] in unit test.
			want: "HTTP/1.1 200 OK\r\nConnection: close\r\n\r\nbody",
		},
		{
			name: "nil_http_request",
			data: flow.HTTPRequests([]*http.Request{nil}),
			want: "",
		},
		{
			name: "nil_http_response",
			data: flow.HTTPResponses([]*http.Response{nil}),
			want: "",
		},
		{
			name: "column_order_and_duplicate_names",
			data: flow.Rows{{Cols: []string{"key", "number", "key", ""}, Vals: []any{"value", 42, "again", nil}}},
			want: `{"key": "value", "number": 42, "key": "again", "": null}` + "\n",
		},
		{
			name: "invalid_utf8_replaced",
			data: flow.Rows{{Cols: []string{"bad\xff"}, Vals: []any{"x\xffy"}}},
			want: `{"bad` + "�" + `": "x` + "�" + `y"}` + "\n",
		},
		{
			name: "nested_values",
			data: flow.Rows{{Cols: []string{"map", "list"}, Vals: []any{map[string]any{"z": 1, "a": 2}, []any{1, "x"}}}},
			want: `{"map": {"a": 2, "z": 1}, "list": [1, "x"]}` + "\n",
		},
		{
			name: "rows_ndjson",
			data: flow.Rows{
				{Cols: []string{"ascii", "unicode"}, Vals: []any{"abc", "üñîçødé"}},
				{Cols: []string{"spaces"}, Vals: []any{[]byte("a b \t c \v b")}},
				{Cols: []string{"base64"}, Vals: []any{[]byte{0xff, 0x00, 0x01}}},
				{Cols: []string{"control"}, Vals: []any{[]byte("a\x00b")}},
				{Cols: []string{"error"}, Vals: []any{make(chan struct{})}},
				{Cols: []string{"after_error"}, Vals: []any{"ok"}},
			},
			want: `{"ascii": "abc", "unicode": "üñîçødé"}` + "\n" + `{"spaces": "a b \t c \u000b b"}` + "\n" +
				`{"base64": "/wAB"}` + "\n" + `{"control": "YQBi"}` + "\n" + `{"after_error": "ok"}` + "\n",
		},
		{
			name: "json_objects_readable",
			data: flow.JSON{
				jsontext.Value(`{"z":1,"a":[2,3]}`),
				jsontext.Value("{\n  \"b\": {\"c\": null}\n}"),
				jsontext.Value(`{"invalid":}`),
				jsontext.Value(`{"s":"é\/"}`),
			},
			want: `{"z": 1, "a": [2, 3]}` + "\n" + `{"b": {"c": null}}` + "\n" + `{"s": "é/"}` + "\n",
		},
		{
			name: "unhandled_data_type",
			data: flow.FakeDataType{},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fakeStdout := new(strings.Builder)
			s := new(stdoutSender{writer: fakeStdout})
			s.Send(t.Context(), tt.data, codec.FormatDefault)
			s.Close(t.Context())

			if got := fakeStdout.String(); got != tt.want {
				t.Errorf("stdoutSender.Send(%s) = %q, want %q", tt.name, got, tt.want)
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
				data := flow.Rows{{Cols: []string{"goroutine", "call"}, Vals: []any{goroutine, call}}}
				sender.Send(t.Context(), data, codec.FormatDefault)
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

	s.Send(t.Context(), flow.Blobs([][]byte{[]byte("should be dropped")}), codec.FormatDefault)
	if got := fakeStdout.String(); got != "" {
		t.Errorf("stdoutSender.Send() after stdoutSender.Close() wrote %q, want empty", got)
	}
}

func TestStdoutPrintWriteError(t *testing.T) {
	t.Parallel()

	w := failingWriter{}
	tests := []struct {
		name    string
		printFn func() error
	}{
		{"json", func() error { return printJSON(w, flow.JSON{jsontext.Value(`{"a":1}`)}) }},
		{"rows", func() error { return printRows(w, flow.Rows{{Cols: []string{"a"}, Vals: []any{1}}}) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if err := tt.printFn(); err == nil {
				t.Error("print error = nil, want write error")
			}
		})
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write error")
}

func TestMarshalRow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		names   []string
		vals    []any
		want    string
		wantErr bool
	}{
		{
			name:  "readable",
			names: []string{"a", "b", "c"},
			vals:  []any{int64(1), []byte("x y"), []byte{0xff}},
			want:  `{"a": 1, "b": "x y", "c": "/w=="}` + "\n",
		},
		{
			name:  "non_finite_floats",
			names: []string{"nan", "inf", "neg_inf"},
			vals:  []any{math.NaN(), math.Inf(1), math.Inf(-1)},
			want:  `{"nan": "NaN", "inf": "Infinity", "neg_inf": "-Infinity"}` + "\n",
		},
		{
			name: "empty",
			want: `{}` + "\n",
		},
		{
			name:    "unencodable_value",
			names:   []string{"a"},
			vals:    []any{func() {}},
			wantErr: true,
		},
		{
			name:  "duplicate_names",
			names: []string{"a", "a"},
			vals:  []any{1, 2},
			want:  `{"a": 1, "a": 2}` + "\n",
		},
		{
			name:  "empty_name",
			names: []string{""},
			vals:  []any{nil},
			want:  `{"": null}` + "\n",
		},
		{
			name:  "invalid_utf8",
			names: []string{"\xff"},
			vals:  []any{"\xff"},
			want:  "{\"\uFFFD\": \"\uFFFD\"}\n",
		},
		{
			name: "values_mismatch", names: []string{"a", "b"}, vals: []any{1}, wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := marshalRow(tt.names, tt.vals)
			if (err != nil) != tt.wantErr {
				t.Errorf("marshalRow() error = %v, wantErr %v", err, tt.wantErr)
			}
			if string(got) != tt.want {
				t.Errorf("marshalRow() = %q, want %q", got, tt.want)
			}
		})
	}
}

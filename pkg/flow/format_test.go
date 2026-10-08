package flow_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/daabr/versipellis/pkg/flow"
)

func TestFormatContentType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		format flow.Format
		want   string
	}{
		{format: flow.FormatJSON, want: "application/json"},
		{format: flow.FormatJSONArray, want: "application/json"},
		{format: flow.FormatNDJSON, want: "application/x-ndjson"},
		{format: "invalid", want: "invalid"},
	}
	for _, tt := range tests {
		t.Run(string(tt.format), func(t *testing.T) {
			t.Parallel()

			if got := tt.format.ContentType(); got != tt.want {
				t.Errorf("Format(%q).ContentType() = %q, want %q", tt.format, got, tt.want)
			}
		})
	}
}

// zeroFields contains struct fields that must be kept even when they have zero values.
type zeroFields struct {
	A int
	B string
}

func TestFormatEncode(t *testing.T) {
	t.Parallel()

	one := flow.Structured{{"b": int64(2), "a": "x"}}
	two := flow.Structured{{"k": int64(1)}, {"k": int64(2)}}
	bad := flow.Structured{{"k": int64(1)}, {"k": make(chan struct{})}} // Channels cannot be encoded as JSON.

	tests := []struct {
		name    string
		format  flow.Format
		data    flow.Structured
		want    string
		wantErr bool
	}{
		{
			name:   "json_nil",
			format: flow.FormatJSON,
			data:   nil,
			want:   `{}`,
		},
		{
			name:   "json_empty",
			format: flow.FormatJSON,
			data:   flow.Structured{},
			want:   `{}`,
		},
		{
			name:   "json_single_record_as_object",
			format: flow.FormatJSON,
			data:   one,
			want:   `{"a":"x","b":2}`,
		},
		{
			name:   "json_multiple_records_as_array",
			format: flow.FormatJSON,
			data:   two,
			want:   `[{"k":1},{"k":2}]`,
		},
		{
			name:    "json_error",
			format:  flow.FormatJSON,
			data:    bad,
			wantErr: true,
		},
		{
			name:    "json_single_record_error",
			format:  flow.FormatJSON,
			data:    bad[1:],
			wantErr: true,
		},
		{
			name:   "json_array_empty",
			format: flow.FormatJSONArray,
			data:   nil,
			want:   `[]`,
		},
		{
			name:   "json_array_single_record",
			format: flow.FormatJSONArray,
			data:   one,
			want:   `[{"a":"x","b":2}]`,
		},
		{
			name:   "json_array_multiple_records",
			format: flow.FormatJSONArray,
			data:   two,
			want:   `[{"k":1},{"k":2}]`,
		},
		{
			name:    "json_array_error",
			format:  flow.FormatJSONArray,
			data:    bad,
			wantErr: true,
		},
		{
			name:   "ndjson_empty",
			format: flow.FormatNDJSON,
			data:   nil,
			want:   "",
		},
		{
			name:   "ndjson_single_record",
			format: flow.FormatNDJSON,
			data:   one,
			want:   `{"a":"x","b":2}` + "\n",
		},
		{
			name:   "ndjson_multiple_records",
			format: flow.FormatNDJSON,
			data:   two,
			want:   `{"k":1}` + "\n" + `{"k":2}` + "\n",
		},
		{
			name:    "ndjson_error_rejects_entire_chunk",
			format:  flow.FormatNDJSON,
			data:    bad,
			wantErr: true,
		},
		{
			name:   "bytes_are_base64_encoded",
			format: flow.FormatNDJSON,
			data:   flow.Structured{{"b": []byte("hi")}},
			want:   `{"b":"aGk="}` + "\n",
		},
		{
			name:   "html_is_not_escaped",
			format: flow.FormatNDJSON,
			data:   flow.Structured{{"html": "<a href='x'>&</a>"}},
			want:   `{"html":"<a href='x'>&</a>"}` + "\n",
		},
		{
			name:   "zero_struct_fields_are_kept",
			format: flow.FormatNDJSON,
			data:   flow.Structured{{"s": zeroFields{A: 1}}},
			want:   `{"s":{"A":1,"B":""}}` + "\n",
		},
		{
			name:    "unsupported_format",
			format:  "xml",
			data:    one,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := tt.format.Encode(tt.data)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Format(%q).Encode() error = %v, wantErr %v", tt.format, err, tt.wantErr)
			}
			if string(got) != tt.want {
				t.Errorf("Format(%q).Encode() = %q, want %q", tt.format, got, tt.want)
			}
		})
	}
}

// recordingWriter is an [io.Writer] which records the number of write calls, and optionally fails.
type recordingWriter struct {
	bytes.Buffer

	writes int
	err    error
}

func (w *recordingWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.err != nil {
		return 0, w.err
	}
	_, _ = w.Buffer.Write(p) // [bytes.Buffer.Write]: err is always nil.
	return len(p), nil
}

func TestFormatPrint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		format     flow.Format
		data       flow.Structured
		writeErr   error
		want       string
		wantWrites int
		wantErr    bool
	}{
		{
			name:    "unsupported_format",
			format:  flow.FormatJSON,
			data:    flow.Structured{{"k": "v"}},
			wantErr: true,
		},
		{
			name:   "empty",
			format: flow.FormatNDJSON,
			data:   nil,
		},
		{
			name:       "multiple_records_in_single_write",
			format:     flow.FormatNDJSON,
			data:       flow.Structured{{"k": int64(1)}, {"b": int64(2), "a": int64(1)}},
			want:       `{"k":1}` + "\n" + `{"a":1,"b":2}` + "\n",
			wantWrites: 1,
		},
		{
			name:       "printable_bytes_as_string",
			format:     flow.FormatNDJSON,
			data:       flow.Structured{{"b": []byte("héllo wörld")}},
			want:       `{"b":"héllo wörld"}` + "\n",
			wantWrites: 1,
		},
		{
			name:       "whitespace_bytes_as_string",
			format:     flow.FormatNDJSON,
			data:       flow.Structured{{"b": []byte("a\tb\nc")}},
			want:       `{"b":"a\tb\nc"}` + "\n",
			wantWrites: 1,
		},
		{
			name:       "empty_bytes_as_string",
			format:     flow.FormatNDJSON,
			data:       flow.Structured{{"b": []byte{}}},
			want:       `{"b":""}` + "\n",
			wantWrites: 1,
		},
		{
			name:       "invalid_utf8_bytes_as_base64",
			format:     flow.FormatNDJSON,
			data:       flow.Structured{{"b": []byte{0xff, 0xfe}}},
			want:       `{"b":"//4="}` + "\n",
			wantWrites: 1,
		},
		{
			name:       "non_printable_bytes_as_base64",
			format:     flow.FormatNDJSON,
			data:       flow.Structured{{"b": []byte("a\x07b")}},
			want:       `{"b":"YQdi"}` + "\n",
			wantWrites: 1,
		},
		{
			name:   "bad_records_are_skipped",
			format: flow.FormatNDJSON,
			data: flow.Structured{
				{"k": int64(1)},
				{"k": make(chan struct{})}, // Channels cannot be encoded as JSON.
				{"k": int64(3)},
			},
			want:       `{"k":1}` + "\n" + `{"k":3}` + "\n",
			wantWrites: 1,
		},
		{
			name:   "only_bad_records",
			format: flow.FormatNDJSON,
			data:   flow.Structured{{"k": make(chan struct{})}},
		},
		{
			name:       "write_error",
			format:     flow.FormatNDJSON,
			data:       flow.Structured{{"k": int64(1)}},
			writeErr:   errors.New("disk full"),
			wantWrites: 1,
			wantErr:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := &recordingWriter{err: tt.writeErr}
			err := tt.format.Print(w, tt.data)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Format(%q).Print() error = %v, wantErr %v", tt.format, err, tt.wantErr)
			}
			if tt.writeErr != nil && !errors.Is(err, tt.writeErr) {
				t.Errorf("Format(%q).Print() error = %v, want wrapped %v", tt.format, err, tt.writeErr)
			}
			if w.writes != tt.wantWrites {
				t.Errorf("Format(%q).Print() write calls = %d, want %d", tt.format, w.writes, tt.wantWrites)
			}
			if got := w.String(); got != tt.want {
				t.Errorf("Format(%q).Print() output = %q, want %q", tt.format, got, tt.want)
			}
		})
	}
}

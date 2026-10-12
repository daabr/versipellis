package codec_test

import (
	"bytes"
	"encoding/json/jsontext"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/daabr/versipellis/pkg/codec"
	"github.com/daabr/versipellis/pkg/flow"
)

func TestJSONFormatContentType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		fmtr codec.Formatter
		want string
	}{
		{"json", codec.JSON, "application/json"},
		{"json_array", codec.JSONArray, "application/json"},
		{"ndjson", codec.NDJSON, "application/x-ndjson"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.fmtr.ContentType(); got != tt.want {
				t.Errorf("%s.ContentType() = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func objects(objs ...string) flow.JSON {
	out := make(flow.JSON, len(objs))
	for i, obj := range objs {
		out[i] = jsontext.Value(obj)
	}
	return out
}

func TestJSONFormatEncodeJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fmtr    codec.Formatter
		data    flow.JSON
		want    string
		wantErr bool
	}{
		// Payload shapes.
		{
			name: "json_nil",
			fmtr: codec.JSON,
			want: "{}",
		},
		{
			name: "json_array_nil",
			fmtr: codec.JSONArray,
			want: "[]",
		},
		{
			name: "ndjson_nil",
			fmtr: codec.NDJSON,
		},
		{
			name: "json_empty",
			fmtr: codec.JSON,
			data: flow.JSON{},
			want: "{}",
		},
		{
			name: "json_array_empty",
			fmtr: codec.JSONArray,
			data: flow.JSON{},
			want: "[]",
		},
		{
			name: "ndjson_empty",
			fmtr: codec.NDJSON,
			data: flow.JSON{},
		},
		{
			name: "json_single",
			fmtr: codec.JSON,
			data: objects(`{"a":1}`),
			want: `{"a":1}`,
		},
		{
			name: "json_array_single",
			fmtr: codec.JSONArray,
			data: objects(`{"a":1}`),
			want: `[{"a":1}]`,
		},
		{
			name: "ndjson_single",
			fmtr: codec.NDJSON,
			data: objects(`{"a":1}`),
			want: `{"a":1}` + "\n",
		},
		{
			name: "json_multiple",
			fmtr: codec.JSON,
			data: objects(`{"a":1}`, `{"b":2}`),
			want: `[{"a":1},{"b":2}]`,
		},
		{
			name: "json_array_multiple",
			fmtr: codec.JSONArray,
			data: objects(`{"a":1}`, `{"b":2}`),
			want: `[{"a":1},{"b":2}]`,
		},
		{
			name: "ndjson_multiple",
			fmtr: codec.NDJSON,
			data: objects(`{"a":1}`, `{"b":2}`),
			want: `{"a":1}` + "\n" + `{"b":2}` + "\n",
		},
		// Fidelity.
		{
			name: "order_preserved_and_unescaped_html",
			fmtr: codec.JSON,
			data: objects(`{"z":1,"a":"& < >","m":{"y":2,"b":3}}`),
			want: `{"z":1,"a":"& < >","m":{"y":2,"b":3}}`,
		},
		{
			name: "exact_numbers",
			fmtr: codec.NDJSON,
			data: objects(`{"n":12345678901234567890,"f":1.50,"e":-0.5e10}`),
			want: `{"n":12345678901234567890,"f":1.50,"e":-0.5e10}` + "\n",
		},
		{
			name: "pretty_printed_input_compacted",
			fmtr: codec.NDJSON,
			data: objects("{\n  \"a\": [1, 2],\n  \"b\": {\"c\": null}\n}", "{ \"d\" : true }"),
			want: `{"a":[1,2],"b":{"c":null}}` + "\n" + `{"d":true}` + "\n",
		},
		{
			name: "escapes_preserved",
			fmtr: codec.JSON,
			data: objects(`{"s":"a\"b\u00e9\/"}`),
			want: `{"s":"a\"b\u00e9\/"}`,
		},
		// Error handling.
		{
			name:    "json_invalid_object",
			fmtr:    codec.JSON,
			data:    objects(`{"a":1}`, `{"b":}`),
			wantErr: true,
		},
		{
			name:    "ndjson_invalid_object",
			fmtr:    codec.NDJSON,
			data:    objects(`{"a":1}`, `{"b":}`),
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := tt.fmtr.EncodeJSON(tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("jsonFormat.EncodeJSON() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.want == "" && got != nil {
				t.Errorf("jsonFormat.EncodeJSON() = %q, want nil", got)
			}
			if string(got) != tt.want {
				t.Errorf("jsonFormat.EncodeJSON() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestJSONFormatEncodeRows(t *testing.T) {
	t.Parallel()

	ab := []string{"a", "b"}
	dups := []string{"id", "id", ""}
	ts := time.Date(2026, 10, 10, 1, 2, 3, 4, time.UTC)

	tests := []struct {
		name    string
		fmtr    codec.Formatter
		data    flow.Rows
		want    string
		wantErr bool
	}{
		// Payload shapes.
		{name: "json_nil", fmtr: codec.JSON, want: "{}"},
		{name: "json_array_nil", fmtr: codec.JSONArray, want: "[]"},
		{name: "ndjson_nil", fmtr: codec.NDJSON},
		{
			name: "json_single",
			fmtr: codec.JSON,
			data: flow.Rows{{Cols: ab, Vals: []any{int64(1), "x"}}},
			want: `{"a":1,"b":"x"}`,
		},
		{
			name: "json_array_single",
			fmtr: codec.JSONArray,
			data: flow.Rows{{Cols: ab, Vals: []any{int64(1), "x"}}},
			want: `[{"a":1,"b":"x"}]`,
		},
		{
			name: "json_multiple",
			fmtr: codec.JSON,
			data: flow.Rows{{Cols: ab, Vals: []any{int64(1), "x"}}, {Cols: ab, Vals: []any{int64(2), "y"}}},
			want: `[{"a":1,"b":"x"},{"a":2,"b":"y"}]`,
		},
		{
			name: "ndjson_multiple",
			fmtr: codec.NDJSON,
			data: flow.Rows{{Cols: ab, Vals: []any{int64(1), "x"}}, {Cols: ab, Vals: []any{int64(2), "y"}}},
			want: `{"a":1,"b":"x"}` + "\n" + `{"a":2,"b":"y"}` + "\n",
		},
		// Fidelity.
		{
			name: "column_order_and_unescaped_html",
			fmtr: codec.JSON,
			data: flow.Rows{{Cols: []string{"z", "a"}, Vals: []any{int64(1), "& < >"}}},
			want: `{"z":1,"a":"& < >"}`,
		},
		{
			name: "typed_values",
			fmtr: codec.JSON,
			data: flow.Rows{{
				Cols: []string{"bytes", "binary", "time", "float", "bool", "null"},
				Vals: []any{[]byte("abc"), []byte{0xff, 0x00}, ts, 1.5, true, nil},
			}},
			want: `{"bytes":"YWJj","binary":"/wA=","time":"2026-10-10T01:02:03.000000004Z","float":1.5,"bool":true,"null":null}`,
		},
		{
			name: "non_finite_floats",
			fmtr: codec.JSON,
			data: flow.Rows{{
				Cols: []string{"nan", "inf", "neg_inf", "nan32", "finite"},
				Vals: []any{math.NaN(), math.Inf(1), math.Inf(-1), float32(math.NaN()), 2.5},
			}},
			want: `{"nan":"NaN","inf":"Infinity","neg_inf":"-Infinity","nan32":"NaN","finite":2.5}`,
		},
		{
			name: "duplicate_and_empty_columns",
			fmtr: codec.NDJSON,
			data: flow.Rows{{Cols: dups, Vals: []any{1, 2, 3}}, {Cols: dups, Vals: []any{4, 5, 6}}},
			want: `{"id":1,"id_2":2,"_3":3}` + "\n" + `{"id":4,"id_2":5,"_3":6}` + "\n",
		},
		{
			name: "multiple_result_sets",
			fmtr: codec.JSONArray,
			data: flow.Rows{{Cols: ab, Vals: []any{1, 2}}, {Cols: dups, Vals: []any{3, 4, 5}}, {Cols: ab, Vals: []any{6, 7}}},
			want: `[{"a":1,"b":2},{"id":3,"id_2":4,"_3":5},{"a":6,"b":7}]`,
		},
		// Error handling.
		{
			name:    "unencodable_value",
			fmtr:    codec.JSON,
			data:    flow.Rows{{Cols: ab, Vals: []any{1, 2}}, {Cols: ab, Vals: []any{3, func() {}}}},
			wantErr: true,
		},
		{
			name:    "invalid_utf8_column_name",
			fmtr:    codec.JSON,
			data:    flow.Rows{{Cols: []string{"\xff"}, Vals: []any{1}}},
			wantErr: true,
		},
		{
			name:    "values_mismatch",
			fmtr:    codec.NDJSON,
			data:    flow.Rows{{Cols: ab, Vals: []any{1}}},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := tt.fmtr.EncodeRows(tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("jsonFormat.EncodeRows() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.want == "" && got != nil {
				t.Errorf("jsonFormat.EncodeRows() = %q, want nil", got)
			}
			if string(got) != tt.want {
				t.Errorf("jsonFormat.EncodeRows() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDecodeJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
		want    []string // Decoded objects, verbatim.
		wantErr bool
	}{
		// Valid payload shapes.
		{name: "empty"},
		{name: "whitespace", payload: " \n\t "},
		{name: "object", payload: `{"a":1}`, want: []string{`{"a":1}`}},
		{name: "empty_object", payload: `{}`},
		{name: "empty_object_with_whitespace", payload: "{ \n\t}"},
		{name: "empty_array", payload: `[]`},
		{name: "array", payload: `[{"a":1}, {}, {"b":2}]`, want: []string{`{"a":1}`, `{"b":2}`}},
		{name: "ndjson", payload: `{"a":1}` + "\n" + `{"b":2}` + "\n", want: []string{`{"a":1}`, `{"b":2}`}},
		{name: "multiple_arrays", payload: `[{"a":1}] [{"b":2}]`, want: []string{`{"a":1}`, `{"b":2}`}},
		{name: "mixed_stream", payload: `{"a":1} [{"b":2}] {"c":3}`, want: []string{`{"a":1}`, `{"b":2}`, `{"c":3}`}},
		// Lossless objects.
		{
			name:    "big_number_and_order",
			payload: `{"n":12345678901234567890,"f":1.50}`,
			want:    []string{`{"n":12345678901234567890,"f":1.50}`},
		},
		{
			name:    "nested_order",
			payload: `{"obj":{"z":1,"a":[true,null,"s"]}}`,
			want:    []string{`{"obj":{"z":1,"a":[true,null,"s"]}}`},
		},
		{
			name:    "escaped_strings_and_whitespace",
			payload: " [ {\"s\" : \"a\\\"b\\u00e9\" , \"n\":\t-0.5e10 } ] ",
			want:    []string{"{\"s\" : \"a\\\"b\\u00e9\" , \"n\":\t-0.5e10 }"},
		},
		// Errors.
		{name: "syntax_error", payload: `{"a":}`, wantErr: true},
		{name: "duplicate_names", payload: `{"a":1,"a":2}`, wantErr: true},
		{name: "nested_duplicate_names", payload: `{"a":{"b":1,"b":2}}`, wantErr: true},
		{name: "invalid_utf8", payload: "{\"a\":\"\xff\"}", wantErr: true},
		{name: "top_level_number", payload: `42`, wantErr: true},
		{name: "top_level_after_object", payload: `{"a":1} "s"`, wantErr: true},
		{name: "array_of_numbers", payload: `[1,2]`, wantErr: true},
		{name: "nested_array", payload: `[[{"a":1}]]`, wantErr: true},
		{name: "truncated_array", payload: `[{"a":1}`, wantErr: true},
		{name: "truncated_array_start", payload: `[`, wantErr: true},
		{name: "invalid_array_element", payload: `[{"a":1},}`, wantErr: true},
		{name: "invalid_object_in_array", payload: `[{"a":}]`, wantErr: true},
		{name: "invalid_top_level_token", payload: `{"a":1} x`, wantErr: true},
		{name: "invalid_literal", payload: `not JSON`, wantErr: true},
		{name: "invalid_literal_in_array", payload: `[nope]`, wantErr: true},
		{name: "trailing_garbage", payload: `{"a":1} }`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := codec.DecodeJSON([]byte(tt.payload))
			if (err != nil) != tt.wantErr {
				t.Fatalf("DecodeJSON() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && got != nil {
				t.Errorf("DecodeJSON() = %v, want nil on error", got)
			}
			gotStrs := make([]string, 0, len(got))
			for _, obj := range got {
				gotStrs = append(gotStrs, string(obj))
			}
			if !slices.Equal(gotStrs, tt.want) && (len(gotStrs) > 0 || len(tt.want) > 0) {
				t.Errorf("DecodeJSON() = %q, want %q", gotStrs, tt.want)
			}
		})
	}
}

func TestDecodeJSONRoundTrip(t *testing.T) {
	t.Parallel()

	payload := "[{\"id\":12345678901234567890,\"tags\":[\"x\",\"y\"]},\n {\"id\":2,\"nested\":{\"b\":1,\"a\":2}}]"
	tests := []struct {
		name string
		fmtr codec.Formatter
		want string
	}{
		{
			name: "json",
			fmtr: codec.JSON,
			want: `[{"id":12345678901234567890,"tags":["x","y"]},{"id":2,"nested":{"b":1,"a":2}}]`,
		},
		{
			name: "json_array",
			fmtr: codec.JSONArray,
			want: `[{"id":12345678901234567890,"tags":["x","y"]},{"id":2,"nested":{"b":1,"a":2}}]`,
		},
		{
			name: "ndjson",
			fmtr: codec.NDJSON,
			want: `{"id":12345678901234567890,"tags":["x","y"]}` + "\n" + `{"id":2,"nested":{"b":1,"a":2}}` + "\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			objs, err := codec.DecodeJSON([]byte(payload))
			if err != nil {
				t.Fatalf("DecodeJSON() error: %v", err)
			}
			got, err := tt.fmtr.EncodeJSON(objs)
			if err != nil {
				t.Fatalf("EncodeJSON() error: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("EncodeJSON(DecodeJSON()) = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestDecodeJSONZeroCopy(t *testing.T) {
	t.Parallel()

	payload := []byte(`[{"s":"a\"bé"}, {"n":1.50}]`)
	objs, err := codec.DecodeJSON(payload)
	if err != nil {
		t.Fatalf("DecodeJSON() error: %v", err)
	}

	for i, want := range []string{`{"s":"a\"bé"}`, `{"n":1.50}`} {
		obj := objs[i]
		// Verbatim bytes, and the same memory as the payload (no copy).
		if string(obj) != want {
			t.Errorf("object %d = %s, want %s", i, obj, want)
		}
		if j := bytes.Index(payload, obj); j < 0 || &payload[j] != &obj[0] {
			t.Errorf("object %d doesn't reference the payload", i)
		}
		// Appending to an object must not overwrite the rest of the payload.
		if cap(obj) != len(obj) {
			t.Errorf("object %d capacity = %d, want %d", i, cap(obj), len(obj))
		}
	}
}

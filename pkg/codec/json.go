package codec

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"

	"github.com/daabr/versipellis/pkg/flow"
)

const (
	mimeTypeJSON   = "application/json"
	mimeTypeNDJSON = "application/x-ndjson"
)

// JSON, JSONArray, and NDJSON are predefined instances that implement the [Formatter]
// interface. They are immutable, stateless, and safe for concurrent use.
var (
	JSON      = jsonFormat{mime: mimeTypeJSON, array: false}
	JSONArray = jsonFormat{mime: mimeTypeJSON, array: true}
	NDJSON    = jsonFormat{mime: mimeTypeNDJSON}

	newline = []byte{'\n'}

	jsonMarshalOpts = json.JoinOptions(json.Deterministic(true), json.WithMarshalers(NonFiniteFloats))
)

// NonFiniteFloats is a set of marshalers for [json.WithMarshalers], which write NaN and ±Inf
// floats as strings, so encoders of row values will be able to handle them successfully.
var NonFiniteFloats = json.JoinMarshalers(
	json.MarshalToFunc(marshalNonFiniteFloat[float64]),
	json.MarshalToFunc(marshalNonFiniteFloat[float32]),
)

type jsonFormat struct {
	mime  string
	array bool
}

// ContentType returns the MIME type of payloads in this format.
func (f jsonFormat) ContentType() string {
	return f.mime
}

// EncodeJSON supports [FormatJSON], [FormatJSONArray], and [FormatNDJSON]. It writes each JSON object
// exactly as it appeared in its source (e.g., the order of names and the escape sequences in strings
// are preserved), except that whitespace is removed, so each object occupies a single line.
func (f jsonFormat) EncodeJSON(objects flow.JSON) ([]byte, error) {
	if payload, done := f.emptyPayload(len(objects)); done {
		return payload, nil
	}

	// Compacting never makes an object longer, so the payload is allocated exactly once.
	size := 2 // Array brackets.
	for _, obj := range objects {
		size += len(obj) + 1 // Separator: comma or newline.
	}
	payload := make([]byte, 0, size)
	ndjson, array := f.shape(len(objects))

	if array {
		payload = append(payload, '[')
	}
	for i, obj := range objects {
		if array && i > 0 {
			payload = append(payload, ',')
		}
		var err error // AppendFormat also validates the object.
		if payload, err = jsontext.AppendFormat(payload, obj, jsontext.PreserveRawStrings(true)); err != nil {
			return nil, fmt.Errorf("JSON encoding error in object %d: %w", i+1, err)
		}
		if ndjson {
			payload = append(payload, '\n')
		}
	}
	if array {
		payload = append(payload, ']')
	}

	return payload, nil
}

// EncodeRows supports [FormatJSON], [FormatJSONArray], and [FormatNDJSON]. It writes each row as a JSON object,
// with the names and values in their column order, and duplicate or empty column names are made unique with
// [columnNames]. Despite being printable, it prioritizes fidelity over readability. This means byte values are
// always Base64-encoded, null values are not omitted, and if any value cannot be encoded the entire payload is bad.
// Non-finite floats, which JSON numbers can't represent, are written as strings (see [marshalNonFiniteFloat]).
func (f jsonFormat) EncodeRows(rows flow.Rows) ([]byte, error) {
	if payload, done := f.emptyPayload(len(rows)); done {
		return payload, nil
	}

	buf := new(bytes.Buffer)
	enc := jsontext.NewEncoder(buf)
	ndjson, array := f.shape(len(rows))
	if array {
		_ = enc.WriteToken(jsontext.BeginArray) // Can't fail at the beginning of the output.
	}

	var cols, names []string // Cached per result-set (which share the same [flow.Row.Cols] slice).
	for i, row := range rows {
		if !sameSlice(row.Cols, cols) {
			cols, names = row.Cols, columnNames(row.Cols)
		}
		if err := writeRow(enc, names, row.Vals); err != nil {
			return nil, fmt.Errorf("JSON encoding error in row %d: %w", i+1, err)
		}
	}

	if array {
		_ = enc.WriteToken(jsontext.EndArray) // Can't fail after valid objects.
	}
	if ndjson {
		return buf.Bytes(), nil // The encoder writes a newline after each top-level value.
	}
	return bytes.TrimSuffix(buf.Bytes(), newline), nil
}

func (f jsonFormat) emptyPayload(n int) ([]byte, bool) {
	switch {
	case n > 0:
		return nil, false
	case f.mime == mimeTypeNDJSON:
		return nil, true
	case f.array:
		return []byte("[]"), true
	default:
		return []byte("{}"), true
	}
}

// shape reports whether a payload of n > 0 objects in this format is NDJSON, or a JSON array.
func (f jsonFormat) shape(n int) (ndjson, array bool) {
	ndjson = f.mime == mimeTypeNDJSON
	return ndjson, !ndjson && (f.array || n > 1)
}

// sameSlice reports whether both slices have the same length and the same underlying array.
func sameSlice(a, b []string) bool {
	return len(a) == len(b) && (len(a) == 0 || &a[0] == &b[0])
}

// columnNames returns unique and non-empty names for the given column names, so they can be used as the names
// of JSON object members. Names that are already unique and non-empty are kept as-is. Duplicate names (except for
// the first occurrence) and empty names get the column's 1-based position as a suffix (e.g., {"id", "id", "x", ""}
// becomes {"id", "id_2", "x", "_4"}), repeatedly if necessary to avoid conflicts with other columns. If no names need
// to change, it returns the input slice itself, so the result must be treated as read-only, like [flow.Row.Cols].
func columnNames(cols []string) []string {
	// Maps each name to the position where it's kept as-is: its first occurrence for column names,
	// and -1 for generated names (they only need to be reserved, to prevent conflicts with them).
	taken := make(map[string]int, len(cols))
	needsChange := false
	for i, col := range cols {
		if _, found := taken[col]; found || col == "" {
			needsChange = true
			continue
		}
		taken[col] = i
	}
	if !needsChange {
		return cols
	}

	names := make([]string, len(cols))
	for i, col := range cols {
		if pos, found := taken[col]; found && pos == i {
			names[i] = col
			continue
		}

		suffix := "_" + strconv.Itoa(i+1)
		name := col + suffix
		for {
			if _, found := taken[name]; !found {
				break
			}
			name += suffix
		}
		taken[name] = -1
		names[i] = name
	}

	return names
}

func writeRow(enc *jsontext.Encoder, names []string, vals []any) error {
	if len(names) != len(vals) {
		return fmt.Errorf("row has %d columns but %d values", len(names), len(vals))
	}

	_ = enc.WriteToken(jsontext.BeginObject) // Can't fail at the beginning of a top-level value or array element.
	for i, name := range names {
		// May fail due to duplicate names (see [columnNames]) or invalid UTF-8.
		if err := enc.WriteToken(jsontext.String(name)); err != nil {
			return fmt.Errorf("JSON name writing error in column %d: %w", i+1, err)
		}
		if err := json.MarshalEncode(enc, vals[i], jsonMarshalOpts); err != nil {
			return fmt.Errorf("JSON value encoding error in column %q: %w", name, err)
		}
	}
	_ = enc.WriteToken(jsontext.EndObject) // Can't fail after valid members.
	return nil
}

// marshalNonFiniteFloat writes NaN and ±Inf (which are valid values in databases such as PostgreSQL and
// SQLite, but not in JSON) as the strings "NaN", "Infinity", and "-Infinity" - similar to PostgreSQL's
// to_json(), instead of failing the entire payload. Finite values fall back to the default encoding.
func marshalNonFiniteFloat[F float32 | float64](enc *jsontext.Encoder, f F) error {
	switch v := float64(f); {
	case math.IsNaN(v):
		_ = enc.WriteToken(jsontext.String("NaN"))
	case math.IsInf(v, 1):
		_ = enc.WriteToken(jsontext.String("Infinity"))
	case math.IsInf(v, -1):
		_ = enc.WriteToken(jsontext.String("-Infinity"))
	default:
		return errors.ErrUnsupported
	}
	return nil
}

// DecodeJSON parses a payload which contains a single JSON object, an array of JSON objects, or a
// stream of JSON objects (e.g., NDJSON), into [flow.JSON] objects. More generally, it accepts any
// stream of top-level objects and arrays of objects, in any mix (e.g., `{"a":1} [{"b":2}] {"c":3}`),
// and flattens it into a single sequence of objects, in their original order.
//
// It is strict: a syntax error, a duplicate name, or any top-level value or array element
// which isn't an object invalidates the entire payload. It is also lossless: each object
// is kept exactly as it appears in the payload. Empty objects are skipped.
//
// Ownership: the objects reference the payload instead of copying it, so callers must not modify or reuse
// the payload afterwards, and it remains in memory as long as any of the returned objects are in use.
//
// Resource usage: each object costs only a slice header (24 bytes), regardless of its size and contents, so
// even a payload of tiny objects (at least 6 bytes each, e.g., `{"":0}`) needs only 4 times its own size for their
// headers. The slice's spare capacity from appending adds up to ~25% for large payloads (up to 2x for small ones).
func DecodeJSON(payload []byte) (flow.JSON, error) {
	// A [bytes.Buffer] lets the decoder parse the payload in place, without copying it into an internal buffer.
	dec := jsontext.NewDecoder(bytes.NewBuffer(payload))
	var objects flow.JSON
	var err error

	for {
		switch kind := dec.PeekKind(); kind {
		case '{':
			if objects, err = appendObject(dec, payload, objects); err != nil {
				return nil, err
			}

		case '[':
			_, _ = dec.ReadToken() // Can't fail after a successful [jsontext.Decoder.PeekKind].
			for dec.PeekKind() == '{' {
				if objects, err = appendObject(dec, payload, objects); err != nil {
					return nil, err
				}
			}
			if dec.PeekKind() != ']' {
				return nil, unexpectedKind(dec, "array element")
			}
			_, _ = dec.ReadToken() // Same as above.

		case jsontext.KindInvalid:
			if _, err := dec.ReadToken(); !errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("JSON decoding error: %w", err)
			}
			return objects, nil

		default:
			return nil, unexpectedKind(dec, "top-level value")
		}
	}
}

// appendObject reads the next JSON object from the decoder (which validates it),
// and appends it to the given objects as a slice of the payload, unless it's empty.
func appendObject(dec *jsontext.Decoder, payload []byte, objects flow.JSON) (flow.JSON, error) {
	// Only valid until the next read call.
	raw, err := dec.ReadValue()
	if err != nil {
		return nil, fmt.Errorf("JSON decoding error: %w", err)
	}
	if bytes.TrimLeft(raw[1:], " \t\r\n")[0] == '}' {
		return objects, nil // Empty object.
	}

	// Immediately after the value, which the decoder returns verbatim.
	end := dec.InputOffset()
	start := end - int64(len(raw))

	// Limit the capacity, so appending to the object doesn't modify the rest of the payload.
	return append(objects, jsontext.Value(payload[start:end:end])), nil
}

// unexpectedKind reports the kind of the next value, or the decoding error which prevents reading it
// (e.g., [jsontext.Decoder.PeekKind] reports "null" for any token that starts with "n", valid or not).
func unexpectedKind(dec *jsontext.Decoder, position string) error {
	kind := dec.PeekKind()
	if _, err := dec.ReadValue(); err != nil {
		return fmt.Errorf("JSON decoding error: %w", err)
	}
	return fmt.Errorf("unexpected JSON %s as %s, want object", kind, position)
}

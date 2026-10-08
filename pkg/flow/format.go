package flow

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"unicode"
	"unicode/utf8"
)

// Reminder: refactor (in a separate PR) this file into a new package with a proper struct per format and code reuse.

// Format defines how a batch of [Structured] data should be encoded as a single payload.
type Format string

// Format* constants represent all the available encoding formats for batches of [Structured] data.
const (
	FormatJSON      Format = "json"       // JSON object or array, depending on the number of elements.
	FormatJSONArray Format = "json_array" // JSON array, even if it contains a single element.
	FormatNDJSON    Format = "ndjson"     // NDJSON, a.k.a. JSONL (https://ndjson.com/).
)

var (
	jsonOpts = json.JoinOptions(json.Deterministic(true))

	printableJSONOpts = json.JoinOptions(json.Deterministic(true),
		json.WithMarshalers(json.MarshalToFunc(marshalPrintableBytesToString)),
	)
)

// ContentType returns the MIME type of payloads in this format.
func (f Format) ContentType() string {
	switch f {
	case FormatJSON, FormatJSONArray:
		return "application/json"
	case FormatNDJSON:
		return "application/x-ndjson"
	default:
		return string(f)
	}
}

// Encode converts [Structured] records into a single payload in the specified format, preserving the data exactly.
// Byte fields are always Base64-encoded, and if any record cannot be encoded the entire [Chunk] is considered bad.
// Senders that store or forward data must use this function, not [Format.Print].
func (f Format) Encode(data Structured) ([]byte, error) {
	var payload []byte
	var err error

	switch f {
	case FormatJSON:
		if data.Len() == 0 {
			payload, err = json.Marshal(map[string]any{}, jsonOpts)
			break
		}
		if data.Len() == 1 {
			payload, err = json.Marshal(data[0], jsonOpts)
			break
		}
		fallthrough

	case FormatJSONArray:
		payload, err = json.Marshal(data, jsonOpts)

	case FormatNDJSON:
		buf := new(bytes.Buffer)
		for _, obj := range data {
			b, err := json.Marshal(obj, jsonOpts)
			if err != nil {
				return nil, fmt.Errorf("JSON encoding error: %w", err)
			}
			_, _ = buf.Write(append(b, '\n')) // [bytes.Buffer.Write]: err is always nil.
		}
		return buf.Bytes(), nil

	default:
		return nil, fmt.Errorf("unsupported format %q", f)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to encode as %q: %w", f, err)
	}
	return payload, nil
}

// Print writes [Structured] records to the given [io.Writer] in a human-readable-but-irreversible variant of
// the specified format. Printable byte slices are displayed as-is without being converted to Base64-encoded
// strings, and records that cannot be encoded are skipped without affecting the rest of the [Chunk].
// The output is written in a single call, and only if it's not empty. Compare with [Format.Encode].
func (f Format) Print(w io.Writer, data Structured) error {
	if f != FormatNDJSON {
		return fmt.Errorf("unsupported format %q", f)
	}

	buf := new(bytes.Buffer)
	for i, obj := range data {
		b, err := json.Marshal(obj, printableJSONOpts)
		if err != nil {
			slog.Warn("NDJSON encoding error", slog.Any("error", err), slog.Int("index", i))
			continue
		}
		_, _ = buf.Write(append(b, '\n')) // [bytes.Buffer.Write]: err is always nil.
	}

	if buf.Len() > 0 {
		if _, err := w.Write(buf.Bytes()); err != nil {
			return fmt.Errorf("write error: %w", err)
		}
	}
	return nil
}

func marshalPrintableBytesToString(enc *jsontext.Encoder, data []byte) error {
	// Fall back to the default encoding (Base64)?
	if !utf8.Valid(data) {
		return errors.ErrUnsupported
	}
	for _, r := range string(data) {
		if !unicode.IsPrint(r) && !unicode.IsSpace(r) {
			return errors.ErrUnsupported
		}
	}

	// Printable UTF-8 bytes can be safely printed as a JSON string.
	if err := enc.WriteToken(jsontext.String(string(data))); err != nil {
		return fmt.Errorf("JSON string token write error: %w", err)
	}
	return nil
}

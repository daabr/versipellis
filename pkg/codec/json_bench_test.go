package codec_test

import (
	"bytes"
	"strconv"
	"testing"
	"time"

	"github.com/daabr/versipellis/pkg/codec"
	"github.com/daabr/versipellis/pkg/flow"
)

const (
	benchRecords = 100_000
)

// benchPayloads returns NDJSON payloads with tiny objects (the worst case for memory amplification) and typical ones.
func benchPayloads() map[string][]byte {
	tiny := []byte(`{"a":1}` + "\n")
	typical := []byte(`{"id":12345,"name":"abcdefgh","tags":["a","b"],"n":1.5,"ok":true,"nested":{"z":1,"a":2}}` + "\n")
	return map[string][]byte{
		"tiny_objects":    bytes.Repeat(tiny, benchRecords),
		"typical_objects": bytes.Repeat(typical, benchRecords),
	}
}

func BenchmarkDecodeJSON(b *testing.B) {
	for name, payload := range benchPayloads() {
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(payload)))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := codec.DecodeJSON(payload); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkEncodeJSON(b *testing.B) {
	for name, payload := range benchPayloads() {
		objects, err := codec.DecodeJSON(payload)
		if err != nil {
			b.Fatal(err)
		}

		for _, f := range []struct {
			name string
			fmtr codec.Formatter
		}{
			{"json", codec.JSON},
			{"ndjson", codec.NDJSON},
		} {
			b.Run(name+"/"+f.name, func(b *testing.B) {
				b.SetBytes(int64(len(payload)))
				b.ReportAllocs()
				for b.Loop() {
					if _, err := f.fmtr.EncodeJSON(objects); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func BenchmarkEncodeRows(b *testing.B) {
	cols := []string{"id", "name", "score", "ok", "blob", "ts", "missing"}
	ts := time.Date(2026, 10, 10, 1, 2, 3, 0, time.UTC)
	rows := make(flow.Rows, benchRecords)
	for i := range rows {
		rows[i] = flow.Row{
			Cols: cols,
			Vals: []any{int64(i), "abcdefgh" + strconv.Itoa(i), 1.5, true, []byte{0xde, 0xad}, ts, nil},
		}
	}

	for _, f := range []struct {
		name string
		fmtr codec.Formatter
	}{
		{"json", codec.JSON}, {"ndjson", codec.NDJSON},
	} {
		b.Run(f.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := f.fmtr.EncodeRows(rows); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

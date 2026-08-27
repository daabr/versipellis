package dest

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestStdout(t *testing.T) {
	writer = new(strings.Builder)
	lazyInit()
	t.Cleanup(func() {
		writer = os.Stdout
		lazyInit() // Reset encoder to use [os.Stdout].
	})

	tests := []struct {
		name string
		data any
		want string
	}{
		{
			name: "nil",
			data: nil,
			want: "null\n",
		},
		{
			name: "int",
			data: 42,
			want: "42\n",
		},
		{
			name: "json",
			data: map[string]any{"key": "value", "number": 42, "list": []any{1, 2, 3}},
			want: `{"key":"value","list":[1,2,3],"number":42}` + "\n",
		},
		{
			name: "not_json",
			data: map[string]any{"channel": make(chan struct{})}, // Go channels cannot be encoded as JSON.
			want: "",                                             // Log this, but don't pollute [os.Stdout] with non-JSON text.
		},
		// After the "not_json" test case, to ensure it doesn't leave [encoder] in a broken state.
		{
			name: "string",
			data: "just a string",
			want: `"just a string"` + "\n",
		},
		{
			name: "unencoded_html",
			data: "& < >",
			want: `"& < >"` + "\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Stdout(t.Context(), tt.data); err != nil {
				t.Fatalf("Stdout(%s) error = %v, wantErr nil", tt.name, err)
			}

			sb, ok := writer.(*strings.Builder)
			if !ok {
				t.Fatalf("unexpected writer type: %T", writer)
			}
			if got := sb.String(); got != tt.want {
				t.Errorf("Stdout(%s) stdout = %q, want %q", tt.name, got, tt.want)
			}

			sb.Reset() // Clear the buffer for the next test case.
		})
	}
}

const (
	goroutines        = 10
	callsPerGoroutine = 10
)

func TestStdoutConcurrency(t *testing.T) {
	writer = new(bytes.Buffer)
	lazyInit()
	t.Cleanup(func() {
		writer = os.Stdout
		lazyInit() // Reset encoder to use [os.Stdout].
	})

	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Go(func() {
			for i := range callsPerGoroutine {
				if err := Stdout(t.Context(), map[string]any{"goroutine": g, "call": i}); err != nil {
					t.Errorf("Stdout(goroutine %d, call %d) error = %v, wantErr nil", g, i, err)
				}
			}
		})
	}
	wg.Wait()

	buf, ok := writer.(*bytes.Buffer)
	if !ok {
		t.Fatalf("unexpected writer type: %T", writer)
	}

	seen := [goroutines][callsPerGoroutine]int{}
	gotLines := 0

	data, err := buf.ReadBytes('\n')
	for err == nil {
		got := new(struct {
			Goroutine int `json:"goroutine"`
			Call      int `json:"call"`
		})
		if err = json.Unmarshal(data, got); err != nil {
			t.Fatalf("invalid JSON in line %d: %q: %v", gotLines+1, data, err)
		}

		gotLines++
		if got.Goroutine < 0 || got.Goroutine >= goroutines || got.Call < 0 || got.Call >= callsPerGoroutine {
			t.Errorf("invalid index(es) in line %d: %+v", gotLines, got)
		}

		seen[got.Goroutine][got.Call]++
		if seen[got.Goroutine][got.Call] > 1 {
			t.Errorf("instance no. %d of: goroutine %d, call %d", seen[got.Goroutine][got.Call], got.Goroutine, got.Call)
		}

		data, err = buf.ReadBytes('\n')
	}
	if !errors.Is(err, io.EOF) {
		t.Errorf("error reading from buffer: %v", err)
	}

	wantLines := goroutines * callsPerGoroutine
	if gotLines != wantLines {
		t.Errorf("got %d lines in total, want %d", gotLines, wantLines)
	}
}

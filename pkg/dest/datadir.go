package dest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

const (
	dataDir = "data"

	dirPermissions  = 0o700
	filePermissions = 0o600
	fileFlags       = os.O_CREATE | os.O_EXCL | os.O_WRONLY
	dlqAttempts     = 3
)

var dlqInProgress sync.WaitGroup // Reminder: expose to main() a time-bounded wait function in a future PR.

// DeadLetterQueue is an alternative destination for data that could not be delivered successfully
// by other [config.Sender]s. It behaves very similarly to [Stdout], but writes the output to the
// local filesystem instead, specifically to a k-sortable filename in the app's data directory.
func DeadLetterQueue(_ context.Context, data any) {
	if data == nil {
		return // Don't log nil data, other senders use it as a sentinel for batches.
	}

	now := time.Now().UTC()
	payload := serializeData(data)
	if len(payload) == 0 {
		return
	}

	dlqInProgress.Go(func() {
		for range dlqAttempts {
			if asyncWriteToDataDir(payload, now) {
				return
			}
		}
	})
}

func asyncWriteToDataDir(data []byte, now time.Time) bool {
	dir, file := uniqueKSortablePath(dataDir, now)
	path := filepath.Join(dir, file)

	err := os.MkdirAll(dir, dirPermissions)
	if err == nil {
		var f *os.File
		f, err = os.OpenFile(path, fileFlags, filePermissions) //gosec:disable G304: Self-generated path.
		if err == nil {
			defer f.Close()
			if _, err = f.Write(data); err == nil {
				if err = f.Sync(); err == nil {
					return true
				}
			}
			_ = os.Remove(path) // Cleanup in case the file was created but writing to it failed.
		}
	}
	slog.Error("failed to write data to DLQ file", slog.Any("error", err), slog.String("path", path))
	return false
}

func serializeData(data any) []byte {
	switch t := data.(type) {
	case []byte:
		return bytes.Clone(t)

	case *http.Request:
		if t == nil {
			return nil
		}
		if t.Body != nil {
			defer t.Body.Close()
		}
		var buf bytes.Buffer
		if err := t.Write(&buf); err != nil {
			slog.Error("failed to serialize HTTP request into DLQ file", slog.Any("error", err))
			return nil
		}
		return buf.Bytes()

	case *http.Response:
		if t == nil {
			return nil
		}
		if t.Body != nil {
			defer t.Body.Close()
		}
		var buf bytes.Buffer
		if err := t.Write(&buf); err != nil {
			slog.Error("failed to serialize HTTP response into DLQ file", slog.Any("error", err))
			return nil
		}
		return buf.Bytes()
	}

	// Fall-back to JSON encoding for other data types.
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false) // Passing raw data, not rendering it, so don't alter it.

	if err := encoder.Encode(data); err != nil {
		slog.Error("failed to serialize JSON into DLQ file", slog.Any("error", err))
		return nil
	}

	return buf.Bytes()
}

func uniqueKSortablePath(prefix string, now time.Time) (dir, file string) {
	// Intentionally not reusing the 'now' parameter: in case we fail to
	// generate a random number below, we still have a unique timestamp.
	n := time.Now().UnixNano()

	for range dlqAttempts {
		if r, err := rand.Int(rand.Reader, big.NewInt(math.MaxInt64)); err == nil && r.IsInt64() {
			n = r.Int64()
			break
		}
	}

	dir = filepath.Join(prefix, now.Format("2006-01-02__15"))
	file = fmt.Sprintf("%d__%s", now.UnixNano(), strconv.FormatInt(n, 36))
	return dir, file
}

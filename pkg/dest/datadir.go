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
	dlqInProgress.Go(func() { asyncWriteToDataDir(data, now) })
}

func asyncWriteToDataDir(data any, now time.Time) {
	payload := serializeData(data)
	if len(payload) == 0 {
		return
	}

	dir, file := uniqueKSortablePath(dataDir, now)
	path := filepath.Join(dir, file)

	var err error
	err = os.MkdirAll(dir, dirPermissions)
	if err == nil {
		err = os.WriteFile(path, payload, filePermissions)
		if err == nil {
			return
		}
	}
	slog.Error("failed to write data to DLQ file", slog.Any("error", err), slog.String("path", path))
}

func serializeData(data any) []byte {
	switch v := data.(type) {
	case []byte:
		return v

	case *http.Request:
		if v == nil {
			return nil
		}
		if v.Body != nil {
			defer v.Body.Close()
		}
		var buf bytes.Buffer
		if err := v.Write(&buf); err != nil {
			slog.Error("failed to serialize HTTP request into DLQ file", slog.Any("error", err))
			return nil
		}
		return buf.Bytes()

	case *http.Response:
		if v == nil {
			return nil
		}
		if v.Body != nil {
			defer v.Body.Close()
		}
		var buf bytes.Buffer
		if err := v.Write(&buf); err != nil {
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
	n, err := rand.Int(rand.Reader, big.NewInt(math.MaxInt64))
	if err != nil || !n.IsInt64() {
		n = big.NewInt(0)
	}
	suffix := strconv.FormatInt(n.Int64(), 36)
	file = fmt.Sprintf("%d__%s", now.UnixNano(), suffix)

	return filepath.Join(prefix, now.Format("2006-01-02__15")), file
}

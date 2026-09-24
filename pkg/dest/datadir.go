package dest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	attempts = 3

	dirPermissions  = 0o700
	filePermissions = 0o600
	fileFlags       = os.O_CREATE | os.O_EXCL | os.O_WRONLY
)

// DeadLetterQueue is an alternative destination for data that couldn't be delivered
// successfully by other senders. It behaves similarly to [Stdout], but writes the
// data into a local directory instead, specifically to a k-sortable filename.
type DeadLetterQueue struct {
	root *os.Root

	inProgress sync.WaitGroup
	lameDuck   atomic.Bool
	closeMu    sync.RWMutex
	closeOnce  sync.Once
}

// InitDeadLetterQueue initializes an alternative destination for data that couldn't be
// delivered successfully by other senders. It behaves similarly to [Stdout], but writes
// the data into a local directory instead, specifically to a k-sortable filename.
func InitDeadLetterQueue(rootDir string) *DeadLetterQueue {
	// No need to check for errors here: if the directory creation fails
	// for any reason, the subsequent file creation will fail as well.
	_ = os.MkdirAll(rootDir, dirPermissions)

	r, err := os.OpenRoot(rootDir)
	if err != nil {
		slog.Error("dead-letter-queue directory initialization error", slog.Any("error", err))
		return nil
	}

	return &DeadLetterQueue{root: r}
}

// Send serializes and writes any data into a file with a k-sortable name
// within the "data" directory in the process's current working directory.
func (d *DeadLetterQueue) Send(_ context.Context, data any) {
	// Don't log nil data, other senders use it as a sentinel for batches.
	// Also, don't write a new file if we're almost done shutting down.
	if data == nil || d.lameDuck.Load() {
		return
	}

	now := time.Now().UTC()
	payload := serializeData(data)
	if len(payload) == 0 {
		return
	}

	d.closeMu.RLock()
	defer d.closeMu.RUnlock()

	if d.lameDuck.Load() {
		return
	}

	d.inProgress.Go(func() {
		for range attempts {
			if d.asyncWriteFile(payload, now, dirPermissions, filePermissions) {
				return
			}
		}
	})
}

// Close waits (up to 1 second, not [CloseTimeout]) for disk writes which are currently in progress to
// complete, and prevents new files from being created. This is the last step before process termination.
func (d *DeadLetterQueue) Close(ctx context.Context) {
	d.closeOnce.Do(func() {
		d.closeMu.Lock()
		d.lameDuck.Store(true)
		d.closeMu.Unlock()

		shutdownCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()

		done := make(chan struct{})
		go func() {
			defer close(done)
			d.inProgress.Wait()
			_ = d.root.Close()
		}()

		select {
		case <-done:
			// All done.
		case <-shutdownCtx.Done():
			slog.Error("closing Dead-Letter-Queue writer forcefully")
			// Not *really* stopping disk writes, but the next step in
			// main() is process termination, which does achieve this.
			return
		}
	})
}

func serializeData(data any) []byte {
	switch t := data.(type) {
	case []byte:
		// Mutation of the original byte slice is not a concern because it's already abandoned by the data
		// source. On the other hand, GC pressure due to duplicating huge blobs is something we need to avoid.
		return t

	case *http.Request:
		if t == nil {
			return nil
		}
		if t.Body != nil {
			defer t.Body.Close()
		}
		buf := new(bytes.Buffer)
		if err := t.Write(buf); err != nil {
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
		buf := new(bytes.Buffer)
		if err := t.Write(buf); err != nil {
			slog.Error("failed to serialize HTTP response into DLQ file", slog.Any("error", err))
			return nil
		}
		return buf.Bytes()
	}

	// Fall-back to JSON encoding for other data types.
	buf := new(bytes.Buffer)
	encoder := json.NewEncoder(buf)
	encoder.SetEscapeHTML(false) // Passing raw data, not rendering it, so don't alter it.

	if err := encoder.Encode(data); err != nil {
		slog.Error("failed to serialize JSON into DLQ file", slog.Any("error", err))
		return nil
	}

	return buf.Bytes()
}

func (d *DeadLetterQueue) asyncWriteFile(data []byte, now time.Time, dirPerms, filePerms os.FileMode) bool {
	dir, file := uniqueKSortablePath(now)
	path := filepath.Join(dir, file)

	if err := d.root.Mkdir(dir, dirPerms); err != nil && !errors.Is(err, os.ErrExist) {
		slog.Error("failed to create DLQ subdirectory", slog.Any("error", err), slog.String("dir", dir))
		return false
	}

	f, err := d.root.OpenFile(path, fileFlags, filePerms)
	if err != nil {
		slog.Error("failed to create DLQ file", slog.Any("error", err), slog.String("path", path))
		return false
	}
	defer f.Close()

	if err := writeAndSync(f, data); err != nil {
		slog.Error("failed to write DLQ file", slog.Any("error", err), slog.String("path", path))
		_ = d.root.Remove(path) // Cleanup in case the file was created but writing to it failed.
		return false
	}

	return true
}

func uniqueKSortablePath(now time.Time) (dir, file string) {
	// Intentionally not reusing the 'now' parameter: in case we fail to generate
	// a random number below, we still have a relatively unique timestamp to use,
	// even if there are multiple files being created at the same time.
	n := time.Now().UnixNano()

	for range attempts {
		if r, err := rand.Int(rand.Reader, big.NewInt(math.MaxInt64)); err == nil && r.IsInt64() {
			n = r.Int64()
			break
		}
	}

	dir = now.Format("2006-01-02__15")
	file = fmt.Sprintf("%d__%s", now.UnixNano(), strconv.FormatInt(n, 36))
	return dir, file
}

func writeAndSync(f *os.File, data []byte) error {
	_, err := f.Write(data)
	return errors.Join(err, f.Sync())
}

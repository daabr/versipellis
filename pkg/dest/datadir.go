package dest

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/daabr/versipellis/pkg/codec"
	"github.com/daabr/versipellis/pkg/config"
	"github.com/daabr/versipellis/pkg/flow"
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
		slog.Error("Dead-Letter Queue directory initialization error", slog.Any("error", err))
		return nil
	}

	return &DeadLetterQueue{root: r}
}

// Send encodes and writes data into a file with a k-sortable name within the "data" directory in the process's
// current working directory. Structured data (e.g., JSON objects and SQL rows) is encoded in the given format,
// or as JSON by default.
//
// The DLQ is the last [config.Sender] to be closed, after closing all the other configured I/O components has
// already completed or timed out. This means that this function can be called after [DeadLetterQueue.Close] only
// by a collector or receiver goroutine that outlived its own close timeout. If that happens, the provided data is
// undeliverable anyway, so we can only warn about it and release it resources before the process terminates.
func (d *DeadLetterQueue) Send(ctx context.Context, data flow.Chunk, format config.Format) {
	if flow.IsEmpty(data) {
		return
	}

	if d.lameDuck.Load() {
		discardAfterClose(ctx, data, format)
		return
	}

	d.closeMu.RLock()
	now := time.Now().UTC()
	defer d.closeMu.RUnlock()

	if d.lameDuck.Load() {
		discardAfterClose(ctx, data, format)
		return
	}

	d.inProgress.Go(func() {
		files, ext := encode(data, format)
		for _, file := range files {
			if len(file) == 0 {
				continue
			}
			for range attempts {
				if d.asyncWriteFile(file, ext, now, dirPermissions, filePermissions) {
					break
				}
			}
		}
	})
}

// Close waits (up to 1 second) for disk writes which are currently in progress to complete,
// and prevents new files from being created. This is the last step before process termination.
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
			slog.Warn("closing Dead-Letter Queue writer forcefully", slog.Any("error", shutdownCtx.Err()))
			// Not *really* stopping disk writes, but the next step in
			// main() is process termination, which does achieve this.
			return
		}
	})
}

func discardAfterClose(ctx context.Context, data flow.Chunk, format config.Format) {
	slog.Warn("data loss due to process termination", slog.String("data_type", fmt.Sprintf("%T", data)),
		slog.Int("batch_size", data.Len()), slog.String("format", string(format)),
	)
	Discard.Send(ctx, data, format)
}

//bodyclose:handled
func encode(data flow.Chunk, format config.Format) (files [][]byte, ext string) {
	switch chunk := data.(type) {
	case flow.Blobs:
		return chunk, fileExtension(format)
	case flow.HTTPRequests:
		ext = ".http_req"
		for _, req := range chunk {
			if file := encodeHTTPRequest(req); file != nil {
				files = append(files, file)
			}
		}
	case flow.HTTPResponses:
		ext = ".http_resp"
		for _, resp := range chunk {
			if file := encodeHTTPResponse(resp); file != nil {
				files = append(files, file)
			}
		}
	case flow.JSON:
		return encodeStructured(chunk, format, codec.Formatter.EncodeJSON, printJSON)
	case flow.Rows:
		return encodeStructured(chunk, format, codec.Formatter.EncodeRows, printRows)
	default:
		slog.Error("unhandled data type in DLQ", slog.String("data_type", fmt.Sprintf("%T", chunk)))
	}

	return files, ext
}

func fileExtension(format config.Format) string {
	if format == "" || format == codec.FormatDefault {
		return ""
	}
	ext := "." + strings.Map(stripInvalidChars, strings.ToLower(string(format)))
	if ext == "." {
		return ""
	}
	return ext
}

func stripInvalidChars(r rune) rune {
	if r < '0' || (r > '9' && r != '_' && r < 'a') || r > 'z' {
		return -1
	}
	return r
}

// encodeStructured encodes structured data in the given format (JSON by default)
// into a single DLQ file. However, the DLQ is a last resort, so if that fails, we
// prefer to save whatever we can in a lossy way instead of dropping all the data.
func encodeStructured[C flow.JSON | flow.Rows](
	chunk C, format config.Format, strict func(codec.Formatter, C) ([]byte, error), lossy func(io.Writer, C) error,
) ([][]byte, string) {
	format, fmtr := codec.Resolve(format, codec.FormatJSON)
	content, err := strict(fmtr, chunk)
	if err == nil {
		return [][]byte{content}, fileExtension(format)
	}

	slog.Warn("failed to encode structured data into DLQ file, falling back to lossy NDJSON", slog.Any("error", err),
		slog.String("data_type", fmt.Sprintf("%T", chunk)), slog.String("format", string(format)),
	)
	buf := new(bytes.Buffer)
	_ = lossy(buf, chunk)
	if buf.Len() == 0 {
		return nil, ""
	}
	return [][]byte{buf.Bytes()}, ".lossy_ndjson"
}

func encodeHTTPRequest(req *http.Request) []byte {
	if req == nil {
		return nil
	}
	if req.Body != nil {
		defer req.Body.Close()
	}

	redactHeaders(req.Header)
	redactHeaders(req.Trailer)
	if req.URL != nil {
		req.URL.RawQuery = redactQuery(req.URL.RawQuery)
	}

	buf := new(bytes.Buffer)
	if err := req.Write(buf); err != nil {
		slog.Error("failed to write HTTP request into DLQ file", slog.Any("error", err))
		return nil
	}
	return buf.Bytes()
}

func encodeHTTPResponse(resp *http.Response) []byte {
	if resp == nil {
		return nil
	}
	if resp.Body != nil {
		defer resp.Body.Close()
	}

	redactHeaders(resp.Header)
	redactHeaders(resp.Trailer)

	buf := new(bytes.Buffer)
	if err := resp.Write(buf); err != nil {
		slog.Error("failed to write HTTP response into DLQ file", slog.Any("error", err))
		return nil
	}
	return buf.Bytes()
}

func (d *DeadLetterQueue) asyncWriteFile(data []byte, ext string, now time.Time, dirPerms, filePerms os.FileMode) bool {
	dir, file := uniqueKSortablePath(now)
	path := filepath.Join(dir, file+ext)

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
		_ = f.Close()
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

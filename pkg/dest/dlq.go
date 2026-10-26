package dest

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/daabr/versipellis/pkg/codec"
	"github.com/daabr/versipellis/pkg/config"
	"github.com/daabr/versipellis/pkg/flow"
)

const (
	attempts        = 3
	dirPermissions  = 0o700
	filePermissions = 0o600
	fileFlags       = os.O_CREATE | os.O_EXCL | os.O_WRONLY
	incompleteExt   = ".incomplete"
)

type writeResult int

const (
	writeOK writeResult = iota
	writeRetry
	writeAbort // The DLQ directory is full.
)

// DeadLetterQueue is an alternative destination for data that couldn't be delivered
// successfully by other senders. It behaves similarly to [Stdout], but writes the
// data into a local directory instead, specifically to a k-sortable file path.
type DeadLetterQueue struct {
	maxSize  int64
	warnSize int64

	root  *os.Root
	state config.State

	// Size accounting is disabled if both maxSize and warnSize are 0. Otherwise, [DeadLetterQueue.measureSizeLoop]
	// measures all the files in the DLQ directory at startup and from time to time, to notice external changes.
	// Between measurements, totalSize follows the DLQ's own writes: each write reserves its size before
	// creating a file (so concurrent writes can't exceed maxSize), and releases it if the write fails.
	sizeMu    sync.Mutex
	totalSize int64

	// Writes are tracked in newFiles by their slash-separated final paths, from their reservation until they
	// end, or until the end of a concurrent measurement. Measurements skip tracked files (with or without their
	// ".incomplete" suffix) and add their reserved sizes instead, so no file is counted twice or by a partial size.
	newFiles  map[string]newFile
	measuring bool

	// The warned latch logs the warnSize warning only once, until a measurement shows that totalSize is under it.
	warned bool

	// If maxSize is configured, writes wait until [DeadLetterQueue.measureSizeLoop] closes this channel after its
	// first measurement (even if it failed), so they can't exceed maxSize before the existing files are counted.
	// It's nil when maxSize is 0, because then writes don't need the size of the existing files.
	measured chan struct{}

	// Successful writes signal to waitLess (without blocking), so
	// [DeadLetterQueue.measureSizeLoop] reverts to [remeasureMinDelay].
	waitLess chan struct{}
}

// newFile is a tracked write: its reserved size, and whether it ended during a measurement.
type newFile struct {
	size int64
	done bool
}

// InitDeadLetterQueue initializes an alternative destination for data that couldn't be
// delivered successfully by other senders. It behaves similarly to [Stdout], but writes
// the data into a local directory instead, specifically to a k-sortable file path.
func InitDeadLetterQueue(rootDir string, rawCfg any) (*DeadLetterQueue, bool) {
	maxSize, warnSize, err := parseDLQ(rawCfg)
	if err != nil {
		slog.Error("invalid Dead-Letter Queue configuration", slog.Any("error", err))
		return nil, false
	}

	// No need to check for errors here: if the directory creation fails
	// for any reason, the subsequent file creation will fail as well.
	_ = os.MkdirAll(rootDir, dirPermissions)

	r, err := os.OpenRoot(rootDir)
	if err != nil {
		slog.Error("Dead-Letter Queue directory initialization error", slog.Any("error", err))
		return nil, false
	}

	dlq := &DeadLetterQueue{maxSize: maxSize, warnSize: warnSize, root: r, newFiles: make(map[string]newFile)}
	if maxSize > 0 {
		dlq.measured = make(chan struct{})
	}
	dlq.waitLess = make(chan struct{}, 1)
	dlq.state.Start()
	go dlq.measureSizeLoop()
	return dlq, true
}

// Send encodes and writes data into a file with a k-sortable name within the "data" directory in the process's
// current working directory. Structured data (e.g., JSON objects and SQL rows) is encoded in the given format,
// or as JSON by default.
//
// The DLQ is the last [config.Sender] to be closed, after closing all the other configured I/O components has
// already completed or timed out. This means that this function can be called after [DeadLetterQueue.Close] only
// by a collector or receiver goroutine that outlived its own close timeout. If that happens, the provided data is
// undeliverable anyway, so we can only warn about it and release its resources before the process terminates.
func (d *DeadLetterQueue) Send(ctx context.Context, data flow.Chunk, format config.Format) {
	if flow.IsEmpty(data) {
		return
	}

	if d.state.IfClosingElseLock() {
		discardAfterClose(ctx, data, format)
		return
	}
	now := time.Now().UTC()
	defer d.state.CloseMu.RUnlock()

	ctx = context.WithoutCancel(ctx)
	d.state.InProgress.Go(func() {
		if !d.waitForSize() {
			discardAfterClose(ctx, data, format)
			return
		}

		files, ext := encode(data, format)
		for _, file := range files {
			if len(file) == 0 {
				continue
			}
			for range attempts {
				if d.asyncWriteFile(file, ext, now, dirPermissions, filePermissions) != writeRetry {
					break
				}
			}
		}
	})
}

// waitForSize waits until the first measurement of the DLQ directory's total size ends (see [DeadLetterQueue.measured]).
// It returns false if the DLQ started closing first, so the caller must discard its data.
func (d *DeadLetterQueue) waitForSize() bool {
	if d.measured == nil {
		return true
	}

	// Check first without waiting, because if the DLQ is closing too, the choice below is random.
	select {
	case <-d.measured:
		return true
	default:
	}

	select {
	case <-d.measured:
		return true
	case <-d.state.Closing:
		return false
	}
}

// Close waits (up to 1 second) for disk writes which are currently in progress to complete,
// and prevents new files from being created. This is the last step before process termination.
func (d *DeadLetterQueue) Close(context.Context) {
	d.state.CloseOnce.Do(func() {
		d.state.BeginClose()

		done := make(chan struct{})
		go func() {
			defer close(done)
			d.state.InProgress.Wait()
			_ = d.root.Close()
		}()

		select {
		case <-done:
			// All done.
		case <-time.After(time.Second):
			slog.Warn("closing Dead-Letter Queue writer forcefully")
			// Not *really* stopping disk writes, but the next step in
			// main() is process termination, which does achieve this.
		}

		d.state.FinishClose()
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

func (d *DeadLetterQueue) asyncWriteFile(data []byte, ext string, now time.Time, dirPerms, filePerms os.FileMode) writeResult {
	dir, file := uniqueKSortablePath(now)
	finalPath := filepath.Join(dir, file+ext)
	tempPath := finalPath + incompleteExt

	size := int64(len(data))
	if !d.reserve(finalPath, size) {
		return writeAbort
	}
	written := false
	defer func() { d.release(finalPath, written) }()

	if err := d.root.Mkdir(dir, dirPerms); err != nil && !errors.Is(err, os.ErrExist) {
		slog.Error("failed to create DLQ subdirectory", slog.Any("error", err), slog.String("dir", dir))
		return writeRetry
	}

	f, err := d.root.OpenFile(tempPath, fileFlags, filePerms)
	if err != nil {
		slog.Error("failed to create DLQ file", slog.Any("error", err), slog.String("path", tempPath))
		return writeRetry
	}

	if err := writeAndSync(f, data); err != nil {
		slog.Error("failed to write DLQ file", slog.Any("error", err), slog.String("path", tempPath))
		_ = d.root.Remove(tempPath)
		return writeRetry
	}

	written = true
	select {
	case d.waitLess <- struct{}{}:
	default: // A signal is already pending, or size accounting is disabled (nil channel in tests).
	}

	if err := d.root.Rename(tempPath, finalPath); err != nil {
		slog.Error("failed to rename DLQ file", slog.Any("error", err), slog.String("path", tempPath))
		// Don't remove the temporary file, its contents are correct and the name is easy to fix later
		// upon review. Also don't report a failure, retrying would only duplicate the file.
	}

	return writeOK
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
	return errors.Join(err, f.Sync(), f.Close())
}

// measureSizeLoop measures the total size of all the files in the DLQ directory in the background: at startup, and then
// again after [remeasureMinDelay]. While the total size doesn't change, the delay doubles (see [nextRemeasureDelay]),
// but new writes reset it to the minimum, even during a longer wait. It exits immediately if no size thresholds are
// configured. It's not registered in [config.State.InProgress] because it shouldn't block [DeadLetterQueue.Close].
func (d *DeadLetterQueue) measureSizeLoop() {
	if d.maxSize+d.warnSize == 0 {
		return
	}

	prevSize, _ := d.measureSize()
	if d.measured != nil {
		// Release pending writes even if the measurement failed and logged an error, otherwise they'd wait in memory
		// until shutdown and then be discarded, which is worse than accounting only for the DLQ's own writes.
		close(d.measured)
	}
	nextDelay := remeasureMinDelay
	nextCheck := time.Now().Add(nextDelay)
	timer := time.NewTimer(nextDelay)
	defer timer.Stop()

	for {
		select {
		case <-d.state.Closing:
			return
		case <-timer.C:
			// Timer fired, measure the size.
		case <-d.waitLess:
			// Measure again within the minimal delay, unless the next measurement is already sooner,
			// so frequent writes can only bring the next measurement closer, never postpone it.
			nextDelay = remeasureMinDelay
			if time.Until(nextCheck) > nextDelay {
				nextCheck = time.Now().Add(nextDelay)
				timer.Reset(nextDelay)
			}
			continue
		}

		if currentSize, ok := d.measureSize(); ok {
			nextDelay = nextRemeasureDelay(nextDelay, currentSize != prevSize)
			prevSize = currentSize
		}
		nextCheck = time.Now().Add(nextDelay)
		timer.Reset(nextDelay)
	}
}

// measureSize measures the total size of all the files in the DLQ directory, and updates
// [DeadLetterQueue.totalSize] accordingly. It returns false if the measurement failed or was interrupted.
func (d *DeadLetterQueue) measureSize() (int64, bool) {
	d.sizeMu.Lock()
	d.measuring = true
	d.sizeMu.Unlock()

	var size int64
	err := fs.WalkDir(d.root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		switch {
		case d.state.IsClosing():
			return fs.SkipAll
		case err != nil && path == ".":
			return err
		case err != nil || entry.IsDir():
			return nil // Skip unreadable entries, but keep measuring the rest.
		case d.isTracked(path):
			return nil // Writing in progress: use expected rather than current size.
		}

		info, err := entry.Info()
		if err != nil {
			return nil //nolint:nilerr // File probably deleted after it was listed, don't abort entire measurement.
		}
		size += info.Size()
		return nil
	})
	if err != nil && !d.state.IsClosing() {
		slog.Error("failed to measure Dead-Letter Queue directory size", slog.Any("error", err))
	}

	d.sizeMu.Lock()
	d.measuring = false
	for path, file := range d.newFiles {
		size += file.size
		if file.done {
			delete(d.newFiles, path)
		}
	}
	ok := err == nil && !d.state.IsClosing()
	if ok {
		d.totalSize = size
		if d.totalSize < d.warnSize {
			d.warned = false
		}
		d.warnIfNeeded()
	}
	d.sizeMu.Unlock()

	return size, ok
}

func (d *DeadLetterQueue) isTracked(path string) bool {
	d.sizeMu.Lock()
	defer d.sizeMu.Unlock()
	_, found := d.newFiles[filepath.ToSlash(strings.TrimSuffix(path, incompleteExt))]
	return found
}

// reserve accounts for a new file with the given size, before it's created, unless that
// would exceed [DeadLetterQueue.maxSize], in which case the caller must discard its data.
func (d *DeadLetterQueue) reserve(path string, size int64) bool {
	if d.maxSize+d.warnSize == 0 {
		return true
	}
	d.sizeMu.Lock()
	defer d.sizeMu.Unlock()

	if d.maxSize > 0 && d.totalSize+size > d.maxSize {
		slog.Warn("Dead-Letter Queue is full, discarding new data", slog.Int64("dlq_limit", d.maxSize),
			slog.Int64("dlq_size", d.totalSize), slog.Int64("new_data_size", size),
		)
		return false
	}
	d.totalSize += size
	d.newFiles[filepath.ToSlash(path)] = newFile{size: size}
	d.warnIfNeeded()
	return true
}

// release ends the tracking of a file that was reserved with [DeadLetterQueue.reserve]. If it wasn't
// written, its reserved size is released too. Otherwise, it's still tracked until the end of the
// current measurement (if there is one), because that measurement may have skipped the file.
func (d *DeadLetterQueue) release(path string, written bool) {
	d.sizeMu.Lock()
	defer d.sizeMu.Unlock()

	path = filepath.ToSlash(path)
	file, found := d.newFiles[path]
	switch {
	case !found:
		return
	case !written:
		d.totalSize -= file.size
		delete(d.newFiles, path)
	case d.measuring:
		d.newFiles[path] = newFile{size: file.size, done: true}
	default:
		delete(d.newFiles, path)
	}
}

// warnIfNeeded logs a warning if the Dead-Letter Queue reaches its size warning threshold.
// It only logs the warning once, until the total size measurement drops below the threshold.
func (d *DeadLetterQueue) warnIfNeeded() {
	if d.warned || d.warnSize == 0 || d.totalSize < d.warnSize {
		return
	}

	d.warned = true
	slog.Warn("Dead-Letter Queue has reached its size warning threshold",
		slog.Int64("total_size", d.totalSize), slog.Int64("warn_size", d.warnSize),
	)
}

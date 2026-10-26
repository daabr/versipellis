package dest

import (
	"errors"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
	"time"
)

const (
	// Minimal delay between measurements of the DLQ directory's total size, to notice external changes
	// (e.g., files that were deleted after replaying them). It's a prime number of seconds (7 minutes + 1).
	remeasureMinDelay = 421 * time.Second

	// As long as there's no DLQ activity, the delay between measurements doubles, but not beyond this limit.
	remeasureMaxDelay = time.Hour
)

// nextRemeasureDelay returns the delay until the next measurement of the DLQ directory's total size: the
// minimal delay after a change, otherwise double the given delay, unless that exceeds the maximal delay.
func nextRemeasureDelay(delay time.Duration, changed bool) time.Duration {
	switch {
	case changed:
		delay = remeasureMinDelay
	case delay*2 <= remeasureMaxDelay:
		delay *= 2
	}
	return delay
}

// measureSizeLoop measures the total size of all the files in the DLQ directory in the background: at startup, and then
// again after [remeasureMinDelay]. While the total size doesn't change, the delay doubles (see [nextRemeasureDelay]),
// but new writes reset it to the minimum, even during a longer wait. It exits immediately if no size thresholds are
// configured. It's not registered in [config.State.InProgress] because it shouldn't block [DeadLetterQueue.Close].
func (d *DeadLetterQueue) measureSizeLoop() {
	if d.maxSize+d.warnSize == 0 {
		return
	}

	prevSize, ok := d.measureSize()
	if !ok && d.state.IsClosing() {
		return // Interrupted by shutdown: pending writes must observe [config.State.Closing] instead.
	}
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

	w := &sizeWalk{d: d}
	err := fs.WalkDir(d.root.FS(), ".", w.visit)
	closing := d.state.IsClosing()
	switch {
	case closing:
		// Interrupted, nothing to report.
	case err != nil:
		slog.Error("failed to measure Dead-Letter Queue directory size", slog.Any("error", err))
	case w.skipped > 0:
		slog.Warn("Dead-Letter Queue size measurement skipped unreadable entries", slog.Any("error", w.firstErr),
			slog.Int("skipped_entries", w.skipped),
		)
	}

	d.sizeMu.Lock()
	d.measuring = false
	size := w.size
	for path, file := range d.newFiles {
		size += file.size
		if file.done {
			delete(d.newFiles, path)
		}
	}
	ok := err == nil && !closing
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

// sizeWalk accumulates a measurement of the DLQ directory's total size (see [DeadLetterQueue.measureSize]).
type sizeWalk struct {
	d        *DeadLetterQueue
	size     int64
	skipped  int
	firstErr error
}

// visit is the [fs.WalkDirFunc] of [DeadLetterQueue.measureSize]. It skips unreadable entries other than the root
// directory, rather than failing the entire measurement. Only unreadable subdirectories can hide anything (file sizes
// don't require read permission), and the DLQ creates readable ones, so an unreadable one was changed externally,
// and the DLQ can't add files to it either. Failing instead would freeze the total size, so a full DLQ would discard
// all new data, even after replayed files are deleted.
func (w *sizeWalk) visit(path string, entry fs.DirEntry, err error) error {
	switch {
	case w.d.state.IsClosing():
		return fs.SkipAll
	case err != nil && path == ".":
		return err
	case err != nil:
		w.skip(err)
		return nil
	case entry.IsDir() || w.d.isTracked(path):
		return nil // Tracked files are being written: use their expected rather than current sizes.
	}

	info, err := entry.Info()
	if err != nil {
		w.skip(err)
		return nil
	}
	w.size += info.Size()
	return nil
}

// skip records an unreadable entry, unless it was deleted after it was listed.
func (w *sizeWalk) skip(err error) {
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	w.skipped++
	if w.firstErr == nil {
		w.firstErr = err
	}
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

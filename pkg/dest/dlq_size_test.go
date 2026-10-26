package dest

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/daabr/versipellis/pkg/codec"
	"github.com/daabr/versipellis/pkg/flow"
)

// TestDLQSizeRemeasure checks that the size of all the files in the DLQ directory is measured at startup,
// and again only after [remeasureMinDelay], which also notices external changes.
func TestDLQSizeRemeasure(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		writeTestFile(t, filepath.Join(dir, "external.bin"), 100)
		writeTestFile(t, filepath.Join(dir, "sub", "leftover.json"+incompleteExt), 50)

		dlq, ok := InitDeadLetterQueue(dir, map[string]any{"warning_size": int64(120)})
		if !ok {
			t.Fatal("failed to initialize DeadLetterQueue")
		}
		defer dlq.Close(t.Context())

		synctest.Wait()
		assertDLQSize(t, dlq, "initial measurement", 150, true)

		writeTestFile(t, filepath.Join(dir, "external2.bin"), 40)
		synctest.Sleep(remeasureMinDelay - 1)
		assertDLQSize(t, dlq, "before re-measurement", 150, true)

		synctest.Sleep(1)
		synctest.Wait()
		assertDLQSize(t, dlq, "after re-measurement", 190, true)

		for _, name := range []string{"external.bin", "external2.bin", "sub"} {
			if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
				t.Fatalf("os.RemoveAll() error = %v", err)
			}
		}
		synctest.Sleep(remeasureMinDelay)
		synctest.Wait()
		assertDLQSize(t, dlq, "after deletions", 0, false)
	})
}

// TestDLQSizeRemeasureBackoff checks that the delay between measurements doubles while the total size
// doesn't change, and that new writes reset it to the minimum, even during a longer wait.
func TestDLQSizeRemeasureBackoff(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		dlq, ok := InitDeadLetterQueue(dir, map[string]any{"warning_size": int64(1000)})
		if !ok {
			t.Fatal("failed to initialize DeadLetterQueue")
		}
		defer dlq.Close(t.Context())

		// Unchanged measurements after 1x and 2x the minimal delay, so the next one is after 4x.
		synctest.Wait()
		synctest.Sleep(remeasureMinDelay)
		synctest.Sleep(2 * remeasureMinDelay)
		synctest.Wait()

		writeTestFile(t, filepath.Join(dir, "external1"), 10)
		synctest.Sleep(4*remeasureMinDelay - 1)
		synctest.Wait()
		assertDLQSize(t, dlq, "before the backed-off measurement", 0, false)
		synctest.Sleep(1)
		synctest.Wait()
		assertDLQSize(t, dlq, "after the backed-off measurement", 10, false)

		// That change reset the delay, then it doubles twice again, and a write cuts the 4x wait short.
		synctest.Sleep(remeasureMinDelay)
		synctest.Sleep(2 * remeasureMinDelay)
		synctest.Sleep(remeasureMinDelay)
		dlq.Send(t.Context(), flow.Blobs{[]byte("12345")}, codec.FormatRaw)
		synctest.Wait()
		writeTestFile(t, filepath.Join(dir, "external2"), 20)

		synctest.Sleep(remeasureMinDelay - 1)
		synctest.Wait()
		assertDLQSize(t, dlq, "before the measurement after a write", 15, false) // Accounted write only.
		synctest.Sleep(1)
		synctest.Wait()
		assertDLQSize(t, dlq, "after the measurement after a write", 35, false)
	})
}

func TestNextRemeasureDelay(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		delay   time.Duration
		changed bool
		want    time.Duration
	}{
		{"changed_at_min", remeasureMinDelay, true, remeasureMinDelay},
		{"changed_after_backoff", 8 * remeasureMinDelay, true, remeasureMinDelay},
		{"unchanged_1x", remeasureMinDelay, false, 2 * remeasureMinDelay},
		{"unchanged_2x", 2 * remeasureMinDelay, false, 4 * remeasureMinDelay},
		{"unchanged_4x", 4 * remeasureMinDelay, false, 8 * remeasureMinDelay},
		{"unchanged_8x", 8 * remeasureMinDelay, false, 8 * remeasureMinDelay}, // 16x would exceed the maximum.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := nextRemeasureDelay(tt.delay, tt.changed); got != tt.want {
				t.Errorf("nextRemeasureDelay(%v, %v) = %v, want %v", tt.delay, tt.changed, got, tt.want)
			}
		})
	}
}

func writeTestFile(t *testing.T, path string, size int) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), dirPermissions); err != nil {
		t.Fatalf("os.MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", size)), filePermissions); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
}

func assertDLQSize(t *testing.T, dlq *DeadLetterQueue, when string, wantSize int64, wantWarned bool) {
	t.Helper()

	if got := lockedTotalSize(dlq); got != wantSize {
		t.Errorf("DLQ size %s = %d, want %d", when, got, wantSize)
	}
	dlq.sizeMu.Lock()
	defer dlq.sizeMu.Unlock()
	if dlq.warned != wantWarned {
		t.Errorf("DLQ size warning state %s = %v, want %v", when, dlq.warned, wantWarned)
	}
}

// lockedTotalSize returns the total size of the DLQ directory, as accounted so far.
func lockedTotalSize(dlq *DeadLetterQueue) int64 {
	dlq.sizeMu.Lock()
	defer dlq.sizeMu.Unlock()
	return dlq.totalSize
}

// newTestDLQ returns a [DeadLetterQueue] for the given directory, without starting its background measurements.
func newTestDLQ(t *testing.T, dir string, maxSize, warnSize int64) *DeadLetterQueue {
	t.Helper()

	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("os.OpenRoot() error = %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return &DeadLetterQueue{maxSize: maxSize, warnSize: warnSize, root: r, newFiles: make(map[string]newFile)}
}

func TestDLQSizeWarning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		warningSize int64
		sizes       []int64 // Sequence of reservations, each followed by a check of the warning state.
		want        []bool
	}{
		{"disabled", 0, []int64{1, 100}, []bool{false, false}},
		{"below_threshold", 100, []int64{50, 49}, []bool{false, false}},
		{"reached_threshold", 100, []int64{50, 50}, []bool{false, true}},
		{"latched_above_threshold", 100, []int64{50, 51, 100}, []bool{false, true, true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			d := newTestDLQ(t, t.TempDir(), 1000, tt.warningSize)
			for i, size := range tt.sizes {
				if !d.reserve(strconv.Itoa(i), size) {
					t.Fatalf("reserve() #%d = false, want true", i+1)
				}
				if d.warned != tt.want[i] {
					t.Errorf(
						"warning state after reserve() #%d with total %d = %v, want %v",
						i+1,
						d.totalSize,
						d.warned,
						tt.want[i],
					)
				}
			}
		})
	}
}

// TestDLQSizeWarningReset checks that the warning state is reset only when a measurement
// shows that the total size is under the threshold, and that the maximum size is enforced.
func TestDLQSizeWarningReset(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	d := newTestDLQ(t, dir, 250, 150)
	for _, name := range []string{"a", "b"} {
		if !d.reserve(name, 100) {
			t.Fatalf("reserve(%q) = false, want true", name)
		}
		writeTestFile(t, filepath.Join(dir, name), 100)
		d.release(name, true)
	}
	assertDLQSize(t, d, "after crossing the warning size", 200, true)

	if d.reserve("c", 100) {
		t.Error("reserve() beyond the maximum size = true, want false")
	}
	if !d.reserve("d", 50) {
		t.Error("reserve() up to the maximum size = false, want true")
	}
	d.release("d", false) // A failed write releases its reserved size.
	assertDLQSize(t, d, "after a failed write", 200, true)

	d.measureSize()
	assertDLQSize(t, d, "after measuring the same size", 200, true)

	if err := os.Remove(filepath.Join(dir, "b")); err != nil {
		t.Fatalf("os.Remove() error = %v", err)
	}
	d.measureSize()
	assertDLQSize(t, d, "after measuring a smaller size", 100, false)
}

func TestDLQSizeRelease(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		measuring   bool
		written     bool
		wantTracked bool
		wantSize    int64
	}{
		{"failed", false, false, false, 0},
		{"failed_while_measuring", true, false, false, 0},
		{"written", false, true, false, 10},
		{"written_while_measuring", true, true, true, 10}, // Tracked until the end of the measurement.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			d := newTestDLQ(t, t.TempDir(), 0, 100)
			if !d.reserve("dir/file", 10) {
				t.Fatal("reserve() = false, want true")
			}
			d.measuring = tt.measuring
			d.release("dir/file", tt.written)

			file, tracked := d.newFiles["dir/file"]
			if tracked != tt.wantTracked {
				t.Errorf("release() tracked = %v, want %v", tracked, tt.wantTracked)
			}
			if tracked && !file.done {
				t.Error("release() of a written file during a measurement didn't mark it as done")
			}
			if d.totalSize != tt.wantSize {
				t.Errorf("release() total size = %d, want %d", d.totalSize, tt.wantSize)
			}
		})
	}
}

// TestDLQSizeMeasureTracking checks that measurements count tracked files exactly once, by their reserved sizes.
func TestDLQSizeMeasureTracking(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "external"), 1)
	writeTestFile(t, filepath.Join(dir, "dir", "in_flight"+incompleteExt), 5) // Partially written.
	writeTestFile(t, filepath.Join(dir, "dir", "finished"), 20)

	d := newTestDLQ(t, dir, 0, 100)
	d.newFiles["dir/in_flight"] = newFile{size: 10}
	d.newFiles["dir/finished"] = newFile{size: 20, done: true} // Finished during the previous measurement.
	d.measureSize()

	if got, want := d.totalSize, int64(1+10+20); got != want {
		t.Errorf("total size after measurement = %d, want %d", got, want)
	}
	if _, found := d.newFiles["dir/finished"]; found {
		t.Error("a file that finished during the measurement is still tracked after it")
	}
	if _, found := d.newFiles["dir/in_flight"]; !found {
		t.Error("an in-flight file isn't tracked after the measurement")
	}
}

func TestDeadLetterQueueMaxSizeConcurrency(t *testing.T) {
	t.Parallel()

	const fileSize, maxFiles = 100, 10

	dir := t.TempDir()
	dlq, ok := InitDeadLetterQueue(dir, map[string]any{"max_size": int64(fileSize * maxFiles)})
	if !ok {
		t.Fatal("failed to initialize DeadLetterQueue")
	}
	t.Cleanup(func() { dlq.Close(t.Context()) })

	var wg sync.WaitGroup
	for range 4 * maxFiles {
		wg.Go(func() {
			dlq.Send(t.Context(), flow.Blobs{[]byte(strings.Repeat("x", fileSize))}, codec.FormatRaw)
		})
	}
	wg.Wait()
	dlq.state.InProgress.Wait()

	files := 0
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files++
		}
		return err
	})
	if err != nil {
		t.Fatalf("filepath.WalkDir() error = %v", err)
	}
	if files != maxFiles {
		t.Errorf("DLQ files = %d, want %d", files, maxFiles)
	}
	if got := lockedTotalSize(dlq); got != fileSize*maxFiles {
		t.Errorf("DLQ size = %d, want %d", got, fileSize*maxFiles)
	}
}

// TestDLQMaxSizeBeforeMeasurement checks that writes wait for the first measurement of the DLQ directory, so a
// directory that's already full can't grow beyond the maximum size, and that they're discarded if the DLQ closes first.
func TestDLQMaxSizeBeforeMeasurement(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		closeFirst bool
	}{
		{"measured_first", false},
		{"closed_first", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			writeTestFile(t, filepath.Join(dir, "existing"), 100) // Already full.

			synctest.Test(t, func(t *testing.T) {
				d := newTestDLQ(t, dir, 100, 0)
				d.measured = make(chan struct{})
				d.state.Start()

				d.Send(t.Context(), flow.Blobs{[]byte("new data")}, codec.FormatRaw)
				synctest.Wait()
				if got := lockedTotalSize(d); got != 0 {
					t.Errorf("DLQ size before the first measurement = %d, want 0 (no reservations)", got)
				}

				start := time.Now()
				if tt.closeFirst {
					d.Close(t.Context())
					if elapsed := time.Since(start); elapsed > 0 {
						t.Errorf("Close() with a pending write took %v, want no wait", elapsed)
					}
				} else {
					d.measureSize()
					close(d.measured)
					d.state.InProgress.Wait()
					assertDLQSize(t, d, "after the first measurement", 100, false)
				}
			})

			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("os.ReadDir() error = %v", err)
			}
			if len(entries) != 1 || entries[0].Name() != "existing" {
				t.Errorf("DLQ directory entries = %v, want only the existing file", entries)
			}
		})
	}
}

// TestDLQMeasurementInterruptedByClose checks that a first measurement that was interrupted by
// shutdown doesn't release pending writes, which would then bypass the maximum size.
func TestDLQMeasurementInterruptedByClose(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "existing"), 100)

	d := newTestDLQ(t, dir, 100, 0)
	d.measured = make(chan struct{})
	d.state.Start()
	d.state.BeginClose()
	d.measureSizeLoop() // Returns immediately because the DLQ is closing.

	select {
	case <-d.measured:
		t.Error("measureSizeLoop() released pending writes after an interrupted measurement")
	default:
	}
}

// TestDLQSizeUnreadableEntries checks that measurements skip unreadable subdirectories, rather than failing.
func TestDLQSizeUnreadableEntries(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root can read unreadable directories")
	}

	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "readable", "file"), 100)
	writeTestFile(t, filepath.Join(dir, "unreadable", "file"), 50)

	d := newTestDLQ(t, dir, 1000, 0)
	d.measureSize()
	assertDLQSize(t, d, "before a subdirectory became unreadable", 150, false)

	unreadable := filepath.Join(dir, "unreadable")
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatalf("os.Chmod() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, dirPermissions) }) // Allow [testing.T.TempDir] cleanup.

	if _, ok := d.measureSize(); !ok {
		t.Error("measureSize() with an unreadable subdirectory = false, want true")
	}
	assertDLQSize(t, d, "after a subdirectory became unreadable", 100, false)
}

// vanishedEntry is a [fs.DirEntry] of a file that was deleted after it was listed.
type vanishedEntry struct{}

func (vanishedEntry) Name() string               { return "vanished" }
func (vanishedEntry) IsDir() bool                { return false }
func (vanishedEntry) Type() fs.FileMode          { return 0 }
func (vanishedEntry) Info() (fs.FileInfo, error) { return nil, fs.ErrNotExist }

func TestSizeWalkSkip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		entry       fs.DirEntry
		err         error
		wantSkipped int
	}{
		{"vanished_dir", nil, &fs.PathError{Op: "readdir", Path: "dir", Err: fs.ErrNotExist}, 0},
		{"vanished_file", vanishedEntry{}, nil, 0},
		{"unreadable_dir", nil, &fs.PathError{Op: "readdir", Path: "dir", Err: fs.ErrPermission}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := &sizeWalk{d: newTestDLQ(t, t.TempDir(), 1000, 0)}
			if err := w.visit("dir/entry", tt.entry, tt.err); err != nil {
				t.Errorf("visit() error = %v, want nil", err)
			}
			if w.skipped != tt.wantSkipped {
				t.Errorf("visit() skipped entries = %d, want %d", w.skipped, tt.wantSkipped)
			}
		})
	}
}

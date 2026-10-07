package sql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/daabr/versipellis/pkg/config"
	"github.com/daabr/versipellis/pkg/cron"
	"github.com/daabr/versipellis/pkg/dest"
	"github.com/daabr/versipellis/pkg/flow"
)

func TestNewCollector(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		base    *config.BaseCollector
		cfg     map[string]any
		wantErr bool
	}{
		{
			name: "missing_driver_type",
			base: &config.BaseCollector{Type: config.CollectorTypeSQL},
			cfg: map[string]any{
				"connection": "connection",
				"query":      "SELECT 1",
			},
			wantErr: true,
		},
		{
			name: "unrecognized_driver_type",
			base: &config.BaseCollector{Type: config.CollectorTypeSQL},
			cfg: map[string]any{
				"type":       "unknown",
				"connection": "connection",
				"query":      "SELECT 1",
			},
			wantErr: true,
		},
		{
			name: "missing_connection",
			base: &config.BaseCollector{Type: config.CollectorTypeSQL},
			cfg: map[string]any{
				"type":  DriverTypeSQLite,
				"query": "SELECT 1",
			},
			wantErr: true,
		},
		{
			name: "missing_query",
			base: &config.BaseCollector{Type: config.CollectorTypeSQL},
			cfg: map[string]any{
				"type":       DriverTypeSQLite,
				"connection": ":memory:",
			},
			wantErr: true,
		},
		{
			name: "invalid_timeout",
			base: &config.BaseCollector{Type: config.CollectorTypeSQL},
			cfg: map[string]any{
				"type":       DriverTypeSQLite,
				"connection": "connection",
				"query":      "SELECT 1",
				"timeout":    "invalid",
			},
			wantErr: true,
		},
		{
			name: "negative_timeout_is_allowed",
			base: &config.BaseCollector{Type: config.CollectorTypeSQL},
			cfg: map[string]any{
				"type":       DriverTypeSQLite,
				"connection": "connection",
				"query":      "SELECT 1",
				"timeout":    "-5s",
			},
			wantErr: false,
		},
		{
			name: "invalid_batch",
			base: &config.BaseCollector{Type: config.CollectorTypeSQL},
			cfg: map[string]any{
				"type":       DriverTypeSQLite,
				"connection": "connection",
				"query":      "SELECT 1",
				"batch":      "invalid",
			},
			wantErr: true,
		},
		{
			name: "invalid_batch_time_window",
			base: &config.BaseCollector{Type: config.CollectorTypeSQL},
			cfg: map[string]any{
				"type":       DriverTypeSQLite,
				"connection": "connection",
				"query":      "SELECT 1",
				"batch":      map[string]any{"time_window": "invalid"},
			},
			wantErr: true,
		},
		{
			name: "happy_path",
			base: &config.BaseCollector{Type: config.CollectorTypeSQL},
			cfg: map[string]any{
				"type":       strings.ToUpper(DriverTypeSQLite), // Test case-insensitivity of the driver type.
				"connection": "connection",
				"query":      "SELECT 1",
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c, gotErr := NewCollector(tt.base, tt.cfg)
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("NewCollector() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
			if c != nil {
				if b := c.Base(); !reflect.DeepEqual(b, tt.base) {
					t.Errorf("Collector.Base() = %+v, want %+v", b, tt.base)
				}
			}
		})
	}
}

func TestLoadAndCheckQuery(t *testing.T) {
	t.Parallel()

	queryWithSpaces := "\n SELECT 1  \n\n"
	tempDir := t.TempDir()
	err := os.WriteFile(filepath.Join(tempDir, "empty.sql"), []byte{}, 0o600) //gosec:disable G304 // Unit test.
	if err != nil {
		t.Fatal(err)
	}

	err = os.WriteFile(filepath.Join(tempDir, "query.sql"), []byte(queryWithSpaces), 0o600) //gosec:disable G304 // Unit test.
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		query   string
		path    string
		want    string
		wantErr bool
	}{
		{
			name:    "no_query_or_file",
			wantErr: true,
		},
		{
			name:    "both_query_and_file",
			query:   queryWithSpaces,
			path:    filepath.Join(tempDir, "query.sql"),
			wantErr: true,
		},
		{
			name:  "valid_inline_query",
			query: queryWithSpaces,
			want:  "SELECT 1",
		},
		{
			name:    "query_file_not_found",
			path:    filepath.Join(tempDir, "nonexistent"),
			wantErr: true,
		},
		{
			name:    "query_file_is_directory",
			path:    tempDir,
			wantErr: true,
		},
		{
			name:    "empty_query_file",
			path:    filepath.Join(tempDir, "empty.sql"),
			wantErr: true,
		},
		{
			name: "valid_query_file",
			path: filepath.Join(tempDir, "query.sql"),
			want: "SELECT 1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := map[string]any{
				"query":      tt.query,
				"query_file": tt.path,
			}

			got, gotErr := loadQuery(cfg)
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("loadQuery() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("loadQuery() got = %q, want %q", got, tt.want)
			}

			got, gotErr = checkQuery(got, gotErr)
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("checkQuery() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("checkQuery() got = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCollectorStartNilGuard(t *testing.T) {
	t.Parallel()

	var nilCollector *Collector
	if ok := nilCollector.Start(t.Context()); ok {
		t.Error("nil Collector.Start() = true, want false")
	}
}

func TestCollectorStart(t *testing.T) {
	t.Parallel()

	base, err := config.NewBaseCollector(
		map[string]any{"type": config.CollectorTypeSQL, "schedule": "@once"},
		"TestCollectorStart", map[string]config.Sender{"": dest.Discard},
	)
	if err != nil {
		t.Fatalf("config.NewBaseCollector() error: %v", err)
	}

	c, err := NewCollector(base, map[string]any{
		"type":       DriverTypeSQLite,
		"connection": ":memory:",
		"query":      "SELECT 1",
	})
	if err != nil {
		t.Fatalf("NewCollector() error: %v", err)
	}

	if ok := c.Start(t.Context()); !ok {
		t.Fatal("Collector.Start() failed")
	}
	if ok := c.Start(t.Context()); !ok {
		t.Fatal("second Collector.Start() failed (should be idempotent)")
	}

	<-c.Done() // Wait for the collector's goroutine to finish its work.
}

func TestCollectorConnectionStringError(t *testing.T) {
	t.Parallel()

	base, err := config.NewBaseCollector(
		map[string]any{"type": config.CollectorTypeSQL, "schedule": "@once"},
		"TestCollectorConnectionStringError", map[string]config.Sender{"": dest.Discard},
	)
	if err != nil {
		t.Fatalf("config.NewBaseCollector() error: %v", err)
	}

	tests := []string{
		DriverTypeCockroachDB,
		DriverTypeMariaDB,
		DriverTypeMSSQL,
		DriverTypeODBC,
		DriverTypeOracle,
		DriverTypePostgres,
		DriverTypePostgreSQL,
		DriverTypeSAPHANA,
	}
	for _, driver := range tests {
		t.Run(driver, func(t *testing.T) {
			t.Parallel()

			c, err := NewCollector(base, map[string]any{
				"type":       driver,
				"connection": "invalid_connection_string",
				"query":      "SELECT 1",
			})
			if err != nil {
				t.Fatalf("NewCollector() error: %v", err)
			}

			if ok := c.Start(t.Context()); ok {
				t.Fatal("Collector.Start() succeeded unexpectedly")
			}
		})
	}
}

func TestOpenDBInvalidDriver(t *testing.T) {
	t.Parallel()

	if _, err := openDB(t.Context(), "invalid_driver", "connection"); err == nil {
		t.Error("openDB(invalid_driver) error = nil, wantErr = true")
	}
}

func TestOpenDBPingFailure(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel() // Canceled context causes PingContext to fail immediately.

	if _, err := openDB(ctx, DriverTypeMySQL, "user:pass@tcp(127.0.0.1:1)/dbname"); err == nil {
		t.Error("openDB() error = nil, wantErr = true")
	}
}

func TestScheduleNextQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		schedule string
		cancel   bool
		isAsync  bool
	}{
		{
			name:     "context_cancellation",
			schedule: "@daily",
			cancel:   true,
		},
		{
			name:     "behind_schedule_skip",
			schedule: "@every 1s",
			isAsync:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				base, err := config.NewBaseCollector(
					map[string]any{"type": config.CollectorTypeSQL, "schedule": tt.schedule},
					tt.name, map[string]config.Sender{"": dest.Discard},
				)
				if err != nil {
					t.Fatalf("config.NewBaseCollector() error: %v", err)
				}

				c, err := NewCollector(base, map[string]any{
					"type":       DriverTypeSQLite,
					"connection": ":memory:",
					"query":      "SELECT 1;",
				})
				if err != nil {
					t.Fatalf("NewCollector() error: %v", err)
				}

				c.db, err = openDB(t.Context(), c.driver, c.conn)
				if err != nil {
					t.Fatalf("openDB() error: %v", err)
				}
				t.Cleanup(func() { _ = c.db.Close() })

				ctx, cancel := context.WithCancel(t.Context())
				c.cancelSched = cancel
				if tt.cancel {
					cancel()
				} else {
					t.Cleanup(cancel)
				}

				if !tt.isAsync {
					c.scheduleNext(ctx, ctx, time.Now())
					return
				}

				go c.scheduleNext(ctx, ctx, time.Now().Add(-5*time.Second))
				synctest.Wait()
				cancel()
				synctest.Wait()
			})
		})
	}
}

func TestCollectorExecuteQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     map[string]any
		closeDB bool
		wantOK  bool
	}{
		{
			name: "zero_rows_returned_not_an_error",
			cfg: map[string]any{
				"type":       DriverTypeSQLite,
				"connection": ":memory:",
				"query":      "SELECT 1 WHERE 1 = 0;",
			},
			wantOK: true,
		},
		{
			name: "multiple_rows_returned",
			cfg: map[string]any{
				"type":       DriverTypeSQLite,
				"connection": ":memory:",
				"query":      "SELECT 1 UNION SELECT 2 UNION SELECT 3",
			},
			wantOK: true,
		},
		{
			name: "query_syntax_error",
			cfg: map[string]any{
				"type":       DriverTypeSQLite,
				"connection": ":memory:",
				"query":      "SELECT * FROM nonexistent_table;",
			},
			wantOK: false,
		},
		{
			name: "begin_tx_failure",
			cfg: map[string]any{
				"type":       DriverTypeSQLite,
				"connection": ":memory:",
				"query":      "SELECT 1;",
			},
			closeDB: true,
			wantOK:  false,
		},
		{
			name: "happy_path",
			cfg: map[string]any{
				"type":       DriverTypeSQLite,
				"connection": ":memory:",
				"query":      "SELECT 1",
			},
			wantOK: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sched, err := cron.Parse("@once", nil)
			if err != nil {
				t.Fatalf("cron.Parse() error: %v", err)
			}
			base := &config.BaseCollector{Type: config.CollectorTypeSQL, Schedule: sched, Send: dest.Discard.Send}
			coll, err := NewCollector(base, tt.cfg)
			if err != nil {
				t.Fatalf("NewCollector() error: %v", err)
			}
			coll.db, err = sql.Open(coll.driver, coll.conn)
			if err != nil {
				t.Fatalf("sql.Open() error: %v", err)
			}

			if tt.closeDB {
				if err := coll.db.Close(); err != nil {
					t.Fatalf("sql.DB.Close() error: %v", err)
				}
			} else {
				t.Cleanup(func() { _ = coll.db.Close() })
			}

			if gotOK := coll.executeQuery(t.Context()); gotOK != tt.wantOK {
				t.Errorf("Collector.executeQuery() = %v, want %v", gotOK, tt.wantOK)
			}
			if timestampUpdated := !coll.prevStart.IsZero(); timestampUpdated != tt.wantOK {
				t.Errorf("Collector.prevXXXX checkpoint updated = %v, want %v", timestampUpdated, tt.wantOK)
			}
		})
	}
}

func TestCollectorProcessResults(t *testing.T) {
	t.Parallel()

	var err error
	coll := &Collector{batch: newTestBatcher(t, dest.Discard.Send)}
	coll.db, err = sql.Open(DriverTypeSQLite, ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	t.Cleanup(func() { _ = coll.db.Close() })

	rows, err := coll.db.QueryContext(t.Context(), "SELECT 1")
	if err != nil {
		t.Fatalf("db.QueryContext() error: %v", err)
	}
	_ = rows.Close() //nolint:sqlclosecheck // Close rows immediately so [sql.Rows.Columns] fails.

	if _, err := coll.processResults(t.Context(), rows); err == nil {
		t.Error("Collector.processResults() error = nil, wantErr = true")
	}
}

func TestCollectorProcessResultsWithFakeDriver(t *testing.T) {
	t.Parallel()

	registerFakeSQLDriver()

	tests := []struct {
		name     string
		dsn      string
		wantRows int
		wantErr  bool
	}{
		{
			name:     "multiple_result_sets",
			dsn:      "noErrors",
			wantRows: 3,
			wantErr:  false,
		},
		{
			name:     "row_iteration_error",
			dsn:      "rowsNextError",
			wantRows: 1,
			wantErr:  true,
		},
		{
			name:     "row_set_iteration_error",
			dsn:      "rowsNextResultSetError",
			wantRows: 1,
			wantErr:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var err error
			coll := &Collector{batch: newTestBatcher(t, dest.Discard.Send)}
			coll.db, err = sql.Open(fakeSQLDriverName, tt.dsn)
			if err != nil {
				t.Fatalf("sql.Open() error: %v", err)
			}
			t.Cleanup(func() { _ = coll.db.Close() })

			rows, err := coll.db.QueryContext(t.Context(), "SELECT 1; SELECT 2;")
			if err != nil {
				t.Fatalf("db.QueryContext() error: %v", err)
			}
			t.Cleanup(func() { _ = rows.Close() })

			gotRows, gotErr := coll.processResults(t.Context(), rows)
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("Collector.processResults() error = %v, wantErr = %v", gotErr, tt.wantErr)
			}
			if gotRows != tt.wantRows {
				t.Errorf("Collector.processResults() row count = %d, want %d", gotRows, tt.wantRows)
			}
		})
	}
}

func TestCollectorExecuteQueryBatches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		destination string
		batch       map[string]any
		rows        int
		wantSizes   []int // Number of rows in each chunk sent to the destination.
	}{
		{
			name:      "no_rows",
			rows:      0,
			wantSizes: nil,
		},
		{
			name:      "default_batch_size",
			rows:      1234,
			wantSizes: []int{500, 500, 234}, // The last partial batch is flushed after the query.
		},
		{
			name:      "custom_batch_size",
			batch:     map[string]any{"max_items": int64(1000)},
			rows:      2500,
			wantSizes: []int{1000, 1000, 500},
		},
		{
			name:      "batching_disabled",
			batch:     map[string]any{"max_items": int64(0)},
			rows:      3,
			wantSizes: []int{1, 1, 1},
		},
		{
			name:        "discard_destination_disables_batching",
			destination: config.SenderTypeDiscard,
			rows:        3,
			wantSizes:   []int{1, 1, 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var mu sync.Mutex
			var gotSizes []int
			var gotValues []int64
			send := func(_ context.Context, chunk flow.Chunk) {
				rows, ok := chunk.(flow.Structured)
				if !ok {
					t.Errorf("sent chunk type = %T, want flow.Structured", chunk)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				gotSizes = append(gotSizes, len(rows))
				for _, row := range rows {
					x, _ := row["x"].(int64)
					gotValues = append(gotValues, x)
				}
			}

			base := &config.BaseCollector{Type: config.CollectorTypeSQL, Send: send}
			base.Destination = tt.destination
			cfg := map[string]any{
				"type":       DriverTypeSQLite,
				"connection": ":memory:",
				"query": fmt.Sprintf(
					"WITH RECURSIVE n(x) AS (SELECT 1 WHERE %[1]d > 0 UNION ALL SELECT x+1 FROM n WHERE x < %[1]d) "+
						"SELECT x FROM n", tt.rows,
				),
			}
			if tt.batch != nil {
				cfg["batch"] = tt.batch
			}
			coll, err := NewCollector(base, cfg)
			if err != nil {
				t.Fatalf("NewCollector() error: %v", err)
			}
			if coll.db, err = sql.Open(coll.driver, coll.conn); err != nil {
				t.Fatalf("sql.Open() error: %v", err)
			}
			t.Cleanup(func() { _ = coll.db.Close() })

			if !coll.executeQuery(t.Context()) {
				t.Fatal("Collector.executeQuery() = false, want true")
			}

			mu.Lock()
			defer mu.Unlock()
			if !reflect.DeepEqual(gotSizes, tt.wantSizes) {
				t.Errorf("sent chunk sizes = %v, want %v", gotSizes, tt.wantSizes)
			}
			// All the rows, in order, exactly once.
			for i, x := range gotValues {
				if x != int64(i+1) {
					t.Fatalf("sent row %d has x = %d, want %d", i, x, i+1)
				}
			}
			if len(gotValues) != tt.rows {
				t.Errorf("sent rows = %d, want %d", len(gotValues), tt.rows)
			}
		})
	}
}

// TestCollectorProcessResultsCanceledBetweenResultSets is a regression test: a cancellation that occurs between
// result-sets must be reported as an error, otherwise the query's checkpoint would advance as if it succeeded.
func TestCollectorProcessResultsCanceledBetweenResultSets(t *testing.T) {
	t.Parallel()

	registerFakeSQLDriver()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	// Batching is disabled in [newTestBatcher], so each row is sent synchronously, as soon as it's scanned.
	send := func(_ context.Context, chunk flow.Chunk) {
		if rows, ok := chunk.(flow.Structured); ok && len(rows) > 0 && rows[0]["col"] == int64(1) {
			cancel() // Right after the only row of the 1st result-set.
		}
	}

	var err error
	coll := &Collector{batch: newTestBatcher(t, send)}
	if coll.db, err = sql.Open(fakeSQLDriverName, "noErrors"); err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	t.Cleanup(func() { _ = coll.db.Close() })

	rows, err := coll.db.QueryContext(ctx, "SELECT 1; SELECT 2;")
	if err != nil {
		t.Fatalf("db.QueryContext() error: %v", err)
	}
	t.Cleanup(func() { _ = rows.Close() })

	gotRows, err := coll.processResults(ctx, rows)
	if err == nil {
		t.Error("Collector.processResults() error = nil, want a cancellation error")
	}
	if gotRows != 1 {
		t.Errorf("Collector.processResults() row count = %d, want 1", gotRows)
	}
}

func TestCollectorClose(t *testing.T) {
	t.Parallel()

	t.Run("unstarted", func(t *testing.T) {
		t.Parallel()

		c := &Collector{batch: newTestBatcher(t, dest.Discard.Send)}
		c.Close(t.Context())
	})

	t.Run("fake_pg_pool", func(t *testing.T) {
		t.Parallel()

		c := &Collector{pgPool: new(fakePGPool), usingPG: true, batch: newTestBatcher(t, dest.Discard.Send)}
		_, c.cancelSched = context.WithCancel(t.Context())
		c.closed = make(chan struct{})
		t.Cleanup(c.cancelSched)

		c.Close(t.Context())
		c.Close(t.Context())

		<-c.Done()
	})

	t.Run("in_memory_sqlite", func(t *testing.T) {
		t.Parallel()

		db, err := sql.Open(DriverTypeSQLite, ":memory:")
		if err != nil {
			t.Fatalf("sql.Open() error: %v", err)
		}

		c := &Collector{db: db, batch: newTestBatcher(t, dest.Discard.Send)}
		_, c.cancelSched = context.WithCancel(t.Context())
		c.closed = make(chan struct{})

		c.Close(t.Context())

		<-c.Done()
	})
}

func TestCollectorCloseTimeout(t *testing.T) {
	t.Parallel()

	testTimeout := CloseTimeout + abortTimeout

	tests := []struct {
		name   string
		pgPool pgPool
		want2  time.Duration
	}{
		{
			name:   "with_fake_pg_pool",
			pgPool: &fakePGPool{closeTimeout: true},
			want2:  testTimeout,
		},
		{
			name:   "without_any_db",
			pgPool: nil,
			want2:  0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				coll := &Collector{
					driver:  DriverTypePostgres,
					pgPool:  tt.pgPool,
					usingPG: tt.pgPool != nil,
					timeout: testTimeout,
					batch:   newTestBatcher(t, dest.Discard.Send),
				}

				// Test case 1: Close() before Start() should return immediately and have no effect.
				start := time.Now()
				coll.Close(t.Context())
				if got := time.Since(start); got != 0 {
					t.Fatalf("Collector.Close(1) timeout behaved unexpectedly: got %v, want %v", got, 0)
				}

				// Test case 2: Close() after Start() should block until the DB is closed / the timeout expires.
				_, coll.cancelSched = context.WithCancel(t.Context())

				start = time.Now()
				coll.Close(t.Context())
				if got := time.Since(start); got != tt.want2 {
					t.Errorf("Collector.Close(2) timeout behaved unexpectedly: got %v, want %v", got, tt.want2)
				}

				synctest.Sleep(testTimeout * 3)
			})
		})
	}
}

const (
	fakeSQLDriverName = "versipellis-fake-sql-driver"
)

// newTestBatcher returns a row batcher for unit tests, with batching disabled: each call
// to [flow.Batcher.Add] sends its rows to the given function immediately and synchronously.
func newTestBatcher(tb testing.TB, send config.SendFunc) *flow.Batcher[map[string]any] {
	tb.Helper()

	b, err := flow.NewBatcher(flow.Limits{}, func(ctx context.Context, rows []map[string]any) {
		send(ctx, flow.Structured(rows))
	}, nil)
	if err != nil {
		tb.Fatalf("flow.NewBatcher() error: %v", err)
	}
	return b
}

var registerFakeSQLDriver = sync.OnceFunc(func() {
	sql.Register(fakeSQLDriverName, new(fakeSQLDriver))
})

type fakeSQLDriver struct{}

// Open uses the DSN to set desired failure modes as connection parameters.
func (*fakeSQLDriver) Open(dsn string) (driver.Conn, error) {
	c := &fakeSQLConn{}
	switch {
	case strings.Contains(dsn, "rowsNextError"):
		c.rowsNextError = true
	case strings.Contains(dsn, "rowsNextResultSetError"):
		c.rowsNextResultSetError = true
	}
	return c, nil
}

type fakeSQLConn struct {
	rowsNextError          bool
	rowsNextResultSetError bool
}

func (c *fakeSQLConn) Prepare(_ string) (driver.Stmt, error) {
	return nil, errors.New("not implemented")
}

func (c *fakeSQLConn) Close() error {
	return nil
}

func (c *fakeSQLConn) Begin() (driver.Tx, error) {
	return &fakeSQLTx{}, nil
}

func (c *fakeSQLConn) QueryContext(_ context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	return &fakeSQLRows{nextError: c.rowsNextError, nextResultSetError: c.rowsNextResultSetError}, nil
}

type fakeSQLTx struct{}

func (*fakeSQLTx) Commit() error {
	return nil
}

func (*fakeSQLTx) Rollback() error {
	return nil
}

// fakeSQLRows yields 1 row per result-set, across 3 result-sets, unless mode is one of the
// fakeSQLMode* constants, in which case it fails instead of completing normally. This is used
// to test the correct handling of multiple result-sets (which the MySQL driver supports, for
// example), and of driver-level iteration errors, in [Collector.processResults].
type fakeSQLRows struct {
	set  int
	read bool

	nextError          bool
	nextResultSetError bool
}

func (r *fakeSQLRows) Columns() []string { return []string{"col"} }
func (r *fakeSQLRows) Close() error      { return nil }

func (r *fakeSQLRows) Next(d []driver.Value) error {
	if r.read {
		if r.nextError {
			return errors.New("fake row iteration error")
		}
		return io.EOF
	}
	r.read = true
	d[0] = int64(r.set + 1)
	return nil
}

func (r *fakeSQLRows) HasNextResultSet() bool {
	return r.set < 2
}

func (r *fakeSQLRows) NextResultSet() error {
	if r.nextResultSetError {
		return errors.New("next row-set iteration error")
	}
	r.set++
	r.read = false
	return nil
}

func TestCollectorConcurrencyLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		limit     int64
		wantCount int32
	}{
		{
			name:      "0_no_concurrency",
			limit:     0,
			wantCount: 1,
		},
		{
			name:      "1_no_concurrency",
			limit:     1,
			wantCount: 1,
		},
		{
			name:      "2_bounded_concurrency",
			limit:     2,
			wantCount: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				base, err := config.NewBaseCollector(
					map[string]any{
						"type":              config.CollectorTypeSQL,
						"schedule":          "@every 1s",
						"concurrency_limit": tt.limit,
						"destination":       config.SenderTypeDiscard,
					},
					tt.name, map[string]config.Sender{config.SenderTypeDiscard: dest.Discard},
				)
				if err != nil {
					t.Fatalf("config.NewBaseCollector() error: %v", err)
				}

				started := make(chan struct{}, 10)
				unblock := make(chan struct{})
				var count atomic.Int32

				base.Send = func(context.Context, flow.Chunk) {
					count.Add(1)
					select {
					case started <- struct{}{}:
					default:
					}
					<-unblock
				}

				c, err := NewCollector(base, map[string]any{
					"type":       DriverTypeSQLite,
					"connection": ":memory:",
					"query":      "SELECT 1;",
				})
				if err != nil {
					t.Fatalf("NewCollector() error: %v", err)
				}

				c.db, err = openDB(t.Context(), c.driver, c.conn)
				if err != nil {
					t.Fatalf("openDB() error: %v", err)
				}
				t.Cleanup(func() { _ = c.db.Close() })

				ctx, cancel := context.WithCancel(t.Context())
				c.cancelSched = cancel

				go c.scheduleNext(ctx, ctx, time.Now())

				// Advance to 1st tick.
				synctest.Sleep(time.Second)
				<-started

				// Advance to 2nd tick.
				synctest.Sleep(time.Second)
				if tt.limit > 1 {
					<-started
				}

				// Unblock in-flight queries and let them finish.
				close(unblock)
				synctest.Sleep(10 * time.Millisecond)

				cancel()
				synctest.Wait()

				if got := count.Load(); got != tt.wantCount {
					t.Errorf("executed queries = %d, want %d", got, tt.wantCount)
				}
			})
		})
	}
}

func TestCollectorExecuteWithConcurrencyCanceled(t *testing.T) {
	t.Parallel()

	c := &Collector{batch: newTestBatcher(t, dest.Discard.Send)}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	ch := make(chan struct{}, 1)
	ch <- struct{}{}

	c.executeWithConcurrency(ctx, t.Context(), ch, time.Now())

	if len(ch) != 1 {
		t.Errorf("len(ch) = %d, want 1", len(ch))
	}
}

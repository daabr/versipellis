package sql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/daabr/versipellis/pkg/config"
	"github.com/daabr/versipellis/pkg/cron"
	"github.com/daabr/versipellis/pkg/dest"
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
			name: "nil_base",
			base: nil,
			cfg: map[string]any{
				"sql": map[string]any{},
			},
			wantErr: true,
		},
		{
			name: "wrong_base_type",
			base: &config.BaseCollector{Type: config.CollectorTypeHTTP},
			cfg: map[string]any{
				"type": config.CollectorTypeHTTP,
				"sql":  map[string]any{},
			},
			wantErr: true,
		},
		{
			name:    "nil_collector_cfg",
			base:    &config.BaseCollector{Type: config.CollectorTypeSQL},
			cfg:     nil,
			wantErr: true,
		},
		{
			name: "missing_sql_section",
			base: &config.BaseCollector{Type: config.CollectorTypeSQL},
			cfg: map[string]any{
				"type": config.CollectorTypeSQL,
				"sol":  map[string]any{},
			},
			wantErr: true,
		},
		{
			name: "invalid_sql_section",
			base: &config.BaseCollector{Type: config.CollectorTypeSQL},
			cfg: map[string]any{
				"type": config.CollectorTypeSQL,
				"sql":  "not a map",
			},
			wantErr: true,
		},
		{
			name: "missing_driver_type",
			base: &config.BaseCollector{Type: config.CollectorTypeSQL},
			cfg: map[string]any{
				"type": config.CollectorTypeSQL,
				"sql": map[string]any{
					"connection": "connection",
					"query":      "SELECT 1",
				},
			},
			wantErr: true,
		},
		{
			name: "unrecognized_driver_type",
			base: &config.BaseCollector{Type: config.CollectorTypeSQL},
			cfg: map[string]any{
				"type": config.CollectorTypeSQL,
				"sql": map[string]any{
					"type":       "unknown",
					"connection": "connection",
					"query":      "SELECT 1",
				},
			},
			wantErr: true,
		},
		{
			name: "missing_connection",
			base: &config.BaseCollector{Type: config.CollectorTypeSQL},
			cfg: map[string]any{
				"type": config.CollectorTypeSQL,
				"sql": map[string]any{
					"type":  DriverTypeSQLite,
					"query": "SELECT 1",
				},
			},
			wantErr: true,
		},
		{
			name: "missing_query",
			base: &config.BaseCollector{Type: config.CollectorTypeSQL},
			cfg: map[string]any{
				"type": config.CollectorTypeSQL,
				"sql": map[string]any{
					"type":       DriverTypeSQLite,
					"connection": ":memory:",
				},
			},
			wantErr: true,
		},
		{
			name: "invalid_timeout",
			base: &config.BaseCollector{Type: config.CollectorTypeSQL},
			cfg: map[string]any{
				"type": config.CollectorTypeSQL,
				"sql": map[string]any{
					"type":       DriverTypeSQLite,
					"connection": "connection",
					"query":      "SELECT 1",
					"timeout":    "invalid",
				},
			},
			wantErr: true,
		},
		{
			name: "negative_timeout_is_allowed",
			base: &config.BaseCollector{Type: config.CollectorTypeSQL},
			cfg: map[string]any{
				"type": config.CollectorTypeSQL,
				"sql": map[string]any{
					"type":       DriverTypeSQLite,
					"connection": "connection",
					"query":      "SELECT 1",
					"timeout":    "-5s",
				},
			},
			wantErr: false,
		},
		{
			name: "happy_path",
			base: &config.BaseCollector{Type: config.CollectorTypeSQL},
			cfg: map[string]any{
				"type": config.CollectorTypeSQL,
				"sql": map[string]any{
					"type":       strings.ToUpper(DriverTypeSQLite), // Test case-insensitivity of the driver type.
					"connection": "connection",
					"query":      "SELECT 1",
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, gotErr := NewCollector(tt.base, tt.cfg); (gotErr != nil) != tt.wantErr {
				t.Errorf("NewCollector() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
		})
	}
}

func TestLoadAndCheckQuery(t *testing.T) {
	t.Parallel()

	queryWithSpaces := "\nSELECT 1  \n\n"
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

	base, err := config.NewBaseCollector(map[string]any{"type": config.CollectorTypeSQL, "schedule": "@once"})
	if err != nil {
		t.Fatalf("config.NewBaseCollector() error: %v", err)
	}

	c, err := NewCollector(base, map[string]any{
		"type": config.CollectorTypeSQL,
		"sql": map[string]any{
			"type":       DriverTypeSQLite,
			"connection": ":memory:",
			"query":      "SELECT 1",
		},
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

	base, err := config.NewBaseCollector(map[string]any{"type": config.CollectorTypeSQL, "schedule": "@once"})
	if err != nil {
		t.Fatalf("config.NewBaseCollector() error: %v", err)
	}

	tests := []string{
		DriverTypeCockroachDB,
		DriverTypeMariaDB,
		DriverTypeMSSQL,
		DriverTypeODBC,
		DriverTypePostgres,
		DriverTypePostgreSQL,
		DriverTypeSAPHANA,
	}
	for _, driver := range tests {
		t.Run(driver, func(t *testing.T) {
			t.Parallel()

			c, err := NewCollector(base, map[string]any{
				"type": config.CollectorTypeSQL,
				"sql": map[string]any{
					"type":       driver,
					"connection": "invalid_connection_string",
					"query":      "SELECT 1",
				},
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
			synctest.Test(t, func(t *testing.T) {
				base, err := config.NewBaseCollector(map[string]any{
					"type":     config.CollectorTypeSQL,
					"schedule": tt.schedule,
				})
				if err != nil {
					t.Fatalf("config.NewBaseCollector() error: %v", err)
				}

				c, err := NewCollector(base, map[string]any{
					"type": config.CollectorTypeSQL,
					"sql": map[string]any{
						"type":       DriverTypeSQLite,
						"connection": ":memory:",
						"query":      "SELECT 1;",
					},
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
				c.done = ctx.Done()
				c.cancel = cancel
				if tt.cancel {
					cancel()
				} else {
					t.Cleanup(cancel)
				}

				if !tt.isAsync {
					c.scheduleNextQuery(ctx, time.Now())
					return
				}

				go c.scheduleNextQuery(ctx, time.Now().Add(-5*time.Second))
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
		sender  dest.Sender
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
			name: "processing_error",
			cfg: map[string]any{
				"type":       DriverTypeSQLite,
				"connection": ":memory:",
				"query":      "SELECT 1",
			},
			sender: fakeSender(errors.New("fake sender error")),
			wantOK: false,
		},
		{
			name: "happy_path_with_dummy_sender",
			cfg: map[string]any{
				"type":       DriverTypeSQLite,
				"connection": ":memory:",
				"query":      "SELECT 1",
			},
			sender: fakeSender(nil),
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
			base := &config.BaseCollector{
				Type:     config.CollectorTypeSQL,
				Schedule: sched,
				Sender:   tt.sender,
			}
			c, err := NewCollector(base, map[string]any{"type": config.CollectorTypeSQL, "sql": tt.cfg})
			if err != nil {
				t.Fatalf("NewCollector() error: %v", err)
			}
			c.db, err = sql.Open(c.driver, c.conn)
			if err != nil {
				t.Fatalf("sql.Open() error: %v", err)
			}

			if tt.closeDB {
				if err := c.db.Close(); err != nil {
					t.Fatalf("sql.DB.Close() error: %v", err)
				}
			} else {
				t.Cleanup(func() { _ = c.db.Close() })
			}

			if gotOK := c.executeQuery(t.Context()); gotOK != tt.wantOK {
				t.Errorf("Collector.executeQuery() = %v, want %v", gotOK, tt.wantOK)
			}
			if timestampUpdated := !c.prevStart.IsZero(); timestampUpdated != tt.wantOK {
				t.Errorf("Collector.prevXXXX checkpoint updated = %v, want %v", timestampUpdated, tt.wantOK)
			}
		})
	}
}

func fakeSender(err error) dest.Sender {
	return func(_ context.Context, _ any) error {
		return err
	}
}

func TestProcessResults(t *testing.T) {
	t.Parallel()

	db, err := sql.Open(DriverTypeSQLite, ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	rows, err := db.QueryContext(t.Context(), "SELECT 1")
	if err != nil {
		t.Fatalf("db.QueryContext() error: %v", err)
	}
	_ = rows.Close() // Close rows immediately so [sql.Rows.Columns] fails.

	if _, err := processResults(t.Context(), rows, nil, 1); err == nil {
		t.Error("processResults() error = nil, wantErr = true")
	}
}

func TestProcessResultsWithFakeDriver(t *testing.T) {
	t.Parallel()
	registerFakeSQLDriver()

	tests := []struct {
		name         string
		dsn          string
		wantRowCount int
		wantErr      bool
	}{
		{
			name:         "multiple_result_sets",
			dsn:          "noErrors",
			wantRowCount: 3,
			wantErr:      false,
		},
		{
			name:         "row_iteration_error",
			dsn:          "rowsNextError",
			wantRowCount: 1,
			wantErr:      true,
		},
		{
			name:         "row_set_iteration_error",
			dsn:          "rowsNextResultSetError",
			wantRowCount: 1,
			wantErr:      true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db, err := sql.Open(fakeSQLDriverName, tt.dsn)
			if err != nil {
				t.Fatalf("sql.Open() error: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })

			rows, err := db.QueryContext(t.Context(), "SELECT 1; SELECT 2;")
			if err != nil {
				t.Fatalf("db.QueryContext() error: %v", err)
			}
			t.Cleanup(func() { _ = rows.Close() })

			gotRowCount, gotErr := processResults(t.Context(), rows, nil, 1)
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("processResults() error = %v, wantErr = %v", gotErr, tt.wantErr)
			}
			if gotRowCount != tt.wantRowCount {
				t.Errorf("processResults() row count = %d, want %d", gotRowCount, tt.wantRowCount)
			}
		})
	}
}

func TestCollectorClose(t *testing.T) {
	t.Parallel()

	t.Run("unstarted", func(t *testing.T) {
		t.Parallel()

		c := &Collector{}
		c.Close()
	})

	t.Run("fake_pg_pool", func(t *testing.T) {
		t.Parallel()

		var ctx context.Context
		c := &Collector{pgPool: fakePGPool{}, usingPG: true}
		ctx, c.cancel = context.WithCancel(t.Context())
		c.done = ctx.Done()

		c.Close()
		c.Close()

		<-c.Done()
	})

	t.Run("in_memory_sqlite", func(t *testing.T) {
		t.Parallel()

		db, err := sql.Open(DriverTypeSQLite, ":memory:")
		if err != nil {
			t.Fatalf("sql.Open() error: %v", err)
		}

		var ctx context.Context
		c := &Collector{db: db}
		ctx, c.cancel = context.WithCancel(t.Context())
		c.done = ctx.Done()

		c.Close()

		<-c.Done()
	})
}

func TestCollectorCloseTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := &Collector{
			driver:  DriverTypePostgres,
			pgPool:  fakePGPool{closeTimeout: true},
			usingPG: true,
		}
		start := time.Now()
		c.Close()

		if time.Since(start) != closeTimeout {
			t.Error("Collector.Close() timeout behaved unexpectedly")
		}
		synctest.Sleep(closeTimeout * 2)
	})
}

const (
	fakeSQLDriverName = "versipellis-fake-sql-driver"
)

var registerFakeSQLDriver = sync.OnceFunc(func() {
	sql.Register(fakeSQLDriverName, fakeSQLDriver{})
})

type fakeSQLDriver struct{}

// Open uses the DSN to set desired failure modes as connection parameters.
func (fakeSQLDriver) Open(dsn string) (driver.Conn, error) {
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

func (fakeSQLTx) Commit() error {
	return nil
}

func (fakeSQLTx) Rollback() error {
	return nil
}

// fakeSQLRows yields 1 row per result-set, across 3 result-sets, unless mode is one of the
// fakeSQLMode* constants, in which case it fails instead of completing normally. This is used
// to test the correct handling of multiple result-sets (which the MySQL driver supports, for
// example), and of driver-level iteration errors, in [processResults].
type fakeSQLRows struct {
	set  int
	read bool

	nextError          bool
	nextResultSetError bool
}

func (r *fakeSQLRows) Columns() []string { return []string{"col"} }
func (r *fakeSQLRows) Close() error      { return nil }

func (r *fakeSQLRows) Next(dest []driver.Value) error {
	if r.read {
		if r.nextError {
			return errors.New("fake row iteration error")
		}
		return io.EOF
	}
	r.read = true
	dest[0] = int64(r.set + 1)
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

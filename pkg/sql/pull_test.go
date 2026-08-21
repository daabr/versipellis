package sql

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daabr/versipellis/pkg/config"
)

func TestNewPuller(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		base    *config.BasePuller
		pullCfg map[string]any
		wantErr bool
	}{
		{
			name: "nil_base",
			base: nil,
			pullCfg: map[string]any{
				"sql": map[string]any{},
			},
			wantErr: true,
		},
		{
			name: "wrong_base_type",
			base: &config.BasePuller{Type: config.PullTypeHTTP},
			pullCfg: map[string]any{
				"type": config.PullTypeHTTP,
				"sql":  map[string]any{},
			},
			wantErr: true,
		},
		{
			name:    "nil_pull_cfg",
			base:    &config.BasePuller{Type: config.PullTypeSQL},
			pullCfg: nil,
			wantErr: true,
		},
		{
			name: "missing_sql_section",
			base: &config.BasePuller{Type: config.PullTypeSQL},
			pullCfg: map[string]any{
				"type": config.PullTypeSQL,
				"sol":  map[string]any{},
			},
			wantErr: true,
		},
		{
			name: "invalid_sql_section",
			base: &config.BasePuller{Type: config.PullTypeSQL},
			pullCfg: map[string]any{
				"type": config.PullTypeSQL,
				"sql":  "not a map",
			},
			wantErr: true,
		},
		{
			name: "missing_driver_type",
			base: &config.BasePuller{Type: config.PullTypeSQL},
			pullCfg: map[string]any{
				"type": config.PullTypeSQL,
				"sql": map[string]any{
					"connection": "connection",
					"query":      "SELECT 1",
				},
			},
			wantErr: true,
		},
		{
			name: "unrecognized_driver_type",
			base: &config.BasePuller{Type: config.PullTypeSQL},
			pullCfg: map[string]any{
				"type": config.PullTypeSQL,
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
			base: &config.BasePuller{Type: config.PullTypeSQL},
			pullCfg: map[string]any{
				"type": config.PullTypeSQL,
				"sql": map[string]any{
					"type":  DriverTypeSQLite,
					"query": "SELECT 1",
				},
			},
			wantErr: true,
		},
		{
			name: "missing_query",
			base: &config.BasePuller{Type: config.PullTypeSQL},
			pullCfg: map[string]any{
				"type": config.PullTypeSQL,
				"sql": map[string]any{
					"type":       DriverTypeSQLite,
					"connection": ":memory:",
				},
			},
			wantErr: true,
		},
		{
			name: "invalid_timeout",
			base: &config.BasePuller{Type: config.PullTypeSQL},
			pullCfg: map[string]any{
				"type": config.PullTypeSQL,
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
			base: &config.BasePuller{Type: config.PullTypeSQL},
			pullCfg: map[string]any{
				"type": config.PullTypeSQL,
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
			base: &config.BasePuller{Type: config.PullTypeSQL},
			pullCfg: map[string]any{
				"type": config.PullTypeSQL,
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

			if _, gotErr := NewPuller(tt.base, tt.pullCfg); (gotErr != nil) != tt.wantErr {
				t.Errorf("NewPuller() error = %v, wantErr %v", gotErr, tt.wantErr)
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

func TestPullerStartNilGuard(t *testing.T) {
	t.Parallel()

	var nilPuller *Puller
	if ok := nilPuller.Start(t.Context()); ok {
		t.Error("nil Puller.Start() = true, want false")
	}
}

func TestPullerStart(t *testing.T) {
	t.Parallel()

	base, err := config.NewBasePuller(map[string]any{"type": config.PullTypeSQL, "schedule": "@once"})
	if err != nil {
		t.Fatalf("config.NewBasePuller() error: %v", err)
	}

	puller, err := NewPuller(base, map[string]any{
		"type": config.PullTypeSQL,
		"sql": map[string]any{
			"type":       DriverTypeSQLite,
			"connection": ":memory:",
			"query":      "SELECT 1",
		},
	})
	if err != nil {
		t.Fatalf("NewPuller() error: %v", err)
	}

	if ok := puller.Start(t.Context()); !ok {
		t.Fatal("Puller.Start() failed")
	}
	if ok := puller.Start(t.Context()); !ok {
		t.Fatal("second Puller.Start() failed (should be idempotent)")
	}

	<-puller.Done() // Wait for the puller's goroutine to finish its work.
}

func TestPullerConnectionStringError(t *testing.T) {
	t.Parallel()

	base, err := config.NewBasePuller(map[string]any{"type": config.PullTypeSQL, "schedule": "@once"})
	if err != nil {
		t.Fatalf("config.NewBasePuller() error: %v", err)
	}

	tests := []string{
		DriverTypeMariaDB,
		DriverTypeMSSQL,
		DriverTypePostgres,
		DriverTypeSAPHANA,
	}
	for _, driver := range tests {
		t.Run(driver, func(t *testing.T) {
			t.Parallel()

			puller, err := NewPuller(base, map[string]any{
				"type": config.PullTypeSQL,
				"sql": map[string]any{
					"type":       driver,
					"connection": "invalid_connection_string",
					"query":      "SELECT 1",
				},
			})
			if err != nil {
				t.Fatalf("NewPuller() error: %v", err)
			}

			if ok := puller.Start(t.Context()); ok {
				t.Fatal("Puller.Start() succeeded unexpectedly")
			}
		})
	}
}

func TestOpenDBInvalidDriver(t *testing.T) {
	t.Parallel()

	if _, err := OpenDB(t.Context(), "invalid_driver", "connection"); err == nil {
		t.Error("OpenDB(invalid_driver) error = nil, wantErr = true")
	}
}

func TestOpenDBPingFailure(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel() // Canceled context causes PingContext to fail immediately.

	if _, err := OpenDB(ctx, DriverTypeMySQL, "user:pass@tcp(127.0.0.1:1)/dbname"); err == nil {
		t.Error("OpenDB() error = nil, wantErr = true")
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

			base, err := config.NewBasePuller(map[string]any{"type": config.PullTypeSQL, "schedule": tt.schedule})
			if err != nil {
				t.Fatalf("config.NewBasePuller() error: %v", err)
			}
			puller, err := NewPuller(base, map[string]any{
				"type": config.PullTypeSQL,
				"sql": map[string]any{
					"type":       DriverTypeSQLite,
					"connection": ":memory:",
					"query":      "SELECT 1;",
				},
			})
			if err != nil {
				t.Fatalf("NewPuller() error: %v", err)
			}

			ctx, cancel := context.WithCancel(t.Context())
			if tt.cancel {
				cancel()
			} else {
				t.Cleanup(cancel)
			}

			if !tt.isAsync {
				puller.scheduleNextQuery(ctx, time.Now())
				return
			}

			go puller.scheduleNextQuery(ctx, time.Now().Add(-5*time.Second))
			time.Sleep(50 * time.Millisecond)
			cancel()
		})
	}
}

func TestPullerExecuteQuery(t *testing.T) {
	t.Parallel()

	base, err := config.NewBasePuller(map[string]any{"type": config.PullTypeSQL, "schedule": "@once"})
	if err != nil {
		t.Fatalf("config.NewBasePuller() error: %v", err)
	}

	puller, err := NewPuller(base, map[string]any{
		"type": config.PullTypeSQL,
		"sql": map[string]any{
			"type":       DriverTypeSQLite,
			"connection": ":memory:",
			"query":      "SELECT 1",
		},
	})
	if err != nil {
		t.Fatalf("NewPuller() error: %v", err)
	}

	// Substitute the [Puller.Start] logic...
	puller.db, err = sql.Open(puller.driver, puller.conn)
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	t.Cleanup(func() { _ = puller.db.Close() })

	var ctx context.Context
	ctx, puller.cancel = context.WithCancel(t.Context())
	puller.done = ctx.Done()

	// ...In order to run [Puller.executeQuery] directly.
	gotPayload, gotRowCount, gotOK := puller.executeQuery(ctx)
	want := "{\"1\":1}\n"
	if got := string(gotPayload); got != want {
		t.Errorf("Puller.executeQuery() = %q, want %q", got, want)
	}
	if gotRowCount != 1 {
		t.Errorf("Puller.executeQuery() row count = %d, want 1", gotRowCount)
	}
	if !gotOK {
		t.Errorf("Puller.executeQuery() ok = %v, want true", gotOK)
	}
}

func TestPullerExecuteQueryExtraBranches(t *testing.T) {
	t.Parallel()

	base, err := config.NewBasePuller(map[string]any{"type": config.PullTypeSQL, "schedule": "@once"})
	if err != nil {
		t.Fatalf("config.NewBasePuller() error: %v", err)
	}

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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			puller, err := NewPuller(base, map[string]any{"type": config.PullTypeSQL, "sql": tt.cfg})
			if err != nil {
				t.Fatalf("NewPuller() error: %v", err)
			}
			puller.db, err = sql.Open(puller.driver, puller.conn)
			if err != nil {
				t.Fatalf("sql.Open() error: %v", err)
			}

			if tt.closeDB {
				if err := puller.db.Close(); err != nil {
					t.Fatalf("sql.DB.Close() error: %v", err)
				}
			} else {
				t.Cleanup(func() { _ = puller.db.Close() })
			}
			if _, _, gotOK := puller.executeQuery(t.Context()); gotOK != tt.wantOK {
				t.Errorf("Puller.executeQuery() ok = %v, want %v", gotOK, tt.wantOK)
			}
		})
	}
}

func TestSerializeAndCloseError(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	rows, err := db.QueryContext(t.Context(), "SELECT 1;")
	if err != nil {
		t.Fatalf("db.QueryContext() error: %v", err)
	}
	_ = rows.Close() // Close rows immediately so [sql.Rows.Columns] fails.

	if _, _, err := serializeAndClose(rows); err == nil {
		t.Error("serializeAndClose() expected error on closed rows, got nil")
	}
}

func TestPullerClose(t *testing.T) {
	t.Parallel()

	t.Run("unstarted", func(t *testing.T) {
		t.Parallel()

		puller := &Puller{}
		puller.Close()
	})

	t.Run("fake_pg_pool", func(t *testing.T) {
		t.Parallel()

		var ctx context.Context
		puller := &Puller{pgPool: fakePGPool{}, usingPG: true}
		ctx, puller.cancel = context.WithCancel(t.Context())
		puller.done = ctx.Done()

		puller.Close()
		puller.Close()

		<-puller.Done()
	})

	t.Run("in_memory_sqlite", func(t *testing.T) {
		t.Parallel()

		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatalf("sql.Open() error: %v", err)
		}

		var ctx context.Context
		puller := &Puller{db: db}
		ctx, puller.cancel = context.WithCancel(t.Context())
		puller.done = ctx.Done()

		puller.Close()

		<-puller.Done()
	})
}

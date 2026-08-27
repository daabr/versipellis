package sql

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/daabr/versipellis/pkg/config"
	"github.com/daabr/versipellis/pkg/dest"
)

func TestCollectorConnectToPostgres(t *testing.T) {
	t.Parallel()

	// Pgxpool can parse the DSN below just fine, but we force ping failure by canceling the context.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	tests := []struct {
		name    string
		coll    *Collector
		ctx     context.Context //nolint:containedctx // Purely for test coverage purposes.
		wantErr bool
	}{
		{
			name:    "already_using_pg",
			coll:    &Collector{usingPG: true},
			ctx:     t.Context(),
			wantErr: false,
		},
		{
			name:    "invalid_dsn",
			coll:    &Collector{conn: "://invalid-dsn"},
			ctx:     t.Context(),
			wantErr: true,
		},
		{
			name:    "unreachable_host",
			coll:    &Collector{conn: "postgres://127.0.0.1:1/dbname"},
			ctx:     ctx,
			wantErr: true,
		},
		{
			name:    "pool_already_set",
			coll:    &Collector{pgPool: fakePGPool{}},
			ctx:     t.Context(),
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if err := tt.coll.connectToPostgres(tt.ctx); (err != nil) != tt.wantErr {
				t.Errorf("Collector.connectToPostgres() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !tt.coll.usingPG {
				t.Error("Collector.connectToPostgres() usingPG = false, want true")
			}
		})
	}
}

func TestCollectorStartPostgres(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		usingPG bool
	}{
		{
			name:    "using_pg",
			usingPG: true,
		},
		{
			name:    "not_using_pg",
			usingPG: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			base, err := config.NewBaseCollector(map[string]any{"type": config.CollectorTypeSQL, "schedule": "@once"}, "")
			if err != nil {
				t.Fatalf("config.NewBaseCollector() error: %v", err)
			}

			c, err := NewCollector(base, map[string]any{
				"type": config.CollectorTypeSQL,
				"sql": map[string]any{
					"type":       DriverTypePostgres,
					"connection": "postgres://localhost:5432/dbname",
					"query":      "SELECT 1",
				},
			})
			if err != nil {
				t.Fatalf("NewCollector() error: %v", err)
			}

			c.pgPool = fakePGPool{cols: []string{"1"}, rows: [][]any{{1}}}
			c.usingPG = tt.usingPG
			ctx := t.Context()
			if tt.usingPG {
				ctx, c.cancel = context.WithCancel(t.Context())
				c.done = ctx.Done()
			}

			if ok := c.Start(ctx); !ok {
				t.Fatal("Collector.Start() failed")
			}
			if tt.usingPG {
				go c.scheduleNextQuery(ctx, time.Now())
			}

			<-c.Done() // Wait for the collector's goroutine to finish its work.
		})
	}
}

func TestCollectorExecutePostgresQuery(t *testing.T) {
	t.Parallel()

	base, err := config.NewBaseCollector(map[string]any{"type": config.CollectorTypeSQL, "schedule": "@once"}, "")
	if err != nil {
		t.Fatalf("config.NewBaseCollector() error: %v", err)
	}

	c, err := NewCollector(base, map[string]any{
		"sql": map[string]any{
			"type":       DriverTypePostgres,
			"connection": "postgres://localhost:5432/dbname",
			"query":      "SELECT 1",
		},
	})
	if err != nil {
		t.Fatalf("NewCollector() error: %v", err)
	}

	c.pgPool = fakePGPool{cols: []string{"1"}, rows: [][]any{{1}}}
	c.usingPG = true

	if !c.executeQuery(t.Context()) {
		t.Error("Collector.executeQuery() = false, want true")
	}
	if c.prevStart.IsZero() || c.prevEnd.IsZero() {
		t.Error("Collector.executeQuery() did not update the checkpoint on success")
	}
}

func TestCollectorExecutePostgresQueryErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		pool           fakePGPool
		wantOK         bool
		wantCheckpoint bool
	}{
		{
			name:   "begin_tx_error",
			pool:   fakePGPool{beginErr: errors.New("begin tx error")},
			wantOK: false,
		},
		{
			name:   "query_error",
			pool:   fakePGPool{queryErr: errors.New("query error")},
			wantOK: false,
		},
		{
			name:           "timestamp_checkpoint_after_partial_success",
			pool:           fakePGPool{cols: []string{"col"}, rows: [][]any{{1}}, finalErr: errors.New("final error")},
			wantOK:         false,
			wantCheckpoint: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			coll := &Collector{driver: DriverTypePostgres, query: "SELECT 1", pgPool: tt.pool, usingPG: true}
			if ok := coll.executeQuery(t.Context()); ok != tt.wantOK {
				t.Errorf("Collector.executeQuery() = %v, want %v", ok, tt.wantOK)
			}
			if gotCheckpoint := !coll.prevStart.IsZero(); gotCheckpoint != tt.wantCheckpoint {
				t.Errorf("Collector.executeQuery() checkpoint updated = %v, want %v", gotCheckpoint, tt.wantCheckpoint)
			}
		})
	}
}

func TestProcessPostgresResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		sender  dest.Sender
		noRows  bool
		wantErr bool
	}{
		{
			name:    "no_rows",
			noRows:  true,
			wantErr: false,
		},
		{
			name:    "with_rows_but_no_sender",
			sender:  nil,
			noRows:  false,
			wantErr: false,
		},
		{
			name:    "with_rows_and_sender",
			sender:  fakeSender(nil),
			noRows:  false,
			wantErr: false,
		},
		{
			name:    "with_rows_and_sender_and_error",
			sender:  fakeSender(errors.New("sender error")),
			noRows:  false,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rows := &fakePGRows{cols: []string{"id", "name"}, index: -1}
			if !tt.noRows {
				rows.rows = [][]any{
					{1, "Alice"},
					{2, "Bob"},
					{3, "Carol"},
				}
			}

			gotRowCount, err := processPostgresResults(t.Context(), rows, tt.sender)
			switch {
			case (err != nil) != tt.wantErr:
				t.Errorf("processPostgresResults() error = %v, wantErr = %v", err, tt.wantErr)
			case tt.wantErr && gotRowCount != 0:
				t.Errorf("processPostgresResults() row count = %d, want 0 on error", gotRowCount)
			case !tt.wantErr && gotRowCount != len(rows.rows):
				t.Errorf("processPostgresResults() row count = %d, want %d", gotRowCount, len(rows.rows))
			}
		})
	}
}

func TestProcessPostgresResultsErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		rows *fakePGRows
	}{
		{
			name: "scan_error",
			rows: &fakePGRows{
				cols:    []string{"col"},
				rows:    [][]any{{1}},
				index:   -1,
				scanErr: errors.New("scan error"),
			},
		},
		{
			name: "final_error",
			rows: &fakePGRows{
				cols:     []string{"col"},
				rows:     [][]any{{1}},
				index:    -1,
				finalErr: errors.New("final error"),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := processPostgresResults(t.Context(), tt.rows, nil); err == nil {
				t.Errorf("processPostgresResults() error = nil, wantErr = true")
			}
		})
	}
}

// Simple replacement of [pgxpool.Pool].
type fakePGPool struct {
	cols []string
	rows [][]any

	closeTimeout bool

	beginErr error
	queryErr error
	finalErr error
}

func (p fakePGPool) BeginTx(_ context.Context, _ pgx.TxOptions) (pgx.Tx, error) {
	if p.beginErr != nil {
		return nil, p.beginErr
	}
	return fakePGTx{cols: p.cols, rows: p.rows, queryErr: p.queryErr, finalErr: p.finalErr}, nil
}

func (p fakePGPool) Close() {
	if p.closeTimeout {
		synctest.Sleep(closeTimeout * 2)
	}
}

// Simple implementation of the [pgx.Tx] interface.
type fakePGTx struct {
	cols []string
	rows [][]any

	queryErr  error
	commitErr error
	finalErr  error
}

// Begin starts a pseudo nested transaction.
func (t fakePGTx) Begin(_ context.Context) (pgx.Tx, error) {
	return fakePGTx{cols: t.cols, rows: t.rows}, nil
}

// Commit commits the transaction if this is a real transaction or releases the savepoint if this is a pseudo nested
// transaction. Commit will return an error where errors.Is([pgx.ErrTxClosed]) is true if the Tx is already closed,
// but is otherwise safe to call multiple times. If the commit fails with a rollback status (e.g., the transaction
// was already in a broken state) then an error where errors.Is([pgx.ErrTxCommitRollback]) is true will be returned.
func (t fakePGTx) Commit(_ context.Context) error {
	return t.commitErr
}

// Rollback rolls back the transaction if this is a real transaction or rolls back to the savepoint if this is a pseudo
// nested transaction. Rollback will return an error where errors.Is([pgx.ErrTxClosed]) is true if the Tx is already closed,
// but is otherwise safe to call multiple times. Hence, a defer [pgx.Tx.Rollback] is safe even if [pgx.Tx.Commit] will be
// caller first in a non-error condition. Any other failure of a real transaction will result in the connection being closed.
func (t fakePGTx) Rollback(_ context.Context) error {
	return nil
}

// Query executes a query against the database and returns the resulting rows.
func (t fakePGTx) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	if t.queryErr != nil {
		return nil, t.queryErr
	}
	return &fakePGRows{cols: t.cols, rows: t.rows, index: -1, finalErr: t.finalErr}, nil
}

func (t fakePGTx) CopyFrom(_ context.Context, _ pgx.Identifier, _ []string, _ pgx.CopyFromSource) (int64, error) {
	return 0, errors.New("not implemented")
}

func (t fakePGTx) SendBatch(_ context.Context, _ *pgx.Batch) pgx.BatchResults {
	return nil // Not implemented.
}

func (t fakePGTx) LargeObjects() pgx.LargeObjects {
	return pgx.LargeObjects{} // Not implemented.
}

func (t fakePGTx) Prepare(_ context.Context, _, _ string) (*pgconn.StatementDescription, error) {
	return nil, errors.New("not implemented")
}

func (t fakePGTx) Exec(_ context.Context, _ string, _ ...any) (commandTag pgconn.CommandTag, err error) {
	return pgconn.CommandTag{}, errors.New("not implemented")
}

func (t fakePGTx) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return nil // Not implemented.
}

func (t fakePGTx) Conn() *pgx.Conn {
	return nil // Not implemented.
}

// Simple implementation of the [pgx.Rows] interface.
type fakePGRows struct {
	cols  []string
	rows  [][]any
	index int

	scanErr  error
	finalErr error
}

// FieldDescriptions returns the field descriptions of the columns. It may return nil.
// In particular this can occur when there was an error executing the query.
func (r *fakePGRows) FieldDescriptions() []pgconn.FieldDescription {
	fds := make([]pgconn.FieldDescription, len(r.cols))
	for i, col := range r.cols {
		fds[i] = pgconn.FieldDescription{Name: col}
	}
	return fds
}

// Next prepares the next row for reading. It returns true if there is another row and false if no more rows are
// available or a fatal error has occurred. It automatically closes rows upon returning false (whether due to all
// rows having been read or due to an error).
//
// Callers should check [pgx.Rows.Err] after [pgx.Rows.Next] returns false to detect whether
// result-set reading ended prematurely due to an error. See [pgx.Conn.Query] for details.
//
// For simpler error handling, consider using the higher-level pgx v5 [pgx.CollectRows] and [pgx.ForEachRow] helpers instead.
func (r *fakePGRows) Next() bool {
	r.index++
	return r.index < len(r.rows)
}

// Values returns the decoded row values. As with [pgx.Rows.Scan], it is an error to call
// [pgx.Rows.Values] without first calling [pgx.Rows.Next] and checking that it returned true.
func (r *fakePGRows) Values() ([]any, error) {
	return r.rows[r.index], nil
}

// Scan reads the values from the current row into dest values positionally. Dest can include pointers to core types, values
// implementing the [pgx.RowScanner] interface, and nil. Nil will skip the value entirely. It is an error to call [pgx.Rows.Scan]
// without first calling [pgx.Rows.Next] and checking that it returned true. [pgx.Rows] is automatically closed upon error.
func (r *fakePGRows) Scan(dest ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}

	if r.index >= len(r.rows) {
		return pgx.ErrNoRows
	}

	if len(dest) == 1 {
		if rc, ok := dest[0].(pgx.RowScanner); ok {
			return rc.ScanRow(r) //nolint:wrapcheck // Fake for testing purposes, propagate the error as-is.
		}
	}

	for i := range dest {
		ptr, _ := dest[i].(*any)
		*ptr = r.rows[r.index][i]
	}
	return nil
}

// Err returns any error that occurred while executing a query or reading its results. Err must be called after
// the Rows is closed (either by calling Close or by Next returning false) to check if the query was successful.
// If it is called before the Rows is closed it may return nil even if the query failed on the server.
func (r *fakePGRows) Err() error {
	return r.finalErr
}

// Close closes the rows, making the connection ready for use again.
// It is safe to call Close after rows is already closed.
func (r *fakePGRows) Close() {}

// CommandTag returns the command tag from this query. It is only available after Rows is closed.
func (r *fakePGRows) CommandTag() pgconn.CommandTag {
	return pgconn.CommandTag{} // Not implemented.
}

// RawValues returns the unparsed bytes of the row values. The returned
// data is only valid until the next Next call or the Rows is closed.
func (r *fakePGRows) RawValues() [][]byte {
	return nil // Not implemented.
}

// Conn returns the underlying *Conn on which the query was executed. This may return nil
// if Rows did not come from a *Conn (e.g., if it was created by RowsFromResultReader).
func (r *fakePGRows) Conn() *pgx.Conn {
	return nil // Not implemented.
}

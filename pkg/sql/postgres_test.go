package sql

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/daabr/versipellis/pkg/config"
)

func TestPullerConnectToPostgres(t *testing.T) {
	t.Parallel()

	// Pgxpool can parse the DSN below just fine, but we force ping failure by canceling the context.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	tests := []struct {
		name    string
		puller  *Puller
		ctx     context.Context //nolint:containedctx // Purely for test coverage purposes.
		wantErr bool
	}{
		{
			name:    "already_using_pg",
			puller:  &Puller{usingPG: true},
			ctx:     t.Context(),
			wantErr: false,
		},
		{
			name:    "invalid_dsn",
			puller:  &Puller{conn: "://invalid-dsn"},
			ctx:     t.Context(),
			wantErr: true,
		},
		{
			name:    "unreachable_host",
			puller:  &Puller{conn: "postgres://127.0.0.1:1/dbname"},
			ctx:     ctx,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if err := tt.puller.connectToPostgres(tt.ctx); (err != nil) != tt.wantErr {
				t.Errorf("Puller.connectToPostgres() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPullerStartPostgres(t *testing.T) {
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

			base, err := config.NewBasePuller(map[string]any{"type": config.PullTypeSQL, "schedule": "@once"})
			if err != nil {
				t.Fatalf("config.NewBasePuller() error: %v", err)
			}

			puller, err := NewPuller(base, map[string]any{
				"type": config.PullTypeSQL,
				"sql": map[string]any{
					"type":       DriverTypePostgres,
					"connection": "postgres://localhost:5432/dbname",
					"query":      "SELECT 1",
				},
			})
			if err != nil {
				t.Fatalf("NewPuller() error: %v", err)
			}

			puller.pgPool = fakePGPool{cols: []string{"1"}, rows: [][]any{{1}}}
			puller.usingPG = tt.usingPG
			ctx := t.Context()
			if tt.usingPG {
				ctx, puller.cancel = context.WithCancel(t.Context())
				puller.done = ctx.Done()
			}

			if ok := puller.Start(ctx); !ok {
				t.Fatal("Puller.Start() failed")
			}
			if tt.usingPG {
				go puller.scheduleNextQuery(ctx, time.Now())
			}

			<-puller.Done() // Wait for the puller's goroutine to finish its work.
		})
	}
}

func TestPullerExecutePostgresQuery(t *testing.T) {
	t.Parallel()

	base, err := config.NewBasePuller(map[string]any{"type": config.PullTypeSQL, "schedule": "@once"})
	if err != nil {
		t.Fatalf("config.NewBasePuller() error: %v", err)
	}

	puller, err := NewPuller(base, map[string]any{
		"sql": map[string]any{
			"type":       DriverTypePostgres,
			"connection": "postgres://localhost:5432/dbname",
			"query":      "SELECT 1",
		},
	})
	if err != nil {
		t.Fatalf("NewPuller() error: %v", err)
	}

	puller.pgPool = fakePGPool{cols: []string{"1"}, rows: [][]any{{1}}}
	puller.usingPG = true

	gotPayload, gotRowCount, gotOK := puller.executeQuery(t.Context())
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

func TestPullerExecutePostgresQueryErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pool    fakePGPool
		wantOK  bool
		wantRow int
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
			name: "serialize_error",
			pool: fakePGPool{
				cols: []string{"unsupported"},
				rows: [][]any{{make(chan int)}}, // Channels cannot be encoded into JSON.
			},
			wantOK: false,
		},
		{
			name: "commit_error_still_succeeds",
			pool: fakePGPool{
				cols:      []string{"id"},
				rows:      [][]any{{1}},
				commitErr: errors.New("commit error"),
			},
			wantOK:  true,
			wantRow: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := &Puller{
				Type:    config.PullTypeSQL,
				driver:  DriverTypePostgres,
				query:   "SELECT 1",
				pgPool:  tt.pool,
				usingPG: true,
			}

			payload, rowCount, ok := p.executeQuery(t.Context())
			if ok != tt.wantOK {
				t.Errorf("executeQuery() ok = %v, want %v", ok, tt.wantOK)
			}
			if rowCount != tt.wantRow {
				t.Errorf("executeQuery() rowCount = %d, want %d", rowCount, tt.wantRow)
			}
			if tt.wantOK && len(payload) == 0 {
				t.Errorf("executeQuery() len(payload) = 0, expected > 0")
			}
		})
	}
}

func TestSerializeAndClosePostgres(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		noRows bool
		want   []byte
	}{
		{
			name:   "no_rows",
			noRows: true,
		},
		{
			name: "with_rows",
			want: []byte("{\"id\":1,\"name\":\"Alice\"}\n{\"id\":2,\"name\":\"Bob\"}\n{\"id\":3,\"name\":\"Charlie\"}\n"),
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
					{3, "Charlie"},
				}
			}

			gotPayload, gotRowCount, err := serializeAndClosePostgres(rows)
			if err != nil {
				t.Fatalf("serializeAndClosePostgres() error: %v", err)
			}
			if gotRowCount != len(rows.rows) {
				t.Fatalf("serializeAndClosePostgres() row count = %d, want %d", gotRowCount, len(rows.rows))
			}
			if !reflect.DeepEqual(gotPayload, tt.want) {
				t.Fatalf("serializeAndClosePostgres(): got %q, want %q", string(gotPayload), string(tt.want))
			}
		})
	}
}

func TestSerializeAndClosePostgresErrors(t *testing.T) {
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
			name: "json_encode_error",
			rows: &fakePGRows{
				cols:  []string{"unsupported"},
				rows:  [][]any{{make(chan int)}}, // Channels cannot be encoded into JSON.
				index: -1,
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

			if _, _, err := serializeAndClosePostgres(tt.rows); err == nil {
				t.Errorf("serializeAndClosePostgres() error = nil, wantErr = true")
			}
		})
	}
}

// Simple replacement of [pgxpool.Pool].
type fakePGPool struct {
	cols []string
	rows [][]any

	beginErr  error
	queryErr  error
	commitErr error
}

func (p fakePGPool) BeginTx(_ context.Context, _ pgx.TxOptions) (pgx.Tx, error) {
	if p.beginErr != nil {
		return nil, p.beginErr
	}
	return fakePGTx{cols: p.cols, rows: p.rows, queryErr: p.queryErr, commitErr: p.commitErr}, nil
}

func (p fakePGPool) Close() {}

// Simple implementation of the [pgx.Tx] interface.
type fakePGTx struct {
	cols []string
	rows [][]any

	queryErr  error
	commitErr error
}

// Begin starts a pseudo nested transaction.
func (t fakePGTx) Begin(_ context.Context) (pgx.Tx, error) {
	return fakePGTx{cols: t.cols, rows: t.rows}, nil
}

// Commit commits the transaction if this is a real transaction or releases the savepoint if this is a pseudo nested
// transaction. Commit will return an error where errors.Is([pgx.ErrTxClosed]) is true if the Tx is already closed,
// but is otherwise safe to call multiple times. If the commit fails with a rollback status (e.g. the transaction
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
	return &fakePGRows{cols: t.cols, rows: t.rows, index: -1}, nil
}

func (t fakePGTx) CopyFrom(_ context.Context, _ pgx.Identifier, _ []string, _ pgx.CopyFromSource) (int64, error) {
	return 0, nil // Not implemented.
}

func (t fakePGTx) SendBatch(_ context.Context, _ *pgx.Batch) pgx.BatchResults {
	return nil // Not implemented.
}

func (t fakePGTx) LargeObjects() pgx.LargeObjects {
	return pgx.LargeObjects{} // Not implemented.
}

func (t fakePGTx) Prepare(_ context.Context, _, _ string) (*pgconn.StatementDescription, error) {
	return nil, nil // Not implemented.
}

func (t fakePGTx) Exec(_ context.Context, _ string, _ ...any) (commandTag pgconn.CommandTag, err error) {
	return pgconn.CommandTag{}, nil // Not implemented.
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
// if Rows did not come from a *Conn (e.g. if it was created by RowsFromResultReader).
func (r *fakePGRows) Conn() *pgx.Conn {
	return nil // Not implemented.
}

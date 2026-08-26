package sql

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	// See https://pkg.go.dev/modernc.org/sqlite#Driver.Open for connection string details.
	benchDSN   = "file:%s?_journal_mode=OFF&_synchronous=OFF"
	benchQuery = "SELECT * FROM bench;"
)

func BenchmarkCollector(b *testing.B) {
	tests := []struct {
		name     string
		rows     int
		intCols  int
		textCols int
	}{
		{"1k_rows", 1000, 5, 5},
		{"10k_rows", 10000, 5, 5},
		{"100k_rows", 100000, 5, 5},
	}
	for _, tt := range tests {
		b.Run(tt.name, func(b *testing.B) {
			dsn := setupDB(b, tt.rows, tt.intCols, tt.textCols) + "&mode=ro"
			readers := make([]*sql.DB, runtime.GOMAXPROCS(0))
			for i := range readers {
				readers[i] = openSQLiteDB(b, dsn)
				b.Cleanup(func() { readers[i].Close() })
			}
			cores := atomic.Int32{}
			ctx := b.Context()

			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				i := int(cores.Add(1)-1) % len(readers)
				// The collector's query execution timestamps are not thread-safe,
				// so we have to create a separate collector for each goroutine. Similarly,
				// each collector has its own read-only [sql.DB] to minimize SQLite locking.
				coll := &Collector{driver: DriverTypeSQLite, query: benchQuery, db: readers[i]}

				for pb.Next() {
					if !coll.executeQuery(ctx) {
						b.Error("unexpected SQL query error")
						return
					}
				}
			})

			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*tt.rows), "ns/row")
			b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "queries/sec")
			b.ReportMetric(float64(b.N*tt.rows)/b.Elapsed().Seconds()/1000.0, "rows/ms")
		})
	}
}

// setupDB is a helper function in order to close its [sql.DB] connection before the benchmark starts.
// This is important because each collector during the test has its own read-only [sql.DB] connection.
func setupDB(b *testing.B, rows, intCols, textCols int) string {
	b.Helper()

	dsn := fmt.Sprintf(benchDSN, filepath.Join(b.TempDir(), "bench_db.sqlite"))
	db := openSQLiteDB(b, dsn)
	defer db.Close()

	createTable(b, db, intCols, textCols)
	populateTable(b, db, rows, intCols, textCols)
	return dsn
}

func openSQLiteDB(b *testing.B, dsn string) *sql.DB {
	b.Helper()

	db, err := openDB(b.Context(), DriverTypeSQLite, dsn)
	if err != nil {
		b.Fatalf("openDB() error: %v", err)
	}
	return db
}

func createTable(b *testing.B, db *sql.DB, intCols, textCols int) {
	b.Helper()

	var create strings.Builder
	create.WriteString("CREATE TABLE bench (")
	for i := range intCols + textCols {
		if i > 0 {
			create.WriteString(", ")
		}
		fmt.Fprintf(&create, "col%d ", i+1)
		if i < intCols {
			create.WriteString("INTEGER")
		} else {
			create.WriteString("TEXT")
		}
	}
	create.WriteString(");")

	if _, err := db.ExecContext(b.Context(), create.String()); err != nil {
		b.Fatalf("db.ExecContext() error: %v", err)
	}
}

func populateTable(b *testing.B, db *sql.DB, rows, intCols, textCols int) {
	b.Helper()

	tx, err := db.BeginTx(b.Context(), nil)
	if err != nil {
		b.Fatalf("db.BeginTx() error: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	var insert strings.Builder
	insert.WriteString("INSERT INTO bench VALUES (")
	insert.WriteString(strings.Repeat("?, ", intCols+textCols-1))
	insert.WriteString("?);")

	stmt, err := tx.PrepareContext(b.Context(), insert.String())
	if err != nil {
		b.Fatalf("tx.PrepareContext() error: %v", err)
	}
	defer func() { _ = stmt.Close() }()

	for i := range rows {
		vals := make([]any, intCols+textCols)
		for j := range intCols {
			vals[j] = int64(1000*i + j)
		}
		for j := range textCols {
			vals[intCols+j] = fmt.Sprintf("text_%d_%d", i, j)
		}
		if _, err := stmt.ExecContext(b.Context(), vals...); err != nil {
			b.Fatalf("stmt.ExecContext() error: %v", err)
		}
	}

	if err := tx.Commit(); err != nil {
		b.Fatalf("tx.Commit() error: %v", err)
	}
}

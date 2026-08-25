package sql

import (
	"database/sql"
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	// See https://pkg.go.dev/modernc.org/sqlite#Driver.Open for connection string details.
	benchmarkDSN   = "file::memory:?cache=shared&_journal_mode=OFF&_synchronous=OFF"
	benchmarkQuery = "SELECT * FROM bench;"
)

func BenchmarkCollector(b *testing.B) {
	tests := []struct {
		name     string
		rows     int
		intCols  int
		textCols int
		serial   bool
	}{
		{"1k_rows_serial", 1000, 5, 5, true},
		{"10k_rows_serial", 10000, 5, 5, true},
		{"100k_rows_serial", 100000, 5, 5, true},

		{"1k_rows_parallel", 1000, 5, 5, false},
		{"10k_rows_parallel", 10000, 5, 5, false},
		{"100k_rows_parallel", 100000, 5, 5, false},
	}
	for _, tt := range tests {
		b.Run(tt.name, func(b *testing.B) {
			db := openInMemoryDB(b, benchmarkDSN)
			createTable(b, db, tt.intCols, tt.textCols)
			populateTable(b, db, tt.rows, tt.intCols, tt.textCols)
			fails := atomic.Int64{}
			rowTime := atomic.Int64{}

			if tt.serial {
				coll := &Collector{driver: DriverTypeSQLite, conn: benchmarkDSN, query: benchmarkQuery, db: db}
				for b.Loop() {
					if !coll.executeQuery(b.Context()) {
						fails.Add(1)
					}
					rowTime.Add(coll.prevEnd.Sub(coll.prevStart).Microseconds())
				}
				b.ReportMetric(float64(rowTime.Load())/float64(b.N*tt.rows), "μs/row")
				b.ReportMetric(float64(fails.Load()), "fails")
				return
			}

			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					// The collector's timestamps are not thread-safe, so we have to create a new one for
					// each goroutine, but empirically, this doesn't seem to affect the measurements much.
					coll := &Collector{driver: DriverTypeSQLite, conn: benchmarkDSN, query: benchmarkQuery, db: db}
					if !coll.executeQuery(b.Context()) {
						fails.Add(1)
					}
					rowTime.Add(coll.prevEnd.Sub(coll.prevStart).Microseconds())
				}
			})
			b.ReportMetric(float64(rowTime.Load())/float64(runtime.GOMAXPROCS(0)*b.N*tt.rows), "μs/rows/proc")
			b.ReportMetric(float64(fails.Load()), "fails")
		})
	}
}

func openInMemoryDB(b *testing.B, dsn string) *sql.DB {
	b.Helper()

	db, err := openDB(b.Context(), DriverTypeSQLite, dsn)
	if err != nil {
		b.Fatalf("openDB() error: %v", err)
	}
	b.Cleanup(func() {
		_ = db.Close()
	})
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
		b.Fatalf("DB.Exec(CREATE TABLE) error: %v", err)
	}
}

func populateTable(b *testing.B, db *sql.DB, rows, intCols, textCols int) {
	b.Helper()

	tx, err := db.BeginTx(b.Context(), nil)
	if err != nil {
		b.Fatalf("db.BeginTx() error: %v", err)
	}
	b.Cleanup(func() { _ = tx.Rollback() })

	var insert strings.Builder
	insert.WriteString("INSERT INTO bench VALUES (")
	insert.WriteString(strings.Repeat("?, ", intCols+textCols-1))
	insert.WriteString("?);")

	stmt, err := tx.PrepareContext(b.Context(), insert.String())
	if err != nil {
		b.Fatalf("tx.PrepareContext() error: %v", err)
	}
	b.Cleanup(func() { _ = stmt.Close() })

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

	if err := stmt.Close(); err != nil {
		b.Fatalf("stmt.Close() error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		b.Fatalf("tx.Commit() error: %v", err)
	}
}

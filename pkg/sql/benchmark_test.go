package sql

import (
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
)

func BenchmarkSQLitePuller(b *testing.B) {
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
			// See https://pkg.go.dev/modernc.org/sqlite#Driver.Open
			dsn := "file::memory:?cache=shared&_journal_mode=OFF&_synchronous=OFF"
			db := openInMemorySQLiteDB(b, dsn)
			createTable(b, db, tt.intCols, tt.textCols)
			populateTable(b, db, tt.rows, tt.intCols, tt.textCols)

			var fails, micros int64
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					puller := &Puller{driver: DriverTypeSQLite, conn: dsn, query: "SELECT * FROM bench;", db: db}
					if !puller.executeQuery(b.Context()) {
						atomic.AddInt64(&fails, 1)
					}
					atomic.AddInt64(&micros, puller.prevEnd.Sub(puller.prevStart).Microseconds())
				}
			})
			perRow := float64(micros) / float64(b.N) / float64(tt.rows)
			b.ReportMetric(perRow-2, "μs/op/row") // Account for our extra overhead that Go doesn't measure in its own ns/op.
			b.ReportMetric(float64(fails), "fails")
		})
	}
}

func openInMemorySQLiteDB(b *testing.B, dsn string) *sql.DB {
	b.Helper()

	db, err := OpenDB(b.Context(), DriverTypeSQLite, dsn)
	if err != nil {
		b.Fatalf("OpenDB() error: %v", err)
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

	var insert strings.Builder
	insert.WriteString("INSERT INTO bench VALUES (")
	insert.WriteString(strings.Repeat("?, ", intCols+textCols-1))
	insert.WriteString("?);")

	stmt, err := tx.PrepareContext(b.Context(), insert.String())
	if err != nil {
		b.Fatalf("tx.PrepareContext() error: %v", err)
	}

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

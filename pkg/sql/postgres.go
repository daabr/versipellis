package sql

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/daabr/versipellis/pkg/push"
)

// Defines the minimal interface required for [pgxpool.Pool], for testing purposes.
type pgPool interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
	Close()
}

// Never called directly, only through [Puller.Start] when the driver is PostgreSQL.
func (p *Puller) connectToPostgres(ctx context.Context) error {
	if p.usingPG {
		return nil
	}
	if p.pgPool != nil {
		p.usingPG = true
		return nil
	}

	pool, err := pgxpool.New(ctx, p.conn)
	if err != nil {
		return fmt.Errorf("connection error: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()

	err = pool.Ping(pingCtx)
	if err != nil {
		pool.Close()
		return fmt.Errorf("ping error: %w", err)
	}

	p.pgPool = pool
	p.usingPG = true
	return nil
}

// Never called directly, only through [Puller.executeQuery] when the driver is PostgreSQL.
// This means that these 2 functions do and return the same things, but do it differently.
func (p *Puller) executePostgresQuery(ctx context.Context) bool {
	tx, err := p.pgPool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		slog.Warn("failed to begin read-only SQL transaction", slog.Any("error", err), slog.String("driver", p.driver))
		return false
	}
	defer func() { _ = tx.Rollback(context.Background()) }() //nolint:contextcheck // Ctx is potentially already canceled.

	start := time.Now()
	rows, err := tx.Query(ctx, p.query)
	if err != nil {
		slog.Warn("failed to execute SQL query", slog.Any("error", err), slog.String("driver", p.driver),
			slog.Time("start_time", start), slog.Duration("duration", time.Since(start)),
		)
		return false
	}

	rowCount, err := processPostgresResults(rows)
	end := time.Now()
	ok := err == nil
	if !ok {
		slog.Warn("error while processing SQL query results", slog.Any("error", err),
			slog.String("driver", p.driver), slog.Int("successfully_processed_rows", rowCount),
		)
	} else {
		slog.Debug("SQL query completed successfully",
			slog.String("driver", p.driver), slog.Int("rows", rowCount),
			slog.Time("start_time", start), slog.Duration("exec_duration", end.Sub(start)),
		)
	}

	if ok || rowCount > 0 {
		p.prevStart = start.UTC()
		p.prevEnd = end.UTC()
	}
	return ok
}

// PostgreSQL-specific variant of [processResults]. Using [pgx]
// instead of [sql] for better performance and PostgreSQL feature support.
func processPostgresResults(rows pgx.Rows) (int, error) {
	cols := rows.FieldDescriptions()
	size := len(cols)
	vals := make([]any, size)
	ptrs := make([]any, size)
	for i := range size {
		ptrs[i] = &vals[i]
	}

	rowCount := 0
	_, err := pgx.ForEachRow(rows, ptrs, func() error { // [pgx.ForEachRow] closes [pgx.Rows] automatically.
		row := make(map[string]any, size)
		for i, col := range cols {
			row[col.Name] = vals[i]
		}
		if err := push.Stdout(row); err != nil {
			return fmt.Errorf("failed to process row %d: %w", rowCount+1, err)
		}
		rowCount++
		return nil
	})
	if err != nil {
		return rowCount, fmt.Errorf("row processing error: %w", err)
	}
	return rowCount, nil
}

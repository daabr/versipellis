package sql

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/daabr/versipellis/pkg/config"
)

// Defines the minimal interface required for [pgxpool.Pool], for testing purposes.
type pgPool interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
	Close()
}

// Never called directly, only through [Collector.Start] when the driver is PostgreSQL.
func (c *Collector) connectToPostgres(ctx context.Context) error {
	if c.usingPG {
		return nil
	}
	if c.pgPool != nil {
		c.usingPG = true
		return nil
	}

	pool, err := pgxpool.New(ctx, c.conn)
	if err != nil {
		return fmt.Errorf("connection error: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()

	if err = pool.Ping(pingCtx); err != nil {
		pool.Close()
		return fmt.Errorf("ping error: %w", err)
	}

	c.pgPool = pool
	c.usingPG = true
	return nil
}

// Never called directly, only through [Collector.executeQuery] when the driver is PostgreSQL.
// This means that these 2 functions do and return the same things, but in a different way.
func (c *Collector) executePostgresQuery(execCtx, queryCtx context.Context, sender config.Sender) bool {
	tx, err := c.pgPool.BeginTx(queryCtx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		slog.Warn("failed to begin read-only SQL transaction", slog.Any("error", err),
			slog.String("driver", c.driver), slog.String("name", c.Name),
		)
		return false
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(execCtx)) }()

	start := time.Now()
	rows, err := tx.Query(queryCtx, c.query)
	if err != nil {
		slog.Warn("failed to execute SQL query", slog.Any("error", err), slog.String("driver", c.driver),
			slog.String("name", c.Name), slog.Time("start_time", start), slog.Duration("duration", time.Since(start)),
		)
		return false
	}

	rowCount, err := processPostgresResults(execCtx, rows, sender)
	end := time.Now()
	ok := err == nil
	if !ok {
		slog.Warn("error while processing SQL query results", slog.Any("error", err), slog.String("driver", c.driver),
			slog.String("name", c.Name), slog.Int("successfully_processed_rows", rowCount),
		)
	} else {
		slog.Debug("SQL query completed successfully", slog.String("driver", c.driver), slog.String("name", c.Name),
			slog.Int("rows", rowCount), slog.Time("start_time", start), slog.Duration("duration", end.Sub(start)),
		)
	}

	if ok || rowCount > 0 {
		c.checkpointMu.Lock()
		if start.UTC().After(c.prevStart) {
			c.prevStart = start.UTC()
			c.prevEnd = end.UTC()
		}
		c.checkpointMu.Unlock()
	}
	return ok
}

// PostgreSQL-specific variant of [processResults]. Using [pgx]
// instead of [sql] for better performance and PostgreSQL feature support.
func processPostgresResults(ctx context.Context, rows pgx.Rows, sender config.Sender) (int, error) {
	cols := rows.FieldDescriptions()
	size := len(cols)
	vals := make([]any, size)
	ptrs := make([]any, size)
	for i := range size {
		ptrs[i] = &vals[i]
	}

	rowCount := 0
	_, err := pgx.ForEachRow(rows, ptrs, func() error { // [pgx.ForEachRow] closes [pgx.Rows] automatically.
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("query result processing canceled: %w", err)
		}
		row := make(map[string]any, size)
		for i, col := range cols {
			row[col.Name] = vals[i]
		}
		if sender != nil {
			sender(ctx, row) // Returns quickly (usually asynchronous internally).
		}
		rowCount++
		return nil
	})
	if err != nil {
		return rowCount, fmt.Errorf("row processing error: %w", err)
	}
	return rowCount, nil
}

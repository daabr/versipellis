package sql

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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
func (p *Puller) executePostgresQuery(ctx context.Context) ([]byte, int, bool) {
	tx, err := p.pgPool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		slog.Warn("failed to begin read-only SQL transaction", slog.Any("error", err), slog.String("driver", p.driver))
		return nil, 0, false
	}
	defer func() { _ = tx.Rollback(context.Background()) }() //nolint:contextcheck // Ctx is already canceled.

	start := time.Now().UTC()
	rows, err := tx.Query(ctx, p.query)
	if err != nil {
		slog.Warn("failed to execute SQL query", slog.Any("error", err), slog.String("driver", p.driver))
		return nil, 0, false
	}
	payload, rowCount, err := serializeAndClosePostgres(rows)
	if err != nil {
		slog.Warn("failed to serialize SQL query results", slog.Any("error", err), slog.String("driver", p.driver))
		return nil, 0, false
	}
	end := time.Now().UTC()
	if err := tx.Commit(ctx); err != nil {
		slog.Info("failed to commit read-only SQL transaction", slog.Any("error", err), slog.String("driver", p.driver))
		// Don't abort - we already have the payload, and the transaction is read-only anyway.
	}

	p.prevStart = start
	p.prevEnd = end
	return payload, rowCount, true
}

// PostgreSQL-specific variant of [serializeAndClose]. Using [pgx]
// instead of [sql] for better performance and feature support.
func serializeAndClosePostgres(rows pgx.Rows) ([]byte, int, error) {
	cols := rows.FieldDescriptions()
	size := len(cols)
	vals := make([]any, size)
	ptrs := make([]any, size)
	for i := range size {
		ptrs[i] = &vals[i]
	}

	var buf bytes.Buffer
	payload := json.NewEncoder(&buf)
	payload.SetEscapeHTML(false) // Passing raw data, not rendering it, so not altering it either.
	rowCount := 0

	_, err := pgx.ForEachRow(rows, ptrs, func() error { // [pgx.ForEachRow] closes [pgx.Rows] automatically.
		row := make(map[string]any, size)
		for i, col := range cols {
			row[col.Name] = vals[i]
		}
		if err := payload.Encode(row); err != nil {
			return fmt.Errorf("failed to encode row %d into JSON: %w", rowCount+1, err)
		}
		rowCount++
		return nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("failed to process after %d rows: %w", rowCount, err)
	}
	if rowCount == 0 {
		return nil, 0, nil
	}
	return buf.Bytes(), rowCount, nil
}

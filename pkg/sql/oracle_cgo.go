//go:build cgo

package sql

import (
	"context"
	"database/sql"

	// Import "thick mode" driver for runtime registration in [sql].
	// Requires cgo during builds, and Oracle Instant Client during runtime.
	_ "github.com/godror/godror"
)

func connectToOracle(ctx context.Context, conn string) (*sql.DB, error) {
	return openDB(ctx, "godror", conn)
}

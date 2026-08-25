//go:build cgo && odbc

package sql

import (
	"context"
	"database/sql"

	// Import driver for runtime registration in [sql].
	// Requires cgo and unixODBC headers (sql.h) to be installed.
	// Build with: "CGO_ENABLED=1 go build -tags odbc".
	_ "github.com/alexbrainman/odbc"
)

func connectToODBC(ctx context.Context, conn string) (*sql.DB, error) {
	return openDB(ctx, DriverTypeODBC, conn)
}

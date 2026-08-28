//go:build !cgo

package sql

import (
	"context"
	"database/sql"
	"errors"
)

func connectToOracle(_ context.Context, _ string) (*sql.DB, error) {
	return nil, errors.New("this executable was built without CGO enabled, so Oracle Database support is not available")
}

//go:build !cgo || !odbc

package sql

import (
	"context"
	"database/sql"
	"errors"
)

func connectToODBC(_ context.Context, _ string) (*sql.DB, error) {
	return nil, errors.New("this executable was built without CGO enabled or without the ODBC tag")
}

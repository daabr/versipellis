package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	// Import drivers for runtime registration in [sql].
	_ "github.com/SAP/go-hdb/driver"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/microsoft/go-mssqldb"
	_ "github.com/snowflakedb/gosnowflake/v2"
	_ "modernc.org/sqlite"

	"github.com/daabr/versipellis/pkg/config"
	"github.com/daabr/versipellis/pkg/push"
)

// DriverType* constants represent all the available SQL database drivers for configurations in the TOML file.
const (
	DriverTypeCockroachDB = "cockroachdb"
	DriverTypeMariaDB     = "mariadb"
	DriverTypeMSSQL       = "mssql"
	DriverTypeMySQL       = "mysql"
	DriverTypePostgres    = "postgres"
	DriverTypePostgreSQL  = "postgresql"
	DriverTypeSAPHANA     = "sap_hana"
	DriverTypeSnowflake   = "snowflake"
	DriverTypeSQLite      = "sqlite"
	DriverTypeSQLServer   = "sqlserver"
)

var validDriverTypes = []string{
	DriverTypeCockroachDB,
	DriverTypeMariaDB,
	DriverTypeMSSQL,
	DriverTypeMySQL,
	DriverTypePostgres,
	DriverTypePostgreSQL,
	DriverTypeSAPHANA,
	DriverTypeSnowflake,
	DriverTypeSQLite,
	DriverTypeSQLServer,
}

const (
	pingTimeout  = 5 * time.Second
	closeTimeout = 5 * time.Second

	defaultQueryTimeout = time.Minute
)

// Puller contains all the configuration and state details
// for pulling data from SQL-based relational databases.
type Puller struct {
	config.BasePuller

	driver  string
	conn    string
	query   string
	timeout time.Duration

	// Mutually-exclusive connections, depending on the driver type.
	db      *sql.DB
	pgPool  pgPool
	usingPG bool

	// For checkpointing: timestamps of the last (at least partially) successful query.
	prevStart time.Time
	prevEnd   time.Time

	cancel    context.CancelFunc
	done      <-chan struct{}
	closeOnce sync.Once
}

// NewPuller creates a new [Puller] from the given configuration, which was read from
// a TOML file. It checks the details and returns an error if any of them is invalid.
func NewPuller(base *config.BasePuller, pullCfg map[string]any) (*Puller, error) {
	switch {
	case base == nil:
		return nil, errors.New("base puller cannot be nil")
	case base.Type != config.PullTypeSQL:
		return nil, fmt.Errorf("this puller type is %q, but must be %q", base.Type, config.PullTypeSQL)
	case pullCfg == nil || pullCfg["sql"] == nil:
		return nil, errors.New("[pull.sql] TOML config section is missing")
	}

	sqlCfg, ok := pullCfg["sql"].(map[string]any)
	if !ok {
		return nil, errors.New("[pull.sql] isn't a valid TOML config section")
	}
	query, err := checkQuery(loadQuery(sqlCfg))
	if err != nil {
		return nil, err
	}
	timeout, err := time.ParseDuration(config.Value(sqlCfg, "timeout", defaultQueryTimeout.String()))
	if err != nil {
		return nil, fmt.Errorf("invalid query timeout duration: %w", err)
	}

	p := &Puller{
		BasePuller: *base,
		driver:     strings.ToLower(strings.TrimSpace(config.Value(sqlCfg, "type", ""))),
		conn:       config.Value(sqlCfg, "connection", ""),
		query:      query,
		timeout:    timeout,
	}

	switch {
	case !slices.Contains(validDriverTypes, p.driver):
		return nil, fmt.Errorf("unrecognized SQL driver type %q", p.driver)
	case p.conn == "":
		return nil, fmt.Errorf("SQL puller config for %q must have a database connection string", p.driver)
	default:
		return p, nil
	}
}

func loadQuery(cfg map[string]any) (string, error) {
	query := strings.TrimSpace(config.Value(cfg, "query", ""))
	path := strings.TrimSpace(config.Value(cfg, "query_file", ""))

	switch {
	case query == "" && path == "":
		return "", errors.New("no SQL query provided")
	case query != "" && path != "":
		return "", errors.New("both SQL query string and SQL query file provided, specify only one")
	case query != "" && path == "":
		return query, nil
	}

	queryFromFile, err := os.ReadFile(path) //gosec:disable G304 // Path is configurable by design.
	if err != nil {
		return "", err
	}
	query = strings.TrimSpace(string(queryFromFile))
	if query == "" {
		return "", errors.New("specified SQL query file is empty: " + path)
	}
	return query, nil
}

func checkQuery(query string, err error) (string, error) {
	if err != nil {
		return "", err
	}

	// No-op for now, but we can add more checks here in the future if needed.
	return query, nil
}

// Start connects to the configured SQL-based relational database and starts pulling data from it.
// This function returns immediately, and the puller runs asynchronously in the background.
// This function is idempotent: only the first call will actually start a goroutine. However,
// it is not meant to be safe for concurrency, initialize pullers only in the main goroutine.
func (p *Puller) Start(ctx context.Context) bool {
	if p == nil {
		slog.Error("SQL puller is misconfigured")
		return false
	}
	if p.cancel != nil {
		slog.Error("SQL puller already started") // This is a programming error...
		return true                              // ...But a harmless one.
	}

	var db *sql.DB
	var err error
	switch p.driver {
	case DriverTypeCockroachDB, DriverTypePostgres, DriverTypePostgreSQL:
		err = p.connectToPostgres(ctx)
	case DriverTypeMariaDB:
		db, err = OpenDB(ctx, DriverTypeMySQL, p.conn)
	case DriverTypeMSSQL:
		db, err = OpenDB(ctx, DriverTypeSQLServer, p.conn)
	case DriverTypeSAPHANA:
		db, err = OpenDB(ctx, "hdb", p.conn)
	default:
		db, err = OpenDB(ctx, p.driver, p.conn)
	}
	if err != nil {
		slog.Warn("failed to connect to SQL-based database", slog.Any("err", err), slog.String("driver", p.driver))
		return false
	}

	p.db = db
	ctx, p.cancel = context.WithCancel(ctx)
	p.done = ctx.Done()

	slog.Info("starting to pull data with SQL queries", slog.String("driver", p.driver), slog.String("schedule", p.Cronspec))
	go p.scheduleNextQuery(ctx, time.Now())
	return true
}

// OpenDB opens a connection to a specific database, and pings it to ensure it's reachable.
func OpenDB(ctx context.Context, driver, conn string) (*sql.DB, error) {
	db, err := sql.Open(driver, conn)
	if err != nil {
		return nil, fmt.Errorf("connection error: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()

	err = db.PingContext(pingCtx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping error: %w", err)
	}

	return db, nil
}

func (p *Puller) scheduleNextQuery(ctx context.Context, prev time.Time) {
	defer p.Close()
	for {
		nextStart := p.Schedule.Next(prev)
		if nextStart.IsZero() {
			if p.Schedule.RunsOnlyOnce() {
				slog.Info("SQL puller finished one-time execution", slog.String("driver", p.driver))
			} else {
				slog.Error("SQL puller stopped due to scheduler bug - no next instance for: " + p.Cronspec)
			}
			return
		}
		if now := time.Now(); !p.Schedule.RunsOnlyOnce() && now.After(nextStart) {
			slog.Warn("SQL puller is behind schedule, skipping missed execution",
				slog.String("driver", p.driver), slog.Time("skipped", nextStart),
				slog.Duration("gap", now.Sub(nextStart)),
			)
			prev = nextStart
			continue
		}

		timer := time.NewTimer(time.Until(nextStart))
		select {
		case <-ctx.Done():
			timer.Stop() // No need to drain since Go 1.23.
			return
		case <-timer.C:
			p.executeQuery(ctx)
			prev = nextStart
		}
	}
}

// This is never called directly, only through [Puller.scheduleNextQuery]. Therefore, it's safe to assume
// that either [Puller.db] or [Puller.pgPool] are non-nil, given that [Puller.Start] had to succeed first.
func (p *Puller) executeQuery(ctx context.Context) bool {
	queryCtx := ctx
	var cancel context.CancelFunc
	if p.timeout > 0 {
		queryCtx, cancel = context.WithTimeout(ctx, p.timeout)
	}
	if cancel != nil {
		defer cancel()
	}

	// [Puller.db] and [Puller.pgPool]/[Puller.usingPG] are mutually exclusive,
	// so if the latter is non-nil we have to use it instead of the former.
	if p.usingPG {
		return p.executePostgresQuery(queryCtx)
	}

	tx, err := p.db.BeginTx(queryCtx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		slog.Warn("failed to begin read-only SQL transaction", slog.Any("error", err), slog.String("driver", p.driver))
		return false
	}
	defer func() { _ = tx.Rollback() }()

	start := time.Now()
	rows, err := tx.QueryContext(queryCtx, p.query)
	if err != nil {
		slog.Warn("failed to execute SQL query", slog.Any("error", err), slog.String("driver", p.driver),
			slog.Time("start_time", start), slog.Duration("duration", time.Since(start)),
		)
		return false
	}
	defer rows.Close()

	rowCount, err := processResults(rows, 1)
	end := time.Now()
	ok := err == nil
	if !ok {
		slog.Warn("error while processing SQL query results", slog.Any("error", err),
			slog.String("driver", p.driver), slog.Int("successfully_processed_rows", rowCount),
		)
	} else {
		stats := p.db.Stats()
		slog.Debug("SQL query completed successfully",
			slog.String("driver", p.driver), slog.Int("rows", rowCount),
			slog.Time("start_time", start), slog.Duration("exec_duration", end.Sub(start)),
			slog.Int("in_use_conns", stats.InUse), slog.Int("idle_conns", stats.Idle),
		)
	}

	if ok || rowCount > 0 {
		p.prevStart = start.UTC()
		p.prevEnd = end.UTC()
	}
	return ok
}

func processResults(rows *sql.Rows, resultSet int) (int, error) {
	cols, err := rows.Columns()
	if err != nil {
		return 0, fmt.Errorf("failed to read SQL column names in result-set %d: %w", resultSet, err)
	}

	rowCount := 0
	for rows.Next() {
		row, err := scanRow(rows, cols)
		if err != nil {
			return 0, fmt.Errorf("failed to scan row %d in result-set %d: %w", rowCount+1, resultSet, err)
		}
		if err := push.Stdout(row); err != nil {
			return rowCount, fmt.Errorf("failed to process row %d in result-set %d: %w", rowCount+1, resultSet, err)
		}
		rowCount++
	}
	if err := rows.Err(); err != nil {
		return rowCount, fmt.Errorf("row iteration error: %w", err)
	}

	// Support multiple result-sets for multiple statements, using recursion.
	if rows.NextResultSet() {
		nextRowCount, err := processResults(rows, resultSet+1)
		rowCount += nextRowCount
		if err != nil {
			return rowCount, err
		}
	}
	if err := rows.Err(); err != nil {
		return rowCount, fmt.Errorf("row-set iteration error: %w", err)
	}

	return rowCount, nil
}

func scanRow(rows *sql.Rows, cols []string) (map[string]any, error) {
	size := len(cols)
	vals := make([]any, size)
	ptrs := make([]any, size)
	for i := range size {
		ptrs[i] = &vals[i]
	}

	if err := rows.Scan(ptrs...); err != nil {
		return nil, fmt.Errorf("row scanning error: %w", err)
	}

	row := make(map[string]any, size)
	for i, col := range cols {
		row[col] = vals[i]
	}
	return row, nil
}

// Done returns a channel that signals and gets closed when the puller has finished its work and is no longer running.
func (p *Puller) Done() <-chan struct{} {
	return p.done
}

// Close closes the database connection pool, prevents new queries from starting, and waits for
// all queries that have started processing on the server to finish (up to a point). It then
// signals through the [Puller.Done] channel that the puller isn't executing queries anymore.
// It is safe (though useless) to call even if [Puller.Start] was never called, but either
// way it is meant to be called only in the same goroutine as [Puller.scheduleNextQuery].
func (p *Puller) Close() {
	p.closeOnce.Do(func() {
		if p.cancel != nil {
			defer p.cancel()
		}

		if p.db == nil && !p.usingPG {
			return
		}

		done := make(chan struct{})
		go func() {
			defer close(done)

			if p.db != nil {
				_ = p.db.Close()
			}
			if p.usingPG {
				p.pgPool.Close()
			}
		}()

		timer := time.NewTimer(closeTimeout)
		select {
		case <-done:
			timer.Stop() // No need to drain since Go 1.23.
		case <-timer.C:
			slog.Warn("closing SQL connection pool forcefully",
				slog.String("driver", p.driver), slog.Duration("timeout", closeTimeout),
			)
		}
	})
}

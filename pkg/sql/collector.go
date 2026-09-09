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
	"github.com/daabr/versipellis/pkg/dest"
)

// DriverType* constants represent all the available SQL database drivers for configurations in the TOML file.
const (
	DriverTypeCockroachDB = "cockroachdb"
	DriverTypeMariaDB     = "mariadb"
	DriverTypeMSSQL       = "mssql"
	DriverTypeMySQL       = "mysql"
	DriverTypeODBC        = "odbc"
	DriverTypeOracle      = "oracle"
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
	DriverTypeODBC,
	DriverTypeOracle,
	DriverTypePostgres,
	DriverTypePostgreSQL,
	DriverTypeSAPHANA,
	DriverTypeSnowflake,
	DriverTypeSQLite,
	DriverTypeSQLServer,
}

const (
	closeTimeout = 5 * time.Second
	pingTimeout  = 5 * time.Second

	defaultQueryTimeout = time.Minute
)

// Collector contains all the configuration and state details for querying SQL-based databases.
type Collector struct {
	config.BaseCollector

	driver  string
	conn    string
	query   string
	timeout time.Duration

	// Mutually-exclusive connections, depending on the driver type.
	db      *sql.DB
	pgPool  pgPool
	usingPG bool

	// For checkpointing: timestamps of the last (at least partially) successful query.
	checkpointMu sync.Mutex
	prevStart    time.Time
	prevEnd      time.Time

	cancelSched context.CancelFunc
	cancelExec  context.CancelFunc
	closeDone   chan struct{}
	inProgress  sync.WaitGroup
	closeOnce   sync.Once
}

// Base returns a copy of the collector's static and generic configuration details.
// Specifically, it does not copy references such as the Schedule and Sender fields.
func (c *Collector) Base() *config.BaseCollector {
	return &config.BaseCollector{
		Type:        c.Type,
		Name:        c.Name,
		Cronspec:    c.Cronspec,
		Trigger:     c.Trigger,
		Concurrency: c.Concurrency,
		Destination: c.Destination,
	}
}

// NewCollector creates a new [Collector] from the given configuration, which was read from
// a TOML file. It checks the details and returns an error if any of them is invalid.
func NewCollector(base *config.BaseCollector, cfg map[string]any) (*Collector, error) {
	switch {
	case base == nil:
		return nil, errors.New("base collector cannot be nil")
	case base.Type != config.CollectorTypeSQL:
		return nil, fmt.Errorf("collector type is %q, but must be %q", base.Type, config.CollectorTypeSQL)
	case cfg == nil || cfg["sql"] == nil:
		return nil, errors.New("[collector.sql] TOML config section is missing")
	}

	sqlCfg, ok := cfg["sql"].(map[string]any)
	if !ok {
		return nil, errors.New("[collector.sql] isn't a valid TOML config section")
	}
	query, err := checkQuery(loadQuery(sqlCfg))
	if err != nil {
		return nil, err
	}
	timeout, err := time.ParseDuration(config.Value(sqlCfg, "timeout", defaultQueryTimeout.String()))
	if err != nil {
		return nil, fmt.Errorf("invalid query timeout duration: %w", err)
	}

	c := &Collector{
		BaseCollector: *base,
		driver:        strings.ToLower(strings.TrimSpace(config.Value(sqlCfg, "type", ""))),
		conn:          config.Value(sqlCfg, "connection", ""),
		query:         query,
		timeout:       timeout,
	}

	switch {
	case !slices.Contains(validDriverTypes, c.driver):
		return nil, fmt.Errorf("unrecognized SQL driver type %q", c.driver)
	case c.conn == "":
		return nil, fmt.Errorf("SQL collector config for %q must have a database connection string", c.driver)
	default:
		return c, nil
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

// Start connects to the configured SQL-based database and starts sending queries to it. This function
// returns immediately, and the collector runs asynchronously in the background. This function is
// idempotent: only the first call will actually start a goroutine. However, it is not meant to be
// safe for concurrency, initialize collectors only in the main goroutine. Lastly, the collector
// obeys the cancellation of the provided context, but with a grace period of [Collector.timeout].
func (c *Collector) Start(ctx context.Context) bool {
	if c == nil {
		slog.Error("SQL collector is misconfigured")
		return false
	}
	if c.cancelSched != nil {
		slog.Error("SQL collector already started") // This is a programming error...
		return true                                 // ...But a harmless one.
	}

	var err error
	switch c.driver {
	case DriverTypeCockroachDB, DriverTypePostgres, DriverTypePostgreSQL:
		err = c.connectToPostgres(ctx)
	case DriverTypeMariaDB:
		c.db, err = openDB(ctx, DriverTypeMySQL, c.conn)
	case DriverTypeMSSQL:
		c.db, err = openDB(ctx, DriverTypeSQLServer, c.conn)
	case DriverTypeODBC:
		c.db, err = connectToODBC(ctx, c.conn)
	case DriverTypeOracle:
		c.db, err = connectToOracle(ctx, c.conn)
	case DriverTypeSAPHANA:
		c.db, err = openDB(ctx, "hdb", c.conn)
	default:
		c.db, err = openDB(ctx, c.driver, c.conn)
	}
	if err != nil {
		slog.Warn("failed to connect to SQL-based database", slog.Any("error", err),
			slog.String("driver", c.driver), slog.String("name", c.Name),
		)
		return false
	}

	var schedCtx, execCtx context.Context
	schedCtx, c.cancelSched = context.WithCancel(ctx)
	execCtx, c.cancelExec = context.WithCancel(context.WithoutCancel(ctx))
	c.closeDone = make(chan struct{})

	slog.Info("starting to execute SQL queries", slog.String("driver", c.driver),
		slog.String("name", c.Name), slog.String("schedule", c.Cronspec),
	)
	go c.scheduleNext(schedCtx, execCtx, time.Now())
	return true
}

// openDB opens a connection to a specific database, and pings it to ensure it's actually reachable.
func openDB(ctx context.Context, driver, conn string) (*sql.DB, error) {
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

func (c *Collector) scheduleNext(ctx, execCtx context.Context, prev time.Time) {
	sem := make(chan struct{}, max(c.Concurrency, 1))
	defer c.Close()

	for {
		nextStart := c.Schedule.Next(prev)
		if nextStart.IsZero() {
			if c.Schedule.RunsOnlyOnce() {
				done := make(chan struct{})
				go func() {
					defer close(done)
					c.inProgress.Wait()
				}()
				select {
				case <-ctx.Done():
				case <-done:
				}
				slog.Info("SQL collector finished one-time execution",
					slog.String("driver", c.driver), slog.String("name", c.Name),
				)
			} else {
				slog.Error("SQL collector stopped due to scheduler bug - no next instance",
					slog.String("driver", c.driver), slog.String("name", c.Name), slog.String("schedule", c.Cronspec),
				)
			}
			return
		}
		if now := time.Now(); !c.Schedule.RunsOnlyOnce() && now.After(nextStart) {
			slog.Warn("SQL collector is behind schedule, skipping missed execution",
				slog.String("driver", c.driver), slog.String("name", c.Name),
				slog.Time("skipped", nextStart), slog.Duration("gap", now.Sub(nextStart)),
			)
			prev = nextStart
			continue
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(nextStart)):
			c.checkConcurrency(ctx, execCtx, sem, nextStart)
			prev = nextStart
		}
	}
}

func (c *Collector) checkConcurrency(ctx, execCtx context.Context, sem chan struct{}, scheduled time.Time) {
	if ctx.Err() != nil { // Instead of ctx.Done() in the select block below - to check ctx before sem.
		return
	}
	select {
	case sem <- struct{}{}:
		c.inProgress.Go(func() {
			defer func() { <-sem }()
			c.executeQuery(execCtx)
		})
	default:
		slog.Warn("SQL collector is at its concurrency limit, skipping query",
			slog.String("driver", c.driver), slog.String("name", c.Name),
			slog.Int("limit", cap(sem)), slog.Time("skipped", scheduled),
		)
	}
}

// executeQuery shouldn't be called directly, only through [Collector.scheduleNext]. Therefore, it's safe to assume
// that either [Collector.db] or [Collector.pgPool] are non-nil, given that [Collector.Start] had to succeed first.
// The provided ctx is an execution context (execCtx) detached from parent cancellation, allowing in-flight queries
// to finish within [Collector.timeout] during graceful shutdown before being forcefully canceled.
func (c *Collector) executeQuery(ctx context.Context) bool {
	var cancel context.CancelFunc
	if c.timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
	}
	if cancel != nil {
		defer cancel()
	}

	// [Collector.db] and [Collector.pgPool]/[Collector.usingPG] are mutually exclusive,
	// so if the latter is non-nil we have to use it instead of the former.
	if c.usingPG {
		return c.executePostgresQuery(ctx, c.Sender)
	}

	tx, err := c.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		slog.Warn("failed to begin read-only SQL transaction", slog.Any("error", err),
			slog.String("driver", c.driver), slog.String("name", c.Name),
		)
		return false
	}
	defer func() { _ = tx.Rollback() }()

	start := time.Now()
	rows, err := tx.QueryContext(ctx, c.query)
	if err != nil {
		slog.Warn("failed to execute SQL query", slog.Any("error", err),
			slog.String("driver", c.driver), slog.String("name", c.Name),
			slog.Time("start_time", start), slog.Duration("duration", time.Since(start)),
		)
		return false
	}
	defer rows.Close()

	rowCount, err := processResults(ctx, rows, c.Sender, 1)
	end := time.Now()
	ok := err == nil
	if !ok {
		slog.Warn("error while processing SQL query results", slog.Any("error", err),
			slog.String("driver", c.driver), slog.String("name", c.Name),
			slog.Int("successfully_processed_rows", rowCount),
		)
	} else {
		stats := c.db.Stats()
		slog.Debug("SQL query execution completed successfully",
			slog.String("driver", c.driver), slog.String("name", c.Name), slog.Int("rows", rowCount),
			slog.Time("start_time", start), slog.Duration("exec_duration", end.Sub(start)),
			slog.Int("in_use_conns", stats.InUse), slog.Int("idle_conns", stats.Idle),
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

func processResults(ctx context.Context, rows *sql.Rows, sender dest.Sender, resultSet int) (int, error) {
	cols, err := rows.Columns()
	if err != nil {
		return 0, fmt.Errorf("failed to read SQL column names in result-set %d: %w", resultSet, err)
	}

	rowCount := 0
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return rowCount, fmt.Errorf("query result processing canceled: %w", err)
		}
		row, err := scanRow(rows, cols)
		if err != nil {
			return rowCount, fmt.Errorf("failed to scan row %d in result-set %d: %w", rowCount+1, resultSet, err)
		}
		if sender != nil {
			if err := sender(ctx, row); err != nil {
				return rowCount, fmt.Errorf("failed to process row %d in result-set %d: %w", rowCount+1, resultSet, err)
			}
		}
		rowCount++
	}
	if err := rows.Err(); err != nil {
		return rowCount, fmt.Errorf("row iteration error: %w", err)
	}

	// Support multiple result-sets for multiple statements, using recursion.
	if rows.NextResultSet() {
		nextRowCount, err := processResults(ctx, rows, sender, resultSet+1)
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

// Done returns a channel that closes when shutdown completes or its grace period expires.
func (c *Collector) Done() <-chan struct{} {
	return c.closeDone
}

// Close waits (up to [Collector.timeout]) for queries that are in progress to finish, after new ones are no longer being
// scheduled. It is safe to call multiple times, even if [Collector.Start] wasn't called, but it's meant to be called only
// at the end of the [Collector.scheduleNext] goroutine. If there are still pending queries after the timeout, the collector
// will forcefully close their connections. It then signals through the [Collector.Done] channel that it's ready to shut down.
func (c *Collector) Close() {
	if c == nil || c.cancelSched == nil {
		return
	}

	c.closeOnce.Do(func() {
		c.cancelSched()

		done := make(chan struct{})
		go func() {
			defer close(done)

			c.inProgress.Wait()

			if c.db != nil {
				_ = c.db.Close()
			}
			if c.usingPG && c.pgPool != nil {
				c.pgPool.Close()
			}
		}()

		timeout := c.timeout
		if timeout <= 0 || timeout > closeTimeout {
			timeout = closeTimeout // Ensure the timeout is within acceptable bounds.
		}

		select {
		case <-done:
			// All done.
		case <-time.After(timeout):
			slog.Warn("closing SQL connection pool forcefully", slog.String("driver", c.driver),
				slog.String("name", c.Name), slog.Duration("timeout", timeout),
			)
			if c.cancelExec != nil {
				c.cancelExec()
			}
		}

		if c.closeDone != nil {
			close(c.closeDone)
		}
	})
}

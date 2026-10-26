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

	"github.com/daabr/versipellis/pkg/codec"
	"github.com/daabr/versipellis/pkg/config"
	"github.com/daabr/versipellis/pkg/flow"
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
	// CloseTimeout is the maximum amount of time to wait for SQL queries to finish before forcefully
	// canceling them. This is used during shutdown, so it's intentionally short and not configurable.
	CloseTimeout = 5 * time.Second

	// AbortTimeout is the maximum amount of time to wait for a SQL query to be aborted after [CloseTimeout].
	AbortTimeout = time.Second

	pingTimeout = 5 * time.Second

	defaultQueryTimeout = time.Minute
)

var defaultBatchLimits = flow.Limits{MaxItems: 500, Window: time.Second}

// Collector contains all the configuration and state details for querying SQL-based databases.
type Collector struct {
	config.BaseCollector

	driver  string
	conn    string
	query   string
	timeout time.Duration
	format  config.Format

	// Mutually-exclusive connections, depending on the driver type.
	db      *sql.DB
	pgPool  pgPool
	usingPG bool

	state config.State
	batch *flow.Batcher[flow.Row]

	// For checkpointing: timestamps of the last (at least partially) successful query.
	checkpointMu sync.Mutex
	prevStart    time.Time
	prevEnd      time.Time
}

// Base returns a copy of the collector's static and generic configuration details.
// Specifically, it does not copy references such as the Schedule and Send fields.
func (c *Collector) Base() *config.BaseCollector {
	return &config.BaseCollector{
		Type:        c.Type,
		Name:        c.Name,
		Destination: c.Destination,

		Cronspec:    c.Cronspec,
		Trigger:     c.Trigger,
		Concurrency: c.Concurrency,
	}
}

// NewCollector creates a new [Collector] from the given configuration, which was
// read from a TOML file. It checks the details and returns an error if any of them
// is semantically invalid, but the caller is responsible for providing usable input.
func NewCollector(base *config.BaseCollector, cfg map[string]any) (*Collector, error) {
	c := &Collector{
		BaseCollector: *base,
		driver:        strings.ToLower(strings.TrimSpace(config.Value(cfg, "type", ""))),
		conn:          config.Value(cfg, "connection", ""),
	}

	switch {
	case !slices.Contains(validDriverTypes, c.driver):
		return nil, fmt.Errorf("unrecognized SQL driver type %q", c.driver)
	case c.conn == "":
		return nil, errors.New("connection field required but not found")
	}

	var err error
	var limits flow.Limits
	if c.query, err = checkQuery(loadQuery(cfg)); err != nil {
		return nil, err
	}
	if c.timeout, err = time.ParseDuration(config.Value(cfg, "timeout", defaultQueryTimeout.String())); err != nil {
		return nil, fmt.Errorf("invalid query timeout duration: %w", err)
	}

	if c.format, err = codec.ParseStructuredFormat(cfg); err != nil {
		return nil, fmt.Errorf("format setting: %w", err)
	}
	if c.format == codec.FormatDefault {
		c.format = codec.FormatNDJSON
	}

	if limits, err = config.BatchLimits(cfg["batch"], c.Name, defaultBatchLimits); err != nil {
		return nil, fmt.Errorf("SQL row batching limits: %w", err)
	}
	if c.Destination == config.SenderTypeDiscard || c.Destination == config.SenderTypeNone {
		limits = flow.Limits{} // No point in enabling batching if all the data will be discarded anyway.
	}
	opts := new(flow.Options[flow.Row]{Guard: c.guard})
	if c.batch, err = flow.NewBatcher(limits, c.sendRows, opts); err != nil {
		return nil, fmt.Errorf("SQL row batching: %w", err)
	}

	return c, nil
}

func loadQuery(cfg map[string]any) (string, error) {
	query := strings.TrimSpace(config.Value(cfg, "query", ""))
	path := strings.TrimSpace(config.Value(cfg, "query_file", ""))

	switch {
	case query == "" && path == "":
		return "", errors.New("query or query_file field required but not found")
	case query != "" && path != "":
		return "", errors.New("both query and query_file fields provided, specify only one")
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

// guard synchronizes dispatches of delayed batches (see [flow.Options.Guard]) with [Collector.Close], so a timed
// dispatch that has already taken a batch finishes passing it to the sender before Close flushes the batcher and
// signals [Collector.Done]. Otherwise, the batch could reach the sender after it was closed, and be discarded.
// Timed dispatches that acquire the lock after [Collector.Close] don't need to be skipped:
// its flush invalidates their pending batch, so they find nothing to dispatch.
func (c *Collector) guard(flush func()) {
	c.state.CloseMu.RLock()
	defer c.state.CloseMu.RUnlock()
	flush()
}

// sendRows is the dispatch function of [Collector.batch], so it passes the configured format through the
// batcher. It reads [Collector.Send] when it's called, not at construction time, so tests may replace it.
func (c *Collector) sendRows(ctx context.Context, rows []flow.Row) {
	c.Send(ctx, flow.Rows(rows), c.format) // Free conversion: the sender takes ownership of the chunk.
}

// Start connects to the configured SQL-based database and starts sending queries to it. This method
// returns immediately, and the collector runs asynchronously in the background. This method is not safe for
// concurrency, initialize collectors only in the main goroutine. Lastly, the collector obeys the cancellation
// of the provided context, but with a grace period for queries in progress (see [Collector.Close]).
func (c *Collector) Start(ctx context.Context) bool {
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

	schedCtx, execCtx := c.state.StartWith(ctx)
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

func (c *Collector) scheduleNext(schedCtx, execCtx context.Context, prev time.Time) {
	sem := make(chan struct{}, max(c.Concurrency, 1))
	defer c.Close(execCtx)

	for {
		nextStart := c.Schedule.Next(prev)
		if nextStart.IsZero() {
			if c.Schedule.RunsOnlyOnce() {
				done := make(chan struct{})
				go func() {
					defer close(done)
					c.state.InProgress.Wait()
				}()
				select {
				case <-schedCtx.Done():
				case <-done:
					slog.Info("SQL collector finished one-time execution",
						slog.String("driver", c.driver), slog.String("name", c.Name),
					)
				}
			} else {
				slog.Error("SQL collector stopped due to scheduler bug - no next instance",
					slog.String("driver", c.driver), slog.String("name", c.Name), slog.String("schedule", c.Cronspec),
				)
			}
			return
		}
		if now := time.Now(); !c.Schedule.RunsOnlyOnce() && now.After(nextStart) {
			slog.Warn("SQL collector is behind schedule, skipping missed execution", slog.String("driver", c.driver),
				slog.String("name", c.Name), slog.Time("skipped", nextStart), slog.Duration("gap", now.Sub(nextStart)),
			)
			prev = nextStart
			continue
		}

		select {
		case <-schedCtx.Done():
			return
		case <-time.After(time.Until(nextStart)):
			c.executeWithConcurrencyLimit(schedCtx, execCtx, sem, nextStart)
			prev = nextStart
		}
	}
}

func (c *Collector) executeWithConcurrencyLimit(schedCtx, execCtx context.Context, sem chan struct{}, scheduled time.Time) {
	// Checking schedCtx.Err() here instead of using schedCtx.Done() in the select
	// block below, to guarantee that we check the closing state before the semaphore.
	if schedCtx.Err() != nil {
		return
	}

	select {
	case sem <- struct{}{}:
		c.state.InProgress.Go(func() {
			defer func() { <-sem }()
			c.executeQuery(execCtx)
		})
	default:
		slog.Warn("SQL collector is at its concurrency limit, skipping query", slog.String("driver", c.driver),
			slog.String("name", c.Name), slog.Int("limit", cap(sem)), slog.Time("skipped", scheduled),
		)
	}
}

// executeQuery shouldn't be called directly, only through [Collector.scheduleNext]. Therefore, it's safe to
// assume that either [Collector.db] or [Collector.pgPool] are non-nil, since [Collector.Start] had to succeed
// first. The provided context is an execution context (execCtx) detached from scheduler cancellation, allowing
// ongoing queries to finish even during graceful shutdown, until [Collector.Close] forcefully cancels them.
func (c *Collector) executeQuery(ctx context.Context) bool {
	queryCtx := ctx
	var cancel context.CancelFunc
	if c.timeout > 0 {
		queryCtx, cancel = context.WithTimeout(ctx, c.timeout)
	}
	if cancel != nil {
		defer cancel()
	}

	// [Collector.db] and [Collector.pgPool]/[Collector.usingPG] are mutually exclusive,
	// so if the latter is non-nil we have to use it instead of the former.
	if c.usingPG {
		return c.executePostgresQuery(ctx, queryCtx)
	}

	tx, err := c.db.BeginTx(queryCtx, nil)
	if err != nil {
		slog.Warn("failed to begin SQL transaction", slog.Any("error", err),
			slog.String("driver", c.driver), slog.String("name", c.Name),
		)
		return false
	}
	defer func() { _ = tx.Rollback() }()

	start := time.Now()
	rows, err := tx.QueryContext(queryCtx, c.query)
	if err != nil {
		slog.Warn("failed to execute SQL query", slog.Any("error", err), slog.String("driver", c.driver),
			slog.String("name", c.Name), slog.Time("start_time", start), slog.Duration("duration", time.Since(start)),
		)
		return false
	}
	defer rows.Close()

	count, err := c.processResults(queryCtx, rows)
	end := time.Now()
	c.batch.Flush(ctx)

	// Queries may modify data (e.g., "DELETE ... RETURNING"), so commit only if all the results were processed.
	// Otherwise, roll back: partial results were already dispatched, but it's better to collect them again later
	// than to lose the rest. [sql.Tx.Commit] waits for the rows to be closed, so close them first (if they aren't).
	ok := err == nil
	if !ok {
		slog.Warn("error while processing SQL query results", slog.Any("error", err), slog.String("driver", c.driver),
			slog.String("name", c.Name), slog.Int("successfully_processed_rows", count),
		)
	} else if err = errors.Join(rows.Close(), tx.Commit()); err != nil {
		ok = false
		slog.Warn("failed to commit SQL transaction", slog.Any("error", err), slog.String("driver", c.driver),
			slog.String("name", c.Name), slog.Int("successfully_processed_rows", count),
		)
	} else {
		stats := c.db.Stats()
		slog.Debug("SQL query completed successfully", slog.String("driver", c.driver), slog.String("name", c.Name),
			slog.Int("rows", count), slog.Time("start_time", start), slog.Duration("duration", end.Sub(start)),
			slog.Int("in_use_conns", stats.InUse), slog.Int("idle_conns", stats.Idle),
		)
	}

	if ok || count > 0 {
		c.checkpointMu.Lock()
		if start.UTC().After(c.prevStart) {
			c.prevStart = start.UTC()
			c.prevEnd = end.UTC()
		}
		c.checkpointMu.Unlock()
	}

	return ok
}

// processResults batches and dispatches partial results even when an error interrupts processing,
// to prevent data loss. It supports multiple result-sets for multiple statements. The provided
// context is tied to the query's lifecycle, which is based on the collector's entire execution
// lifecycle, so it has a limited grace period to complete before being forcefully canceled.
func (c *Collector) processResults(ctx context.Context, rows *sql.Rows) (int, error) {
	totalScanned := 0
	for resultSet := 1; ctx.Err() == nil; resultSet++ {
		cols, err := rows.Columns()
		if err != nil {
			return totalScanned, fmt.Errorf("failed to read SQL column names in result-set %d: %w", resultSet, err)
		}
		// Rows share this slice and outlive this loop (senders encode them asynchronously),
		// but nothing is preventing drivers from reusing it, so we copy it once per result-set.
		cols = slices.Clone(cols)

		rsScanned := 0
		for rows.Next() {
			row, err := scanRow(rows, cols)
			if err != nil {
				return totalScanned, fmt.Errorf("failed to scan row %d in result-set %d: %w", rsScanned+1, resultSet, err)
			}
			if err := ctx.Err(); err != nil {
				return totalScanned, fmt.Errorf("query result processing canceled: %w", err)
			}
			c.batch.AddItem(ctx, row) // Async I/O, it doesn't matter which ctx we use (it's detached by batcher anyway).
			totalScanned++
			rsScanned++
		}
		if err := rows.Err(); err != nil {
			return totalScanned, fmt.Errorf("row iteration error: %w", err)
		}

		if !rows.NextResultSet() {
			break
		}
	}

	if err := ctx.Err(); err != nil {
		return totalScanned, fmt.Errorf("query result processing canceled: %w", err)
	}
	if err := rows.Err(); err != nil {
		return totalScanned, fmt.Errorf("row-set iteration error: %w", err)
	}

	return totalScanned, nil
}

// scanRow scans the current row into a new [flow.Row], which shares the given column names (they're read-only) with
// all the other rows of the same result-set, and has its own values slice (the sender takes ownership of it).
func scanRow(rows *sql.Rows, cols []string) (flow.Row, error) {
	size := len(cols)
	vals := make([]any, size)
	ptrs := make([]any, size)
	for i := range size {
		ptrs[i] = &vals[i]
	}

	if err := rows.Scan(ptrs...); err != nil {
		return flow.Row{}, fmt.Errorf("row scanning error: %w", err)
	}

	return flow.Row{Cols: cols, Vals: vals}, nil
}

// Done returns a channel that closes when shutdown completes or its grace period expires.
func (c *Collector) Done() <-chan struct{} {
	return c.state.Closed
}

// Close waits (up to [CloseTimeout] + [AbortTimeout]) for queries that are in progress to finish, once new
// ones can no longer be scheduled. It is safe to call multiple times, even if [Collector.Start] wasn't called,
// but it's meant to be called only at the end of the [Collector.scheduleNext] goroutine. If there are still pending
// queries after the timeout, the collector cancels their context, and waits a little longer for them to stop.
// It then flushes any remaining rows, and signals through [Collector.Done] that it's ready to shut down.
func (c *Collector) Close(ctx context.Context) {
	if !c.state.IsStarted() {
		return
	}

	c.state.CloseOnce.Do(func() {
		c.state.BeginClose()

		done := make(chan struct{})
		go func() {
			defer close(done)
			c.state.InProgress.Wait()
		}()

		timeout := c.timeout
		if timeout <= 0 || timeout > CloseTimeout {
			timeout = CloseTimeout // Ensure the timeout is within acceptable bounds.
		}

		select {
		case <-done:
			c.closePools() // All done, so closing the pools can't block.
		case <-time.After(timeout):
			slog.Warn("closing SQL connection forcefully", slog.String("driver", c.driver),
				slog.String("name", c.Name), slog.Duration("timeout", timeout),
			)
			c.state.StopStragglers()
		}

		// Wait for aborted workers to actually stop, if there are any,
		// and dispatch any data they managed to collect, so we don't lose it.
		select {
		case <-done:
			// Immediate if the previous select block didn't encounter a timeout,
			// so no need to nest this select block inside the previous one.
		case <-time.After(AbortTimeout):
			slog.Error("aborted SQL collector didn't stop immediately",
				slog.String("driver", c.driver), slog.String("name", c.Name),
			)
		}

		// Wait for any timed dispatch in progress (see [Collector.guard]), and
		// flush before allowing senders to start closing - to prevent data loss.
		c.state.CloseMu.Lock()
		defer c.state.CloseMu.Unlock()

		c.batch.Flush(ctx)
		c.state.FinishClose()
	})
}

// closePools releases the collector's idle database connections. It's called by [Collector.Close]
// only when no queries are in progress, because closing the pools blocks until all connections
// are released, and it doesn't abort hung queries (canceling "execCtx" does). This matters
// for one-time collectors, which may finish long before the process exits.
func (c *Collector) closePools() {
	if c.db != nil {
		_ = c.db.Close()
	}
	if c.usingPG && c.pgPool != nil {
		c.pgPool.Close()
	}
}

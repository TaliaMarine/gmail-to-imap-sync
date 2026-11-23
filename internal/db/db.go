// Package db owns the PostgreSQL connection pool and schema migrations.
package db

import (
	"context"
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB bundles the pool and the generated queries.
type DB struct {
	Pool *pgxpool.Pool
	*store.Queries
	url string
}

// Open connects to PostgreSQL and verifies the connection.
func Open(ctx context.Context, url string) (*DB, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &DB{Pool: pool, Queries: store.New(pool), url: url}, nil
}

// Close releases the pool.
func (d *DB) Close() { d.Pool.Close() }

// Ping reports whether the database is reachable.
func (d *DB) Ping(ctx context.Context) error { return d.Pool.Ping(ctx) }

// Migrate applies pending migrations. golang-migrate takes a PostgreSQL
// advisory lock while it runs, so concurrent replicas serialise safely.
func (d *DB) Migrate() error {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, "pgx5"+trimScheme(d.url))
	if err != nil {
		return fmt.Errorf("migrate init: %w", err)
	}
	defer func() { _, _ = m.Close() }()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

// trimScheme turns postgres:// or postgresql:// into the "://..." remainder
// so it can be prefixed with the pgx5 driver scheme.
func trimScheme(url string) string {
	for _, p := range []string{"postgresql", "postgres"} {
		if len(url) > len(p) && url[:len(p)] == p {
			return url[len(p):]
		}
	}
	return url
}

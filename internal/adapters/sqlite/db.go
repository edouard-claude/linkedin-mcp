// Package sqlite implements domain.TenantStore on top of a local SQLite file,
// using the pure Go modernc.org/sqlite driver so the binary stays static.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"

	"github.com/edouard-claude/linkedin-mcp/migrations"

	_ "modernc.org/sqlite" // database/sql driver "sqlite"
)

// pragmas are applied to the connection at open time. WAL keeps readers from
// blocking the writer, and foreign keys are off by default in SQLite.
var pragmas = []string{
	"PRAGMA journal_mode = WAL",
	"PRAGMA synchronous = NORMAL",
	"PRAGMA foreign_keys = ON",
	"PRAGMA busy_timeout = 5000",
}

// open dials the database file and applies the pragmas.
//
// MaxOpenConns is 1 on purpose: every write in this server is short, and a
// single connection removes SQLITE_BUSY from the picture entirely.
func open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_txlock=immediate")
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)

	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("apply %q: %w", p, err)
		}
	}
	return db, nil
}

// migrate applies every embedded migration that has not run yet, in lexical
// order, each in its own transaction.
func migrate(ctx context.Context, db *sql.DB) error {
	const createTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
		name       TEXT PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`
	if _, err := db.ExecContext(ctx, createTable); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	slices.Sort(entries)

	for _, name := range entries {
		applied, err := migrationApplied(ctx, db, name)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		body, err := migrations.FS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if err := applyMigration(ctx, db, name, string(body)); err != nil {
			return err
		}
	}
	return nil
}

func migrationApplied(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var one int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM schema_migrations WHERE name = ?`, name).Scan(&one)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	default:
		return false, fmt.Errorf("check migration %s: %w", name, err)
	}
}

func applyMigration(ctx context.Context, db *sql.DB, name, body string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", name, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, body); err != nil {
		return fmt.Errorf("apply migration %s: %w", name, err)
	}
	const insert = `INSERT INTO schema_migrations (name, applied_at) VALUES (?, unixepoch())`
	if _, err := tx.ExecContext(ctx, insert, name); err != nil {
		return fmt.Errorf("record migration %s: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", name, err)
	}
	return nil
}

// DBPathDir returns the directory a database path lives in, so the caller can
// create it before opening.
func DBPathDir(path string) string { return filepath.Dir(path) }

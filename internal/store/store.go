// Package store is the bot's own state: customers, plans, orders, wallets,
// discount codes and settings, in a single SQLite file. Nothing here talks
// to Telegram or to the panel.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure Go, so the binary builds with CGO off
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// ErrNotFound is returned when a looked-up row does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned when a guarded state change lost a race - most
// importantly when an order was already moved on by someone else.
var ErrConflict = errors.New("conflict")

// ErrInsufficientBalance is returned when a wallet debit would go negative.
var ErrInsufficientBalance = errors.New("insufficient balance")

type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Open opens (creating if needed) the database at path and applies any
// pending migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	// WAL lets the admin's read of the order list proceed while a delivery
	// is writing; busy_timeout turns a momentary lock into a short wait
	// instead of an immediate SQLITE_BUSY error. foreign_keys is off by
	// default in SQLite and must be asked for per connection.
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite serialises writers anyway; one connection makes that explicit
	// and removes a whole class of "database is locked" surprises.
	db.SetMaxOpenConns(1)

	s := &Store{db: db, now: time.Now}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// BackupTo writes a consistent snapshot of the whole database to dest using
// SQLite's own VACUUM INTO, which is safe to run while the bot is serving.
// dest must not already exist.
func (s *Store) BackupTo(ctx context.Context, dest string) error {
	if strings.ContainsAny(dest, "'\x00") {
		return errors.New("backup path must not contain quotes")
	}
	_, err := s.db.ExecContext(ctx, "VACUUM INTO '"+dest+"'")
	return err
}

func (s *Store) unix() int64 { return s.now().Unix() }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)

	for _, name := range names {
		var applied int
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, name).Scan(&applied); err != nil {
			return err
		}
		if applied > 0 {
			continue
		}
		body, err := migrationFiles.ReadFile(name)
		if err != nil {
			return err
		}
		if err := s.inTx(ctx, func(tx *sql.Tx) error {
			for _, stmt := range splitStatements(string(body)) {
				if _, err := tx.ExecContext(ctx, stmt); err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
			}
			_, err := tx.ExecContext(ctx,
				`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`, name, s.unix())
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

// splitStatements splits a migration on semicolons that end a line,
// dropping comment-only lines first so a semicolon inside a comment can
// never cut a statement in half.
func splitStatements(sqlText string) []string {
	var cleaned []string
	for _, line := range strings.Split(sqlText, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		cleaned = append(cleaned, line)
	}
	var out []string
	for _, part := range strings.Split(strings.Join(cleaned, "\n"), ";\n") {
		if p := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(part), ";")); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// inTx runs fn in a transaction, committing on success and rolling back on
// any error or panic.
func (s *Store) inTx(ctx context.Context, fn func(tx *sql.Tx) error) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			tx.Rollback()
			panic(p)
		}
		if err != nil {
			tx.Rollback()
			return
		}
		err = tx.Commit()
	}()
	return fn(tx)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/golang-migrate/migrate/v4"
	pgmigrate "github.com/golang-migrate/migrate/v4/database/postgres"
	filesource "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/metamin44/hookflow/internal/platform/config"
)

// ErrNoMigration indicates that no migration has been applied.
var ErrNoMigration = errors.New("no migration has been applied")

// DirtyError reports a dirty migration state. It intentionally never forces a version.
type DirtyError struct {
	Version uint
}

func (e *DirtyError) Error() string {
	return fmt.Sprintf("migration state is dirty at version %d; inspect and recover it manually; no force was applied", e.Version)
}

type runnerBackend interface {
	Up() error
	Steps(int) error
	Version() (uint, bool, error)
	Close() error
}

// Runner executes explicit migration commands through golang-migrate.
type Runner struct {
	backend runnerBackend
}

// NewRunner creates a migration runner using the approved migration directory.
//
// The runner is never invoked automatically by application startup.
func NewRunner(ctx context.Context, cfg config.Database, migrationsDir string) (*Runner, error) {
	dir, err := ResolveMigrationsDir(migrationsDir)
	if err != nil {
		return nil, err
	}

	sourceURL, err := migrationSourceURL(dir)
	if err != nil {
		return nil, err
	}

	src, err := new(filesource.File).Open(sourceURL)
	if err != nil {
		return nil, fmt.Errorf("open migration source: %w", err)
	}

	connConfig, err := newConnConfig(cfg)
	if err != nil {
		_ = src.Close()
		return nil, err
	}

	sqlDB := stdlib.OpenDB(*connConfig)
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		_ = src.Close()
		return nil, fmt.Errorf("verify migration database connection: %w", err)
	}

	driver, err := pgmigrate.WithInstance(sqlDB, &pgmigrate.Config{})
	if err != nil {
		_ = sqlDB.Close()
		_ = src.Close()
		return nil, fmt.Errorf("initialize migration database driver: %w", err)
	}

	m, err := migrate.NewWithInstance("file", src, "postgres", driver)
	if err != nil {
		_ = driver.Close()
		_ = src.Close()
		return nil, fmt.Errorf("initialize migration runner: %w", err)
	}

	return &Runner{backend: &migrateBackend{m: m}}, nil
}

// Up applies all pending Up migrations.
//
// A false result with a nil error means there was nothing to apply.
func (r *Runner) Up() (bool, error) {
	if err := r.ensureClean(); err != nil {
		return false, err
	}

	err := r.backend.Up()
	if errors.Is(err, migrate.ErrNoChange) {
		return false, nil
	}
	if err != nil {
		if dirtyErr := asDirtyError(err); dirtyErr != nil {
			return false, dirtyErr
		}
		return false, fmt.Errorf("apply migrations: %w", err)
	}
	return true, nil
}

// Down rolls back exactly n applied migrations.
//
// A false result with a nil error means there was nothing to roll back.
func (r *Runner) Down(n int) (bool, error) {
	if n <= 0 {
		return false, errors.New("down count must be a positive integer")
	}
	if err := r.ensureClean(); err != nil {
		return false, err
	}

	err := r.backend.Steps(-n)
	if errors.Is(err, migrate.ErrNoChange) {
		return false, nil
	}
	if err != nil {
		if dirtyErr := asDirtyError(err); dirtyErr != nil {
			return false, dirtyErr
		}
		return false, fmt.Errorf("roll back migrations: %w", err)
	}
	return true, nil
}

// Version reports the current migration version and whether it is dirty.
func (r *Runner) Version() (uint, bool, error) {
	version, dirty, err := r.backend.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, ErrNoMigration
	}
	if err != nil {
		return 0, false, fmt.Errorf("read migration version: %w", err)
	}
	return version, dirty, nil
}

func (r *Runner) ensureClean() error {
	version, dirty, err := r.backend.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read migration version: %w", err)
	}
	if dirty {
		return &DirtyError{Version: version}
	}
	return nil
}

// Close releases the migration source and database resources.
func (r *Runner) Close() error {
	if r == nil || r.backend == nil {
		return nil
	}
	return r.backend.Close()
}

func asDirtyError(err error) *DirtyError {
	var dirtyErr migrate.ErrDirty
	if errors.As(err, &dirtyErr) {
		return &DirtyError{Version: uint(dirtyErr.Version)}
	}
	return nil
}

type migrateBackend struct {
	m *migrate.Migrate
}

func (b *migrateBackend) Up() error {
	return b.m.Up()
}

func (b *migrateBackend) Steps(n int) error {
	return b.m.Steps(n)
}

func (b *migrateBackend) Version() (uint, bool, error) {
	return b.m.Version()
}

func (b *migrateBackend) Close() error {
	sourceErr, databaseErr := b.m.Close()
	return errors.Join(sourceErr, databaseErr)
}

// ResolveMigrationsDir returns an explicit migration directory or discovers the
// repository's db/migrations directory from the working or executable directory.
func ResolveMigrationsDir(explicit string) (string, error) {
	if explicit != "" {
		return validateMigrationsDir(explicit)
	}

	starts := make([]string, 0, 2)
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	if executable, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(executable))
	}

	for _, start := range starts {
		if dir, ok := findMigrationsDir(start); ok {
			return dir, nil
		}
	}

	return "", errors.New("migration directory db/migrations not found; run from the repository or provide an explicit migration path")
}

func validateMigrationsDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve migration directory: %w", err)
	}

	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("migration directory is not accessible: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("migration path is not a directory: %s", abs)
	}
	return abs, nil
}

func findMigrationsDir(start string) (string, bool) {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			candidate := filepath.Join(dir, "db", "migrations")
			if info, err := os.Stat(candidate); err == nil && info.IsDir() {
				return candidate, true
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func migrationSourceURL(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve migration directory: %w", err)
	}

	// golang-migrate's file source concatenates the URL host and path. On
	// Windows, file://C:/... therefore becomes the absolute path C:/....
	return "file://" + filepath.ToSlash(abs), nil
}

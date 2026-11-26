package database

import (
	"errors"
	"fmt"
	"testing"

	"github.com/golang-migrate/migrate/v4"
)

type fakeMigrationBackend struct {
	version    uint
	dirty      bool
	versionErr error
	upErr      error
	stepsErr   error
	closeErr   error

	upCalls    int
	stepsCalls []int
	closeCalls int
}

func (f *fakeMigrationBackend) Up() error {
	f.upCalls++
	return f.upErr
}

func (f *fakeMigrationBackend) Steps(n int) error {
	f.stepsCalls = append(f.stepsCalls, n)
	return f.stepsErr
}

func (f *fakeMigrationBackend) Version() (uint, bool, error) {
	return f.version, f.dirty, f.versionErr
}

func (f *fakeMigrationBackend) Close() error {
	f.closeCalls++
	return f.closeErr
}

func TestRunnerUpNoChange(t *testing.T) {
	backend := &fakeMigrationBackend{version: 13, upErr: migrate.ErrNoChange}
	runner := &Runner{backend: backend}

	changed, err := runner.Up()
	if err != nil {
		t.Fatalf("Up() error: %v", err)
	}
	if changed {
		t.Fatal("Up() reported a change for ErrNoChange")
	}
	if backend.upCalls != 1 {
		t.Fatalf("backend Up() calls = %d, want 1", backend.upCalls)
	}
}

func TestRunnerUpApplied(t *testing.T) {
	backend := &fakeMigrationBackend{version: 13}
	runner := &Runner{backend: backend}

	changed, err := runner.Up()
	if err != nil {
		t.Fatalf("Up() error: %v", err)
	}
	if !changed {
		t.Fatal("Up() did not report applied migrations")
	}
}

func TestRunnerUpDirty(t *testing.T) {
	backend := &fakeMigrationBackend{version: 13, dirty: true}
	runner := &Runner{backend: backend}

	_, err := runner.Up()
	if err == nil {
		t.Fatal("Up() succeeded against a dirty migration state")
	}
	var dirtyErr *DirtyError
	if !errors.As(err, &dirtyErr) {
		t.Fatalf("Up() error = %T, want *DirtyError", err)
	}
	if dirtyErr.Version != 13 {
		t.Fatalf("dirty version = %d, want 13", dirtyErr.Version)
	}
	if backend.upCalls != 0 {
		t.Fatal("Up() called the backend against a dirty migration state")
	}
}

func TestRunnerUpDirtyFromBackend(t *testing.T) {
	backend := &fakeMigrationBackend{
		version: 13,
		upErr:   migrate.ErrDirty{Version: 13},
	}
	runner := &Runner{backend: backend}

	_, err := runner.Up()
	if err == nil {
		t.Fatal("Up() succeeded when the backend reported a dirty state")
	}
	var dirtyErr *DirtyError
	if !errors.As(err, &dirtyErr) {
		t.Fatalf("Up() error = %T, want *DirtyError", err)
	}
	if dirtyErr.Version != 13 {
		t.Fatalf("dirty version = %d, want 13", dirtyErr.Version)
	}
}

func TestRunnerUpVersionError(t *testing.T) {
	backend := &fakeMigrationBackend{versionErr: errors.New("version failure")}
	runner := &Runner{backend: backend}

	_, err := runner.Up()
	if err == nil {
		t.Fatal("Up() succeeded when version lookup failed")
	}
	if backend.upCalls != 0 {
		t.Fatal("Up() called the backend after version lookup failed")
	}
}

func TestRunnerDownNoChange(t *testing.T) {
	backend := &fakeMigrationBackend{version: 13, stepsErr: migrate.ErrNoChange}
	runner := &Runner{backend: backend}

	changed, err := runner.Down(2)
	if err != nil {
		t.Fatalf("Down() error: %v", err)
	}
	if changed {
		t.Fatal("Down() reported a change for ErrNoChange")
	}
	if len(backend.stepsCalls) != 1 || backend.stepsCalls[0] != -2 {
		t.Fatalf("backend Steps() calls = %v, want [-2]", backend.stepsCalls)
	}
}

func TestRunnerDownApplied(t *testing.T) {
	backend := &fakeMigrationBackend{version: 13}
	runner := &Runner{backend: backend}

	changed, err := runner.Down(2)
	if err != nil {
		t.Fatalf("Down() error: %v", err)
	}
	if !changed {
		t.Fatal("Down() did not report rolled back migrations")
	}
	if len(backend.stepsCalls) != 1 || backend.stepsCalls[0] != -2 {
		t.Fatalf("backend Steps() calls = %v, want [-2]", backend.stepsCalls)
	}
}

func TestRunnerDownInvalidCount(t *testing.T) {
	for _, n := range []int{0, -1} {
		t.Run(fmt.Sprintf("count_%d", n), func(t *testing.T) {
			backend := &fakeMigrationBackend{version: 13}
			runner := &Runner{backend: backend}

			if _, err := runner.Down(n); err == nil {
				t.Fatalf("Down(%d) succeeded", n)
			}
			if len(backend.stepsCalls) != 0 {
				t.Fatal("Down() called the backend with an invalid count")
			}
		})
	}
}

func TestRunnerDownDirty(t *testing.T) {
	backend := &fakeMigrationBackend{version: 13, dirty: true}
	runner := &Runner{backend: backend}

	_, err := runner.Down(1)
	if err == nil {
		t.Fatal("Down() succeeded against a dirty migration state")
	}
	var dirtyErr *DirtyError
	if !errors.As(err, &dirtyErr) {
		t.Fatalf("Down() error = %T, want *DirtyError", err)
	}
	if len(backend.stepsCalls) != 0 {
		t.Fatal("Down() called the backend against a dirty migration state")
	}
}

func TestRunnerVersion(t *testing.T) {
	backend := &fakeMigrationBackend{version: 13}
	runner := &Runner{backend: backend}

	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version() error: %v", err)
	}
	if version != 13 || dirty {
		t.Fatalf("Version() = (%d, %t), want (13, false)", version, dirty)
	}
}

func TestRunnerVersionDirty(t *testing.T) {
	backend := &fakeMigrationBackend{version: 13, dirty: true}
	runner := &Runner{backend: backend}

	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version() error: %v", err)
	}
	if version != 13 || !dirty {
		t.Fatalf("Version() = (%d, %t), want (13, true)", version, dirty)
	}
}

func TestRunnerVersionNoMigration(t *testing.T) {
	backend := &fakeMigrationBackend{versionErr: migrate.ErrNilVersion}
	runner := &Runner{backend: backend}

	_, _, err := runner.Version()
	if !errors.Is(err, ErrNoMigration) {
		t.Fatalf("Version() error = %v, want ErrNoMigration", err)
	}
}

func TestRunnerVersionError(t *testing.T) {
	backend := &fakeMigrationBackend{versionErr: errors.New("version failure")}
	runner := &Runner{backend: backend}

	if _, _, err := runner.Version(); err == nil {
		t.Fatal("Version() succeeded when the backend failed")
	}
}

func TestRunnerClose(t *testing.T) {
	backend := &fakeMigrationBackend{}
	runner := &Runner{backend: backend}

	if err := runner.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	if backend.closeCalls != 1 {
		t.Fatalf("backend Close() calls = %d, want 1", backend.closeCalls)
	}
}

func TestRunnerCloseError(t *testing.T) {
	closeErr := errors.New("close failure")
	backend := &fakeMigrationBackend{closeErr: closeErr}
	runner := &Runner{backend: backend}

	if err := runner.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("Close() error = %v, want %v", err, closeErr)
	}
}

func TestRunnerCloseNil(t *testing.T) {
	var runner *Runner
	if err := runner.Close(); err != nil {
		t.Fatalf("nil Runner Close() error: %v", err)
	}
}

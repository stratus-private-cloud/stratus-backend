package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stratus-private-cloud/stratus-backend/internal/config"
	"github.com/stratus-private-cloud/stratus-backend/internal/db"
)

// slowMigration is a store whose migration takes longer than a startup step
// is allowed, and that says whether it was handed a deadline.
type slowMigration struct {
	db.Store
	took        time.Duration
	hadDeadline bool
}

func (s *slowMigration) Migrate(ctx context.Context) error {
	_, s.hadDeadline = ctx.Deadline()
	select {
	case <-time.After(s.took):
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.Store.Migrate(ctx)
}

// TestAMigrationOutlastsTheStartupTimeout is #567: the migration shared the
// startup steps' thirty seconds, so one over a large table failed every start,
// and the settings read after it would have found the deadline spent.
// Not parallel: it shortens a package variable.
func TestAMigrationOutlastsTheStartupTimeout(t *testing.T) {
	cfg := config.Config{Database: config.DatabaseDSN{
		Scheme: config.SchemeSQLite,
		Path:   filepath.Join(t.TempDir(), "stratus.db"),
	}}
	// Opened before the timeout shrinks: on a loaded runner opening SQLite
	// alone has outlasted a short one.
	inner, err := openDatabase(t.Context(), cfg.Database)
	if err != nil {
		t.Fatal(err)
	}

	was := startupTimeout
	startupTimeout = 250 * time.Millisecond
	t.Cleanup(func() { startupTimeout = was })

	slow := &slowMigration{Store: inner, took: 4 * startupTimeout}
	open := func(context.Context, config.DatabaseDSN) (db.Store, error) { return slow, nil }

	store, set, err := New(cfg, "test", "").openMetadata(t.Context(), open)
	if store != nil {
		t.Cleanup(func() { _ = store.Close() })
	}
	if err != nil {
		t.Fatalf("openMetadata = %v, want a slow migration to finish", err)
	}
	if slow.hadDeadline {
		t.Error("Migrate was handed a deadline")
	}
	if set == nil {
		t.Error("the settings were not read after the migration")
	}
}

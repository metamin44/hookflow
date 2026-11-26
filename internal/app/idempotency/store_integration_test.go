//go:build integration

package idempotency

import (
	"context"
	"crypto/rand"
	"fmt"
	"testing"
	"time"

	"github.com/metamin44/hookflow/internal/platform/config"
	"github.com/metamin44/hookflow/internal/platform/database"
)

func TestPostgresStoreDeletesOnlyExpiredBatchIntegration(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("integration database configuration unavailable: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pool, err := database.NewPool(ctx, cfg.Database)
	if err != nil {
		t.Fatalf("NewPool() error: %v", err)
	}
	defer pool.Close()

	// Everything is written inside one transaction that is always rolled back,
	// so the test never deletes or alters pre-existing data.
	tx, err := pool.Raw().Begin(ctx)
	if err != nil {
		t.Fatalf("begin test transaction: %v", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	organizationID := testUUID(t)
	eventID := testUUID(t)

	if _, err := tx.Exec(ctx,
		`INSERT INTO organizations (id, name, status) VALUES ($1::uuid, $2, 'active')`,
		organizationID, "sweep-test-"+organizationID,
	); err != nil {
		t.Fatalf("insert test organization: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO events (id, organization_id, type, payload, destination_count, expires_at)
		 VALUES ($1::uuid, $2::uuid, 'test.idempotency_sweep', '{}'::jsonb, 0, now() + interval '1 hour')`,
		eventID, organizationID,
	); err != nil {
		t.Fatalf("insert test event: %v", err)
	}

	insertRecord := func(key string, createdAgo, expiresFromNow string) {
		t.Helper()
		if _, err := tx.Exec(ctx,
			`INSERT INTO idempotency_records
			     (id, organization_id, idempotency_key, request_hash, event_id, created_at, expires_at)
			 VALUES ($1::uuid, $2::uuid, $3, decode(repeat('00', 32), 'hex'), $4::uuid,
			         now() - $5::interval, now() + $6::interval)`,
			testUUID(t), organizationID, key, eventID, createdAgo, expiresFromNow,
		); err != nil {
			t.Fatalf("insert idempotency record %s: %v", key, err)
		}
	}

	insertRecord("expired-1", "73 hours", "-1 hour")
	insertRecord("expired-2", "73 hours", "-2 hours")
	insertRecord("expired-3", "73 hours", "-3 hours")
	insertRecord("live", "1 hour", "71 hours")

	store := NewPostgresStore(tx)

	deleted, err := store.DeleteExpiredBatch(ctx, 2)
	if err != nil {
		t.Fatalf("first DeleteExpiredBatch() error: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("first batch deleted %d rows, want 2", deleted)
	}

	deleted, err = store.DeleteExpiredBatch(ctx, 2)
	if err != nil {
		t.Fatalf("second DeleteExpiredBatch() error: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("second batch deleted %d rows, want 1", deleted)
	}

	deleted, err = store.DeleteExpiredBatch(ctx, 2)
	if err != nil {
		t.Fatalf("third DeleteExpiredBatch() error: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("third batch deleted %d rows, want 0", deleted)
	}

	var remaining int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM idempotency_records WHERE organization_id = $1::uuid`,
		organizationID,
	).Scan(&remaining); err != nil {
		t.Fatalf("count remaining records: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("remaining records = %d, want 1 (the live record)", remaining)
	}

	var liveKey string
	if err := tx.QueryRow(ctx,
		`SELECT idempotency_key FROM idempotency_records WHERE organization_id = $1::uuid`,
		organizationID,
	).Scan(&liveKey); err != nil {
		t.Fatalf("read remaining record: %v", err)
	}
	if liveKey != "live" {
		t.Fatalf("remaining record = %q, want %q", liveKey, "live")
	}
}

func testUUID(t *testing.T) string {
	t.Helper()

	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("generate test uuid: %v", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4, test fixture only
	b[8] = (b[8] & 0x3f) | 0x80 // variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

package db_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"tablekit.de/tablekit/internal/db"
	"tablekit.de/tablekit/internal/db/sqlcgen"
)

func testStores(t *testing.T) (*db.Store, *db.Store) {
	t.Helper()
	appDSN := os.Getenv("TABLEKIT_TEST_DATABASE_URL")
	ownerDSN := os.Getenv("TABLEKIT_TEST_OWNER_DATABASE_URL")
	if appDSN == "" || ownerDSN == "" {
		t.Skip("TABLEKIT_TEST_DATABASE_URL and TABLEKIT_TEST_OWNER_DATABASE_URL are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	appStore, err := db.Open(ctx, appDSN, 8)
	if err != nil {
		t.Fatalf("open app store: %v", err)
	}
	ownerStore, err := db.Open(ctx, ownerDSN, 4)
	if err != nil {
		appStore.Close()
		t.Fatalf("open owner store: %v", err)
	}
	t.Cleanup(func() {
		appStore.Close()
		ownerStore.Close()
	})
	return appStore, ownerStore
}

func seedMerchant(t *testing.T, owner *db.Store, slug, host string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var id uuid.UUID
	err := owner.WithOwner(ctx, func(tx pgx.Tx, _ *sqlcgen.Queries) error {
		if err := tx.QueryRow(ctx,
			`INSERT INTO merchants (name, slug) VALUES ($1, $2) RETURNING id`, "Test "+slug, slug).
			Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO merchant_domains (merchant_id, host, is_primary) VALUES ($1, $2, true)`, id, host); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed merchant %s: %v", slug, err)
	}
	t.Cleanup(func() {
		_ = owner.WithOwner(context.Background(), func(tx pgx.Tx, _ *sqlcgen.Queries) error {
			_, err := tx.Exec(context.Background(), `DELETE FROM merchants WHERE id = $1`, id)
			return err
		})
	})
	return id
}

func seedStaff(t *testing.T, owner *db.Store, merchantID uuid.UUID, email string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var id uuid.UUID
	err := owner.WithOwner(ctx, func(tx pgx.Tx, _ *sqlcgen.Queries) error {
		return tx.QueryRow(ctx,
			`INSERT INTO staff_users (merchant_id, role, email, name) VALUES ($1, 'manager', $2, 'Staff') RETURNING id`,
			merchantID, email).Scan(&id)
	})
	if err != nil {
		t.Fatalf("seed staff: %v", err)
	}
	return id
}

func TestAppRoleCannotBypassRLS(t *testing.T) {
	appStore, _ := testStores(t)
	if err := appStore.CheckAppRole(context.Background()); err != nil {
		t.Fatalf("app role must not be superuser or BYPASSRLS: %v", err)
	}
}

func TestTenantIsolationReads(t *testing.T) {
	appStore, ownerStore := testStores(t)
	ctx := context.Background()

	merchantA := seedMerchant(t, ownerStore, "rls-a-"+uuid.NewString()[:8], "a-"+uuid.NewString()[:8]+".example.test")
	merchantB := seedMerchant(t, ownerStore, "rls-b-"+uuid.NewString()[:8], "b-"+uuid.NewString()[:8]+".example.test")
	seedStaff(t, ownerStore, merchantA, "a@example.test")
	seedStaff(t, ownerStore, merchantB, "b@example.test")

	var seen []uuid.UUID
	err := appStore.WithTenant(ctx, merchantA, func(q *sqlcgen.Queries) error {
		rows, err := q.ListStaff(ctx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			seen = append(seen, row.MerchantID)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tenant A read: %v", err)
	}
	if len(seen) == 0 {
		t.Fatal("tenant A should see its own staff")
	}
	for _, id := range seen {
		if id != merchantA {
			t.Fatalf("tenant A saw rows for merchant %s", id)
		}
	}
}

func TestTenantIsolationRejectsCrossTenantWrite(t *testing.T) {
	appStore, ownerStore := testStores(t)
	ctx := context.Background()

	merchantA := seedMerchant(t, ownerStore, "rls-w-"+uuid.NewString()[:8], "w-"+uuid.NewString()[:8]+".example.test")
	merchantB := seedMerchant(t, ownerStore, "rls-x-"+uuid.NewString()[:8], "x-"+uuid.NewString()[:8]+".example.test")
	seedStaff(t, ownerStore, merchantB, "b2@example.test")

	err := appStore.WithTenant(ctx, merchantA, func(q *sqlcgen.Queries) error {
		_, err := q.CreateStaff(ctx, sqlcgen.CreateStaffParams{
			Column1: merchantB,
			Role:    sqlcgen.StaffRoleManager,
			Column3: "intruder@example.test",
			Column4: "Intruder",
			Column6: false,
		})
		return err
	})
	if err == nil {
		t.Fatal("cross-tenant insert must be rejected by row-level security")
	}
}

func TestNoTenantFailsClosed(t *testing.T) {
	appStore, ownerStore := testStores(t)
	ctx := context.Background()
	merchantA := seedMerchant(t, ownerStore, "rls-n-"+uuid.NewString()[:8], "n-"+uuid.NewString()[:8]+".example.test")
	seedStaff(t, ownerStore, merchantA, "n@example.test")

	err := appStore.WithTenant(ctx, uuid.Nil, func(q *sqlcgen.Queries) error {
		_, err := q.ListStaff(ctx)
		return err
	})
	if !errors.Is(err, db.ErrNoTenant) {
		t.Fatalf("expected ErrNoTenant, got %v", err)
	}
}

func TestResolveMerchantByHost(t *testing.T) {
	appStore, ownerStore := testStores(t)
	ctx := context.Background()
	host := "resolver-" + uuid.NewString()[:8] + ".example.test"
	merchantA := seedMerchant(t, ownerStore, "rls-r-"+uuid.NewString()[:8], host)

	route, err := appStore.ResolveMerchantByHost(ctx, host)
	if err != nil {
		t.Fatalf("resolve host: %v", err)
	}
	if route.ID != merchantA {
		t.Fatalf("resolved wrong merchant: %s", route.ID)
	}
	if _, err := appStore.ResolveMerchantByHost(ctx, "missing-"+uuid.NewString()[:8]+".example.test"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for unknown host, got %v", err)
	}
}

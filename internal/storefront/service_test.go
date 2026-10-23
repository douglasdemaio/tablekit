package storefront

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

func TestIsOpen(t *testing.T) {
	hours := []Hour{
		{Weekday: 1, Opens: "11:00", Closes: "22:00"},
		{Weekday: 5, Opens: "18:00", Closes: "02:00"},
	}
	cases := []struct {
		name string
		t    time.Time
		want bool
	}{
		{"during window", time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC), true},   // Monday
		{"before window", time.Date(2026, 10, 19, 10, 59, 0, 0, time.UTC), false}, // Monday
		{"at closing", time.Date(2026, 10, 19, 22, 0, 0, 0, time.UTC), false},     // Monday
		{"overnight same day", time.Date(2026, 10, 23, 23, 0, 0, 0, time.UTC), true},
		{"overnight next day", time.Date(2026, 10, 24, 1, 30, 0, 0, time.UTC), true},
		{"after overnight", time.Date(2026, 10, 24, 2, 0, 0, 0, time.UTC), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsOpen(hours, tc.t); got != tc.want {
				t.Fatalf("IsOpen(%s) = %v, want %v", tc.t, got, tc.want)
			}
		})
	}
	if !IsOpen(nil, time.Now()) {
		t.Fatal("merchant without hours must be open")
	}
}

func TestMatchZone(t *testing.T) {
	zones := []Zone{
		{Name: "Bamberg", Prefixes: []string{"96"}, MinOrderMinor: 1500, Active: true},
		{Name: "Bamberg 0", Prefixes: []string{"960"}, MinOrderMinor: 1000, Active: true},
		{Name: "Disabled", Prefixes: []string{"97"}, Active: false},
	}
	zone, ok := MatchZone(zones, " 96052 ")
	if !ok || zone.Name != "Bamberg 0" {
		t.Fatalf("expected longest prefix match, got %+v %v", zone, ok)
	}
	if _, ok := MatchZone(zones, "97070"); ok {
		t.Fatal("inactive zone must not match")
	}
	if _, ok := MatchZone(zones, "10115"); ok {
		t.Fatal("unknown prefix must not match")
	}
}

func TestIncludedTax(t *testing.T) {
	if got := IncludedTax(1190, 1900); got != 190 {
		t.Fatalf("tax on 11.90 at 19%% = %d, want 190", got)
	}
	if got := IncludedTax(1000, 0); got != 0 {
		t.Fatalf("zero rate must yield no tax, got %d", got)
	}
}

func TestPlaceOrderAndHandoff(t *testing.T) {
	appStore, ownerStore := testStores(t)
	ctx := context.Background()
	merchantID := seedMerchant(t, ownerStore)

	var itemID, customerID uuid.UUID
	err := ownerStore.WithOwner(ctx, func(tx pgx.Tx, _ *sqlcgen.Queries) error {
		if err := tx.QueryRow(ctx,
			`INSERT INTO customer_accounts (email, name) VALUES ('ada-' || gen_random_uuid() || '@example.com', 'Ada') RETURNING id`).Scan(&customerID); err != nil {
			return err
		}
		var categoryID uuid.UUID
		if err := tx.QueryRow(ctx,
			`INSERT INTO menu_categories (merchant_id, name) VALUES ($1, 'Mains') RETURNING id`,
			merchantID).Scan(&categoryID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx,
			`INSERT INTO menu_items (merchant_id, category_id, name, price_minor)
			 VALUES ($1, $2, 'Schnitzel', 1250) RETURNING id`,
			merchantID, categoryID).Scan(&itemID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO delivery_zones (merchant_id, name, postcode_prefixes, min_order_minor, delivery_fee_minor)
			 VALUES ($1, 'Bamberg', ARRAY['96'], 1500, 300)`, merchantID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed menu: %v", err)
	}

	svc := NewService(appStore)
	split, err := svc.Load(ctx, merchantID)
	if err != nil {
		t.Fatalf("load storefront: %v", err)
	}
	if len(split.Menu) != 1 || len(split.Menu[0].Items) != 1 {
		t.Fatalf("unexpected menu: %+v", split.Menu)
	}

	placed, err := svc.PlaceOrder(ctx, merchantID, OrderInput{
		CustomerID: customerID,
		Name:       "Ada",
		Fulfilment: sqlcgen.FulfilmentModePickup,
		Lines:      []CartLine{{ItemID: itemID, Quantity: 2}},
	})
	if err != nil {
		t.Fatalf("place order: %v", err)
	}
	if placed.Total != 2500 || placed.PickupCode == "" || len(placed.PublicRef) != 12 {
		t.Fatalf("unexpected placed order: %+v", placed)
	}

	order, err := svc.OrderByRef(ctx, merchantID, placed.PublicRef)
	if err != nil {
		t.Fatalf("load order: %v", err)
	}
	if order.Order.SubtotalMinor != 2500 || order.Order.TaxMinor != 399 {
		t.Fatalf("unexpected totals: %+v", order.Order)
	}
	if len(order.Items) != 1 || order.Items[0].Quantity != 2 {
		t.Fatalf("unexpected items: %+v", order.Items)
	}

	staffID := uuid.New()
	if err := svc.VerifyPickup(ctx, merchantID, staffID, placed.ID, placed.PickupCode); !errors.Is(err, ErrNotReady) {
		t.Fatalf("expected ErrNotReady, got %v", err)
	}

	if _, err := ownerStore.Pool().Exec(ctx,
		`UPDATE orders SET status = 'ready' WHERE id = $1`, placed.ID); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	if err := svc.VerifyPickup(ctx, merchantID, staffID, placed.ID, "000000"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("expected ErrInvalidCode, got %v", err)
	}
	if err := svc.VerifyPickup(ctx, merchantID, staffID, placed.ID, placed.PickupCode); err != nil {
		t.Fatalf("verify pickup: %v", err)
	}
	done, err := svc.OrderByRef(ctx, merchantID, placed.PublicRef)
	if err != nil {
		t.Fatalf("reload order: %v", err)
	}
	if done.Order.Status != sqlcgen.OrderStatusCompleted || done.Order.PaymentStatusEnum != sqlcgen.PaymentStatusPaid {
		t.Fatalf("order not completed/paid: %+v", done.Order)
	}
}

func TestPlaceOrderRejectsDeliveryOutsideZone(t *testing.T) {
	appStore, ownerStore := testStores(t)
	ctx := context.Background()
	merchantID := seedMerchant(t, ownerStore)
	var itemID, customerID uuid.UUID
	err := ownerStore.WithOwner(ctx, func(tx pgx.Tx, _ *sqlcgen.Queries) error {
		if err := tx.QueryRow(ctx,
			`INSERT INTO customer_accounts (email, name) VALUES ('ada-' || gen_random_uuid() || '@example.com', 'Ada') RETURNING id`).Scan(&customerID); err != nil {
			return err
		}
		var categoryID uuid.UUID
		if err := tx.QueryRow(ctx,
			`INSERT INTO menu_categories (merchant_id, name) VALUES ($1, 'Mains') RETURNING id`,
			merchantID).Scan(&categoryID); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`INSERT INTO menu_items (merchant_id, category_id, name, price_minor)
			 VALUES ($1, $2, 'Schnitzel', 1250) RETURNING id`,
			merchantID, categoryID).Scan(&itemID)
	})
	if err != nil {
		t.Fatalf("seed menu: %v", err)
	}
	svc := NewService(appStore)
	_, err = svc.PlaceOrder(ctx, merchantID, OrderInput{
		CustomerID: uuid.New(),
		Name:       "Ada",
		Fulfilment: sqlcgen.FulfilmentModeSelfDelivery,
		Address: DeliveryAddress{
			Recipient: "Ada", Phone: "0170", Line1: "Teststr. 1", Postcode: "10115", City: "Berlin",
		},
		Lines: []CartLine{{ItemID: itemID, Quantity: 1}},
	})
	if !errors.Is(err, ErrDeliveryUnavailable) {
		t.Fatalf("expected ErrDeliveryUnavailable, got %v", err)
	}
}

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

func seedMerchant(t *testing.T, owner *db.Store) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := owner.WithOwner(context.Background(), func(tx pgx.Tx, _ *sqlcgen.Queries) error {
		return tx.QueryRow(context.Background(),
			`INSERT INTO merchants (name, slug) VALUES ('Test Restaurant', $1) RETURNING id`,
			"test-"+uuid.NewString()[:8]).Scan(&id)
	})
	if err != nil {
		t.Fatalf("seed merchant: %v", err)
	}
	return id
}

func TestBrandDefaultsWithoutRow(t *testing.T) {
	appStore, ownerStore := testStores(t)
	ctx := context.Background()
	merchantID := seedMerchant(t, ownerStore)
	svc := NewService(appStore)
	b, err := svc.Brand(ctx, merchantID)
	if err != nil {
		t.Fatalf("brand defaults: %v", err)
	}
	if b.Template != "classic-bistro" || b.Font == "" || b.Primary == "" {
		t.Fatalf("unexpected defaults: %+v", b)
	}
}

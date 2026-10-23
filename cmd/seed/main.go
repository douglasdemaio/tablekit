package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"tablekit.de/tablekit/internal/db"
	"tablekit.de/tablekit/internal/db/sqlcgen"
)

type seedItem struct {
	name  string
	price int
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
}

func run() error {
	ownerDSN := os.Getenv("TABLEKIT_MIGRATE_DATABASE_URL")
	if ownerDSN == "" {
		return errors.New("TABLEKIT_MIGRATE_DATABASE_URL is required (owner role)")
	}
	slug := strings.ToLower(strings.TrimSpace(os.Getenv("TABLEKIT_BOOTSTRAP_SLUG")))
	if slug == "" {
		slug = "bamberg-test"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := db.Open(ctx, ownerDSN, 4)
	if err != nil {
		return err
	}
	defer store.Close()

	return seed(ctx, store, slug)
}

func seed(ctx context.Context, store *db.Store, slug string) error {
	return store.WithOwner(ctx, func(tx pgx.Tx, _ *sqlcgen.Queries) error {
		var merchantID string
		if err := tx.QueryRow(ctx, `SELECT id FROM merchants WHERE slug = $1 AND deleted_at IS NULL`, slug).Scan(&merchantID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("merchant %q not found; run bootstrap first", slug)
			}
			return err
		}

		for weekday := 0; weekday <= 6; weekday++ {
			if _, err := tx.Exec(ctx, `
				INSERT INTO opening_hours (merchant_id, weekday, opens, closes)
				SELECT $1, $2, '11:00', '22:00'
				WHERE NOT EXISTS (
					SELECT 1 FROM opening_hours WHERE merchant_id = $1 AND weekday = $2
				)`, merchantID, weekday); err != nil {
				return err
			}
		}
		var zones int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM delivery_zones WHERE merchant_id = $1`, merchantID).Scan(&zones); err != nil {
			return err
		}
		if zones == 0 {
			if _, err := tx.Exec(ctx, `
				INSERT INTO delivery_zones (merchant_id, name, postcode_prefixes, min_order_minor, delivery_fee_minor)
				VALUES ($1, 'Bamberg', ARRAY['96'], 1500, 300)`, merchantID); err != nil {
				return err
			}
		}

		var existing int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM menu_items WHERE merchant_id = $1`, merchantID).Scan(&existing); err != nil {
			return err
		}
		if existing > 0 {
			fmt.Printf("merchant %s already has menu data, skipping seed\n", slug)
			return nil
		}

		categories := []struct {
			name  string
			items []seedItem
		}{
			{"Starters", []seedItem{
				{"Garlic Bread", 450},
				{"Tomato Soup", 590},
				{"Bruschetta", 650},
			}},
			{"Mains", []seedItem{
				{"Schnitzel with Fries", 1390},
				{"Bamberg Onion Steak", 1890},
				{"Margherita Pizza", 990},
				{"Veggie Burger", 1250},
			}},
			{"Drinks", []seedItem{
				{"Still Water 0.5l", 250},
				{"Cola 0.33l", 320},
				{"Helles Beer 0.5l", 420},
				{"Espresso", 220},
			}},
		}

		type createdItem struct {
			id    string
			name  string
			price int
		}
		var all []createdItem
		for ci, cat := range categories {
			var categoryID string
			if err := tx.QueryRow(ctx, `
				INSERT INTO menu_categories (merchant_id, name, sort_order)
				VALUES ($1, $2, $3) RETURNING id`, merchantID, cat.name, ci).Scan(&categoryID); err != nil {
				return err
			}
			for ii, item := range cat.items {
				var itemID string
				if err := tx.QueryRow(ctx, `
					INSERT INTO menu_items (merchant_id, category_id, name, price_minor, currency, sort_order)
					VALUES ($1, $2, $3, $4, 'EUR', $5) RETURNING id`,
					merchantID, categoryID, item.name, item.price, ii).Scan(&itemID); err != nil {
					return err
				}
				all = append(all, createdItem{itemID, item.name, item.price})
			}
		}

		orders := []struct {
			ref        string
			status     string
			payment    string
			fulfilment string
			customer   string
			notes      string
			lines      []int
		}{
			{"TK-1001", "paid", "paid", "pickup", "Anna Schmidt", "", []int{0, 4, 10}},
			{"TK-1002", "accepted", "paid", "self_delivery", "Markus Weber", "Please ring the bell twice.", []int{2, 5, 9}},
			{"TK-1003", "preparing", "paid", "pickup", "Julia Hoffmann", "No onions on the pizza.", []int{3, 6, 8}},
			{"TK-1004", "ready", "paid", "courier", "Thomas Fischer", "", []int{1, 5, 7}},
		}

		for _, o := range orders {
			lines := o.lines
			var subtotal int
			for _, idx := range lines {
				subtotal += all[idx].price
			}
			tax := subtotal * 7 / 107
			total := subtotal
			var orderID string
			if err := tx.QueryRow(ctx, `
				INSERT INTO orders (merchant_id, public_ref, status, fulfilment, payment_method, payment_status,
					currency, subtotal_minor, tax_minor, total_minor, contact_name, notes, placed_at)
				VALUES ($1, $2, $3, $4, 'card', $5, 'EUR', $6, $7, $8, $9, $10, now())
				RETURNING id`,
				merchantID, o.ref, o.status, o.fulfilment, o.payment, subtotal, tax, total, o.customer, o.notes).Scan(&orderID); err != nil {
				return err
			}
			for _, idx := range lines {
				item := all[idx]
				if _, err := tx.Exec(ctx, `
					INSERT INTO order_items (merchant_id, order_id, menu_item_id, name_snapshot,
						unit_price_minor, quantity, line_total_minor)
					VALUES ($1, $2, $3, $4, $5, 1, $5)`,
					merchantID, orderID, item.id, item.name, item.price); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO order_events (merchant_id, order_id, event_type, to_status, actor_type, metadata)
				VALUES ($1, $2, 'seeded', $3, 'system', '{"source":"seed"}'::jsonb)`,
				merchantID, orderID, o.status); err != nil {
				return err
			}
		}

		fmt.Printf("seeded merchant %s: %d categories, %d items, %d orders\n", slug, len(categories), len(all), len(orders))
		return nil
	})
}

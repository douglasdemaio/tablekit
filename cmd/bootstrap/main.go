package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"tablekit.de/tablekit/internal/db/sqlcgen"

	"tablekit.de/tablekit/internal/crypto"
	"tablekit.de/tablekit/internal/db"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "bootstrap:", err)
		os.Exit(1)
	}
}

func run() error {
	ownerDSN := os.Getenv("TABLEKIT_MIGRATE_DATABASE_URL")
	if ownerDSN == "" {
		return errors.New("TABLEKIT_MIGRATE_DATABASE_URL is required (owner role)")
	}

	name := strings.TrimSpace(os.Getenv("TABLEKIT_BOOTSTRAP_MERCHANT_NAME"))
	slug := strings.ToLower(strings.TrimSpace(os.Getenv("TABLEKIT_BOOTSTRAP_SLUG")))
	host := strings.ToLower(strings.TrimSpace(os.Getenv("TABLEKIT_BOOTSTRAP_HOST")))
	email := strings.ToLower(strings.TrimSpace(os.Getenv("TABLEKIT_BOOTSTRAP_EMAIL")))
	ownerName := strings.TrimSpace(os.Getenv("TABLEKIT_BOOTSTRAP_OWNER_NAME"))
	password := os.Getenv("TABLEKIT_BOOTSTRAP_PASSWORD")

	if name == "" || slug == "" || host == "" || email == "" || password == "" {
		return errors.New("TABLEKIT_BOOTSTRAP_MERCHANT_NAME, _SLUG, _HOST, _EMAIL and _PASSWORD are required")
	}
	if len(password) < 12 {
		return errors.New("TABLEKIT_BOOTSTRAP_PASSWORD must be at least 12 characters")
	}
	if ownerName == "" {
		ownerName = email
	}

	hash, err := crypto.HashPassword(password)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := db.Open(ctx, ownerDSN, 4)
	if err != nil {
		return err
	}
	defer store.Close()

	var merchantID string
	err = store.WithOwner(ctx, func(tx pgx.Tx, _ *sqlcgen.Queries) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO merchants (name, slug)
			VALUES ($1, $2)
			ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name
			RETURNING id`, name, slug)
		if err := row.Scan(&merchantID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE merchant_domains SET is_primary = false
			WHERE merchant_id = $1 AND is_primary AND host <> $2`, merchantID, host); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO merchant_domains (merchant_id, host, is_primary)
			VALUES ($1, $2, true)
			ON CONFLICT (host) DO UPDATE SET merchant_id = EXCLUDED.merchant_id, is_primary = true`, merchantID, host)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO staff_users (merchant_id, role, email, name, password_hash, mfa_required)
			VALUES ($1, 'owner', $2, $3, $4, true)
			ON CONFLICT (merchant_id, lower(email))
			DO UPDATE SET password_hash = EXCLUDED.password_hash, name = EXCLUDED.name, role = 'owner'`,
			merchantID, email, ownerName, hash)
		return err
	})
	if err != nil {
		return err
	}

	fmt.Printf("merchant %s (%s)\nowner %s\nhost %s\n", slug, merchantID, email, host)
	return nil
}

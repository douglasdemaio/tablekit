package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"tablekit.de/tablekit/internal/db/sqlcgen"
)

var (
	ErrNotFound   = errors.New("db: not found")
	ErrNoTenant   = errors.New("db: no tenant in context")
	ErrUnsafeRole = errors.New("db: application role must not be superuser or BYPASSRLS")
)

type Store struct {
	pool *pgxpool.Pool
	q    *sqlcgen.Queries
}

func Open(ctx context.Context, dsn string, maxConns int32) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	cfg.MinConns = 1
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 15 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["application_name"] = "tablekit"
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "15000"
	cfg.ConnConfig.RuntimeParams["lock_timeout"] = "5000"
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "10000"

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &Store{pool: pool, q: sqlcgen.New(pool)}, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) Queries() *sqlcgen.Queries { return s.q }

// CheckAppRole refuses to start if the connected role can bypass row-level
// security. This is a startup gate, not a substitute for the RLS policies.
func (s *Store) CheckAppRole(ctx context.Context) error {
	var super, bypass bool
	err := s.pool.QueryRow(ctx,
		`SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`).
		Scan(&super, &bypass)
	if err != nil {
		return fmt.Errorf("check database role: %w", err)
	}
	if super || bypass {
		return ErrUnsafeRole
	}
	return nil
}

// WithTenant runs fn inside a transaction whose row-level-security context is
// fixed to merchantID. The setting is transaction-local, so pooled connections
// cannot leak one tenant's context into another request.
func (s *Store) WithTenant(ctx context.Context, merchantID uuid.UUID, fn func(*sqlcgen.Queries) error) error {
	return s.WithTenantTx(ctx, merchantID, func(_ pgx.Tx, q *sqlcgen.Queries) error {
		return fn(q)
	})
}

func (s *Store) WithTenantTx(ctx context.Context, merchantID uuid.UUID, fn func(pgx.Tx, *sqlcgen.Queries) error) error {
	if merchantID == uuid.Nil {
		return ErrNoTenant
	}
	return s.withTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.merchant_id', $1, true)`, merchantID.String()); err != nil {
			return fmt.Errorf("set tenant: %w", err)
		}
		return fn(tx, sqlcgen.New(tx))
	})
}

// WithOwner runs fn as the owner/worker role with no tenant context. It is used
// for platform-global work such as webhook and billing processing. Never use it
// to serve tenant requests.
func (s *Store) WithOwner(ctx context.Context, fn func(pgx.Tx, *sqlcgen.Queries) error) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		return fn(tx, sqlcgen.New(tx))
	})
}

func (s *Store) withTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func Normalize(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

type MerchantRoute struct {
	ID   uuid.UUID
	Slug string
	Name string
}

// ResolveMerchantByHost is the only pre-tenant lookup. The database function is
// SECURITY DEFINER and returns routing data only, never tenant content.
func (s *Store) ResolveMerchantByHost(ctx context.Context, host string) (MerchantRoute, error) {
	var m MerchantRoute
	err := s.pool.QueryRow(ctx,
		`SELECT id, slug, name FROM resolve_merchant_by_host($1)`, host).
		Scan(&m.ID, &m.Slug, &m.Name)
	if err != nil {
		return MerchantRoute{}, Normalize(err)
	}
	return m, nil
}

type PaymentAccountRoute struct {
	MerchantRoute
	Status string
}

// ResolveMerchantByPaymentAccount maps a Stripe connected account to its
// merchant for platform-level webhook processing. Like ResolveMerchantByHost,
// the database function is SECURITY DEFINER and exposes routing data only.
func (s *Store) ResolveMerchantByPaymentAccount(ctx context.Context, accountID string) (PaymentAccountRoute, error) {
	var m PaymentAccountRoute
	err := s.pool.QueryRow(ctx,
		`SELECT id, slug, name, status FROM resolve_merchant_by_payment_account($1)`, accountID).
		Scan(&m.ID, &m.Slug, &m.Name, &m.Status)
	if err != nil {
		return PaymentAccountRoute{}, Normalize(err)
	}
	return m, nil
}

func (s *Store) Pool() *pgxpool.Pool { return s.pool }

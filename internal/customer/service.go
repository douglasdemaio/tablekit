// Package customer implements platform-wide customer accounts with email
// one-time-code login. Customer rows are global; order history is always
// scoped by the tenant supplied by the caller.
package customer

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"tablekit.de/tablekit/internal/crypto"
	"tablekit.de/tablekit/internal/db"
	"tablekit.de/tablekit/internal/db/sqlcgen"
	"tablekit.de/tablekit/internal/email"
)

var (
	ErrNotFound     = errors.New("customer: not found")
	ErrInvalidInput = errors.New("customer: invalid input")
	ErrInvalidCode  = errors.New("customer: invalid or expired code")
	ErrRateLimited  = errors.New("customer: rate limited")
	ErrUnauthorized = errors.New("customer: not signed in")
)

const (
	CodeTTL        = 10 * time.Minute
	MaxCodeTries   = 5
	MaxCodesEmail  = 5
	MaxCodesIP     = 20
	codeEmailLimit = 15 * time.Minute
)

type Service struct {
	store             *db.Store
	mailer            email.Sender
	log               *slog.Logger
	Now               func() time.Time
	TTL               time.Duration
	RateLimitDisabled bool
}

func NewService(store *db.Store, mailer email.Sender, logger *slog.Logger, ttl time.Duration, rateLimitDisabled bool) *Service {
	return &Service{store: store, mailer: mailer, log: logger, Now: time.Now, TTL: ttl, RateLimitDisabled: rateLimitDisabled}
}

type LoginResult struct {
	Customer sqlcgen.CustomerAccount
	Session  sqlcgen.CustomerSession
	Token    string
	Expires  time.Time
}

// RequestCode creates a one-time login code and emails it. Unknown addresses
// receive the same response as known ones.
func (s *Service) RequestCode(ctx context.Context, emailAddr, ip string) error {
	emailAddr = normalizeEmail(emailAddr)
	if !validEmail(emailAddr) {
		return ErrInvalidInput
	}
	now := s.Now()
	q := s.store.Queries()
	if !s.RateLimitDisabled {
		n, err := q.CountRecentCustomerLoginCodes(ctx, emailAddr)
		if err != nil {
			return err
		}
		if n >= MaxCodesEmail {
			return ErrRateLimited
		}
		if ip != "" {
			n, err := q.CountCustomerLoginCodesForIP(ctx, parseIP(ip))
			if err != nil {
				return err
			}
			if n >= MaxCodesIP {
				return ErrRateLimited
			}
		}
	}
	code, err := randomCode()
	if err != nil {
		return err
	}
	hash, err := crypto.HashPin(code)
	if err != nil {
		return err
	}
	if _, err := q.CreateCustomerLoginCode(ctx, sqlcgen.CreateCustomerLoginCodeParams{
		Column1: emailAddr,
		Column2: hash,
		Column3: pgtype.Timestamptz{Time: now.Add(CodeTTL), Valid: true},
		Ip:      parseIP(ip),
	}); err != nil {
		return err
	}
	body := fmt.Sprintf("Your tablekit login code is %s.\n\nIt expires in %d minutes. If you did not request it, ignore this email.\n", code, int(CodeTTL.Minutes()))
	if err := s.mailer.Send(ctx, emailAddr, "Your tablekit login code", body); err != nil {
		s.log.Error("send login code", "error", err)
		return errors.New("customer: could not send email")
	}
	return nil
}

// VerifyCode checks a code, creating the account on first successful login.
func (s *Service) VerifyCode(ctx context.Context, emailAddr, code, ip, userAgent string) (LoginResult, error) {
	emailAddr = normalizeEmail(emailAddr)
	code = strings.TrimSpace(code)
	if !validEmail(emailAddr) || len(code) != 6 || !digits(code) {
		return LoginResult{}, ErrInvalidCode
	}
	q := s.store.Queries()
	row, err := q.GetLatestCustomerLoginCode(ctx, emailAddr)
	if err != nil {
		return LoginResult{}, ErrInvalidCode
	}
	now := s.Now()
	if row.UsedAt.Valid || row.Attempts >= MaxCodeTries || !row.ExpiresAt.Valid || now.After(row.ExpiresAt.Time) {
		return LoginResult{}, ErrInvalidCode
	}
	ok, err := crypto.VerifyPin(code, row.CodeHash)
	if err != nil || !ok {
		_ = q.IncrementCustomerLoginCodeAttempts(ctx, row.ID)
		return LoginResult{}, ErrInvalidCode
	}
	if err := q.UseCustomerLoginCode(ctx, row.ID); err != nil {
		return LoginResult{}, err
	}

	account, err := q.GetCustomerByEmail(ctx, emailAddr)
	if errors.Is(err, pgx.ErrNoRows) {
		account, err = q.CreateCustomer(ctx, emailAddr)
		if err != nil {
			if again, getErr := q.GetCustomerByEmail(ctx, emailAddr); getErr == nil {
				account, err = again, nil
			}
		}
	}
	if err != nil {
		return LoginResult{}, err
	}
	if err := q.TouchCustomerLogin(ctx, account.ID); err != nil {
		return LoginResult{}, err
	}

	token, err := crypto.RandomToken(32)
	if err != nil {
		return LoginResult{}, err
	}
	expires := now.Add(s.TTL)
	session, err := q.CreateCustomerSession(ctx, sqlcgen.CreateCustomerSessionParams{
		Column1: account.ID,
		Column2: crypto.HashToken(token),
		Ip:      parseIP(ip),
		Column4: truncate(userAgent, 512),
		Column5: pgtype.Timestamptz{Time: expires, Valid: true},
	})
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{Customer: account, Session: session, Token: token, Expires: expires}, nil
}

// Authenticate resolves a customer session cookie.
func (s *Service) Authenticate(ctx context.Context, token string) (sqlcgen.CustomerSession, sqlcgen.CustomerAccount, error) {
	if token == "" {
		return sqlcgen.CustomerSession{}, sqlcgen.CustomerAccount{}, ErrUnauthorized
	}
	q := s.store.Queries()
	session, err := q.GetCustomerSessionByTokenHash(ctx, crypto.HashToken(token))
	if err != nil {
		return sqlcgen.CustomerSession{}, sqlcgen.CustomerAccount{}, ErrUnauthorized
	}
	account, err := q.GetCustomerByID(ctx, session.CustomerID)
	if err != nil {
		return sqlcgen.CustomerSession{}, sqlcgen.CustomerAccount{}, ErrUnauthorized
	}
	_ = q.TouchCustomerSession(ctx, session.ID)
	return session, account, nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return s.store.Queries().RevokeCustomerSessionByToken(ctx, crypto.HashToken(token))
}

func (s *Service) UpdateProfile(ctx context.Context, customerID uuid.UUID, name, phone string) error {
	name = strings.TrimSpace(name)
	phone = strings.TrimSpace(phone)
	if len(name) > 120 || len(phone) > 40 {
		return ErrInvalidInput
	}
	return s.store.Queries().UpdateCustomerProfile(ctx, sqlcgen.UpdateCustomerProfileParams{
		Name: name, Phone: phone, Column3: customerID,
	})
}

type AddressInput struct {
	Label         string
	Recipient     string
	Phone         string
	Line1         string
	Line2         string
	Postcode      string
	City          string
	Country       string
	DeliveryNotes string
	IsDefault     bool
}

func (in *AddressInput) normalize() bool {
	in.Label = strings.TrimSpace(in.Label)
	in.Recipient = strings.TrimSpace(in.Recipient)
	in.Phone = strings.TrimSpace(in.Phone)
	in.Line1 = strings.TrimSpace(in.Line1)
	in.Line2 = strings.TrimSpace(in.Line2)
	in.Postcode = strings.ToUpper(strings.TrimSpace(in.Postcode))
	in.City = strings.TrimSpace(in.City)
	in.Country = strings.ToUpper(strings.TrimSpace(in.Country))
	in.DeliveryNotes = strings.TrimSpace(in.DeliveryNotes)
	if in.Country == "" {
		in.Country = "DE"
	}
	return in.Recipient != "" && in.Phone != "" && in.Line1 != "" && in.Postcode != "" &&
		in.City != "" && len(in.Country) == 2 && len(in.Label) <= 60 &&
		len(in.Recipient) <= 120 && len(in.Phone) <= 40 && len(in.Line1) <= 200 &&
		len(in.Line2) <= 200 && len(in.Postcode) <= 12 && len(in.City) <= 120 &&
		len(in.DeliveryNotes) <= 500
}

func (s *Service) ListAddresses(ctx context.Context, customerID uuid.UUID) ([]sqlcgen.CustomerAddress, error) {
	return s.store.Queries().ListCustomerAddresses(ctx, customerID)
}

func (s *Service) CreateAddress(ctx context.Context, customerID uuid.UUID, in AddressInput) (sqlcgen.CustomerAddress, error) {
	if !in.normalize() {
		return sqlcgen.CustomerAddress{}, ErrInvalidInput
	}
	var out sqlcgen.CustomerAddress
	err := s.store.WithOwner(ctx, func(_ pgx.Tx, q *sqlcgen.Queries) error {
		existing, err := q.ListCustomerAddresses(ctx, customerID)
		if err != nil {
			return err
		}
		addr, err := q.CreateCustomerAddress(ctx, sqlcgen.CreateCustomerAddressParams{
			Column1:  customerID,
			Column2:  in.Label,
			Column3:  in.Recipient,
			Column4:  in.Phone,
			Column5:  in.Line1,
			Column6:  in.Line2,
			Column7:  in.Postcode,
			Column8:  in.City,
			Column9:  in.Country,
			Column10: in.DeliveryNotes,
		})
		if err != nil {
			return err
		}
		if in.IsDefault || len(existing) == 0 {
			if err := q.ClearDefaultCustomerAddress(ctx, customerID); err != nil {
				return err
			}
			if err := q.SetDefaultCustomerAddress(ctx, sqlcgen.SetDefaultCustomerAddressParams{
				Column1: addr.ID, Column2: customerID,
			}); err != nil {
				return err
			}
			addr.IsDefault = true
		}
		out = addr
		return nil
	})
	return out, err
}

func (s *Service) Address(ctx context.Context, customerID, addressID uuid.UUID) (sqlcgen.CustomerAddress, error) {
	addr, err := s.store.Queries().GetCustomerAddress(ctx, sqlcgen.GetCustomerAddressParams{
		Column1: addressID, Column2: customerID,
	})
	if err != nil {
		return sqlcgen.CustomerAddress{}, ErrNotFound
	}
	return addr, nil
}

func (s *Service) UpdateAddress(ctx context.Context, customerID, addressID uuid.UUID, in AddressInput) error {
	if !in.normalize() {
		return ErrInvalidInput
	}
	return s.store.WithOwner(ctx, func(_ pgx.Tx, q *sqlcgen.Queries) error {
		if _, err := q.GetCustomerAddress(ctx, sqlcgen.GetCustomerAddressParams{
			Column1: addressID, Column2: customerID,
		}); err != nil {
			return ErrNotFound
		}
		if err := q.UpdateCustomerAddress(ctx, sqlcgen.UpdateCustomerAddressParams{
			Label: in.Label, Recipient: in.Recipient, Phone: in.Phone,
			Line1: in.Line1, Line2: in.Line2, Postcode: in.Postcode,
			City: in.City, Country: in.Country, DeliveryNotes: in.DeliveryNotes,
			Column10: addressID, Column11: customerID,
		}); err != nil {
			return err
		}
		if in.IsDefault {
			return s.makeDefault(ctx, q, customerID, addressID)
		}
		return nil
	})
}

func (s *Service) SetDefaultAddress(ctx context.Context, customerID, addressID uuid.UUID) error {
	return s.store.WithOwner(ctx, func(_ pgx.Tx, q *sqlcgen.Queries) error {
		return s.makeDefault(ctx, q, customerID, addressID)
	})
}

func (s *Service) makeDefault(ctx context.Context, q *sqlcgen.Queries, customerID, addressID uuid.UUID) error {
	if _, err := q.GetCustomerAddress(ctx, sqlcgen.GetCustomerAddressParams{
		Column1: addressID, Column2: customerID,
	}); err != nil {
		return ErrNotFound
	}
	if err := q.ClearDefaultCustomerAddress(ctx, customerID); err != nil {
		return err
	}
	return q.SetDefaultCustomerAddress(ctx, sqlcgen.SetDefaultCustomerAddressParams{
		Column1: addressID, Column2: customerID,
	})
}

func (s *Service) DeleteAddress(ctx context.Context, customerID, addressID uuid.UUID) error {
	return s.store.WithOwner(ctx, func(_ pgx.Tx, q *sqlcgen.Queries) error {
		addr, err := q.GetCustomerAddress(ctx, sqlcgen.GetCustomerAddressParams{
			Column1: addressID, Column2: customerID,
		})
		if err != nil {
			return ErrNotFound
		}
		if err := q.DeleteCustomerAddress(ctx, sqlcgen.DeleteCustomerAddressParams{
			Column1: addressID, Column2: customerID,
		}); err != nil {
			return err
		}
		if addr.IsDefault {
			remaining, err := q.ListCustomerAddresses(ctx, customerID)
			if err != nil {
				return err
			}
			if len(remaining) > 0 {
				return q.SetDefaultCustomerAddress(ctx, sqlcgen.SetDefaultCustomerAddressParams{
					Column1: remaining[0].ID, Column2: customerID,
				})
			}
		}
		return nil
	})
}

func (s *Service) ShareWith(ctx context.Context, customerID, merchantID uuid.UUID) error {
	return s.store.Queries().UpsertCustomerShare(ctx, sqlcgen.UpsertCustomerShareParams{
		Column1: customerID, Column2: merchantID,
	})
}

func (s *Service) RevokeShare(ctx context.Context, customerID, merchantID uuid.UUID) error {
	return s.store.Queries().DeleteCustomerShare(ctx, sqlcgen.DeleteCustomerShareParams{
		Column1: customerID, Column2: merchantID,
	})
}

func (s *Service) Shares(ctx context.Context, customerID uuid.UUID) ([]sqlcgen.ListCustomerSharesRow, error) {
	return s.store.Queries().ListCustomerShares(ctx, customerID)
}

func (s *Service) HasShared(ctx context.Context, customerID, merchantID uuid.UUID) bool {
	_, err := s.store.Queries().GetCustomerShare(ctx, sqlcgen.GetCustomerShareParams{
		Column1: customerID, Column2: merchantID,
	})
	return err == nil
}

func (s *Service) Orders(ctx context.Context, merchantID, customerID uuid.UUID) ([]sqlcgen.ListCustomerOrdersRow, error) {
	var out []sqlcgen.ListCustomerOrdersRow
	err := s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		rows, err := q.ListCustomerOrders(ctx, sqlcgen.ListCustomerOrdersParams{
			Column1: merchantID, Column2: customerID,
		})
		out = rows
		return err
	})
	return out, err
}

func (s *Service) OrderByRef(ctx context.Context, merchantID, customerID uuid.UUID, ref string) (sqlcgen.GetCustomerOrderByRefRow, error) {
	var out sqlcgen.GetCustomerOrderByRefRow
	err := s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		row, err := q.GetCustomerOrderByRef(ctx, sqlcgen.GetCustomerOrderByRefParams{
			Column1: merchantID, Column2: customerID, Column3: ref,
		})
		if err != nil {
			return ErrNotFound
		}
		out = row
		return nil
	})
	return out, err
}

func normalizeEmail(v string) string { return strings.ToLower(strings.TrimSpace(v)) }

func validEmail(v string) bool {
	return len(v) >= 3 && len(v) <= 254 && strings.Contains(v, "@") && !strings.ContainsAny(v, " \t\r\n")
}

func digits(v string) bool {
	for _, r := range v {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func randomCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func parseIP(v string) *netip.Addr {
	v = strings.TrimSpace(v)
	if host, _, err := net.SplitHostPort(v); err == nil {
		v = host
	}
	addr, err := netip.ParseAddr(v)
	if err != nil {
		return nil
	}
	return &addr
}

func truncate(v string, n int) string {
	if len(v) <= n {
		return v
	}
	return v[:n]
}

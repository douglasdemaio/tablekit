package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"tablekit.de/tablekit/internal/config"
	"tablekit.de/tablekit/internal/crypto"
	"tablekit.de/tablekit/internal/db"
	"tablekit.de/tablekit/internal/db/sqlcgen"
)

var (
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	ErrInvalidCode        = errors.New("auth: invalid code")
	ErrNoPasskeys         = errors.New("auth: no passkeys registered")
	ErrPasskeyUnavailable = errors.New("auth: passkey login unavailable")
	ErrTOTPEnabled        = errors.New("auth: totp already enabled")
	ErrChallengeExpired   = errors.New("auth: challenge expired")
	ErrRateLimited        = errors.New("auth: rate limited")
)

type Service struct {
	Store   *db.Store
	Sealer  *crypto.Sealer
	Config  *config.Config
	WA      *webauthn.WebAuthn
	Logger  *slog.Logger
	Limiter *Limiter
	Now     func() time.Time

	dummyOnce sync.Once
	dummyHash string
}

func NewService(store *db.Store, sealer *crypto.Sealer, cfg *config.Config, wa *webauthn.WebAuthn, logger *slog.Logger) *Service {
	return &Service{
		Store:   store,
		Sealer:  sealer,
		Config:  cfg,
		WA:      wa,
		Logger:  logger,
		Limiter: NewLimiter(),
		Now:     time.Now,
	}
}

type LoginResult struct {
	Token     string
	CSRFToken string
	Session   sqlcgen.Session
	Staff     sqlcgen.StaffUser
	Next      string
}

func (s *Service) LoginPassword(ctx context.Context, merchantID uuid.UUID, email, password, ip, userAgent string) (LoginResult, error) {
	var out LoginResult
	err := s.Store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		staff, err := q.GetStaffByEmail(ctx, strings.TrimSpace(email))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				s.verifyDummy(password)
				return ErrInvalidCredentials
			}
			return err
		}
		if staff.PasswordHash == nil || *staff.PasswordHash == "" {
			s.verifyDummy(password)
			return ErrInvalidCredentials
		}
		ok, err := crypto.VerifyPassword(password, *staff.PasswordHash)
		if err != nil {
			return ErrInvalidCredentials
		}
		if !ok {
			return ErrInvalidCredentials
		}

		state := "verified"
		next := "/dashboard"
		role := RoleFromDB(staff.Role)
		needsMFA := staff.MfaRequired || role == RoleOwner
		switch {
		case staff.TotpEnabled:
			state, next = "pending", "/login/mfa"
		case needsMFA:
			state, next = "pending_setup", "/setup/mfa"
		}
		ns, err := s.createSession(ctx, q, merchantID, staff.ID, state, ip, userAgent)
		if err != nil {
			return err
		}
		out = LoginResult{
			Token: ns.Token, CSRFToken: ns.CSRFToken, Session: ns.Session,
			Staff: staff, Next: next,
		}
		return nil
	})
	return out, err
}

func (s *Service) VerifyLoginTOTP(ctx context.Context, merchantID uuid.UUID, session sqlcgen.Session, code string) (LoginResult, error) {
	var out LoginResult
	err := s.Store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		staff, err := q.GetStaffByID(ctx, session.StaffID)
		if err != nil {
			return ErrInvalidCode
		}
		if !staff.TotpEnabled || len(staff.TotpSecretEnc) == 0 {
			return ErrInvalidCode
		}
		step, err := s.validateTOTP(staff, code)
		if err != nil {
			return ErrInvalidCode
		}
		if err := q.SetStaffTOTPStep(ctx, sqlcgen.SetStaffTOTPStepParams{
			Column1: step,
			Column2: staff.ID,
		}); err != nil {
			return err
		}
		ns, err := s.rotateSession(ctx, q, merchantID, session.ID)
		if err != nil {
			return err
		}
		out = LoginResult{
			Token: ns.Token, CSRFToken: ns.CSRFToken, Session: ns.Session,
			Staff: ns.Staff, Next: "/dashboard",
		}
		return nil
	})
	return out, err
}

func (s *Service) createSession(ctx context.Context, q *sqlcgen.Queries, merchantID, staffID uuid.UUID, state, ip, userAgent string) (newSession, error) {
	token, err := crypto.RandomToken(32)
	if err != nil {
		return newSession{}, err
	}
	csrf, err := crypto.RandomToken(32)
	if err != nil {
		return newSession{}, err
	}
	ttl := s.Config.SessionTTL
	if state != "verified" {
		ttl = s.Config.MFAPendingTTL
	}
	sess, err := q.CreateSession(ctx, sqlcgen.CreateSessionParams{
		Column1: merchantID,
		Column2: staffID,
		Column3: crypto.HashToken(token),
		Column4: crypto.HashToken(csrf),
		Column5: state,
		Ip:      parseIP(ip),
		Column7: truncate(userAgent, 512),
		Column8: pgTime(s.Now().Add(ttl)),
	})
	if err != nil {
		return newSession{}, err
	}
	return newSession{Token: token, CSRFToken: csrf, Session: sess}, nil
}

func (s *Service) rotateSession(ctx context.Context, q *sqlcgen.Queries, merchantID, sessionID uuid.UUID) (newSession, error) {
	token, err := crypto.RandomToken(32)
	if err != nil {
		return newSession{}, err
	}
	csrf, err := crypto.RandomToken(32)
	if err != nil {
		return newSession{}, err
	}
	if err := q.RotateSession(ctx, sqlcgen.RotateSessionParams{
		Column1: crypto.HashToken(token),
		Column2: crypto.HashToken(csrf),
		Column3: pgTime(s.Now().Add(s.Config.SessionTTL)),
		Column4: sessionID,
	}); err != nil {
		return newSession{}, err
	}
	sess, err := q.GetSessionByID(ctx, sessionID)
	if err != nil {
		return newSession{}, err
	}
	staff, err := q.GetStaffByID(ctx, sess.StaffID)
	if err != nil {
		return newSession{}, err
	}
	return newSession{Token: token, CSRFToken: csrf, Session: sess, Staff: staff}, nil
}

// BeginTOTPSetup stores a new secret but does not enable it until ConfirmTOTPSetup.
func (s *Service) BeginTOTPSetup(ctx context.Context, merchantID, staffID uuid.UUID) (secret string, uri string, err error) {
	err = s.Store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		staff, err := q.GetStaffByID(ctx, staffID)
		if err != nil {
			return err
		}
		if staff.TotpEnabled {
			return ErrTOTPEnabled
		}
		key, err := totp.Generate(totp.GenerateOpts{
			Issuer:      "tablekit",
			AccountName: staff.Email,
			Period:      30,
			SecretSize:  20,
			Digits:      otp.DigitsSix,
			Algorithm:   otp.AlgorithmSHA1,
		})
		if err != nil {
			return err
		}
		sealed, err := s.Sealer.Seal([]byte(key.Secret()))
		if err != nil {
			return err
		}
		if err := q.SetStaffTOTPSecret(ctx, sqlcgen.SetStaffTOTPSecretParams{
			TotpSecretEnc: sealed,
			Column2:       staff.ID,
		}); err != nil {
			return err
		}
		secret, uri = key.Secret(), key.URL()
		return nil
	})
	return secret, uri, err
}

func (s *Service) ConfirmTOTPSetup(ctx context.Context, merchantID, staffID uuid.UUID, code string) error {
	return s.Store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		staff, err := q.GetStaffByID(ctx, staffID)
		if err != nil {
			return err
		}
		step, err := s.validateTOTP(staff, code)
		if err != nil {
			return ErrInvalidCode
		}
		if err := q.SetStaffTOTPStep(ctx, sqlcgen.SetStaffTOTPStepParams{Column1: step, Column2: staff.ID}); err != nil {
			return err
		}
		return q.EnableStaffTOTP(ctx, staff.ID)
	})
}

func (s *Service) validateTOTP(staff sqlcgen.StaffUser, code string) (int64, error) {
	if len(staff.TotpSecretEnc) == 0 {
		return 0, ErrInvalidCode
	}
	plain, err := s.Sealer.Open(staff.TotpSecretEnc)
	if err != nil {
		return 0, ErrInvalidCode
	}
	code = strings.TrimSpace(code)
	now := s.Now()
	ok, err := totp.ValidateCustom(code, string(plain), now, totp.ValidateOpts{
		Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil || !ok {
		return 0, ErrInvalidCode
	}
	step := now.Unix() / 30
	if staff.TotpLastStep != nil && step <= *staff.TotpLastStep {
		return 0, ErrInvalidCode
	}
	return step, nil
}

func (s *Service) verifyDummy(password string) {
	s.dummyOnce.Do(func() {
		h, err := crypto.HashPassword("dummy-password-never-matches-123")
		if err == nil {
			s.dummyHash = h
		}
	})
	if s.dummyHash != "" {
		_, _ = crypto.VerifyPassword(password, s.dummyHash)
	}
}

func (s *Service) Logout(ctx context.Context, merchantID, sessionID uuid.UUID) error {
	return s.Store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		return q.RevokeSession(ctx, sessionID)
	})
}

func (s *Service) RevokeSession(ctx context.Context, merchantID, staffID, sessionID uuid.UUID) error {
	return s.Store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		return q.RevokeOwnSession(ctx, sqlcgen.RevokeOwnSessionParams{
			Column1: sessionID,
			Column2: staffID,
		})
	})
}

func (s *Service) ListSessions(ctx context.Context, merchantID, staffID uuid.UUID) ([]sqlcgen.ListSessionsForStaffRow, error) {
	var rows []sqlcgen.ListSessionsForStaffRow
	err := s.Store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		var err error
		rows, err = q.ListSessionsForStaff(ctx, staffID)
		return err
	})
	return rows, err
}

func (s *Service) RevokeOtherSessions(ctx context.Context, merchantID, staffID, keepID uuid.UUID) error {
	return s.Store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		return q.RevokeOtherSessions(ctx, sqlcgen.RevokeOtherSessionsParams{
			Column1: staffID,
			Column2: keepID,
		})
	})
}

func (s *Service) TOTPSetupState(ctx context.Context, merchantID, staffID uuid.UUID) (secret, uri string, err error) {
	err = s.Store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		staff, err := q.GetStaffByID(ctx, staffID)
		if err != nil {
			return err
		}
		if len(staff.TotpSecretEnc) == 0 {
			return ErrInvalidCode
		}
		plain, err := s.Sealer.Open(staff.TotpSecretEnc)
		if err != nil {
			return err
		}
		secret = string(plain)
		uri = "otpauth://totp/tablekit:" + url.QueryEscape(staff.Email) +
			"?secret=" + url.QueryEscape(secret) +
			"&issuer=tablekit&period=30&digits=6&algorithm=SHA1"
		return nil
	})
	return secret, uri, err
}

func (s *Service) DisableTOTP(ctx context.Context, merchantID, staffID uuid.UUID) error {
	return s.Store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		return q.DisableStaffTOTP(ctx, staffID)
	})
}

func (s *Service) Audit(ctx context.Context, merchantID uuid.UUID, actorType string, actorID *uuid.UUID, action, targetType, targetID, ip string, meta map[string]any) error {
	raw := []byte("{}")
	if meta != nil {
		encoded, err := json.Marshal(meta)
		if err != nil {
			return err
		}
		raw = encoded
	}
	return s.Store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		return q.InsertAuditLog(ctx, sqlcgen.InsertAuditLogParams{
			Column1: merchantID,
			Column2: actorType,
			ActorID: actorID,
			Column4: action,
			Column5: targetType,
			Column6: targetID,
			Ip:      parseIP(ip),
			Column8: raw,
		})
	})
}

// UpgradeSession rotates a pending session into a fully verified session. Used
// after an MFA or passkey registration ceremony succeeds.
func (s *Service) UpgradeSession(ctx context.Context, merchantID, sessionID uuid.UUID) (LoginResult, error) {
	var out LoginResult
	err := s.Store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		ns, err := s.rotateSession(ctx, q, merchantID, sessionID)
		if err != nil {
			return err
		}
		out = LoginResult{
			Token: ns.Token, CSRFToken: ns.CSRFToken, Session: ns.Session,
			Staff: ns.Staff, Next: "/dashboard",
		}
		return nil
	})
	return out, err
}

package auth

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"

	"tablekit.de/tablekit/internal/config"
	"tablekit.de/tablekit/internal/crypto"
	"tablekit.de/tablekit/internal/db"
	"tablekit.de/tablekit/internal/db/sqlcgen"
)

var (
	ErrSessionInvalid = errors.New("auth: session invalid")
	ErrSessionPending = errors.New("auth: session requires mfa")
)

type SessionManager struct {
	store *db.Store
	cfg   *config.Config
}

func NewSessionManager(store *db.Store, cfg *config.Config) *SessionManager {
	return &SessionManager{store: store, cfg: cfg}
}

type newSession struct {
	Token     string
	CSRFToken string
	Session   sqlcgen.Session
	Staff     sqlcgen.StaffUser
}

func (m *SessionManager) Load(ctx context.Context, merchantID uuid.UUID, rawToken string) (sqlcgen.Session, sqlcgen.StaffUser, error) {
	var session sqlcgen.Session
	var staff sqlcgen.StaffUser
	if rawToken == "" {
		return session, staff, ErrSessionInvalid
	}
	err := m.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		s, err := q.GetSessionByTokenHash(ctx, crypto.HashToken(rawToken))
		if err != nil {
			return err
		}
		st, err := q.GetStaffByID(ctx, s.StaffID)
		if err != nil {
			return err
		}
		if st.DisabledAt.Valid {
			return ErrSessionInvalid
		}
		if s.State != "verified" && s.State != "pending" && s.State != "pending_setup" {
			return ErrSessionInvalid
		}
		if err := q.TouchSession(ctx, s.ID); err != nil {
			return err
		}
		session, staff = s, st
		return nil
	})
	if err != nil {
		if errors.Is(db.Normalize(err), db.ErrNotFound) {
			return session, staff, ErrSessionInvalid
		}
		return session, staff, err
	}
	return session, staff, nil
}

func (m *SessionManager) Create(ctx context.Context, merchantID, staffID uuid.UUID, state, ip, userAgent string) (newSession, error) {
	token, err := crypto.RandomToken(32)
	if err != nil {
		return newSession{}, err
	}
	csrf, err := crypto.RandomToken(32)
	if err != nil {
		return newSession{}, err
	}
	ttl := m.cfg.SessionTTL
	if state != "verified" {
		ttl = m.cfg.MFAPendingTTL
	}
	addr := parseIP(ip)
	var out newSession
	err = m.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		s, err := q.CreateSession(ctx, sqlcgen.CreateSessionParams{
			Column1: merchantID,
			Column2: staffID,
			Column3: crypto.HashToken(token),
			Column4: crypto.HashToken(csrf),
			Column5: state,
			Ip:      addr,
			Column7: truncate(userAgent, 512),
			Column8: pgTime(time.Now().Add(ttl)),
		})
		if err != nil {
			return err
		}
		out = newSession{Token: token, CSRFToken: csrf, Session: s}
		return nil
	})
	return out, err
}

func (m *SessionManager) Rotate(ctx context.Context, merchantID, sessionID uuid.UUID) (newSession, error) {
	token, err := crypto.RandomToken(32)
	if err != nil {
		return newSession{}, err
	}
	csrf, err := crypto.RandomToken(32)
	if err != nil {
		return newSession{}, err
	}
	var out newSession
	err = m.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		if err := q.RotateSession(ctx, sqlcgen.RotateSessionParams{
			Column1: crypto.HashToken(token),
			Column2: crypto.HashToken(csrf),
			Column3: pgTime(time.Now().Add(m.cfg.SessionTTL)),
			Column4: sessionID,
		}); err != nil {
			return err
		}
		s, err := q.GetSessionByID(ctx, sessionID)
		if err != nil {
			return err
		}
		st, err := q.GetStaffByID(ctx, s.StaffID)
		if err != nil {
			return err
		}
		out = newSession{Token: token, CSRFToken: csrf, Session: s, Staff: st}
		return nil
	})
	return out, err
}

func parseIP(raw string) *netip.Addr {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if ap, err := netip.ParseAddrPort(raw); err == nil {
		addr := ap.Addr()
		return &addr
	}
	if addr, err := netip.ParseAddr(raw); err == nil {
		return &addr
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Package staff manages merchant employees: accounts, roles, MFA policy and
// team-wide session administration.
package staff

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"fmt"
	"time"

	"tablekit.de/tablekit/internal/auth"
	"tablekit.de/tablekit/internal/crypto"
	"tablekit.de/tablekit/internal/db"
	"tablekit.de/tablekit/internal/db/sqlcgen"
)

var (
	ErrInvalidInput = errors.New("staff: invalid input")
	ErrNotFound     = errors.New("staff: not found")
	ErrLastOwner    = errors.New("staff: cannot remove the last owner")
)

// MinPasswordLength mirrors the bootstrap requirement.
const MinPasswordLength = 12

type Service struct {
	store *db.Store
}

func NewService(store *db.Store) *Service { return &Service{store: store} }

// Employee is a staff account with computed fields for display.
type Employee struct {
	User sqlcgen.StaffUser
}

// Session is an active login for an employee.
type Session struct {
	ID        uuid.UUID
	StaffID   uuid.UUID
	StaffName string
	Email     string
	Role      auth.Role
	IP        string
	UserAgent string
	LastSeen  string
	Current   bool
}

func (s *Service) List(ctx context.Context, merchantID uuid.UUID) ([]sqlcgen.StaffUser, error) {
	var out []sqlcgen.StaffUser
	err := s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		rows, err := q.ListStaff(ctx)
		if err != nil {
			return err
		}
		out = rows
		return nil
	})
	return out, err
}

// Create adds an employee with an initial password. New accounts must enrol MFA.
func (s *Service) Create(ctx context.Context, merchantID uuid.UUID, email, name, password string, role auth.Role) (sqlcgen.StaffUser, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	name = strings.TrimSpace(name)
	if !validEmail(email) || name == "" || len(password) < MinPasswordLength {
		return sqlcgen.StaffUser{}, ErrInvalidInput
	}
	hash, err := crypto.HashPassword(password)
	if err != nil {
		return sqlcgen.StaffUser{}, err
	}
	var out sqlcgen.StaffUser
	err = s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		u, err := q.CreateStaff(ctx, sqlcgen.CreateStaffParams{
			Column1:      merchantID,
			Role:         dbRole(role),
			Column3:      email,
			Column4:      name,
			PasswordHash: &hash,
			Column6:      true,
		})
		if err != nil {
			return err
		}
		out = u
		return nil
	})
	return out, err
}

// Update changes an employee's name, role and MFA policy. Removing the last
// owner or the last owner's MFA requirement is rejected.
func (s *Service) Update(ctx context.Context, merchantID, staffID uuid.UUID, name string, role auth.Role, mfaRequired bool) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrInvalidInput
	}
	return s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		target, err := q.GetStaffByID(ctx, staffID)
		if err != nil {
			return ErrNotFound
		}
		if err := s.guardOwnerDemotion(ctx, q, target, role, mfaRequired); err != nil {
			return err
		}
		return q.UpdateStaffAccess(ctx, sqlcgen.UpdateStaffAccessParams{
			Column1: name,
			Column2: dbRole(role),
			Column3: mfaRequired,
			Column4: staffID,
		})
	})
}

// ResetPassword sets a new password and revokes the employee's sessions.
func (s *Service) ResetPassword(ctx context.Context, merchantID, staffID uuid.UUID, password string) error {
	if len(password) < MinPasswordLength {
		return ErrInvalidInput
	}
	hash, err := crypto.HashPassword(password)
	if err != nil {
		return err
	}
	return s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		if err := q.UpdateStaffPassword(ctx, sqlcgen.UpdateStaffPasswordParams{PasswordHash: &hash, Column2: staffID}); err != nil {
			return err
		}
		return q.RevokeAllStaffSessions(ctx, staffID)
	})
}

// SetDisabled disables or re-enables an employee, revoking sessions on disable.
func (s *Service) SetDisabled(ctx context.Context, merchantID, staffID uuid.UUID, disabled bool) error {
	return s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		target, err := q.GetStaffByID(ctx, staffID)
		if err != nil {
			return ErrNotFound
		}
		if disabled {
			if err := s.guardLastOwner(ctx, q, target); err != nil {
				return err
			}
			if err := q.DisableStaff(ctx, staffID); err != nil {
				return err
			}
			return q.RevokeAllStaffSessions(ctx, staffID)
		}
		return q.EnableStaff(ctx, staffID)
	})
}

// ActiveSessions lists every live session in the merchant.
func (s *Service) ActiveSessions(ctx context.Context, merchantID, currentSessionID uuid.UUID) ([]Session, error) {
	var out []Session
	err := s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		rows, err := q.ListActiveSessionsForMerchant(ctx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			out = append(out, Session{
				ID:        row.ID,
				StaffID:   row.StaffID,
				StaffName: row.StaffName,
				Email:     row.StaffEmail,
				Role:      auth.Role(row.StaffRoleEnum),
				IP:        fmt.Sprint(row.IpText),
				UserAgent: row.UserAgent,
				LastSeen:  row.LastSeenAt.Time.UTC().Format(time.RFC3339),
				Current:   row.ID == currentSessionID,
			})
		}
		return nil
	})
	return out, err
}

// RevokeSession force-logs-out a single session in the merchant.
func (s *Service) RevokeSession(ctx context.Context, merchantID, sessionID uuid.UUID) error {
	return s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		return q.RevokeSession(ctx, sessionID)
	})
}

// RevokeStaffSessions force-logs-out every session for one employee.
func (s *Service) RevokeStaffSessions(ctx context.Context, merchantID, staffID uuid.UUID) error {
	return s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		return q.RevokeAllStaffSessions(ctx, staffID)
	})
}

func (s *Service) guardOwnerDemotion(ctx context.Context, q *sqlcgen.Queries, target sqlcgen.StaffUser, role auth.Role, mfaRequired bool) error {
	if target.Role != sqlcgen.StaffRoleOwner {
		return nil
	}
	if role == auth.RoleOwner && mfaRequired {
		return nil
	}
	return s.guardLastOwner(ctx, q, target)
}

func (s *Service) guardLastOwner(ctx context.Context, q *sqlcgen.Queries, target sqlcgen.StaffUser) error {
	if target.Role != sqlcgen.StaffRoleOwner {
		return nil
	}
	owners, err := q.ListStaff(ctx)
	if err != nil {
		return err
	}
	active := 0
	for _, u := range owners {
		if u.Role == sqlcgen.StaffRoleOwner && !u.DisabledAt.Valid {
			active++
		}
	}
	if active <= 1 {
		return ErrLastOwner
	}
	return nil
}

func dbRole(r auth.Role) sqlcgen.StaffRoleEnum { return sqlcgen.StaffRoleEnum(r) }

func validEmail(email string) bool {
	at := strings.IndexByte(email, '@')
	return at > 0 && at < len(email)-1 && !strings.ContainsAny(email, " \t")
}

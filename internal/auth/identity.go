package auth

import (
	"context"

	"tablekit.de/tablekit/internal/db/sqlcgen"
	"tablekit.de/tablekit/internal/tenant"
)

type Identity struct {
	Merchant tenant.Merchant
	Staff    sqlcgen.StaffUser
	Session  sqlcgen.Session
}

func (i Identity) Role() Role { return RoleFromDB(i.Staff.Role) }

func (i Identity) Can(p Permission) bool { return Can(i.Role(), p) }

type identityKey struct{}

func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}

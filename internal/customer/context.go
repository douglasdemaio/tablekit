package customer

import (
	"context"

	"tablekit.de/tablekit/internal/db/sqlcgen"
)

type Identity struct {
	Account sqlcgen.CustomerAccount
	Session sqlcgen.CustomerSession
}

type identityKey struct{}

func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}

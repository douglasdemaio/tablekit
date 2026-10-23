package auth

import (
	"github.com/jackc/pgx/v5/pgtype"
	"time"
)

func pgTime(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

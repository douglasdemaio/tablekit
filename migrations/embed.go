package migrations

import "embed"

// FS contains the goose migrations. They are embedded so a self-hosted binary
// can migrate without shipping loose SQL files next to it.
//
//go:embed *.sql
var FS embed.FS

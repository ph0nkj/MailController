// Package migrations embeds the goose SQL migration files.
package migrations

import "embed"

// FS contains all SQL migration files shipped with the binary.
//
//go:embed *.sql
var FS embed.FS

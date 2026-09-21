// Package migrations embeds the SQL migration files so the binary is
// self-contained (distroless image carries no external files).
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS

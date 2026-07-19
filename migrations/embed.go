// Package migrations embeds PortPilot's SQLite schema migrations.
package migrations

import "embed"

// FS exposes the DDL-only schema migrations.
//
//go:embed *.sql
var FS embed.FS

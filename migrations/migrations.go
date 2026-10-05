// Package migrations embeds the SQL migration files so the server binary can
// apply them without the files being present at runtime.
//
// Files are named NNNN_description.sql and applied in name order, once each.
package migrations

import "embed"

// FS holds every migration file.
//
//go:embed *.sql
var FS embed.FS

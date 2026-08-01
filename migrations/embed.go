// Package migrations embeds the platform's versioned PostgreSQL migrations (CW-0010 Unit 3), so the
// binary that applies them carries no runtime dependency on the filesystem layout it was built from.
package migrations

import "embed"

// FS holds every migration file, applied in filename order by internal/platform/postgres.Migrate.
//
//go:embed *.sql
var FS embed.FS

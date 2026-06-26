// Package migrations holds the database schema migrations as embedded SQL, so the
// single binary provisions its own schema on startup with no separate migrate step
// (NFR-D1). Migrations are applied in lexical filename order; name new ones
// NNNN_description.sql so the ordering stays obvious.
package migrations

import "embed"

// Files contains every .sql migration, embedded into the binary at build time.
//
//go:embed *.sql
var Files embed.FS

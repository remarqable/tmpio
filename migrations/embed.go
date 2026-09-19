// Package migrations embeds the goose SQL migrations in the binary so that a
// container can bring its own schema up to date with no goose binary, no source
// tree and no operator at a terminal. The files stay the source of truth for
// `make migrate`; this only gives the server a copy of them.
package migrations

import "embed"

// FS holds every migration, in order.
//
//go:embed *.sql
var FS embed.FS

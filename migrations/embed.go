// Package migrations embeds the SQL schema so the binary creates and upgrades
// its database on its own at startup.
package migrations

import "embed"

// FS holds every .sql migration, applied in lexical order of file name.
//
//go:embed *.sql
var FS embed.FS

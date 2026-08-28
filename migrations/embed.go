// Package migrations embeds the SQL migration files so the compiled server is
// self-contained: deploying a single binary is enough, with no migration
// directory to ship alongside it.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS

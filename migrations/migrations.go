// Package migrations embeds the SQL migration files in this directory so
// they can be applied from Go without shelling out to psql or pulling in
// a migration framework.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS

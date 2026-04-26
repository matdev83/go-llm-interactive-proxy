package db

import "time"

// DefaultPostgresOpenMigrateTimeout bounds composition-root PostgreSQL open (ping,
// pool apply) and initial schema migration when callers wrap I/O in [context.WithTimeout].
const DefaultPostgresOpenMigrateTimeout = 2 * time.Minute

package migrations

import "embed"

// Files is the versioned schema shipped with the backend, independent of XD.
//
//go:embed *.sql
var Files embed.FS

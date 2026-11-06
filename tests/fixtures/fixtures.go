package fixtures

import "embed"

// Files contains synthetic Mihomo protocol examples, not captured user traffic.
//
//go:embed *.json
var Files embed.FS

//go:build !frontend

package web

import "io/fs"

const Embedded = false

var bundled fs.FS

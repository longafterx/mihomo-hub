//go:build frontend

package web

import (
	"embed"
	"io/fs"
)

// all: 必须包含 Nuxt 的 _nuxt 目录；构建产物只由锁定 SHA 的发布脚本生成。
//
//go:embed all:dist
var assets embed.FS

const Embedded = true

var bundled = func() fs.FS {
	files, err := fs.Sub(assets, "dist")
	if err != nil {
		panic(err)
	} // 固定嵌入目录不存在属于构建错误。
	return files
}()

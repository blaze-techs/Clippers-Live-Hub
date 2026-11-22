// Package webui embeds the local control UI.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed assets
var assets embed.FS

// FS returns the UI file system rooted at the assets directory.
func FS() fs.FS {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err)
	}
	return sub
}

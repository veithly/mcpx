// Package webui embeds the React console, so deployed servers do not need Node.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed frontend/dist
var assets embed.FS

func Handler() http.Handler {
	root, err := fs.Sub(assets, "frontend/dist")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(root))
}

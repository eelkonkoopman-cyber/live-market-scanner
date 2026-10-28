// Package web embeds the built UI (web/dist) for serving by the Go binary.
// Until the React app is built, dist/ holds a functional placeholder viewer.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the built UI filesystem rooted at dist/.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return dist
	}
	return sub
}

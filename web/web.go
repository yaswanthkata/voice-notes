// Package web embeds the static test page so the binary is self-contained.
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var files embed.FS

// Static returns the page's files rooted at static/.
func Static() fs.FS {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		panic(err) // only possible if the embed directive is wrong
	}
	return sub
}

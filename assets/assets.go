// Package assets embeds binary assets that ship inside the scanner executable.
package assets

import _ "embed"

// Icon is the application icon as a multi-size .ico file (16, 24, 32, 48 and
// 256 px, 32-bit). It is drawn procedurally; regenerate it from the repository
// root with:
//
//	go run ./tools/genicon
//
//go:embed scanner.ico
var Icon []byte

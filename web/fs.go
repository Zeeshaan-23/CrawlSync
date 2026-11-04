package web

import (
	"embed"
	"io/fs"
)

//go:embed assets css js index.html
var files embed.FS

// Files returns the embedded static filesystem for the CrawlSync dashboard.
func Files() fs.FS {
	return files
}
